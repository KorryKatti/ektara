package asciiart

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRenderNilImage(t *testing.T) {
	if _, err := Render(nil, Options{}); err == nil {
		t.Fatal("want an error for a nil image, got nil")
	}
}

func TestRenderEmptyImage(t *testing.T) {
	got, err := Render(image.NewRGBA(image.Rect(0, 0, 0, 0)), Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "" {
		t.Errorf("a zero-sized image has nothing to draw, got %q", got)
	}
}

func TestRenderShape(t *testing.T) {
	// A white 8x8 block. Every pixel is at the top of the ramp.
	const size = 8
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, color.White)
		}
	}

	got, err := Render(img, Options{Width: size})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// stepX is 1 and stepY is 2, so 8 columns by 4 rows.
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want 4 rows, got %d: %q", len(lines), got)
	}
	for i, line := range lines {
		if len([]rune(line)) != size {
			t.Errorf("row %d: want %d columns, got %d", i, size, len([]rune(line)))
		}
		if strings.TrimSpace(line) == "" {
			t.Errorf("row %d: a white image should not be blank", i)
		}
	}
}

// A terminal cell is about twice as tall as it is wide, so a square picture has
// to come out half as many rows as columns. Any other ratio draws a square cover
// as a tall thin one, which is what it did when the row count was derived from a
// truncated step instead of the source's own shape.
func TestRenderSquareSourceIsSquare(t *testing.T) {
	const size = 300
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, color.White)
		}
	}

	for _, width := range []int{8, 20, 34, 80} {
		got, err := Render(img, Options{Width: width, Mode: HalfBlock})
		if err != nil {
			t.Fatalf("width %d: Render: %v", width, err)
		}
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		if want := width / 2; len(lines) != want {
			t.Errorf("width %d: want %d rows for a square picture, got %d",
				width, want, len(lines))
		}
	}
}

// Every column has to read some of the source. A step rounded down leaves the
// right-hand edge unsampled, which squeezes the picture and loses detail there.
func TestRenderSamplesWholeWidth(t *testing.T) {
	const size = 300
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// the last twenty pixels are the only white ones
			if x >= size-20 {
				img.Set(x, y, color.White)
			} else {
				img.Set(x, y, color.Black)
			}
		}
	}

	got, err := Render(img, Options{Width: 34})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	line := strings.SplitN(got, "\n", 2)[0]
	last := string([]rune(line)[33])
	if last == " " {
		t.Errorf("the right-hand edge was dropped, so the art is squeezed: %q", line)
	}
}

// A width of zero means DefaultWidth, and the image is never scaled up past its
// own pixel count.
func TestRenderWidthClamping(t *testing.T) {
	const size = 4
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, color.White)
		}
	}

	got, err := Render(img, Options{Width: 1000})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	line := strings.SplitN(got, "\n", 2)[0]
	if len([]rune(line)) != size {
		t.Errorf("want the image left at %d columns, got %d", size, len([]rune(line)))
	}
}

func TestRenderDarkAndLight(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.Black) // darkest
	img.Set(1, 0, color.White) // brightest
	img.Set(0, 1, color.Black) // darkest
	img.Set(1, 1, color.White) // brightest

	got, err := Render(img, Options{Width: 2})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.HasPrefix(got, DefaultRamp[:1]) {
		t.Errorf("a black pixel should be the darkest ramp character %q, got %q",
			string([]rune(DefaultRamp)[0]), got)
	}
	if !strings.Contains(got, string([]rune(DefaultRamp)[len([]rune(DefaultRamp))-1])) {
		t.Errorf("a white pixel should be the brightest ramp character, got %q", got)
	}
}

// Colour mode has to escape its output, and the escape must carry the source
// colour rather than the grey used to pick the character.
func TestRenderColor(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.RGBA{R: 0xff, A: 0xff})
		}
	}

	got, err := Render(img, Options{Width: 2, Color: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("colour mode should emit escapes, got %q", got)
	}
	// lipgloss writes truecolor as 38;2;r;g;b rather than a hex triplet.
	if !strings.Contains(got, "38;2;255;0;0") {
		t.Errorf("want the pure red source colour in the output, got %q", got)
	}
}

