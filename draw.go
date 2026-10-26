package main

// The drawing: the styles, and the functions that turn the model into the lines
// that go on the terminal. Every function here answers a string and reads
// nothing but the model.
//
// The screen is one fixed shape, the same on every mode:
//
//	+--------------------------------------------------+
//	| Ektara                                          |  <- title, one line
//	+--------+-----------------------------------------+
//	| menu   |  whatever this mode is about            |
//	| on the |                                         |
//	| left   |                                         |
//	+--------+-----------------------------------------+
//	| > now playing  --------  2:31 / 4:12            |  <- player, four lines
//	+--------------------------------------------------+
//
// Only the middle right box changes from mode to mode.

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	_ "image/jpeg" // so image.Decode can read the cover, which is a jpeg
	_ "image/png"
	"math/rand"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"ektara/asciiart"
)

// The heights and widths, as plain numbers so the arithmetic can be read off the
// drawing above.
const (
	titleHeight = 1

	// The bar along the bottom, counting the line of border above it: five lines
	// of text and that border. lipgloss counts the border as part of the height,
	// so getting this wrong leaves the bottom row unpainted.
	playerBlockHeight = 6

	// The menu column, padding included.
	sidebarWidth = 24

	// The column on the right, padding included. A little wider than the menu
	// because a song title needs more room than a one word menu entry.
	queueWidth = 30

	// The least room the middle box is worth having. Below this it has to start
	// clipping its own text, so a side column is dropped instead.
	narrowestContent = 30
)

// Whether there is room for the menu down the left as well as the content beside
// it. On a narrow terminal the menu goes away.
func (m model) showSidebar() bool {
	if m.width == 0 {
		return true // before the size is known: the first frame is nearly always normal
	}
	return m.width >= sidebarWidth+narrowestContent
}

// Whether there is room for the up-next column once the menu and the content have
// both been served. The menu is asked for first and this gives way.
func (m model) showQueue() bool {
	if m.width == 0 {
		return true
	}
	if !m.showSidebar() {
		return false
	}
	return m.width >= sidebarWidth+narrowestContent+queueWidth
}

// How many columns the middle box gets. Until the terminal has said how big it is
// this is a made up number.
func (m model) contentWidth() int {
	if m.width == 0 {
		return 60
	}
	w := m.width
	if m.showSidebar() {
		w -= sidebarWidth
	}
	if m.showQueue() {
		w -= queueWidth
	}

	// A floor, so the box is never so narrow that a two letter name is the longest
	// thing that fits.
	if w < 10 {
		w = 10
	}
	if w > m.width {
		return m.width
	}
	return w
}

// How many lines the middle box has, once the title and the player bar have taken
// theirs. The floor is one, because the player bar cannot shrink.
func (m model) bodyHeight() int {
	if m.height == 0 {
		return 20
	}
	h := m.height - titleHeight - playerBlockHeight
	if h < 1 {
		return 1
	}
	return h
}

// How many lines a screen may actually write: the box, less its own padding above
// and below, less whatever the errors take at the bottom. Every screen sizes
// itself from this and the frame draws the box at bodyHeight, so they have to
// agree.
func (m model) drawRows() int {
	rows := m.bodyHeight() - 2 - m.errorRows()
	if rows < 1 {
		return 1
	}
	return rows
}

// The one colour that means "this is the important thing on screen": the title
// bar, and the row the cursor is on.
const accent = "#7C5CFF"

// Kept in one place so the colours can be changed without hunting through the
// drawing code.
var (
	titleStyle = lipgloss.NewStyle().
			Height(titleHeight).
			Padding(0, 1).
			Bold(true).
			Foreground(lipgloss.Color("#0B0B0F")).
			Background(lipgloss.Color(accent))

	sidebarStyle = lipgloss.NewStyle().
			Width(sidebarWidth).
			Padding(1, 1).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#181818"))

	// The middle box. The cover art needs this background too, because a braille
	// character is mostly gaps between its dots: without one of its own every gap
	// shows whatever is behind the drawing.
	contentPanel = lipgloss.Color("#121212")

	contentStyle = lipgloss.NewStyle().
			Padding(1, 2).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(contentPanel)

	// The bar along the bottom. The border needs this as well as the text: a border
	// with no background of its own leaves the whole row showing the terminal's
	// own background as a bright band across the bottom of the screen.
	playerPanel = lipgloss.Color("#181818")

	playerStyle = lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(playerPanel).
			BorderTop(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("#333333")).
			BorderBackground(playerPanel)

	// The column on the right, the same near black as the menu on the left.
	queuePanel = lipgloss.Color("#181818")

	queueStyle = lipgloss.NewStyle().
			Width(queueWidth).
			Padding(1, 1).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(queuePanel)

	titleTextStyle = lipgloss.NewStyle().Bold(true)
	dimStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#808080"))
	cursorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color(accent))
	errorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B"))
	greetingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#C4B5FD"))
)

