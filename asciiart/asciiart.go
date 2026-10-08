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
	"image/draw"
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

	// Braille puts eight pixels in each cell, two across and four down, using the
	// dot matrix that every Unicode braille character carries. Four times the
	// detail of HalfBlock in the same space, which is the most a terminal will
	// give without being asked to draw a real image.
	//
	// The dots are on or off and the colour is one per cell, so this draws a
	// tinted stipple rather than true colour. On a photograph that reads as far
	// more detail than a half block does, which is the point.
	Braille
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

	// Trim takes a flat black border off the picture before drawing it and crops
	// what is left to the shape of a video thumbnail. It is meant for thumbnails,
	// which are 4:3 frames holding a 16:9 picture with black bars above and
	// below, where a fifth of the picture is bar and the middle is too small to
	// read.
	//
	// See Trim for what it does to a picture that has no bars.
	Trim bool

	// Background is the colour to fill the gaps in the picture with, in Braille
	// mode. A braille character is mostly empty space, so without a background
	// every gap between the dots shows whatever is behind the drawing, which on a
	// terminal with a transparent background is the desktop. Set it to the colour
	// the art is drawn on and the gaps become that colour.
	//
	// It is ignored by the other modes: a half block and a mono character both
	// fill their whole cell, so they have no gaps and no need of one.
	Background color.Color
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

// thumbnailWide and thumbnailTall are the shape of a video thumbnail once its
// black bars are off, which is the shape everything here is drawn at.
//
// It is exported because the caller has to reserve the right number of rows for
// the art before the art arrives, and working the ratio out in two places is how
// the two drift apart.
const (
	thumbnailWide = 16
	thumbnailTall = 9

	// a cell is about twice as tall as it is wide, so a picture that is n cells
	// wide is n cells by 2n on screen. The ratio of the picture is what decides
	// how many of those rows it fills.
	cellTall = 2
)

// RowsFor is how many lines a thumbnail drawn width columns wide takes up.
//
// The caller needs this to reserve room for the art in a fixed height box, and
// it needs it before the art exists, so it is worked out from the shape rather
// than measured off a drawing that has not arrived.
//
// It rounds up, the way the renderer does, so the answer is never smaller than
// what actually gets drawn. Rounding down would leave the art a row taller than
// the room it was given, and the box would be cut off in the middle of the
// picture.
func RowsFor(width int) int {
	if width < 1 {
		return 0
	}
	span := thumbnailWide * cellTall
	return (width*thumbnailTall + span - 1) / span
}

// WidthFor is the other direction: the width to draw a thumbnail at so that it
// takes up rows lines.
//
// It rounds down, the opposite way, so the two are inverses of each other and the
// art never comes out taller than the room it was given.
func WidthFor(rows int) int {
	if rows < 1 {
		return 0
	}
	return rows * thumbnailWide * cellTall / thumbnailTall
}

// Trim takes a flat black border off a picture and crops what is left to the
// shape of a video thumbnail, keeping the middle.
//
// The border is the reason this exists at all. hqdefault is a 4:3 frame holding
// a 16:9 picture with black bars above and below, and measured on an ordinary
// video its top and bottom tenth are pure black, so a fifth of the cover is a
// bar and the real picture is squeezed into the middle. Drawn like that it is
// hard to make out what the picture is at all.
//
// What is left after the bars are off is cropped to 16:9 rather than to a
// square, because that is the shape of the picture and squashing it to a square
// throws away a third of it. A picture with no bars is only cropped to 16:9.
func Trim(img image.Image) image.Image {
	b := img.Bounds()
	if b.Dx() < 1 || b.Dy() < 1 {
		return img
	}

	top, bottom := trimFlatRows(img, b)

	// the widest 16:9 picture that fits between the bars
	height := bottom - top
	if height < 1 {
		return img
	}
	width := height * thumbnailWide / thumbnailTall
	if width > b.Dx() {
		// the picture is taller than 16:9, so the width is the limit and the
		// height comes off instead
		width = b.Dx()
		height = width * thumbnailTall / thumbnailWide
	}
	if width < 1 || height < 1 {
		return img
	}

	// and the middle of it
	left := b.Min.X + (b.Dx()-width)/2
	y := b.Min.Y + top + (bottom-top-height)/2

	out := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(out, out.Bounds(), img, image.Point{X: left, Y: y}, draw.Src)
	return out
}