func TestRenderRamp(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.White)
		}
	}

	got, err := Render(img, Options{Width: 2, Ramp: " .-~"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// A 2x2 image at width 2 steps one pixel across and two down, so the two
	// white pixels that get sampled both land on the brightest ramp character.
	if strings.TrimSpace(got) != "~~" {
		t.Errorf("want only ramp characters, got %q", got)
	}
}

func TestRenderShortRamp(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if _, err := Render(img, Options{Ramp: "@"}); err == nil {
		t.Fatal("want an error for a one-character ramp, got nil")
	}
}

func TestRenderFileMissing(t *testing.T) {
	if _, err := RenderFile("testdata/does-not-exist.png", Options{}); err == nil {
		t.Fatal("want an error for a missing file, got nil")
	}
}

// HalfBlock draws one cell per pair of source rows, so for the same number of
// output lines it samples twice as many rows of pixels as Mono does: one into
// the foreground and one into the background. That is where the extra vertical
// resolution comes from.
func TestHalfBlockSamplesTwiceAsManyRows(t *testing.T) {
	// Every row gets its own grey, so the number of distinct colours in the
	// output is the number of distinct rows that were read.
	const size = 8
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		v := uint8(y * 32)
		for x := 0; x < size; x++ {
			img.Set(x, y, color.Gray{Y: v})
		}
	}

	// Counts both a foreground and a background, since HalfBlock splits each row
	// of cells across the two.
	distinct := func(t *testing.T, opts Options) int {
		t.Helper()
		got, err := Render(img, opts)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		return len(setOf(reColour.FindAllString(got, -1)))
	}

	mono := distinct(t, Options{Width: size, Color: true})
	blocks := distinct(t, Options{Width: size, Color: true, Mode: HalfBlock})

	if mono != size/2 {
		t.Errorf("Mono should sample every other row, got %d of %d rows", mono, size)
	}
	if blocks != size {
		t.Errorf("HalfBlock should sample every row, got %d of %d", blocks, size)
	}
}

var reColour = regexp.MustCompile(`(?:38|48);2;[0-9]+;[0-9]+;[0-9]+`)

func setOf(matches []string) map[string]bool {
	out := make(map[string]bool, len(matches))
	for _, m := range matches {
		out[m] = true
	}
	return out
}

func TestHalfBlockCells(t *testing.T) {
	// Four columns, two rows: one row of cells, each holding a pixel pair.
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.White)
		}
	}

	got, err := Render(img, Options{Width: 4, Mode: HalfBlock})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if got != string(blockFull)+string(blockFull)+string(blockFull)+string(blockFull)+"\n" {
		t.Errorf("want four full blocks, got %q", got)
	}
}

// A lit pixel above a dark one is the upper block, and the other way round is
// the lower one. Without colour these are the only way to tell them apart.
func TestHalfBlockGlyphs(t *testing.T) {
	cases := []struct {
		name        string
		top, bottom color.Color
		want        rune
	}{
		{"both lit", color.White, color.White, blockFull},
		{"both dark", color.Black, color.Black, ' '},
		{"lit above", color.White, color.Black, blockUpper},
		{"lit below", color.Black, color.White, blockLower},
	}
	for _, tc := range cases {
		if got := blockGlyph(intensity(tc.top), intensity(tc.bottom)); got != tc.want {
			t.Errorf("%s: want %q, got %q", tc.name, tc.want, got)
		}
	}
}