// fg writes s in the style's colour without closing the colour afterwards.
//
// A lipgloss style ends every run of text with a full reset, and a full reset takes
// the background with it, so whatever comes after on the same line goes back to
// the terminal's own background. The reset goes back at the very end of the whole
// frame instead, once, in frame().
func fg(style lipgloss.Style, s string) string {
	if s == "" {
		return ""
	}
	return strings.TrimRight(style.Render(s), reset)
}

// Puts every attribute back to its default.
const reset = "\x1b[m"

// A reset at the end of every line of a block of text.
//
// fg() leaves the colour open so a box background survives to the end of the
// line, which does not work across a line: lipgloss word-wraps a block before
// styling it and its wrapper re-states any style still open at a newline.
//
// The reset goes before the newline rather than after, so it closes the run of
// text rather than the padding lipgloss adds afterwards.
func closeLines(s string) string {
	if !strings.Contains(s, "\x1b[") {
		return s
	}

	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = line + reset
	}
	return strings.Join(lines, "\n")
}

// The three fixed parts around whatever the current mode drew. Every screen
// function below ends by calling this, so no screen has to remember that the
// title, the sidebar and the player exist.
//
// The errors go on here rather than on each screen, because the box is cut off at
// the bottom, so anything a screen drew last is the first thing cut.
func (m model) frame(content string) string {
	title := titleStyle.Width(m.width).Render("  Ektara")

	// Everything drawn goes through closeLines on its way into a box. Doing it here
	// means a screen cannot forget.

	panel := contentStyle.
		Width(m.contentWidth()).
		Height(m.bodyHeight()).
		MaxHeight(m.bodyHeight()).
		Render(closeLines(content + m.errors()))

	// The side columns are beside the content when there is room and gone when
	// there is not, so the content always gets the width it asked for.
	body := panel
	switch {
	case m.showQueue():
		sidebar := sidebarStyle.
			Height(m.bodyHeight()).
			MaxHeight(m.bodyHeight()).
			Render(closeLines(m.drawSidebar()))

		queue := queueStyle.
			Height(m.bodyHeight()).
			MaxHeight(m.bodyHeight()).
			Render(closeLines(m.drawQueue()))

		body = lipgloss.JoinHorizontal(lipgloss.Top, sidebar, panel, queue)

	case m.showSidebar():
		sidebar := sidebarStyle.
			Height(m.bodyHeight()).
			MaxHeight(m.bodyHeight()).
			Render(closeLines(m.drawSidebar()))

		body = lipgloss.JoinHorizontal(lipgloss.Top, sidebar, panel)
	}

	player := playerStyle.
		Width(m.width).
		Height(playerBlockHeight).
		Render(closeLines(m.drawPlayerBar()))

	// The one reset for the whole frame, at the very end.
	return lipgloss.JoinVertical(lipgloss.Left, title, body, player) + reset
}

// The menu, down the left: the six choices, with the marker on the row the cursor
// is on. The cursor only moves on the menu screen, so elsewhere there is no
// marker.
func (m model) drawSidebar() string {

	var b strings.Builder

	b.WriteString(fg(dimStyle, "MENU") + "\n\n")

	for i, text := range menuItems {
		marker := "  "
		line := text
		if i == m.menuCursor && m.mode == modeMenu {
			marker = fg(cursorStyle, "▸ ")
			line = fg(cursorStyle, text)
		}
		b.WriteString(marker + line + "\n")
	}

	return b.String()
}

// The column on the right: what is playing now, and what plays after it. Always
// on screen when there is room.
func (m model) drawQueue() string {
	var b strings.Builder

	// before anything has played there is nothing to queue up
	if len(m.queue) == 0 {
		b.WriteString(fg(dimStyle, "UP NEXT") + "\n\n")
		b.WriteString(fg(dimStyle, "Nothing queued."))
		return b.String()
	}

	b.WriteString(fg(dimStyle, "NOW PLAYING") + "\n\n")

	// m.index is the track playing, not the first one: a and d move it along the
	// queue, so the row at the top follows them
	b.WriteString(m.queueLine(m.index, true) + "\n\n")

	b.WriteString(fg(dimStyle, fmt.Sprintf("UP NEXT (%d)", len(m.queue)-1)) + "\n\n")

	// the rest, with the current track kept in the middle of the window
	start, rows := queueWindow(m.queue, m.index, m.queueRows())
	for i := range rows {
		if start+i == m.index {
			continue // already drawn at the top as now playing
		}
		b.WriteString(m.queueLine(start+i, false) + "\n")
	}

	return b.String()
}