// trimFlatRows finds the first and last rows that are not a flat black bar, and
// returns them as offsets from the top of the bounds.
//
// A row counts as a bar when every pixel in it is darker than barDark. It is not
// a question of how many dark pixels a row has: a bar has some very dark pixels
// and nothing else, while a dark row of the picture itself has dark pixels and
// slightly less dark ones, and only the first is worth throwing away.
func trimFlatRows(img image.Image, b image.Rectangle) (top, bottom int) {
	// bars are not perfectly black once they have been through a video encoder,
	// so the test is not "is it zero" but "is every pixel in this row this dark"
	dark := uint32(barDark) * 0x101

	isBar := func(y int) bool {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, g, _, _ := img.At(x, y).RGBA(); g > dark {
				return false
			}
		}
		return true
	}

	for top < b.Dy() && isBar(b.Min.Y+top) {
		top++
	}

	for bottom = b.Dy(); bottom > top && isBar(b.Min.Y+bottom-1); bottom-- {
	}

	// a picture that is nothing but bars has nothing left to keep
	if top >= bottom {
		return 0, b.Dy()
	}

	return top, bottom
}

// barDark is how dark a row has to be to count as a letterbox bar. It is not
// zero, because a bar that has been through an encoder is a very dark grey
// rather than true black, and a test of exactly zero would keep it.
const barDark = 24

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

	// The crop happens before anything is measured, so every number below is
	// worked out from the picture that is actually going to be drawn.
	if opts.Trim {
		img = Trim(img)
		bounds = img.Bounds()
	}

	cols := opts.Width
	if cols < 1 {
		cols = DefaultWidth
	}
	if cols > bounds.Dx() {
		cols = bounds.Dx()
	}

	// Every mode lays out on one grid, and every cell holds more than one source
	// pixel. How many is the mode's business, and it is worked out below.
	//
	// The row count comes from the requested columns and the source's own shape,
	// not from the size of a source block, so the answer survives being rounded.
	// A terminal cell is about twice as tall as it is wide, so cols columns and
	// rows rows are drawn cols wide by rows*2 tall; setting rows to half the
	// columns times the source height over width makes that the source's aspect
	// ratio exactly. A square picture comes out a square, which is what the
	// height of the picture is for.
	rows := (cols*bounds.Dy() + bounds.Dx()) / (2 * bounds.Dx())
	if rows < 1 {
		rows = 1
	}

	// How many source pixels each mode squeezes into one cell, across and down.
	// Mono and HalfBlock take one across, because a cell has one character in it
	// and one colour; HalfBlock takes two down because a cell has a foreground
	// and a background colour. Braille takes two across and four down, because a
	// braille character is a two by four dot matrix.
	//
	// A braille cell is therefore square in source pixels, the same as a half
	// block cell, which is why one block size suits every mode: the cell is
	// twice as tall as it is wide on screen either way.
	across, down := 1, 2
	if opts.Mode == Braille {
		across, down = 2, 4
	}

	grid := sample(img, bounds, cols*across, rows*down)

	styles := newStyleCache(opts.Color)

	switch opts.Mode {
	case HalfBlock:
		return renderBlocks(grid, rows, styles), nil
	case Braille:
		// the background the gaps between the dots are filled with, which is
		// nothing at all unless the caller said what the art sits on
		bg := uint32(noColor)
		if opts.Background != nil {
			bg = pack(opts.Background)
		}
		return renderBraille(grid, rows, styles, bg), nil
	}

	ramp, err := rampFor(opts)
	if err != nil {
		return "", err
	}
	return renderMono(grid, rows, ramp, styles), nil
}

