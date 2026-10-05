// Package asciiart turns an image into ASCII text.
//
// It is a Go port of go-ascii by nearlynithin, which did the conversion in a
// main package and printed straight to stdout. Same sampling, same luminosity
// and same ramp, but the output comes back as a string so it can be put in a
// TUI instead of a terminal.
//
// Give it somewhere the picture is and it does the rest:
//
//	art, err := asciiart.RenderFile("cover.png", asciiart.Options{Width: 80})
//	art, err := asciiart.RenderURL(ctx, thumbURL, asciiart.Options{Width: 80})
//	art, err := asciiart.RenderSource(ctx, src, asciiart.Options{Width: 80})
//
// There are two ways to draw it. Mode Mono picks a character out of a ramp for
// every pixel, which is what the original did and what survives being pasted
// anywhere. Mode HalfBlock packs two pixels into every cell instead, using the
// upper half block with the top pixel as the foreground colour and the bottom
// one as the background. That doubles the vertical resolution and keeps the
// colour, at the cost of only working where the terminal draws those blocks.
package asciiart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // registered so Decode accepts the formats the original did
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// DefaultWidth is the column count used when Options.Width is not set. It is a
// plain constant rather than the real terminal width, because the caller is in
// a better position to know how much room it has.
const DefaultWidth = 80

// DefaultRamp maps brightness to characters, darkest first. The last character
// is what a black pixel becomes, so it should be a space.
const DefaultRamp = " `@*$#%@"

// The three block characters HalfBlock draws with. The upper one is the useful
// one: alone it shows the foreground in the top half and the background in the
// bottom, which is a whole extra pixel for free.
const (
	blockUpper = '▀' // ▀
	blockLower = '▄' // ▄
	blockFull  = '█' // █
)

// mid is the brightness at which a pixel counts as light, used to choose between
// the blocks when colour is off.
const mid = 128

// maxImageBytes caps how much is read into memory before decoding. Album art is
// a few hundred kilobytes, so nothing legitimate comes close, and it stops a
// large or endless response from being the thing that takes the program down.
const maxImageBytes = 20 << 20

// httpClient fetches remote pictures. Ten seconds is generous for a thumbnail
// and short enough that an address which will not answer does not leave a track
// sitting there with a blank cover.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// Mode is how a pixel becomes a character.
type Mode int

const (
	// Mono picks one ramp character per pixel. Plain text, no colour unless
	// asked for, and the mode the original worked in.
	Mono Mode = iota

	// HalfBlock puts two vertically adjacent pixels in each cell, the top one in
	// the foreground and the bottom one in the background. Twice the vertical
	// resolution of Mono at the same width, and it is what makes the colour
	// worth having.
	HalfBlock
)

// Options controls how an image is converted. The zero value is usable and
// renders a plain-text picture at DefaultWidth.
type Options struct {
	// Width is the number of columns to fit the image into. Values below one
	// mean DefaultWidth. The image is never enlarged past its own width.
	Width int

	// Color emits 24-bit ANSI colour, one colour per source pixel in Mono and
	// one per pixel pair in HalfBlock. Plain text is the default.
	Color bool

	// Ramp replaces DefaultRamp, in Mono mode only. Its last character is the
	// darkest, so a ramp that does not end in a space has no black.
	Ramp string

	// Mode is how each pixel becomes a character. The zero value is Mono.
	Mode Mode
}

// RenderSource converts the picture at src, which is either an http or https
// address or a path on disk. It is the entry point to reach for when the source
// is not known ahead of time, which is most of the time.
func RenderSource(ctx context.Context, src string, opts Options) (string, error) {
	if isURL(src) {
		return RenderURL(ctx, src, opts)
	}
	return RenderFile(src, opts)
}