// One row of the queue column: a marker on the one playing, and the title clipped.
func (m model) queueLine(i int, nowPlaying bool) string {
	marker := "  "
	if nowPlaying {
		marker = fg(cursorStyle, "▸ ")
	}

	// the position in the queue, so the list can be counted
	number := fg(dimStyle, fmt.Sprintf("%2d. ", i+1))

	return marker + number + clip(m.queue[i].Title, queueWidth-8)
}

// The bar along the bottom. Always drawn, even with nothing playing, so the screen
// keeps the same shape from the menu onwards.
//
// Also where every notice is shown, on every screen.
func (m model) drawPlayerBar() string {
	// Clip lines to the bar's width to prevent wrapping and pushing content off-screen.
	inside := m.width - 4 // two for the padding, two to spare

	var b strings.Builder

	// Show loading status on the bar so it doesn't look frozen, and limit it to five lines to keep the screen visible
	if m.track.Title != "" && m.loading {
		b.WriteString(m.centred(clip(m.track.Title, inside), inside) + "\n")
		b.WriteString(m.drawPlayerKeys(inside) + "\n")
		b.WriteString(m.drawTransport(inside) + "\n")
		b.WriteString(m.centred("esc back", inside))
		return b.String()
	}

	// Before anything has played there is no track, so the bar says so rather than
	// showing a row of zeroes.
	if m.track.Title == "" {
		b.WriteString("Nothing playing\n\n")
		b.WriteString(m.drawPlayerKeys(inside) + "\n")
		b.WriteString(m.drawTransport(inside) + "\n")
		b.WriteString(clip(m.notice, inside))
		return b.String()
	}

	// The title first and centred, because it is the one thing on the bar that is
	// about the music rather than about the controls.
	b.WriteString(m.centred(m.track.Title, inside) + "\n")

	// The icon says whether sound is coming out, because the elapsed time carries
	// on while paused and that on its own looks like a broken bar.
	icon := "▶"
	if m.paused {
		icon = "❚❚"
	}

	times := fmt.Sprintf("%s / %s", clock(m.elapsed), clock(m.length))
	row := fmt.Sprintf("%s  %s  %s", icon, seekBar(m.elapsed, m.length, m.barWidth()), times)
	b.WriteString(m.centred(row, inside) + "\n")

	// Below the bar: the keys, then the volume and the transport buttons.
	b.WriteString(m.drawPlayerKeys(inside) + "\n")
	b.WriteString(m.drawTransport(inside) + "\n")

	// the last line is for whatever the player has just been told, such as
	// "volume 60%", or the queue count when there is nothing newer
	last := ""
	if len(m.queue) > 1 {
		last = fmt.Sprintf("queue: %d tracks", len(m.queue))
	}
	if m.notice != "" {
		last = m.notice
	}
	b.WriteString(m.centred(last, inside))

	return b.String()
}

// The line of key hints under the progress bar.
//
// The text is centred first and coloured afterwards, and the order matters: a
// full reset clears the background as well as the foreground, so styling first
// left the padding spaces with no background at all.
func (m model) drawPlayerKeys(inside int) string {
	return fg(dimStyle, m.centred(clip(m.keyHints(), inside), inside))
}

// The line under the keys: the volume on the left, previous and next on the right.
//
// The buttons name the key that presses them rather than being icons, so the row
// explains itself and every character in it is one column wide.
func (m model) drawTransport(inside int) string {
	// no speaker glyph: a speaker is one of the wide emoji and takes two columns
	volume := fmt.Sprintf("vol %s %3d%%", volumeBar(m.soundVolume(), 10), int(m.volume*100+0.5))
	if m.muted {
		volume = fmt.Sprintf("vol %s%3d%% muted", volumeBar(0, 10), int(m.volume*100+0.5))
	}

	buttons := "prev (a)   next (d)"

	// A narrow terminal has room for the volume or the buttons but not both, and
	// the volume is the one worth keeping.
	gap := inside - lipgloss.Width(volume) - lipgloss.Width(buttons)
	if gap < 1 {
		return clip(volume, inside)
	}

	return clip(volume+strings.Repeat(" ", gap)+buttons, inside)
}

