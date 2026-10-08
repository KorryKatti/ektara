package main

// This file is the drawing: the styles, and the functions that turn the model
// into the lines that go on the terminal. Every function here answers a string
// and reads nothing but the model.
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
// Only the middle right box changes from mode to mode. The title, the sidebar
// and the player bar are drawn the same way always, so the screen never jumps
// about as you move around it.

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

// ---------------------------------------------------------------------------
// the shape of the screen
// ---------------------------------------------------------------------------

// The heights and widths are plain numbers so the arithmetic below can be read
// off the drawing at the top of this file.
const (
	// titleHeight is the green bar. It is exactly one line tall.
	titleHeight = 1

	// playerBlockHeight is how tall the bar along the bottom is, counting the line
	// of border above it. It is five lines of text and that border: the centred
	// title, the progress bar, the key hints, the volume and transport, and the
	// notice or queue count.
	//
	// It is passed to lipgloss as the height and it is what the rest of the
	// layout takes off the terminal, so those have to be the same number: lipgloss
	// counts the border as part of the height, and getting this wrong leaves the
	// bottom row of the screen unpainted, which shows as the terminal's own
	// background coming through the bar.
	playerBlockHeight = 6

	// sidebarWidth is how wide the menu column is on the left, padding included.
	sidebarWidth = 24

	// queueWidth is how wide the column on the right is, padding included. It is
	// a little wider than the menu because a song title needs more room than a
	// one word menu entry.
	queueWidth = 30

	// narrowestContent is the least room the middle box is worth having. Below
	// this the box has to start clipping its own text, so it is better to spend
	// everything on it and drop a side column.
	narrowestContent = 30
)

// showSidebar is whether there is room for the menu down the left as well as the
// content beside it. On a narrow terminal the menu goes away and the content
// takes the whole width, because a clipped list is worse than no list.
func (m model) showSidebar() bool {
	// before the size is known, show everything, because the first frame is
	// nearly always drawn at a normal size
	if m.width == 0 {
		return true
	}
	return m.width >= sidebarWidth+narrowestContent
}

// showQueue is whether there is room for the up-next column on the right once
// the menu and the content have both been served.
//
// The menu is asked for first and this one gives way, because a menu that is
// there but useless is worse than one that is simply gone: the content is the
// thing the user came to look at either way.
func (m model) showQueue() bool {
	if m.width == 0 {
		return true
	}
	if !m.showSidebar() {
		return false
	}
	return m.width >= sidebarWidth+narrowestContent+queueWidth
}

// contentWidth is how many columns the middle box gets. Until the terminal has
// said how big it is this is a made up number, because the first frame can
// arrive before the size does.
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

	// a floor, so the box is never so narrow that a two letter name is the
	// longest thing that fits. It is not a promise about the terminal: a terminal
	// narrower than the floor gets the width it actually has, because overflowing
	// is worse than a tight box.
	if w < 10 {
		w = 10
	}
	if w > m.width {
		return m.width
	}
	return w
}

// bodyHeight is how many lines the middle box has, once the title and the player
// bar have taken theirs.
//
// The floor is one rather than something comfortable, because the player bar
// cannot shrink: on a terminal only a few lines tall, giving the middle box a
// generous minimum would push the bottom of the screen off it. One line is a poor
// home for a screen, but it is on the screen.
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

// drawRows is how many lines a screen may actually write: the box, less its own
// padding above and below, less whatever the errors take at the bottom.
//
// Every screen sizes itself from this, and the frame draws the box at
// bodyHeight. They have to agree, or a screen fills the box, the errors get
// pushed off the bottom, and the one line worth reading is the one that goes
// missing. That is the whole reason this is a named number rather than the same
// subtraction written out in five places.
func (m model) drawRows() int {
	rows := m.bodyHeight() - 2 - m.errorRows()
	if rows < 1 {
		return 1
	}
	return rows
}

// ---------------------------------------------------------------------------
// the styles
// ---------------------------------------------------------------------------

// accent is the one colour the interface uses to mean "this is the important
// thing on screen": the title bar, and the row the cursor is on.
//
// It used to be #1DB954, which is Spotify's green. That made ektara look like a
// Spotify screen with the name changed, so it is a violet instead, which is
// nowhere near any of the other music players.
//
// Both places it is used go through this one name, so changing it is a single
// edit here rather than a hunt through the drawing code.
const accent = "#7C5CFF"

