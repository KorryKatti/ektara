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