// s in the middle of a line width columns wide.
//
// Clips first, and centres with spaces rather than lipgloss, so the result is a
// plain string of a known width.
func (m model) centred(s string, width int) string {
	s = clip(s, width)

	space := width - lipgloss.Width(s)
	if space <= 0 {
		return s
	}

	left := space / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", space-left)
}

// A bar for the volume, so the level can be seen rather than read.
//
// Light and dark shades, which are one column wide in every terminal. The full
// block is not: East Asian ambiguous, so it takes two columns in some of them.
func volumeBar(level float64, width int) string {
	if width < 1 {
		return ""
	}
	if level < 0 {
		level = 0
	}
	if level > 1 {
		level = 1
	}

	full := int(level*float64(width) + 0.5)
	if full > width {
		full = width
	}
	if full < 0 {
		full = 0
	}

	return strings.Repeat("▓", full) + strings.Repeat("░", width-full)
}

// How wide the progress bar can be. It shares a line with the icon and the two
// clock times, so it gets whatever is left over.
func (m model) barWidth() int {
	// 2 for the padding, 2 for the icon, 4 for the spaces around the bar, and
	// 5 each for the two times and the " / " between them
	w := m.width - 2 - 2 - 4 - 5 - 3 - 5
	if w < 10 {
		return 10
	}
	return w
}

// The first screen: the greeting, the cover, and the recently played list. The
// choices are in the sidebar, so they are not repeated here.
func (m model) drawMenu() string {
	var b strings.Builder

	// The greeting is clipped: one line in the pool is long enough to wrap on a
	// narrow terminal, and a wrapped line takes a row the box did not give it.
	b.WriteString(fg(greetingStyle, clip(m.greeting, m.contentWidth()-6)) + "\n\n")
	b.WriteString(coverArt(artWidthFor(m.menuArtRows(), m.contentWidth())) + "\n\n")

	b.WriteString(m.drawHomeRecent())

	return m.frame(b.String())
}

// The "recently played" block on the first screen: the history the program was
// started with, newest first, cut to whatever rows are left over after the cover.
func (m model) drawHomeRecent() string {
	if len(m.recent) == 0 {
		return fg(dimStyle, "Nothing played yet. Pick Download or Offline to start.")
	}

	rows := m.drawRows() - m.menuArtRows() - 3
	if rows < 1 {
		return ""
	}

	var b strings.Builder
	b.WriteString(fg(titleTextStyle, "RECENTLY PLAYED") + "\n")

	n := len(m.recent)
	if n > rows {
		n = rows
	}

	for _, t := range m.recent[:n] {
		b.WriteString("  " + clip(t.Title, m.contentWidth()-6) + "\n")
	}

	// say how much was left out, so a short list is not mistaken for all of it
	if len(m.recent) > n {
		b.WriteString(fg(dimStyle, fmt.Sprintf("  and %d more", len(m.recent)-n)) + "\n")
	}

	return b.String()
}

// How many rows of cover the first screen has left once the greeting and the
// blank lines around it have had theirs. Four are spoken for: the greeting, two
// blanks, and one for the gap before the next thing.
//
// Capped at half the box, because the other half is the recently played list.
func (m model) menuArtRows() int {
	rows := m.artRows() - 4

	if half := m.artRows() / 2; rows > half {
		rows = half
	}
	if rows < 0 {
		return 0
	}
	return rows
}

// How many rows there are to draw a cover in, before anything else in the box has
// taken its share.
//
// Deliberately not drawRows, which also counts a notice: notices come from every
// key press, so counting one would resize the cover every time the volume moved.
func (m model) artRows() int {
	rows := m.bodyHeight() - 2 - m.errorRows()
	if rows < 1 {
		return 1
	}
	return rows
}

// Turns the rows the art is allowed into the width to draw it at.
//
// A thumbnail is 16:9, so on screen it is wider than it is tall. The ratio lives
// in the asciiart package, which is where the picture is cropped and drawn.
func artWidthFor(rows, contentWidth int) int {
	w := asciiart.WidthFor(rows)

	// a cap, so a tall terminal does not get a picture wider than the screen, and
	// a floor, so a narrow one does not get a picture with no room for it
	if w > 64 {
		w = 64
	}
	if w > contentWidth-6 {
		w = contentWidth - 6
	}
	if w < 8 {
		return 0 // too narrow for a picture worth drawing, so nothing is drawn
	}
	return w
}

// The command that fetches and draws the cover for the track playing now, at the
// size the screen wants it. Asked for whenever the track or the terminal changes.
func (m model) wantArt() tea.Cmd {
	if m.track.ID == "" {
		return nil
	}
	return artCmd(m.track, m.artWidth())
}