// The colour mode is the whole point of the half blocks: the top pixel has to
// land in the foreground and the bottom one in the background.
func TestHalfBlockColor(t *testing.T) {
	// One column, two rows: a red pixel above a blue one.
	img := image.NewRGBA(image.Rect(0, 0, 1, 2))
	img.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	img.Set(0, 1, color.RGBA{B: 0xff, A: 0xff})

	got, err := Render(img, Options{Width: 1, Mode: HalfBlock, Color: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(got, string(blockUpper)) {
		t.Fatalf("want an upper half block, got %q", got)
	}
	if !strings.Contains(got, "38;2;255;0;0") {
		t.Errorf("want the top pixel in the foreground, got %q", got)
	}
	if !strings.Contains(got, "48;2;0;0;255") {
		t.Errorf("want the bottom pixel in the background, got %q", got)
	}
}

// The last cell row has no second row of pixels below it, so it must not read
// outside the image. A transparent black there would paint a dark edge along
// the bottom of every render.
func TestHalfBlockOddHeight(t *testing.T) {
	// Three rows at a step of one, which is one full pair and one leftover.
	img := image.NewRGBA(image.Rect(0, 0, 1, 3))
	for y := 0; y < 3; y++ {
		img.Set(0, y, color.White)
	}

	got, err := Render(img, Options{Width: 1, Mode: HalfBlock, Color: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if strings.Count(got, "\n") != 2 {
		t.Errorf("want 2 rows of cells for 3 pixel rows, got %q", got)
	}
	if !strings.Contains(got, "48;2;255;255;255") {
		t.Errorf("the leftover row should reuse the one above it, not go black, got %q", got)
	}
}

func TestIntensity(t *testing.T) {
	cases := []struct {
		name string
		in   color.Color
		want float64
	}{
		{"black", color.Black, 0},
		{"white", color.White, 255},
		{"grey", color.Gray{Y: 128}, 128},
	}
	for _, tc := range cases {
		if got := intensity(tc.in); got != tc.want {
			t.Errorf("%s: want %v, got %v", tc.name, tc.want, got)
		}
	}
}

func TestHex(t *testing.T) {
	cases := []struct {
		packed uint32
		want   string
	}{
		{0x000000, "#000000"},
		{0xffffff, "#ffffff"},
		{0xff0000, "#ff0000"},
		{0x00ff00, "#00ff00"},
		{0x0000ff, "#0000ff"},
		{0x0a141e, "#0a141e"},
	}
	for _, tc := range cases {
		if got := hex(tc.packed); got != tc.want {
			t.Errorf("want %q, got %q", tc.want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// sources and resizing
// ---------------------------------------------------------------------------

// pngBytes makes a picture for the tests below to serve or write out.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

// RenderSource is the entry point for a source whose kind is not known ahead of
// time, so it has to work out which of the two it has been handed.
func TestRenderSource(t *testing.T) {
	want := pngBytes(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(want)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "cover.png")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	opts := Options{Width: 4}

	fromURL, err := RenderSource(t.Context(), srv.URL+"/cover.png", opts)
	if err != nil {
		t.Fatalf("RenderSource url: %v", err)
	}
	fromFile, err := RenderSource(t.Context(), path, opts)
	if err != nil {
		t.Fatalf("RenderSource file: %v", err)
	}

	if fromURL != fromFile {
		t.Errorf("a url and a file holding the same picture should render alike:\nurl:  %q\nfile: %q", fromURL, fromFile)
	}
}

// A thumbnail address that 404s is a normal thing to hit, and it should say so
// rather than reporting an unrecognised image format.
func TestRenderURLNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := RenderURL(t.Context(), srv.URL, Options{})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("want a 404 reported as such, got %v", err)
	}
}

// A block is averaged rather than point sampled. Four different pixels dropped
// into one column have to come out as their mean, or a large cover turns to
// speckle instead of a picture.
func TestSampleAveragesBlocks(t *testing.T) {
	// Four colours across one row: red, green, blue, white.
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for i, c := range []color.RGBA{
		{R: 0xff, A: 0xff},
		{G: 0xff, A: 0xff},
		{B: 0xff, A: 0xff},
		{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	} {
		img.SetRGBA(i, 0, c)
		img.SetRGBA(i, 1, c)
	}

	// Width 1 makes stepX 4, so the single output column averages all four.
	got, err := Render(img, Options{Width: 1, Color: true, Mode: HalfBlock})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// (255+0+0+255)/4 in each channel.
	for _, want := range []string{"38;2;127;127;127", "48;2;127;127;127"} {
		if !strings.Contains(got, want) {
			t.Errorf("want the averaged colour %q in %q", want, got)
		}
	}
}

// TestBrailleCarriesMoreDetail is the reason Braille exists. In the same number
// of cells it has to resolve more of the picture than HalfBlock does, or it is
// not worth having.
//
// The test uses a picture of a hard edge: a black half and a white half. A half
// block at any width turns that into a flat run of one glyph, because every cell
// on the edge averages to the same value. Braille turns it into a run of dots
// that traces the edge, because the dots inside each cell are compared with each
// other rather than with the whole picture.
func TestBrailleCarriesMoreDetail(t *testing.T) {
	const size = 40

	// left half black, right half white
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			if x < size/2 {
				img.SetRGBA(x, y, color.RGBA{A: 255})
			} else {
				img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			}
		}
	}

	half, err := Render(img, Options{Width: 20, Mode: HalfBlock, Color: true})
	if err != nil {
		t.Fatal(err)
	}
	braille, err := Render(img, Options{Width: 20, Mode: Braille, Color: true})
	if err != nil {
		t.Fatal(err)
	}

	// both must be the same shape on screen, which is the whole point: the same
	// number of rows for the same picture
	halfRows := strings.Count(half, "\n")
	brailleRows := strings.Count(braille, "\n")
	if halfRows != brailleRows {
		t.Errorf("half block gave %d rows and braille gave %d, want the same",
			halfRows, brailleRows)
	}

	// and braille must actually contain braille characters
	dots := 0
	for _, r := range braille {
		if r >= 0x2800 && r <= 0x28ff {
			dots++
		}
	}
	if dots == 0 {
		t.Error("braille mode produced no braille characters")
	}
}

// TestBrailleRunesAreInTheBrailleBlock checks the bit patterns are laid out the
// way Unicode numbers the dots, rather than in some other order that would draw
// a picture reflected or scrambled.
func TestBrailleRunesAreInTheBrailleBlock(t *testing.T) {
	// a 2x4 grid: left column black, right column white. Every dot on the left
	// is brighter than the cell average and every dot on the right is darker, so
	// only the left column should be raised.
	grid := image.NewRGBA(image.Rect(0, 0, 2, 4))
	for y := range 4 {
		grid.SetRGBA(0, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		grid.SetRGBA(1, y, color.RGBA{A: 255})
	}

	got := brailleRune(grid, 0, 0)

	// the left column is dots 1, 2, 3 and 7, which are bits 0, 1, 2 and 6
	const want = 0x2800 | 0x01 | 0x02 | 0x04 | 0x40
	if got != want {
		t.Errorf("brailleRune = U+%04X, want U+%04X", got, want)
	}
}

// TestTrimRemovesTheLetterbox checks the black bars around a 16:9 video in a 4:3
// thumbnail are thrown away, which is the whole reason Trim exists, and that what
// is left is 16:9 rather than a square.
func TestTrimRemovesTheLetterbox(t *testing.T) {
	// the real shape: 480 wide by 360 tall, holding a 480 by 270 picture with a
	// bar of 45 above and below
	const w, h, bar = 480, 360, 45
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if y < bar || y >= h-bar {
				// a bar, which is very dark grey rather than true black because
				// it has been through a video encoder
				img.SetRGBA(x, y, color.RGBA{R: 8, G: 8, B: 8, A: 255})
				continue
			}
			img.SetRGBA(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}

	out := Trim(img)
	b := out.Bounds()

	// the bars are gone, so the 270 rows of picture are all that is left, and
	// 270 rows of 16:9 is 480 wide
	if b.Dx() != 480 || b.Dy() != 270 {
		t.Errorf("got %dx%d, want 480x270: the bars should have been trimmed and the rest kept",
			b.Dx(), b.Dy())
	}

	// and what is left must be the picture, not a bar
	if r, _, _, _ := out.At(0, 0).RGBA(); r < 100*0x101 {
		t.Errorf("the top of the result is still a bar: r=%d of 65535", r)
	}
}

// TestTrimKeepsTheShape checks a picture with no bars is only cropped to 16:9, and
// is not otherwise eaten.
func TestTrimKeepsTheShape(t *testing.T) {
	// a plain 60x30 picture, which is already 2:1 and so wider than 16:9
	img := image.NewRGBA(image.Rect(0, 0, 60, 30))
	for y := range 30 {
		for x := range 60 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(100 + x), G: uint8(100 + y), B: 128, A: 255})
		}
	}

	out := Trim(img)
	b := out.Bounds()

	// 60 wide at 16:9 is 34 tall, and 30 is all there is, so the width comes off
	// instead and the result is 53 by 30
	if b.Dx() != 53 || b.Dy() != 30 {
		t.Errorf("got %dx%d, want 53x30", b.Dx(), b.Dy())
	}
}

// TestRowsForAndWidthForAgree checks the two conversions are inverses, because
// the layout uses one to reserve room and the other to fill it. If they disagree
// the art is either cut off or leaves a gap.
func TestRowsForAndWidthForAgree(t *testing.T) {
	for _, rows := range []int{4, 8, 13, 16, 20, 30} {
		width := WidthFor(rows)
		if got := RowsFor(width); got > rows {
			t.Errorf("WidthFor(%d) = %d, which needs %d rows", rows, width, got)
		}
	}

	// and both are zero for nothing, rather than negative
	if WidthFor(0) != 0 || RowsFor(0) != 0 {
		t.Error("a width or height of zero should stay zero")
	}
}

// TestTrimmedArtIsRectangular checks the drawn art is wider than it is tall, which
// is the point of trimming to 16:9 rather than to a square. A square drawing of a
// video thumbnail is a third of the picture with the sides or the top and bottom
// thrown away.
func TestTrimmedArtIsRectangular(t *testing.T) {
	// a 4:3 frame with bars, as a thumbnail arrives
	const w, h, bar = 120, 90, 11
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if y < bar || y >= h-bar {
				img.SetRGBA(x, y, color.RGBA{A: 255})
				continue
			}
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 2), G: uint8(y * 2), B: 90, A: 255})
		}
	}

	got, err := Render(img, Options{Width: 40, Color: true, Mode: Braille, Trim: true})
	if err != nil {
		t.Fatal(err)
	}

	rows := strings.Count(got, "\n")
	if rows <= 0 {
		t.Fatalf("no art at all: %q", got)
	}
	// 40 columns of 16:9 art is 11 rows, give or take
	if rows > 20 {
		t.Errorf("the art is %d rows tall for 40 columns, which is not 16:9", rows)
	}
	if want := RowsFor(40); rows > want+1 {
		t.Errorf("the art is %d rows but RowsFor(40) says %d", rows, want)
	}
}