// RenderURL downloads a picture and converts it. The context covers the whole
// job, so cancelling it stops the render as well as the download.
func RenderURL(ctx context.Context, rawURL string, opts Options) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("asciiart: %s: %w", rawURL, err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("asciiart: %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	// A thumbnail address that answers 404 is a normal thing to hit, not a
	// picture, and image.Decode would only report it as an unrecognised format.
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("asciiart: %s: %s", rawURL, resp.Status)
	}

	img, err := decodeImage(resp.Body)
	if err != nil {
		return "", fmt.Errorf("asciiart: %s: %w", rawURL, err)
	}

	return Render(img, opts)
}

// RenderFile reads a picture from disk and converts it. The format is taken from
// the contents, so the extension does not have to be right.
func RenderFile(path string, opts Options) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	img, err := decodeImage(f)
	if err != nil {
		return "", fmt.Errorf("asciiart: %s: %w", path, err)
	}

	return Render(img, opts)
}

// Render converts img and returns the result, one line per row, with a
// trailing newline on each.
func Render(img image.Image, opts Options) (string, error) {
	if img == nil {
		return "", errors.New("asciiart: nil image")
	}

	bounds := img.Bounds()
	if bounds.Dx() < 1 || bounds.Dy() < 1 {
		return "", nil
	}

	cols := opts.Width
	if cols < 1 {
		cols = DefaultWidth
	}
	if cols > bounds.Dx() {
		cols = bounds.Dx()
	}

	// How many source pixels go into one output column. The grid below works in
	// squares of this size, which is the whole of the resizing.
	//
	// It rounds up rather than down so that cols of them span the entire width.
	// Truncating instead drops the remainder off the right-hand edge, because
	// cols*stepX then stops short of the last pixel: a 300px cover at 34 columns
	// gives stepX 8 and covers 272px, so a tenth of the picture is never read.
	// That squeezes the art horizontally, which is most of why it looked tall
	// and narrow.
	stepX := (bounds.Dx() + cols - 1) / cols
	if stepX < 1 {
		stepX = 1
	}

	// Both modes lay out on one grid. A HalfBlock cell splits its own height in
	// two, so it covers twice the source height of a Mono cell and both land on
	// the same number of rows: the grid carries two sub-rows per output row and
	// each mode reads the half it needs. Stepping x and y by the same amount is
	// what makes a HalfBlock cell square, since its two pixels are each half a
	// cell tall and a cell is twice as tall as it is wide.
	//
	// The row count is worked out from the requested columns and the source's
	// own shape rather than from stepX, so the answer survives being rounded. A
	// terminal cell is about twice as tall as it is wide, so cols columns and
	// rows rows are drawn cols wide by rows*2 tall; setting rows to half the
	// columns times the source height over width makes that the source's aspect
	// ratio exactly. A square picture comes out a square, which is what the
	// height of the picture is for.
	rows := (cols*bounds.Dy() + bounds.Dx()) / (2 * bounds.Dx())
	if rows < 1 {
		rows = 1
	}

	grid := sample(img, bounds, stepX, cols, rows)

	styles := newStyleCache(opts.Color)

	if opts.Mode == HalfBlock {
		return renderBlocks(grid, rows, styles), nil
	}

	ramp, err := rampFor(opts)
	if err != nil {
		return "", err
	}
	return renderMono(grid, rows, ramp, styles), nil
}

// sample resamples img down to the grid the renderers walk, averaging each
// block of source pixels into one cell.
//
// Point sampling would be quicker, and it is what the original did, but a 3000px
// cover dropped to 34 columns is reading one pixel in every 7700 and throwing
// the rest away. On anything with detail in it that aliases into speckle rather
// than a picture, which is the difference between a cover and a broken cover.
func sample(img image.Image, bounds image.Rectangle, stepX, cols, rows int) *image.RGBA {
	grid := image.NewRGBA(image.Rect(0, 0, cols, rows*2))

	for j := range rows * 2 {
		for i := range cols {
			// The last block of a row or column usually runs off the edge. It is
			// pulled back onto the last real pixel rather than left empty,
			// because an empty block has nothing to average and would come back
			// transparent black, painting a false dark line along the bottom and
			// the right.
			block := image.Rect(
				clamp(bounds.Min.X+i*stepX, bounds.Min.X, bounds.Max.X-1),
				clamp(bounds.Min.Y+j*stepX, bounds.Min.Y, bounds.Max.Y-1),
				min(bounds.Min.X+i*stepX+stepX, bounds.Max.X),
				min(bounds.Min.Y+j*stepX+stepX, bounds.Max.Y),
			)
			grid.SetRGBA(i, j, blockAverage(img, block))
		}
	}

	return grid
}