// The width the cover should be drawn at on the current screen: the player screen
// gives it half the box, the menu screen the lot of it.
func (m model) artWidth() int {
	if m.mode == modePlaying {
		return artWidthFor(m.playerArtRows(), m.contentWidth())
	}
	return artWidthFor(m.menuArtRows(), m.contentWidth())
}

// The fallback cover, kept in the binary so the first screen has a picture to show
// before anything is playing.
//
//go:embed assets/cover.jpg
var coverJPG []byte

// The drawn fallback, one copy per width it has been drawn at. The screen is
// redrawn ten times a second and each redraw would otherwise turn the picture into
// text all over again.
var cover = struct {
	sync.Mutex
	byWidth map[int]string
}{byWidth: map[int]string{}}

// The fallback cover drawn to the given width, or nothing at all when there is no
// room for one.
func coverArt(width int) string {
	if width < 8 {
		return ""
	}

	cover.Lock()
	defer cover.Unlock()

	if art, ok := cover.byWidth[width]; ok {
		return art
	}

	// a picture that will not draw should leave a gap, not stop the program
	art := "[no cover]"
	if img, _, err := image.Decode(bytes.NewReader(coverJPG)); err == nil {
		if drawn, err := asciiart.Render(img, asciiart.Options{
			Width:      width,
			Color:      true,
			Mode:       asciiart.Braille,
			Trim:       true,
			Background: contentPanel,
		}); err == nil {
			art = drawn
		}
	}

	cover.byWidth[width] = art
	return art
}

// The cover of the track playing, drawn as text, or the empty string if it has not
// arrived yet.
//
// Only shown when it was drawn for this track at this width, so a drawing that was
// on its way when the track changed is not shown against the new one.
func (m model) trackArt() string {
	if m.art == "" {
		return ""
	}
	if m.artKey != artKey(m.track, m.artWidth()) {
		return ""
	}
	return m.art
}

// The typing screen. The same box for a name and for a number; the prompt says
// which.
func (m model) drawSearch() string {
	prompt := "Search YouTube for: "
	if m.askingCount {
		prompt = "How many tracks? "
	}

	var b strings.Builder
	b.WriteString(fg(titleTextStyle, prompt) + "\n\n")
	if m.askingCount {
		b.WriteString("  " + fg(cursorStyle, m.searchInput) + "▌\n\n")
	} else {
		b.WriteString("  " + m.searchField("song:  ", m.searchInput, m.searchFocus == 0) + "\n")
		b.WriteString("  " + m.searchField("artist:", m.searchArtist, m.searchFocus == 1) + fg(dimStyle, " optional") + "\n\n")
	}

	// while the search is out, show that something is happening
	if m.searching {
		b.WriteString("  " + spinFrame(m.spin) + " Searching...\n")
	} else if m.askingCount {
		b.WriteString(fg(dimStyle, "enter confirm, backspace erase, esc back") + "\n")
	} else {
		b.WriteString(fg(dimStyle, "enter confirm, tab song/artist, backspace erase, esc back") + "\n")
	}

	return m.frame(b.String())
}

// One search field, with the cursor when it has the focus.
func (m model) searchField(label, value string, focused bool) string {
	if focused {
		return fg(dimStyle, label+" ") + fg(cursorStyle, value) + "▌"
	}
	return fg(dimStyle, label+" ") + value
}

// The busy animation.
var spinFrames = []string{"|", "/", "-", "\\"}

// The frame for a given tick count. The remainder keeps it inside the list, so the
// counter can grow forever.
func spinFrame(n int) string {
	return spinFrames[n%len(spinFrames)]
}

// The heading for whichever list is showing.
func (m model) itemsTitle() string {
	switch m.mode {
	case modeResults:
		return "Results"
	case modeFiles:
		return "Files in your library"
	case modeHistory:
		return "History"
	}
	return ""
}