// The styles used to draw. Kept in one place so the colours can be changed
// without hunting through the drawing code.
var (
	titleStyle = lipgloss.NewStyle().
			Height(titleHeight).
			Padding(0, 1).
			Bold(true).
			Foreground(lipgloss.Color("#0B0B0F")).
			Background(lipgloss.Color(accent))

	// The two boxes in the middle are given both a height and a maximum height.
	// The height pads a short screen out to fill the frame; the maximum cuts a
	// long one off at the bottom. Only the maximum is what keeps the frame the
	// size of the terminal, and it is the reason no screen has to count its own
	// lines: a long track title or a big queue is cut off rather than pushing
	// the player bar off the bottom of the screen.
	sidebarStyle = lipgloss.NewStyle().
			Width(sidebarWidth).
			Padding(1, 1).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#181818"))

	// contentPanel is the background of the middle box. The cover art needs it as
	// well, because a braille character is mostly gaps between its dots and
	// without a background of its own every one of those gaps shows whatever is
	// behind the drawing. On a terminal with a transparent background that is the
	// desktop, and the cover sits in a bright rectangle.
	contentPanel = lipgloss.Color("#121212")

	contentStyle = lipgloss.NewStyle().
			Padding(1, 2).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(contentPanel)

	// playerPanel is the background of the bar along the bottom. The border needs
	// it as well as the text, and that is not a detail: a border with no
	// background of its own is drawn in the foreground colour alone, so the whole
	// row is left with whatever the terminal's own background is. On a terminal
	// with a transparent background that is the desktop showing through, as a
	// bright band across the bottom of the screen.
	playerPanel = lipgloss.Color("#181818")

	playerStyle = lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(playerPanel).
			BorderTop(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("#333333")).
			BorderBackground(playerPanel)

	// queuePanel is the background of the column on the right, the same near
	// black as the menu on the left so the middle box reads as the bright one.
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
// This is not a matter of taste. A lipgloss style ends every run of text with a
// full reset, and a full reset takes the background with it: whatever comes after
// on the same line goes back to the terminal's own background. Every one of these
// styles is drawn inside a box that has already set a background, and the reset
// was quietly wiping it out for the rest of the line. On a terminal with a dark
// background that is invisible; on one with a transparent background it is the
// desktop showing through in patches, which is the worst kind of bug to look at
// because it moves about as the text changes.
//
// The reset is put back at the very end of the whole frame instead, once, in
// frame(). Nothing here needs one: each box re-states its own background for
// every line it draws.
//
// Empty text is a special case, because there is a caller that styles the empty
// string. lipgloss renders that as a bare colour code with no text and no reset,
// and the cell after it inherits a colour nobody asked for. An empty string
// styles to an empty string.
func fg(style lipgloss.Style, s string) string {
	if s == "" {
		return ""
	}
	return strings.TrimRight(style.Render(s), reset)
}

// reset is the sequence that puts every attribute back to its default. It is
// named because it appears in one place as a thing being removed and in another
// as a thing being added, and the two have to be the same string.
const reset = "\x1b[m"

// ---------------------------------------------------------------------------
// the frame
// ---------------------------------------------------------------------------

// frame puts the three fixed parts around whatever the current mode drew. Every
// screen function below ends by calling this, so no screen has to remember that
// the title, the sidebar and the player exist.
//
// The errors go on here rather than on each screen, for one reason: the box is
// cut off at the bottom to keep it the size of the terminal, so anything a
// screen drew last is the first thing to be cut. A screen that has run out of
// room to say why the music stopped is exactly the screen that needs to say it
// most, so the errors are added after the cutting rather than before it.
func (m model) frame(content string) string {
	title := titleStyle.Width(m.width).Render("  Ektara")

	// the box is bodyHeight tall, which is exactly drawRows plus the box's own
	// padding plus the errors. The screens already left room for the errors by
	// sizing themselves from drawRows, so this is only cutting off anything that
	// went wrong rather than hiding the message.
	panel := contentStyle.
		Width(m.contentWidth()).
		Height(m.bodyHeight()).
		MaxHeight(m.bodyHeight()).
		Render(content + m.errors())

	// the two side columns are beside the content when there is room for them,
	// and gone when there is not, so the content always gets the width it asked
	// for. lipgloss pads a short column out to the height of the tallest, which
	// is what keeps the three of them level.
	body := panel
	switch {
	case m.showQueue():
		sidebar := sidebarStyle.
			Height(m.bodyHeight()).
			MaxHeight(m.bodyHeight()).
			Render(m.drawSidebar())

		queue := queueStyle.
			Height(m.bodyHeight()).
			MaxHeight(m.bodyHeight()).
			Render(m.drawQueue())

		body = lipgloss.JoinHorizontal(lipgloss.Top, sidebar, panel, queue)

	case m.showSidebar():
		sidebar := sidebarStyle.
			Height(m.bodyHeight()).
			MaxHeight(m.bodyHeight()).
			Render(m.drawSidebar())

		body = lipgloss.JoinHorizontal(lipgloss.Top, sidebar, panel)
	}

	player := playerStyle.
		Width(m.width).
		Height(playerBlockHeight).
		Render(m.drawPlayerBar())

	// The one reset for the whole frame, at the very end. Nothing above closes
	// its own colours, because closing them took the background with it and left
	// the terminal showing through; putting a single reset here means the
	// terminal is left in a sane state without any of the boxes in between
	// clearing their own background.
	return lipgloss.JoinVertical(lipgloss.Left, title, body, player) + reset
}

// drawSidebar is the menu, down the left. It is the same six choices as always,
// in the same order, with the marker on the row the cursor is on.
//
// The cursor only moves on the menu screen, so on the other screens this is a
// list of where you can go rather than a list of where you are. That is why
// there is no marker there: the menu is not being used, so nothing on it is
// picked.
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

// drawQueue is the column on the right: what is playing now, and what plays
// after it.
//
// It is always on screen when there is room, rather than only on the player
// screen, because the whole point of a queue is being able to see it while doing
// something else.
func (m model) drawQueue() string {
	var b strings.Builder

	// before anything has played there is nothing to queue up, so the column says
	// so rather than showing an empty list under a heading
	if len(m.queue) == 0 {
		b.WriteString(fg(dimStyle, "UP NEXT") + "\n\n")
		b.WriteString(fg(dimStyle, "Nothing queued."))
		return b.String()
	}

	b.WriteString(fg(dimStyle, "NOW PLAYING") + "\n\n")

	// m.index is the track playing, not the first one: a and d move it along the
	// queue, so the row at the top has to follow them rather than staying on
	// whatever was queued first
	b.WriteString(m.queueLine(m.index, true) + "\n\n")

	b.WriteString(fg(dimStyle, fmt.Sprintf("UP NEXT (%d)", len(m.queue)-1)) + "\n\n")

	// the rest of the queue, with the current track kept in the middle of the
	// window so a and d move it along with the music
	start, rows := queueWindow(m.queue, m.index, m.queueRows())
	for i := range rows {
		if start+i == m.index {
			continue // already drawn at the top as now playing
		}
		b.WriteString(m.queueLine(start+i, false) + "\n")
	}

	return b.String()
}

// queueLine is one row of the queue column: a marker on the one playing, and the
// title clipped so a long name cannot push the column out of shape.
func (m model) queueLine(i int, nowPlaying bool) string {
	marker := "  "
	if nowPlaying {
		marker = fg(cursorStyle, "▸ ")
	}

	// the number of what position in the queue, so the list can be counted
	number := fg(dimStyle, fmt.Sprintf("%2d. ", i+1))

	return marker + number + clip(m.queue[i].Title, queueWidth-8)
}

// drawPlayerBar is the bar along the bottom. It is always drawn, even with
// nothing playing, so the screen keeps the same shape from the menu onwards.
//
// It is also where every notice is shown, on every screen. The middle box is for
// the screen's own contents and a failure, and a notice is the answer to a key
// press, so it belongs somewhere that is always there and costs the box nothing.
func (m model) drawPlayerBar() string {
	// Every line here is clipped to the width of the bar. That is not tidiness: a
	// long line would wrap onto a second row, and the bar is a fixed height, so
	// the extra row would push the bottom of the screen off the terminal.
	inside := m.width - 4 // two for the padding, two to spare

	var b strings.Builder

	// before anything has played there is no track, so the bar says so rather
	// than showing a row of zeroes. The keys are still shown, because they are
	// how anything gets played in the first place.
	if m.track.Title == "" {
		b.WriteString("Nothing playing\n\n")
		b.WriteString(m.drawPlayerKeys(inside) + "\n")
		b.WriteString(m.drawTransport(inside) + "\n")
		b.WriteString(clip(m.notice, inside))
		return b.String()
	}

	// The title is the first line and it is centred, because it is the one thing
	// on the bar that is about the music rather than about the controls.
	b.WriteString(m.centred(m.track.Title, inside) + "\n")

	// The icon says whether sound is coming out, because the elapsed time
	// carries on while paused and that on its own looks like a broken bar.
	icon := "▶"
	if m.paused {
		icon = "❚❚"
	}

	times := fmt.Sprintf("%s / %s", clock(m.elapsed), clock(m.length))
	row := fmt.Sprintf("%s  %s  %s", icon, seekBar(m.elapsed, m.length, m.barWidth()), times)
	b.WriteString(m.centred(row, inside) + "\n")

	// Below the bar: the keys, then the volume and the transport buttons. Both
	// were on this bar already in some form; they are on their own lines now so
	// that nothing on the bar has to share a row with anything else.
	b.WriteString(m.drawPlayerKeys(inside) + "\n")
	b.WriteString(m.drawTransport(inside) + "\n")

	// the last line is for whatever the player has just been told, such as
	// "volume 60%", or for the queue count when there is nothing newer
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

// drawPlayerKeys is the line of key hints under the progress bar.
//
// The text is centred first and coloured afterwards, and the order matters. A
// lipgloss style ends with a full reset, and a full reset clears the background
// as well as the foreground, so styling the text first left the padding spaces on
// either side of it with no background at all. On a terminal with a transparent
// background that is the desktop showing through to the left and right of the
// line.
func (m model) drawPlayerKeys(inside int) string {
	return fg(dimStyle, m.centred(clip(m.keyHints(), inside), inside))
}

// drawTransport is the line under the keys: the volume on the left and the
// previous and next buttons on the right.
//
// The volume is a bar rather than a number, because a number has to be read and a
// bar does not. The mute state is written out as well, since the bar itself looks
// the same whether the sound is on or off.
//
// The buttons name the key that presses them rather than being icons, so the row
// explains itself and every character in it is one column wide.
func (m model) drawTransport(inside int) string {
	// no speaker glyph: a speaker is one of the wide emoji and takes two columns
	// in some terminals, which would shove the row out of shape
	volume := fmt.Sprintf("vol %s %3d%%", volumeBar(m.soundVolume(), 10), int(m.volume*100+0.5))
	if m.muted {
		volume = fmt.Sprintf("vol %s%3d%% muted", volumeBar(0, 10), int(m.volume*100+0.5))
	}

	buttons := "prev (a)   next (d)"

	// A narrow terminal has room for the volume or the buttons but not both, and
	// the volume is the one worth keeping. The line is clipped as a whole at the
	// end regardless, because a line that is too long wraps onto a second row and
	// the bar is a fixed height, so the extra row would push the bottom of the
	// screen off the terminal.
	gap := inside - lipgloss.Width(volume) - lipgloss.Width(buttons)
	if gap < 1 {
		return clip(volume, inside)
	}

	return clip(volume+strings.Repeat(" ", gap)+buttons, inside)
}

// centred puts s in the middle of a line width columns wide.
//
// It clips first. A line longer than the space would wrap onto a second row, and
// the bar is a fixed height, so the extra row would push the bottom of the screen
// off the terminal. Centring is done with spaces rather than by lipgloss so that
// the result is a plain string of a known width, which is what makes that
// checkable.
func (m model) centred(s string, width int) string {
	s = clip(s, width)

	space := width - lipgloss.Width(s)
	if space <= 0 {
		return s
	}

	left := space / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", space-left)
}

// volumeBar is a bar for the volume, so the level can be seen rather than read.
//
// The characters are the light and dark shades, which are one column wide in
// every terminal. The full block is not: it is East Asian ambiguous, so it takes
// two columns in some of them and would make the row longer than the screen.
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

// barWidth is how wide the progress bar can be. It shares a line with the icon
// and the two clock times, so it gets whatever is left over.
func (m model) barWidth() int {
	// 2 for the padding, 2 for the icon, 4 for the spaces around the bar, and
	// 5 each for the two times and the " / " between them
	w := m.width - 2 - 2 - 4 - 5 - 3 - 5
	if w < 10 {
		return 10
	}
	return w
}

// ---------------------------------------------------------------------------
// the screens
// ---------------------------------------------------------------------------

// drawMenu is the first screen: the greeting, the cover, what the highlighted
// choice does, and a reminder of the keys. The choices themselves are in the
// sidebar, so they are not repeated here.
func (m model) drawMenu() string {
	var b strings.Builder

	// The greeting, then the two things worth showing: the cover, and what has
	// been played. The menu is not repeated here because it is already down the
	// left, and neither is the key hint, which the player bar carries.
	// The greeting is clipped: one of the lines in the pool is long enough to
	// wrap on a narrow terminal, and a wrapped line takes a row the box did not
	// give it.
	b.WriteString(fg(greetingStyle, clip(m.greeting, m.contentWidth()-6)) + "\n\n")
	b.WriteString(coverArt(artWidthFor(m.menuArtRows(), m.contentWidth())) + "\n\n")

	b.WriteString(m.drawHomeRecent())

	return m.frame(b.String())
}

// drawHomeRecent is the "recently played" block on the first screen.
//
// It is the history the program was started with, newest first, and it is cut to
// whatever rows are left over after the cover. When there is no history it says
// so rather than showing a heading over nothing, because a heading with nothing
// under it looks like something failed.
func (m model) drawHomeRecent() string {
	if len(m.recent) == 0 {
		return fg(dimStyle, "Nothing played yet. Pick Download or Offline to start.")
	}

	// the heading, a blank line under it, and one row per track
	rows := m.drawRows() - m.menuArtRows() - 3
	if rows < 1 {
		return ""
	}

	var b strings.Builder
	b.WriteString(fg(titleTextStyle, "RECENTLY PLAYED") + "\n")

	// the newest first, and never more than there is room for
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

// menuArtRows is how many rows of cover the first screen has left once the
// greeting and the blank lines around it have had theirs. Four are spoken for:
// the greeting, two blanks, and one for the gap before the next thing.
//
// The cover is capped at half the box, because the other half is the recently
// played list and a heading with nothing under it looks like something failed.
//
// A small number is left as it is rather than rounded up, because on a short
// terminal there is genuinely no room. artWidthFor turns a number too small to
// use into no cover at all.
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

// artRows is how many rows there are to draw a cover in, before anything else in
// the box has taken its share.
//
// This is the box, less its own padding, less two rows for a failure. It is
// deliberately not drawRows, which is the same thing but also counts a notice:
// notices come from every key press, so counting one would change the width of
// the cover every time the volume moved, and the cover would blink out until the
// notice expired. A failure is different, because it stays until something
// replaces it, so the cover resizing once when one arrives is not a flicker.
//
// The result is that the cover only changes size when the terminal does or when
// something has genuinely broken, and never because a key was pressed.
func (m model) artRows() int {
	rows := m.bodyHeight() - 2 - m.errorRows()
	if rows < 1 {
		return 1
	}
	return rows
}

// artWidthFor turns the rows the art is allowed into the width to draw it at.
//
// A thumbnail is 16:9, so on screen it is wider than it is tall and the rows it
// is given decide the width. The ratio lives in the asciiart package, which is
// where the picture is cropped and drawn: keeping the arithmetic here and the
// shape there in step by hand is how the two drift apart.
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
		// too narrow for a picture worth drawing, so nothing is drawn
		return 0
	}
	return w
}

// wantArt is the command that fetches and draws the cover for the track playing
// now, at the size the screen wants it. It is asked for whenever the track or
// the terminal changes, and it does nothing if the drawing already exists.
//
// It lives here rather than in art.go because the width is a drawing decision:
// the art is only worth fetching at the size it is going to be shown at.
func (m model) wantArt() tea.Cmd {
	if m.track.ID == "" {
		return nil
	}
	return artCmd(m.track, m.artWidth())
}

// artWidth is the width the cover should be drawn at on the current screen: the
// player screen gives it half the box, the menu screen the lot of it.
//
// The player screen is where the cover belongs, since that is the screen with a
// track on it. The menu screen shows the fallback picture, because there is
// nothing playing to have a cover of.
func (m model) artWidth() int {
	if m.mode == modePlaying {
		return artWidthFor(m.playerArtRows(), m.contentWidth())
	}
	return artWidthFor(m.menuArtRows(), m.contentWidth())
}

// coverJPG is the fallback cover, kept in the binary so the first screen has a
// picture to show before anything is playing and there is nothing to fetch one
// for. Once a track is playing its own thumbnail is used instead.
//
//go:embed assets/cover.jpg
var coverJPG []byte

// cover holds the drawn fallback, one copy per width it has been drawn at. The
// screen is redrawn ten times a second and each redraw would otherwise turn the
// picture into text all over again. The width is the only thing that changes it,
// so a handful of widths is a handful of copies and no more.
var cover = struct {
	sync.Mutex
	byWidth map[int]string
}{byWidth: map[int]string{}}

// coverArt is the fallback cover, drawn to the given width, or nothing at all
// when there is no room for one.
//
// A width below eight returns the empty string, which is the drawing saying there
// is no space rather than an error.
func coverArt(width int) string {
	if width < 8 {
		return ""
	}

	cover.Lock()
	defer cover.Unlock()

	if art, ok := cover.byWidth[width]; ok {
		return art
	}

	// a picture that will not draw should not stop the program, it should just
	// leave a gap
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

// trackArt is the cover of the track playing, drawn as text, or the empty string
// if it has not arrived yet.
//
// It is only shown when it was drawn for this track at this width, which is what
// the key on the model is for. A drawing that was on its way when the track
// changed is not shown against the new one.
func (m model) trackArt() string {
	if m.art == "" {
		return ""
	}
	if m.artKey != artKey(m.track, m.artWidth()) {
		return ""
	}
	return m.art
}

// drawSearch is the typing screen. It is the same box for a name and for a
// number, the prompt just says which.
func (m model) drawSearch() string {
	prompt := "Search YouTube for: "
	if m.askingCount {
		prompt = "How many tracks? "
	}

	var b strings.Builder
	b.WriteString(fg(titleTextStyle, prompt) + "\n\n")
	b.WriteString("  " + fg(cursorStyle, m.searchInput) + "▌\n\n")

	// while the search is out, show that something is happening. The four
	// frames are plain characters, so this looks the same in every font.
	if m.searching {
		b.WriteString("  " + spinFrame(m.spin) + " Searching...\n")
	} else {
		b.WriteString(fg(dimStyle, "enter confirm, backspace erase, esc back") + "\n")
	}

	return m.frame(b.String())
}

// spinFrames is the busy animation. Spinning through four characters is the
// whole trick.
var spinFrames = []string{"|", "/", "-", "\\"}

// spinFrame picks the frame for a given tick count. The remainder keeps it
// inside the list, so the counter can grow forever.
func spinFrame(n int) string {
	return spinFrames[n%len(spinFrames)]
}

// itemsTitle is the heading for whichever list is showing.
func (m model) itemsTitle() string {
	switch m.mode {
	case modeResults:
		return "Results"
	case modeFiles:
		return "Files in this folder"
	case modeHistory:
		return "History"
	}
	return ""
}

func (m model) drawItems() string {
	var b strings.Builder
	b.WriteString(fg(titleTextStyle, m.itemsTitle()) + "\n\n")

	// only the rows near the cursor are drawn. The history can be hundreds of
	// songs long, and drawing all of them pushes the rest of the screen off the
	// bottom, so the list scrolls instead.
	start, count := listWindow(len(m.items), m.itemCursor, m.listRows())

	for i := 0; i < count; i++ {
		it := m.items[start+i]

		// the title is clipped before it is coloured, never after. clip counts
		// columns, and the colour codes are not columns, so clipping a coloured
		// string cuts through the middle of an escape sequence and leaves the
		// rest of the line with no colour at all.
		title := clip(it.title, m.contentWidth()-6)

		marker := "  "
		if start+i == m.itemCursor {
			marker = fg(cursorStyle, "▸ ")
			title = fg(cursorStyle, title)
		}
		b.WriteString(marker + title + "\n")
	}

	// a count, so it is obvious that the list continues past the bottom. It is
	// only shown when there is more than fits, because "1 of 1" is just noise.
	//
	// The blank line is written outside the colour. A styled string that starts
	// with a newline puts its colour code at the end of the line above, where
	// lipgloss splits the line in two and leaves the code behind on a row that
	// then has no background of its own.
	if len(m.items) > count {
		b.WriteString("\n")
		b.WriteString(fg(dimStyle, fmt.Sprintf("  showing %d-%d of %d",
			start+1, start+count, len(m.items))) + "\n")
	}

	b.WriteString("\n" + fg(dimStyle, "up/down move, enter play, esc back") + "\n")

	return m.frame(b.String())
}

// listRows is how many list rows fit on the screen. The box spends five lines
// on the padding, the heading, the blank line under it, the blank line above
// the key hint and the hint itself, and two more on the count, which is always
// allowed for so the list does not change height as it scrolls. That is what is
// left of the middle box.
//
// The floor is one rather than three, because on a short terminal the box itself
// may only have a line or two, and claiming three rows for the list would draw
// past the bottom of it.
func (m model) listRows() int {
	n := m.drawRows() - 7
	if n < 1 {
		return 1
	}
	return n
}

// listWindow picks the range of rows to draw, keeping the cursor inside it.
//
// total is how many rows there are, cursor is which one is highlighted, and max
// is how many fit on the screen. It returns the index of the first row drawn
// and how many rows there are in that window, which is all the caller needs to
// work out which row it is looking at.
func listWindow(total, cursor, max int) (start, rows int) {
	// everything fits, so draw it all
	if total <= max {
		return 0, total
	}

	// start half a screen above the cursor, so it sits near the middle and
	// there is room to scroll in both directions
	start = cursor - max/2
	if start < 0 {
		start = 0
	}
	if start > total-max {
		start = total - max
	}
	return start, max
}

// drawPlayer is the full screen for a track that is playing. The bar along the
// bottom says the same thing in fewer lines, so this is where the cover and the
// queue go.
func (m model) drawPlayer() string {
	// the loading screen is a different box, not the player box with a gap in
	// it, so a slow track says plainly that it is still starting
	if m.loading {
		var b strings.Builder
		b.WriteString(fg(titleTextStyle, "Loading "+m.track.Title) + "\n\n")
		return m.frame(b.String())
	}

	// This screen is the cover and where the track came from. The keys, the
	// volume and the transport are on the bar along the bottom on every screen,
	// and the queue is in the column on the right on every screen, so repeating
	// either of them here would be saying the same thing twice in one glance.
	//
	// The picture itself arrives from the network a moment after the track does,
	// so this is usually an empty gap for the first second or so. Nothing is shown
	// in that gap rather than the fallback picture: putting the fallback next to a
	// real song would say the wrong thing about which song it is.
	var b strings.Builder

	b.WriteString(fg(titleTextStyle, "♪ "+m.track.Title) + "\n")
	b.WriteString(fg(dimStyle, clip(m.subtitle(), m.contentWidth()-6)) + "\n\n")

	b.WriteString(m.trackArt() + "\n\n")

	return m.frame(b.String())
}

// playerArtRows is how many rows of cover the player screen has left.
//
// Four lines are spoken for before the art: the title, the link under it, and the
// two blank lines. The art then takes what is left, capped at half the box so it
// does not become a wall of dots on a tall terminal.
//
// The queue is not in that count, because the queue is not on this screen any
// more: it is in the column on the right. A notice is not in it either, for the
// reason artRows gives.
//
// A small number is left as it is rather than rounded up, because on a short
// terminal there is genuinely no room. artWidthFor turns a number too small to
// use into no cover at all.
func (m model) playerArtRows() int {
	rows := m.artRows() - 4

	// half the box, so the picture is never more than half the screen
	if half := m.artRows() / 2; rows > half {
		rows = half
	}
	if rows < 0 {
		return 0
	}
	return rows
}

// queueRows is how many lines the queue column spends on the tracks still to
// come: a heading, a blank line, and the tracks themselves.
//
// It is zero when there is nothing queued, which is when the column says so in
// one line instead. At most five tracks are shown however long the queue is,
// because a column that runs the height of the screen is not a list any more.
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

// errors is whatever the program has to say: the last failure, which stays until
// something replaces it, and the last notice, which does not. The frame puts this
// on the bottom of the middle box, so every screen says them in the same place.
// errors is whatever has gone wrong and is still true: the last failure, which
// stays until something replaces it.
//
// The frame puts this on the bottom of the middle box, so every screen says it in
// the same place. A notice is not here; that goes in the player bar.
func (m model) errors() string {
	if m.lastError == "" {
		return ""
	}
	return "\n" + fg(errorStyle, clip(m.lastError, m.contentWidth()-4))
}

// errorRows is how many lines errors() will draw: a blank line and the message,
// so two, or none at all when there is nothing to say.
//
// Only a failure is counted. A notice is drawn in the player bar along the
// bottom, which is on screen whatever the mode is, so a notice costs the middle
// box nothing. That is not only tidier: the notices come from every key press,
// and if one of them took two rows out of the box then the cover would change
// size every time the volume moved.
func (m model) errorRows() int {
	if m.lastError == "" {
		return 0
	}
	return 2
}

// subtitle is the second line of the player: the youtube link for a stream, and
// the file it came from for a local mp3, which has no link.
func (m model) subtitle() string {
	if m.track.Stream {
		return m.track.PageURL
	}
	return m.track.Filename
}

// keyHints is the two lines of keys at the bottom of the player. s and r show
// their state, because a key that does nothing visible is a key nobody presses
// twice.
// keyHints is the line of keys under the progress bar.
//
// The volume is not in here: the line below this one has a volume bar on it, so
// saying the number again would be saying it twice. s and r do show their state,
// because a key that does nothing visible is a key nobody presses twice.
//
// It is one line and not two, because the bar is a fixed height and a second line
// here would push the bottom of the screen off the terminal.
func (m model) keyHints() string {
	return fmt.Sprintf("← → seek   a prev   d next   p pause   s shuffle %s   r repeat %s   q back",
		onOff(m.opts.shuffle), onOff(m.opts.repeat))
}

// tracksToItems turns tracks into rows for a list screen.
func tracksToItems(tracks []Track) []item {
	items := make([]item, 0, len(tracks))
	for _, t := range tracks {
		items = append(items, item{title: t.Title, track: t})
	}
	return items
}

// ---------------------------------------------------------------------------
// the greeting
// ---------------------------------------------------------------------------

// timeMsg is a line of text picked to suit the hour, so the first screen says
// something that is not the same all day. It is a random pick out of six, which
// is the whole trick.
//
// This is called once, when the model is built, and the answer is kept on the
// model. Calling it while drawing would give a new line on every frame, and the
// text would flicker too fast to read.
func timeMsg() string {
	return timeMsgFor(time.Now().Hour())
}

// timeMsgFor is the greeting for a given hour of the day, picked at random out
// of that hour's lines.
func timeMsgFor(hour int) string {
	pool := greetingPool(hour)
	return pool[rand.Intn(len(pool))]
}

// greetingPool is every line that suits a given hour, so a test can check that a
// greeting belongs to the hour it was meant for. The bands are: the small hours,
// the morning, midday, the afternoon, the evening, and the rest of the night.
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

// ---------------------------------------------------------------------------
// small display helpers
// ---------------------------------------------------------------------------

// clock formats a playing time as m:ss, or h:mm:ss once it is over an hour.
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

// onOff says whether a switch is on, in as few characters as possible because
// the key hints have to fit on one line.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// muted marks the volume as silent, because the volume number stays where it
// was while mute is on, so the number on its own would say otherwise.
func muted(b bool) string {
	if b {
		return " (muted)"
	}
	return ""
}

// seekBar is the progress bar, with a pacman at the playback position. The
// pacman opens and closes its mouth as it goes, which is what makes the
// movement easy to follow while the track plays.
//
// The two mouth shapes are Canadian aboriginal syllabics, which most terminal
// fonts carry. A terminal without them shows a box instead of a pacman, which
// is a poor look but not a broken one.
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
	pacman := 'ᗣ' // mouth closed
	if int(elapsed/(250*time.Millisecond))%2 == 0 {
		pacman = 'ᗧ' // mouth open, mid-chomp
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

// queueWindow picks the part of the queue to show, at most max entries, with
// the track playing kept inside it so a and d move the list with the music.
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

// clip shortens s until it is n columns wide, ending in an ellipsis when it had
// to cut, so a long title cannot push the box out of shape.
//
// It counts columns rather than letters, because lipgloss does too: a CJK or
// emoji rune takes two columns, and counting it as one is what makes a box with
// a Japanese title in it look broken.
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