// blockAverage is the mean colour of a rectangle.
//
// It averages the values RGBA hands back, which are premultiplied by alpha.
// For an opaque picture, which is every photograph and every album cover, that
// is a plain mean of the colours. For a transparent one the result stays
// premultiplied and so reads darker than it should, which is the right answer
// for art sitting on a dark panel and the wrong one for art meant to be
// flattened onto something first.
func blockAverage(img image.Image, r image.Rectangle) color.RGBA {
	if r.Empty() {
		return color.RGBA{}
	}

	var rs, gs, bs, as uint64
	var n uint64

	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, ca := img.At(x, y).RGBA()
			rs += uint64(cr)
			gs += uint64(cg)
			bs += uint64(cb)
			as += uint64(ca)
			n++
		}
	}

	if n == 0 {
		return color.RGBA{}
	}

	return color.RGBA{
		R: uint8(rs / n >> 8),
		G: uint8(gs / n >> 8),
		B: uint8(bs / n >> 8),
		A: uint8(as / n >> 8),
	}
}

// renderMono is the original conversion: one ramp character per grid cell, with
// the intensity from the grey version and the colour from the original, so a red
// pixel is dark but still prints red.
func renderMono(grid *image.RGBA, rows int, ramp []rune, styles *styleCache) string {
	var b strings.Builder

	for row := range rows {
		for col := range grid.Bounds().Dx() {
			p := grid.RGBAAt(col, row*2)
			b.WriteString(styles.cell(pack(p), noColor).Render(string(ramp[intensityIndex(intensity(p), ramp)])))
		}
		b.WriteByte('\n')
	}

	return b.String()
}

// renderBlocks walks the grid two sub-rows at a time and draws each pair as one
// cell, the upper sub-row in the foreground and the lower one in the background.
func renderBlocks(grid *image.RGBA, rows int, styles *styleCache) string {
	var b strings.Builder

	for row := range rows {
		if styles.on {
			// Neighbouring cells almost always share a colour, so a run of them
			// is styled once instead of once each. A whole frame drops from tens
			// of kilobytes of escapes to a few, which matters because the frame
			// is rebuilt and rewritten on every repaint.
			var runFg, runBg uint32
			runLen := 0

			for col := range grid.Bounds().Dx() {
				fg, bg := pack(grid.RGBAAt(col, row*2)), pack(grid.RGBAAt(col, row*2+1))

				if runLen > 0 && (fg != runFg || bg != runBg) {
					b.WriteString(styles.cell(runFg, runBg).Render(strings.Repeat(string(blockUpper), runLen)))
					runLen = 0
				}
				runFg, runBg, runLen = fg, bg, runLen+1
			}

			if runLen > 0 {
				b.WriteString(styles.cell(runFg, runBg).Render(strings.Repeat(string(blockUpper), runLen)))
			}
		} else {
			for col := range grid.Bounds().Dx() {
				top := grid.RGBAAt(col, row*2)
				bottom := grid.RGBAAt(col, row*2+1)
				b.WriteRune(blockGlyph(intensity(top), intensity(bottom)))
			}
		}

		b.WriteByte('\n')
	}

	return b.String()
}