func (m model) drawItems() string {
	var b strings.Builder
	b.WriteString(fg(titleTextStyle, m.itemsTitle()) + "\n\n")

	// Only the rows near the cursor are drawn. The history can be hundreds of songs
	// long, so the list scrolls instead.
	start, count := listWindow(len(m.items), m.itemCursor, m.listRows())

	for i := 0; i < count; i++ {
		it := m.items[start+i]

		// Clipped before it is coloured, never after: clipping a coloured string
		// cuts through the middle of an escape sequence.
		title := clip(it.title, m.contentWidth()-6)

		marker := "  "
		if start+i == m.itemCursor {
			marker = fg(cursorStyle, "▸ ")
			title = fg(cursorStyle, title)
		}
		b.WriteString(marker + title + "\n")
	}

	// A count, so it is obvious the list continues past the bottom. Only shown when
	// there is more than fits, because "1 of 1" is just noise.
	//
	// The blank line is written outside the colour: a styled string that starts
	// with a newline puts its colour code at the end of the line above.
	if len(m.items) > count {
		b.WriteString("\n")
		b.WriteString(fg(dimStyle, fmt.Sprintf("  showing %d-%d of %d",
			start+1, start+count, len(m.items))) + "\n")
	}

	b.WriteString("\n" + fg(dimStyle, "up/down move, enter play, e add to queue, esc back") + "\n")

	return m.frame(b.String())
}

// How many list rows fit. The box spends five lines on the padding, the heading,
// the blank line under it, the blank line above the key hint and the hint itself,
// and two more on the count.
//
// The floor is one rather than three, because on a short terminal the box itself
// may only have a line or two.
func (m model) listRows() int {
	n := m.drawRows() - 7
	if n < 1 {
		return 1
	}
	return n
}

// Picks the range of rows to draw, keeping the cursor inside it.
//
// total is how many rows there are, cursor which one is highlighted, max how many
// fit. Returns the index of the first row drawn and how many rows are in the
// window.
func listWindow(total, cursor, max int) (start, rows int) {
	if total <= max {
		return 0, total
	}

	// start half a screen above the cursor, so it sits near the middle
	start = cursor - max/2
	if start < 0 {
		start = 0
	}
	if start > total-max {
		start = total - max
	}
	return start, max
}

// The full screen for a track that is playing. The bar along the bottom says the
// same thing in fewer lines, so this is where the cover goes.
func (m model) drawPlayer() string {
	// the loading screen is a different box, so a slow track says plainly that it
	// is still starting
	if m.loading {
		var b strings.Builder
		b.WriteString(fg(titleTextStyle, "Loading "+m.track.Title) + "\n\n")
		return m.frame(b.String())
	}

	// This screen is the cover and where the track came from. The keys, the volume
	// and the transport are on the bar along the bottom, and the queue is in the
	// column on the right.
	//
	// The picture arrives from the network a moment after the track does, so this
	// is usually an empty gap for the first second. Nothing is shown in that gap
	// rather than the fallback picture: the fallback next to a real song would say
	// the wrong thing about which song it is.
	var b strings.Builder

	b.WriteString(fg(titleTextStyle, "♪ "+m.track.name()) + "\n")
	b.WriteString(fg(dimStyle, clip(m.metaLine(), m.contentWidth()-6)) + "\n")
	b.WriteString(fg(dimStyle, clip(m.subtitle(), m.contentWidth()-6)) + "\n\n")

	b.WriteString(m.trackArt() + "\n\n")

	return m.frame(b.String())
}

// How many rows of cover the player screen has left.
//
// Five lines are spoken for before the art: the title, the details, the link
// under it, and the two blank lines. The art then takes what is left, capped at
// half the box so it does not become a wall of dots on a tall terminal.
func (m model) playerArtRows() int {
	rows := m.artRows() - 5

	if half := m.artRows() / 2; rows > half {
		rows = half // the picture is never more than half the screen
	}
	if rows < 0 {
		return 0
	}
	return rows
}

// How many lines the queue column spends on the tracks still to come: a heading, a
// blank line, and the tracks themselves.
//
// Zero when there is nothing queued, and at most five tracks however long the
// queue is, because a column that runs the height of the screen is not a list any
// more.
func (m model) queueRows() int {
	if len(m.queue) <= 1 {
		return 0
	}
	n := len(m.queue) - 1 // the one playing is not in this list
	if n > 5 {
		n = 5
	}
	return n + 2
}

// Whatever has gone wrong and is still true: the last failure, which stays until
// something replaces it. The frame puts this on the bottom of the middle box.
func (m model) errors() string {
	if m.lastError == "" {
		return ""
	}
	return "\n" + fg(errorStyle, clip(m.lastError, m.contentWidth()-4))
}

// How many lines errors() will draw: a blank line and the message, so two, or
// none when there is nothing to say.
//
// Only a failure is counted. A notice goes in the player bar, so it costs the
// middle box nothing and the cover does not change size on every key press.
func (m model) errorRows() int {
	if m.lastError == "" {
		return 0
	}
	return 2
}

// The second line of the player: the youtube link for a stream, and the file it
// came from for a local mp3.
func (m model) subtitle() string {
	if m.track.Stream {
		return m.track.PageURL
	}
	return m.track.Filename
}