// sample resamples img down to a grid of cols by rows, averaging each block of
// source pixels into one grid cell.
//
// Point sampling would be quicker, and it is what the original did, but a 3000px
// cover dropped to 34 columns is reading one pixel in every 7700 and throwing the
// rest away. On anything with detail in it that aliases into speckle rather than
// a picture, which is the difference between a cover and a broken cover.
//
// cols and rows are the size of the grid in source pixels, which is not the same
// as the number of cells on screen: a cell holds more than one pixel, and how
// many is the renderer's business.
func sample(img image.Image, bounds image.Rectangle, cols, rows int) *image.RGBA {
	grid := image.NewRGBA(image.Rect(0, 0, cols, rows))

	// How many source pixels go into one grid cell, rounded up so that the grid
	// spans the whole picture. Truncating instead drops the remainder off the
	// right-hand edge, because cols*step then stops short of the last pixel: a
	// 300px cover at 34 columns gives step 8 and covers 272px, so a tenth of the
	// picture is never read. That squeezes the art horizontally, which is most of
	// why it looked tall and narrow.
	//
	// The same number is used across and down, so each cell covers a square of
	// the source. The caller picks cols and rows so that those squares come out
	// in the right proportion for the cell shape.
	step := (bounds.Dx() + cols - 1) / cols
	if step < 1 {
		step = 1
	}

	for j := range rows {
		for i := range cols {
			// The last block of a row or column usually runs off the edge. It is
			// pulled back onto the last real pixel rather than left empty,
			// because an empty block has nothing to average and would come back
			// transparent black, painting a false dark line along the bottom and
			// the right.
			block := image.Rect(
				clamp(bounds.Min.X+i*step, bounds.Min.X, bounds.Max.X-1),
				clamp(bounds.Min.Y+j*step, bounds.Min.Y, bounds.Max.Y-1),
				min(bounds.Min.X+i*step+step, bounds.Max.X),
				min(bounds.Min.Y+j*step+step, bounds.Max.Y),
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

// renderBraille draws the picture with braille characters.
//
// A braille character is a two by four grid of dots, so each one carries eight
// source pixels instead of the two a half block carries. That is four times the
// detail in the same space, and it is still ordinary text, so a redraw cannot
// lose it the way a sixel or iTerm2 image is lost.
//
// Each dot is either on or off, so brightness decides which, and the colour of
// the cell is the average of the eight pixels in it. The result is a tinted
// stipple rather than true colour, and on a photograph it carries far more of
// the picture than a half block does.
func renderBraille(grid *image.RGBA, rows int, styles *styleCache, bg uint32) string {
	cols := grid.Bounds().Dx() / 2
	var b strings.Builder

	for row := range rows {
		// where this row's four lines of dots start in the grid
		top := row * 4

		for col := range cols {
			// the eight pixels of this cell, averaged for the colour and each
			// read separately for the dots
			fg := brailleCell(grid, col*2, top, styles.on)

			if styles.on {
				b.WriteString(styles.cell(pack(fg), bg).Render(string(brailleRune(grid, col*2, top))))
				continue
			}
			b.WriteRune(brailleRune(grid, col*2, top))
		}

		b.WriteByte('\n')
	}

	return b.String()
}

// brailleCell is the average colour of the eight pixels in one braille cell, or
// nil when colour is off and the caller does not need it.
func brailleCell(grid *image.RGBA, left, top int, want bool) color.RGBA {
	if !want {
		return color.RGBA{}
	}

	// the cell is two pixels across and four down
	var r, g, bl uint64
	for dy := range 4 {
		for dx := range 2 {
			c := grid.RGBAAt(left+dx, top+dy)
			r += uint64(c.R)
			g += uint64(c.G)
			bl += uint64(c.B)
		}
	}

	return color.RGBA{R: uint8(r / 8), G: uint8(g / 8), B: uint8(bl / 8), A: 255}
}

// brailleRune builds the one braille character for a cell.
//
// The dots are numbered the way Unicode numbers them, which is not row by row
// across: the left column is dots 1, 2, 3 and 7 from the top, and the right
// column is 4, 5, 6 and 8. Each dot is a bit in the character, and the character
// itself starts at U+2800, so a cell with every dot on is U+28FF.
//
// A dot is on when its pixel is brighter than the average of the whole cell.
// That is what turns a smooth picture into a stipple: the bright parts of each
// cell get dots and the dark parts do not.
func brailleRune(grid *image.RGBA, left, top int) rune {
	// the brightness the cell is judged against
	var total float64
	for dy := range 4 {
		for dx := range 2 {
			total += intensity(grid.RGBAAt(left+dx, top+dy))
		}
	}
	average := total / 8

	// the bit for each dot, in the order the dots are numbered. Read as a grid it
	// is 1 4 / 2 5 / 3 6 / 7 8.
	bits := [...][2]int{
		{0, 0x01}, {1, 0x08},
		{2, 0x02}, {3, 0x10},
		{4, 0x04}, {5, 0x20},
		{6, 0x40}, {7, 0x80},
	}

	dots := 0
	for _, bit := range bits {
		dx, dy := bit[0]%2, bit[0]/2
		if intensity(grid.RGBAAt(left+dx, top+dy)) >= average {
			dots |= bit[1]
		}
	}

	return brailleBase + rune(dots)
}

// brailleBase is the first braille character, the one with no dots raised. Every
// other braille character in the block is this plus a bit pattern.
const brailleBase = 0x2800

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