// blockGlyph picks a block for two grey values. With no colour there is nothing
// to tell the halves apart, so both lit halves collapse into a full block and
// both dark ones into a space.
func blockGlyph(top, bottom float64) rune {
	topLit, bottomLit := top >= mid, bottom >= mid

	switch {
	case topLit && bottomLit:
		return blockFull
	case !topLit && !bottomLit:
		return ' '
	case topLit:
		return blockUpper
	default:
		return blockLower
	}
}

// intensity is the perceived brightness of a pixel, 0 to 255.
//
// The original scored a pixel with the luminosity weights, 0.2126 red, 0.7152
// green and 0.0722 blue, on the grey version of it. Those weights sum to
// exactly one, so scoring a grey pixel with them returns the grey value again
// and the arithmetic is dropped here. It kept the brightness identical and
// skipped a rounding error per pixel.
func intensity(c color.Color) float64 {
	r, _, _, _ := color.GrayModel.Convert(c).RGBA()
	return float64(r >> 8)
}

// intensityIndex scales an intensity onto the ramp. The ramp is ordered
// darkest first, so the brightest pixel lands on the last character.
func intensityIndex(i float64, ramp []rune) int {
	n := len(ramp) - 1
	if n < 1 {
		return 0
	}
	idx := int(i / 256 * float64(len(ramp)))
	if idx > n {
		idx = n
	}
	return idx
}

func rampFor(opts Options) ([]rune, error) {
	if opts.Ramp == "" {
		return []rune(DefaultRamp), nil
	}
	r := []rune(opts.Ramp)
	if len(r) < 2 {
		return nil, errors.New("asciiart: ramp needs at least two characters")
	}
	return r, nil
}

func isURL(src string) bool {
	return strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://")
}

func clamp(v, lo, hi int) int {
	return min(max(v, lo), hi)
}

// decodeImage reads a picture out of r, refusing anything unreasonably large
// rather than letting a remote server decide how much memory this uses.
func decodeImage(r io.Reader) (image.Image, error) {
	// One byte past the limit is enough to tell that it was passed, without
	// having read the rest of it to find out how far past.
	data, err := io.ReadAll(io.LimitReader(r, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImageBytes {
		return nil, fmt.Errorf("image is over the %d byte limit", maxImageBytes)
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// noColor marks a channel as one that should be left alone. A packed colour
// tops out at 0xffffff, so this cannot collide with a real one.
const noColor = uint32(0xffffffff)

// pack turns a colour into one number the cache can key on. RGBA hands back 16
// bits per channel and art needs 8, so each is shifted down.
func pack(c color.Color) uint32 {
	r, g, b, _ := c.RGBA()
	return (r>>8)<<16 | (g>>8)<<8 | b>>8
}

// styleCache hands out lipgloss styles, one per distinct colour pair, and reuses
// them for the rest of the render. A picture is thousands of cells and each one
// would otherwise rebuild its own style.
type styleCache struct {
	on    bool
	cache map[uint64]lipgloss.Style
}

func newStyleCache(on bool) *styleCache {
	return &styleCache{on: on, cache: map[uint64]lipgloss.Style{}}
}

// cell returns the style for a foreground and background pair. Either may be
// noColor to leave that channel unset.
func (s *styleCache) cell(fg, bg uint32) lipgloss.Style {
	if !s.on {
		return lipgloss.NewStyle()
	}

	key := uint64(fg)<<32 | uint64(bg)
	if st, ok := s.cache[key]; ok {
		return st
	}

	st := lipgloss.NewStyle()
	if fg != noColor {
		st = st.Foreground(lipgloss.Color(hex(fg)))
	}
	if bg != noColor {
		st = st.Background(lipgloss.Color(hex(bg)))
	}
	s.cache[key] = st
	return st
}

func hex(packed uint32) string {
	const digits = "0123456789abcdef"
	out := []byte{'#', 0, 0, 0, 0, 0, 0}
	for i := 6; i > 0; i-- {
		out[i] = digits[packed&0xf]
		packed >>= 4
	}
	return string(out)
}