// The line under the title: who made it, what it is on, what it sounds like.
//
// Drawn whether there is anything to say or not, because the line takes up the
// same room either way and the cover must not change size when the answer
// arrives.
func (m model) metaLine() string {
	bits := []string{}
	if m.track.Artist != "" {
		bits = append(bits, m.track.Artist)
	}
	if m.track.Album != "" {
		bits = append(bits, m.track.Album)
	}
	if len(m.track.Genres) > 0 {
		bits = append(bits, strings.Join(m.track.Genres, ", "))
	}
	return strings.Join(bits, " · ")
}

// The line of keys under the progress bar.
//
// The volume is not in here: the line below has a volume bar on it. s and r do
// show their state, because a key that does nothing visible is a key nobody
// presses twice.
func (m model) keyHints() string {
	return fmt.Sprintf("← → seek   a prev   d next   p pause   s shuffle %s   r repeat %s   l lyrics   q back",
		onOff(m.opts.shuffle), onOff(m.opts.repeat))
}

func tracksToItems(tracks []Track) []item {
	items := make([]item, 0, len(tracks))
	for _, t := range tracks {
		items = append(items, item{title: t.Title, track: t})
	}
	return items
}

// A line of text picked to suit the hour, so the first screen says something that
// is not the same all day.
//
// Called once, when the model is built, and the answer kept on the model.
func timeMsg() string {
	return timeMsgFor(time.Now().Hour())
}

// The greeting for a given hour, picked at random out of that hour's lines.
func timeMsgFor(hour int) string {
	pool := greetingPool(hour)
	return pool[rand.Intn(len(pool))]
}

// Every line that suits a given hour, so a test can check a greeting belongs to
// the hour it was meant for.
func greetingPool(hour int) []string {
	switch {
	case hour < 5:
		return []string{
			"I am the master of my fate, I am the captain of my soul.",
			"Hello darkness, my old friend.",
			"Do not go gentle into that good night.",
			"See you space cowboy...",
			"In the silence, I find myself.",
			"The night whispers secrets.",
		}

	case hour < 10:
		return []string{
			"Here comes the sun, and I say, it's all right.",
			"Wake up to reality.",
			"I love the smell of napalm in the morning.",
			"It's a new dawn, it's a new day.",
			"Every morning brings new opportunities.",
			"Rise and shine—the world awaits.",
		}

	case hour < 14:
		return []string{
			"It's high noon.",
			"Fortune favors the bold.",
			"There's a starman waiting in the sky.",
			"May the Force be with you.",
			"Peak performance unlocked.",
			"The world is your playground.",
		}

	case hour < 18:
		return []string{
			"The show must go on.",
			"We're gonna need a bigger boat.",
			"Fear is the mind-killer.",
			"Carry on my wayward son.",
			"Every ending is a new beginning.",
			"The best is yet to come.",
		}

	case hour < 22:
		return []string{
			"Why so serious?",
			"I am inevitable.",
			"Paint it black.",
			"The night is dark and full of terrors.",
			"Embrace the shadows within.",
			"What doesn't kill you makes you stronger.",
		}

	default:
		return []string{
			"I can do this all day.",
			"Turn off your mind, relax and float downstream.",
			"End? No, the journey doesn't end here.",
			"To infinity and beyond.",
			"In dreams, we are infinite.",
			"The night whispers secrets only you can hear.",
		}
	}
}

// A playing time as m:ss, or h:mm:ss once it is over an hour.
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Seconds())
	if hours := seconds / 3600; hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

// Whether a switch is on, in as few characters as possible.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// Marks the volume as silent, because the volume number stays where it was while
// mute is on.
func muted(b bool) string {
	if b {
		return " (muted)"
	}
	return ""
}

// The progress bar, with a pacman at the playback position. The pacman opens and
// closes its mouth as it goes, which is what makes the movement easy to follow
// while the track plays.
func seekBar(elapsed, length time.Duration, width int) string {
	// how far through, as a fraction between 0 and 1
	done := 0.0
	if length > 0 {
		done = float64(elapsed) / float64(length)
	}
	if done < 0 {
		done = 0
	}
	if done > 1 {
		done = 1
	}

	// the cell the pacman sits in, and whether its mouth is open right now
	at := int(done * float64(width))
	if at > width-1 {
		at = width - 1
	}
	pacman := 'c' // mouth closed
	if int(elapsed/(250*time.Millisecond))%2 == 0 {
		pacman = 'C' // mouth open, mid-chomp
	}

	bar := make([]rune, width)
	for i := range bar {
		switch {
		case i < at:
			bar[i] = '█' // already played
		case i == at:
			bar[i] = pacman
		default:
			bar[i] = '░' // still to come
		}
	}
	return string(bar)
}

