package main

// This file is the drawing: the styles, and the functions that turn the model
// into the lines that go on the terminal. Every function here answers a string
// and reads nothing but the model.

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// drawing
// ---------------------------------------------------------------------------

// The styles used to draw. Kept in one place so the colours can be changed
// without hunting through the drawing code.
var (
	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)

	titleStyle  = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

// contentWidth is how many columns fit inside the box. Until the terminal has
// said how big it is, this is a made up number, because the first frame can
// arrive before the size does.
func (m model) contentWidth() int {
	if m.width == 0 {
		return 60
	}
	w := m.width - 6 // two for the border, two for the padding, two to spare
	if w < 20 {
		return 20
	}
	return w
}

func (m model) drawMenu() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("ektara") + "\n\n")

	for i, text := range menuItems {
		marker := "  "
		if i == m.menuCursor {
			marker = cursorStyle.Render("▸ ")
		}
		b.WriteString(fmt.Sprintf("%s%s\n", marker, text))
	}

	b.WriteString("\n" + dimStyle.Render("up/down move, enter choose, q quit") + "\n")
	if m.lastError != "" {
		b.WriteString("\n" + errorStyle.Render(m.lastError) + "\n")
	}
	if m.notice != "" {
		b.WriteString("\n" + m.notice + "\n")
	}

	return boxStyle.Width(m.contentWidth()).Render(b.String())
}

func (m model) drawSearch() string {
	prompt := "Search YouTube for: "
	if m.askingCount {
		prompt = "How many tracks? "
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(prompt) + "\n\n")
	b.WriteString("  " + cursorStyle.Render(m.searchInput) + "▌\n\n")

	// while the search is out, show that something is happening. The four
	// frames are plain characters, so this looks the same in every font.
	if m.searching {
		b.WriteString("  " + spinFrame(m.spin) + " Searching...\n")
	} else {
		b.WriteString(dimStyle.Render("enter confirm, backspace erase, esc back") + "\n")
	}

	return boxStyle.Width(m.contentWidth()).Render(b.String())
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
	b.WriteString(titleStyle.Render(m.itemsTitle()) + "\n\n")

	// only the rows near the cursor are drawn. The history can be hundreds of
	// songs long, and drawing all of them pushes the rest of the screen off the
	// bottom, so the list scrolls instead.
	start, count := listWindow(len(m.items), m.itemCursor, m.listRows())

	for i := 0; i < count; i++ {
		it := m.items[start+i]
		marker := "  "
		line := it.title
		if start+i == m.itemCursor {
			marker = cursorStyle.Render("▸ ")
			line = cursorStyle.Render(it.title)
		}
		b.WriteString(fmt.Sprintf("%s%s\n", marker, clip(line, m.contentWidth()-4)))
	}

	// a count, so it is obvious that the list continues past the bottom. It is
	// only shown when there is more than fits, because "1 of 1" is just noise.
	if len(m.items) > count {
		b.WriteString(dimStyle.Render(fmt.Sprintf("\n  showing %d-%d of %d",
			start+1, start+count, len(m.items))) + "\n")
	}

	b.WriteString("\n" + dimStyle.Render("up/down move, enter play, esc back") + "\n")
	if m.notice != "" {
		b.WriteString("\n" + m.notice + "\n")
	}

	return boxStyle.Width(m.contentWidth()).Render(b.String())
}

// listRows is how many list rows fit on the screen. The box takes four lines
// (top border, heading, blank, blank) and three more at the bottom (blank,
// hints, notice), so that is what is left of the height.
func (m model) listRows() int {
	if m.height == 0 {
		return 10
	}
	n := m.height - 12
	if n < 3 {
		return 3
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

func (m model) drawPlayer() string {
	width := m.contentWidth()
	var b strings.Builder

	// the loading screen is a different box, not the player box with a gap in
	// it, so a slow track says plainly that it is still starting
	if m.loading {
		b.WriteString(titleStyle.Render("Loading "+m.track.Title) + "\n")
		if m.notice != "" {
			b.WriteString("\n" + m.notice + "\n")
		}
		return boxStyle.Width(width).Render(b.String())
	}

	b.WriteString(titleStyle.Render("♪ "+m.track.Title) + "\n")
	b.WriteString(dimStyle.Render(clip(m.subtitle(), width-2)) + "\n")

	// a paused marker, so the display says why there is no sound
	state := ""
	if m.paused {
		state = dimStyle.Render("[paused]")
	}

	// elapsed on the left, the total on the right, the pacman between them at
	// the position playback has reached
	b.WriteString(fmt.Sprintf("\n%s %s %s  %s\n",
		clock(m.elapsed), seekBar(m.elapsed, m.length, m.barWidth()), clock(m.length), state))

	b.WriteString("\n" + dimStyle.Render(m.keyHints()) + "\n")
	if m.notice != "" {
		b.WriteString(m.notice + "\n")
	}

	if len(m.queue) > 1 {
		b.WriteString("\n" + dimStyle.Render(fmt.Sprintf("queue (%d)", len(m.queue))) + "\n")
		start, rows := queueWindow(m.queue, m.index, 5)
		for i, t := range rows {
			marker := "  "
			if start+i == m.index {
				marker = cursorStyle.Render("▸ ")
			}
			b.WriteString(fmt.Sprintf("%s%s\n", marker, clip(t.Title, width-6)))
		}
	}

	return boxStyle.Width(width).Render(b.String())
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
func (m model) keyHints() string {
	return fmt.Sprintf("h/k seek  a prev  d next  p pause  s shuffle %s",
		onOff(m.opts.shuffle)) + "\n" +
		fmt.Sprintf("r repeat %s  m mute  q back  vol %d%%%s",
			onOff(m.opts.repeat), int(m.volume*100+0.5), muted(m.muted))
}

// barWidth is how wide the progress bar can be. It shares a line with the
// elapsed and total times, so it gets whatever is left over.
func (m model) barWidth() int {
	w := m.contentWidth() - 2 - 7 - 7 - 8
	if w < 10 {
		return 10
	}
	return w
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