// The part of the queue to show, at most max entries, with the track playing kept
// inside it.
func queueWindow(queue []Track, index, max int) (start int, rows []Track) {
	if len(queue) <= max {
		return 0, queue
	}
	start = index - max/2
	if start < 0 {
		start = 0
	}
	if start > len(queue)-max {
		start = len(queue) - max
	}
	return start, queue[start : start+max]
}

// Shortens s until it is n columns wide, ending in an ellipsis when it had to cut.
//
// Counts columns rather than letters, because lipgloss does too: a CJK or emoji
// rune takes two columns.
func clip(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	// one column is kept for the ellipsis itself
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > n {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// Highlights and centres the current lyric line; untimed lyrics appear as plain text
func (m model) drawLyrics() string {
	var b strings.Builder
	b.WriteString(fg(titleTextStyle, "Lyrics") + "\n\n")
	switch {
	case m.lyricsLoading:
		b.WriteString("	" + spinFrame(m.spin) + fg(dimStyle, " looking for lyrics...") + "\n")
	case m.lyricsFailed:
		b.WriteString(fg(dimStyle, "	could not reach lrclib.net") + "\n")
	case m.lyrics.Instrumental:
		b.WriteString(fg(dimStyle, "	instrumental track mate, whatchu looking for") + "\n")
	case len(m.lyrics.Synced) > 0:
		b.WriteString(m.syncedLines() + "\n")
	case strings.TrimSpace(m.lyrics.Plain) != "":
		b.WriteString(m.plainLines() + "\n")
	default:
		// looked up and got nothing , answer = none
		b.WriteString(fg(dimStyle, "	no lyrics for this track") + "\n")

	}
	hint := "l or esc back"
	if len(m.lyrics.Synced) > 0 {
		hint += " · +/- timing · r reset"
	}
	b.WriteString("\n" + fg(dimStyle, hint) + "\n")
	return m.frame(b.String())
}

// The timed lines, with the one being sung picked out. It follows the music
// until the user scrolls, and then it stays where it was put.
func (m model) syncedLines() string {
	var b strings.Builder

	at := m.lyricAt()
	start := m.followStart()
	if m.lyricsPinned {
		start = max(0, min(m.lyricsTop, m.lyricsTopLimit()))
	}

	total := len(m.lyrics.Synced)
	for n := start; n < total && n < start+m.lyricsRows(); n++ {
		text := clip(m.lyrics.Synced[n].Text, m.contentWidth()-6)
		if n == at {
			b.WriteString(fg(cursorStyle, "▸ "+text) + "\n")
			continue
		}
		b.WriteString("  " + text + "\n")
	}
	return b.String()
}

// Where the timed block starts when it is following the music: the line being
// sung, kept near the middle.
func (m model) followStart() int {
	if len(m.lyrics.Synced) == 0 {
		return 0
	}
	cursor := m.lyricAt()
	if cursor < 0 {
		cursor = 0
	}
	start, _ := listWindow(len(m.lyrics.Synced), cursor, m.lyricsRows())
	return start
}

// The furthest the block can be scrolled while still filling the box.
func (m model) lyricsTopLimit() int {
	total := len(m.lyrics.Synced)
	if total == 0 {
		total = len(strings.Split(m.lyrics.Plain, "\n"))
	}
	return max(0, total-m.lyricsRows())
}

// Words without timings, as a block. There is nothing to follow, so it starts
// at the top and moves only when the arrows ask.
func (m model) plainLines() string {
	var b strings.Builder

	lines := strings.Split(m.lyrics.Plain, "\n")
	start := 0
	if m.lyricsPinned {
		start = max(0, min(m.lyricsTop, m.lyricsTopLimit()))
	}

	for i := start; i < len(lines) && i < start+m.lyricsRows(); i++ {
		b.WriteString("  " + clip(lines[i], m.contentWidth()-6) + "\n")
	}
	return b.String()
}

// Returns the current lyric line index, or -1 before the first line starts
func (m model) lyricAt() int {
	at := -1
	for i, line := range m.lyrics.Synced {
		if line.Time+m.lyricOffset > m.elapsed {
			break
		}
		at = i
	}
	return at
}

// Calculates available lyric lines after reserving space for the heading and footer
func (m model) lyricsRows() int {
	n := m.drawRows() - 4
	if n < 1 {
		return 1
	}
	return n
}
