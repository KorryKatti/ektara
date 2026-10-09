// tui_test.go
package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"ektara/asciiart"
)

// The arithmetic that keeps a long list on screen: the history runs to hundreds
// of rows and the terminal is about thirty lines, so the list shows a slice around
// the cursor. The slice must fit inside the list and contain the cursor; the rest
// is cosmetic.
func TestListWindow(t *testing.T) {
	cases := []struct {
		name       string
		total, cur int
		max        int
		wantStart  int
		wantRows   int
	}{
		// shorter than the screen: all of it is shown, no scrolling
		{"fits", 5, 2, 10, 0, 5},
		{"exactly fits", 10, 9, 10, 0, 10},

		// cursor at the top: window starts at the top
		{"long at top", 71, 0, 18, 0, 18},
		// cursor in the middle: window is centred on it
		{"long in middle", 71, 40, 18, 31, 18},
		// cursor at the bottom: window stops at the end, cursor still inside
		{"long at bottom", 71, 70, 18, 53, 18},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, rows := listWindow(c.total, c.cur, c.max)

			if start != c.wantStart {
				t.Errorf("start = %d, want %d", start, c.wantStart)
			}
			if rows != c.wantRows {
				t.Errorf("rows = %d, want %d", rows, c.wantRows)
			}
			// the invariant that matters: the window is inside the list and
			// contains the cursor
			if start < 0 {
				t.Errorf("start %d is before the list", start)
			}
			if start+rows > c.total {
				t.Errorf("window %d..%d runs past the list of %d", start, start+rows, c.total)
			}
			if c.cur < start || c.cur >= start+rows {
				t.Errorf("cursor %d is outside window %d..%d", c.cur, start, start+rows)
			}
		})
	}
}

// The screen-height maths, including the sizes a terminal can be shrunk to. The
// numbers come off the frame drawn at the top of draw.go.
func TestListRows(t *testing.T) {
	cases := []struct {
		height int
		want   int
	}{
		{0, 11},  // no size reported yet, so a sensible default
		{30, 14}, // the normal size
		{12, 1},  // barely fits under the player bar
		{5, 1},   // absurdly small, but still one row rather than none
	}
	for _, c := range cases {
		m := model{height: c.height}
		if got := m.listRows(); got != c.want {
			t.Errorf("listRows(height=%d) = %d, want %d", c.height, got, c.want)
		}
	}
}

// The busy animation cycles and wraps, so the counter cannot run past the list.
func TestSpinFrame(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 9; i++ {
		f := spinFrame(i)
		if f != spinFrames[i%len(spinFrames)] {
			t.Errorf("spinFrame(%d) = %q, want %q", i, f, spinFrames[i%len(spinFrames)])
		}
		seen[f] = true
	}
	if len(seen) != len(spinFrames) {
		t.Errorf("the animation only used %d of its %d frames", len(seen), len(spinFrames))
	}
}

// While a search is out the box has to say so, and once it is not the typing hint
// has to come back: the screen used to look frozen.
func TestDrawSearchShowsSearching(t *testing.T) {
	m := initialModel(nil, nil)
	m.width, m.height = 80, 30
	m.searchInput = "lofi"

	m.searching = true
	got := m.drawSearch()
	if !strings.Contains(got, "Searching") {
		t.Errorf("searching screen does not mention searching:\n%s", got)
	}
	if strings.Contains(got, "enter confirm") {
		t.Errorf("searching screen still shows the typing hint:\n%s", got)
	}

	m.searching = false
	got = m.drawSearch()
	if strings.Contains(got, "Searching") {
		t.Errorf("idle screen still says searching:\n%s", got)
	}
	if !strings.Contains(got, "enter confirm") {
		t.Errorf("idle screen lost its typing hint:\n%s", got)
	}
}

// Only a screenful of a long list is drawn, and the "showing x-y of n" line says
// the rest is there.
func TestDrawItemsWindows(t *testing.T) {
	m := initialModel(nil, nil)
	m.width, m.height = 80, 30
	m.mode = modeHistory
	for i := 0; i < 71; i++ {
		m.items = append(m.items, item{title: fmt.Sprintf("song number %02d", i)})
	}

	got := m.drawItems()
	if !strings.Contains(got, "showing 1-14 of 71") {
		t.Errorf("missing or wrong count line:\n%s", got)
	}
	if !strings.Contains(got, "song number 00") {
		t.Errorf("first row is missing:\n%s", got)
	}
	// row 40 is well past the window, so it must not be drawn
	if strings.Contains(got, "song number 40") {
		t.Errorf("a row past the window was drawn:\n%s", got)
	}

	// a short list has nothing to count, so no count line at all
	m.items = m.items[:3]
	got = m.drawItems()
	if strings.Contains(got, "showing") {
		t.Errorf("a short list should not be counted:\n%s", got)
	}
}

// A player that does not play anything, so the tick can be tested at the one point
// that matters: the moment a track ends. That path could not be reached before,
// because the real player can only answer "is it still going" by playing something
// out of a sound device, which is how a bug survived in the one place the test above
// claimed to cover.
type fakeAudio struct {
	playing bool
	err     error
	// opens records every input handed to Start, so a test can tell that the
	// queue really moved on rather than only that something was returned.
	opens []string
	// stopped counts Stop calls, which is how the fake tells "the track ended
	// and the next one was loaded" from "the queue ran out".
	stops int
}

func (f *fakeAudio) Start(input string) error { f.opens = append(f.opens, input); return nil }
func (f *fakeAudio) Play()                    { f.playing = true }
func (f *fakeAudio) Pause()                   { f.playing = false }
func (f *fakeAudio) SetVolume(float64)        {}
func (f *fakeAudio) SeekBy(time.Duration)     {}
func (f *fakeAudio) Playing() bool            { return f.playing }
func (f *fakeAudio) Err() error               { return f.err }
func (f *fakeAudio) Position() time.Duration  { return 0 }
func (f *fakeAudio) Length() time.Duration    { return time.Duration(time.Minute) }
func (f *fakeAudio) Stop()                    { f.stops++ }
func (f *fakeAudio) Close()                   {}

// The one rule the tick cannot break: every path out of it hands back another
// tick. A path that does not stops the clock for good, nothing complains, and the
// program just looks frozen. Only one clock is wanted, so startTrack and backToMenu
// must not start their own.
//
// The end-of-a-track cases matter most and went untested for a long time: onTick
// returns move()'s command there, and move() has no tick in it. That froze the
// display the first time any song ended, which was every time.
func TestOnTickAlwaysKeepsTheClockRunning(t *testing.T) {
	queue := []Track{
		{ID: "one", Title: "one"},
		{ID: "two", Title: "two"},
	}

	cases := []struct {
		name  string
		build func(model) model
	}{
		{"on the menu", func(m model) model { return m }},
		{"playing but still loading", func(m model) model {
			m.mode, m.loading = modePlaying, true
			return m
		}},
		{"playing with nothing open yet", func(m model) model {
			m.mode = modePlaying
			return m
		}},
		{"a track that ended, with another to play", func(m model) model {
			m.mode = modePlaying
			m.player = &fakeAudio{playing: false}
			m.queue = queue
			m.track = queue[0]
			return m
		}},
		{"a track that ended, with one left in the queue", func(m model) model {
			m.mode = modePlaying
			m.player = &fakeAudio{playing: false}
			m.queue = queue
			m.index = 1
			m.track = queue[1]
			return m
		}},
		{"a track that broke, with another to play", func(m model) model {
			m.mode = modePlaying
			m.player = &fakeAudio{playing: false, err: errors.New("it broke")}
			m.queue = queue
			m.track = queue[0]
			return m
		}},
		{"a track still going", func(m model) model {
			m.mode = modePlaying
			m.player = &fakeAudio{playing: true}
			m.queue = queue
			return m
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := c.build(initialModel(songDB(t), nil))
			_, cmd := m.onTick()
			if cmd == nil {
				t.Fatal("onTick returned no command at all, so the clock has stopped")
			}
			_ = cmd
		})
	}
}

// The bug, pinned down: when a song ended, onTick returned move()'s command, which
// opens the next track and has no tick in it, so nothing renewed the clock chain.
// Ten seconds after the first song finished the progress bar stopped, the spinner
// stopped, and the queue sat on the last track forever.
//
// Only reachable by actually running the command onTick returned, which is why
// there is a fake player at all.
func TestEndOfTrackKeepsTheClockRunning(t *testing.T) {
	queue := []Track{
		{ID: "one", Title: "one"},
		{ID: "two", Title: "two"},
	}

	m := initialModel(songDB(t), nil)
	m.mode = modePlaying
	m.player = &fakeAudio{playing: false}
	m.queue = queue
	m.track = queue[0]

	_, cmd := m.onTick()
	if cmd == nil {
		t.Fatal("onTick returned no command, so the clock has already stopped")
	}

	// Run what onTick handed back, the way the Bubble Tea runtime would, and
	// check that a tick comes out of it. tea.Batch hands back a BatchMsg
	// holding the commands it was given, so the batch is opened up: the
	// question is not what the command is called but whether running it
	// produces a tickMsg.
	//
	// This is done once and not in a loop over many rounds. A tick command is
	// tea.Tick, which sleeps for the redraw interval before it produces its
	// message, so a test that runs a few hundred of them is a test that takes
	// a minute. Whether the tick is in there at all does not change between
	// round one and round two hundred, so one is enough.
	if !isTick(cmd()) {
		t.Error("what onTick returned does not contain a tick, so the clock stops when the track ends")
	}
}

// Whether msg is a tick, looking one level into a batch: a tick is allowed to be
// batched alongside the work a track change does, so finding it among a batch's
// members counts.
//
// Deliberately does not recurse. Running a batch's own members would run openTrackCmd
// and the Discord update for real on every one of a few hundred rounds, turning a
// test about the clock into a test about the network.
func isTick(msg tea.Msg) bool {
	if _, ok := msg.(tickMsg); ok {
		return true
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return false
	}
	for _, c := range batch {
		if c == nil {
			continue
		}
		if _, ok := c().(tickMsg); ok {
			return true
		}
	}
	return false
}

// The other half: a finished track is also the moment the queue advances.
func TestEndOfTrackMovesTheQueueOn(t *testing.T) {
	queue := []Track{
		{ID: "one", Title: "one", Stream: true, PageURL: "https://example.invalid/one"},
		{ID: "two", Title: "two", Stream: true, PageURL: "https://example.invalid/two"},
	}

	fake := &fakeAudio{playing: false}
	m := initialModel(songDB(t), nil)
	m.mode = modePlaying
	m.player = fake
	m.queue = queue
	m.track = queue[0]

	next, _ := m.onTick()
	after, ok := next.(model)
	if !ok {
		t.Fatalf("onTick gave back %T, want a model", next)
	}
	if after.index != 1 {
		t.Errorf("index is %d after the track ended, want 1", after.index)
	}
	if after.track.ID != "two" {
		t.Errorf("track is %q after the track ended, want two", after.track.ID)
	}
	// the finished track is stopped, not left running underneath the next one
	if fake.stops == 0 {
		t.Error("the finished track was never stopped")
	}
}

// The other end of the queue: the last song finishes, nothing is after it, so the
// menu comes back.
func TestEndOfLastTrackReturnsToTheMenu(t *testing.T) {
	queue := []Track{{ID: "one", Title: "one"}}

	m := initialModel(songDB(t), nil)
	m.mode = modePlaying
	m.player = &fakeAudio{playing: false}
	m.queue = queue
	m.index = 0
	m.track = queue[0]

	next, _ := m.onTick()
	after, ok := next.(model)
	if !ok {
		t.Fatalf("onTick gave back %T, want a model", next)
	}
	if after.mode != modeMenu {
		t.Errorf("mode is %v after the last track ended, want the menu", after.mode)
	}
	if len(after.queue) != 0 {
		t.Errorf("the queue still has %d tracks on it after it ran out", len(after.queue))
	}
}

// A broken track leaves a reason behind: the notice a failure used to raise was
// wiped by the move to the next track, so the reason was never seen. lastError does
// not expire, so the queue can run out and the menu can still say why the music
// stopped.
func TestDrawMenuShowsTheLastFailure(t *testing.T) {
	m := initialModel(nil, nil)
	m.width, m.height = 80, 30
	m.mode = modeMenu

	if strings.Contains(m.drawMenu(), "Sign in to confirm") {
		t.Error("a failure was shown before anything had failed")
	}

	m.lastError = "Playback failed: Sign in to confirm you're not a bot"
	got := m.drawMenu()
	if !strings.Contains(got, "Sign in to confirm") {
		t.Errorf("the menu did not say why playback stopped:\n%s", got)
	}
}

// That the layout holds together. The screen is built out of fixed height boxes
// and lipgloss cuts a box off at the bottom rather than letting it grow, which is
// what stops a long title or a big queue pushing the player bar off the screen. It
// only works if every screen fits at every size, so this walks the sizes a terminal
// can be, every screen, with the awkward state filled in.
//
// The rule: the frame is never wider or taller than the terminal it was told about.
func TestFrameFitsTheTerminal(t *testing.T) {
	sizes := [][2]int{
		{200, 50}, {120, 40}, {100, 30}, {80, 24}, {60, 20}, {40, 10}, {30, 8},
	}
	modes := []mode{modeMenu, modeSearch, modeResults, modeFiles, modeHistory, modePlaying}

	for _, size := range sizes {
		for _, mode := range modes {
			for _, queueLen := range []int{0, 2, 9} {
				m := initialModel(nil, nil)
				m.width, m.height = size[0], size[1]
				m.mode = mode

				// a track with a title and a link long enough to want clipping
				m.track = Track{
					Title:   "a fairly long song title that will want clipping",
					Stream:  true,
					PageURL: "https://youtube.com/watch?v=abcdefghijklmnop",
				}
				m.elapsed, m.length = 95*time.Second, 252*time.Second
				for i := 0; i < queueLen; i++ {
					m.queue = append(m.queue, m.track)
				}
				m.index = 0

				// the messages, the list, and the cursor in the middle of it
				m.lastError = "Playback failed: Sign in to confirm you're not a bot"
				m.notice = "volume 60%"
				m.menuCursor = 3
				m.spin = 7
				m.searchInput = "lofi"
				m.searching = true
				for i := 0; i < 71; i++ {
					m.items = append(m.items, item{title: fmt.Sprintf("song number %02d", i)})
				}
				m.itemCursor = 35

				got := m.View().Content
				if w := lipgloss.Width(got); w > m.width {
					t.Errorf("mode %d, queue %d, at %dx%d: width %d overflows",
						mode, queueLen, m.width, m.height, w)
				}
				if h := strings.Count(got, "\n") + 1; h > m.height {
					t.Errorf("mode %d, queue %d, at %dx%d: height %d overflows",
						mode, queueLen, m.width, m.height, h)
				}
			}
		}
	}
}

// The frame really is one frame: title, menu and player bar drawn the same way
// whatever the screen is. A layout that changed shape between screens is what
// makes a program feel like it is jumping about under you.
func TestFrameShowsTheSamePartsEverywhere(t *testing.T) {
	modes := []mode{modeMenu, modeSearch, modeResults, modeFiles, modeHistory, modePlaying}

	for _, mode := range modes {
		m := initialModel(nil, nil)
		m.width, m.height = 100, 30
		m.mode = mode
		m.track = Track{Title: "Midnight City", Stream: true, PageURL: "https://youtube.com/watch?v=abc"}
		m.elapsed, m.length = 95*time.Second, 252*time.Second

		got := m.View().Content

		if !strings.Contains(got, "Ektara") {
			t.Errorf("mode %d: no title", mode)
		}
		for _, choice := range menuItems {
			if !strings.Contains(got, choice) {
				t.Errorf("mode %d: the menu is missing %q", mode, choice)
			}
		}
		if !strings.Contains(got, "Midnight City") {
			t.Errorf("mode %d: the player bar does not say what is playing", mode)
		}
		if !strings.Contains(got, "1:35 / 4:12") {
			t.Errorf("mode %d: the player bar has no progress", mode)
		}
	}
}

// The sidebar's one fragile point: a name too long for 24 columns wraps onto a
// second line and pushes the rest of the menu down.
func TestMenuNamesAreShortEnough(t *testing.T) {
	// the sidebar is 24 wide and pads one column each side, so 22 are left
	const room = sidebarWidth - 2

	for _, name := range menuItems {
		if lipgloss.Width(name) > room {
			t.Errorf("menu entry %q is %d wide, the sidebar has %d",
				name, lipgloss.Width(name), room)
		}
	}
}

// The two menu lists are the same length, since one is indexed by the cursor into
// the other. A short descriptions list leaves the last rows with nothing to say.
func TestMenuDescriptionsLineUp(t *testing.T) {
	if len(menuItems) != len(menuDescriptions) {
		t.Errorf("%d menu entries but %d descriptions", len(menuItems), len(menuDescriptions))
	}

	m := initialModel(nil, nil)
	for i := range menuItems {
		m.menuCursor = i
		if m.menuDescription() == "" {
			t.Errorf("menu entry %d (%q) has no description", i, menuItems[i])
		}
	}

	// a cursor off the end of the list must not read past it
	m.menuCursor = len(menuItems) + 5
	if got := m.menuDescription(); got != "" {
		t.Errorf("a cursor past the end returned %q", got)
	}
}

// The message the program most needs to say is not the one it drops first. The
// middle box is cut off at the bottom, so whatever a screen drew last would go
// first; the frame makes room for the errors before the cutting, which is why they
// survive on a completely full screen.
func TestErrorsAreAlwaysVisible(t *testing.T) {
	m := initialModel(nil, nil)
	m.width, m.height = 80, 24
	m.mode = modeMenu

	// a failure, then something even newer to say
	m.lastError = "Playback failed: Sign in to confirm you're not a bot"
	m.notice = "volume 60%"

	got := m.View().Content
	if !strings.Contains(stripped(got), "Sign in to confirm") {
		t.Errorf("the failure is missing:\n%s", stripped(got))
	}
	if !strings.Contains(stripped(got), "volume 60%") {
		t.Errorf("the notice is missing:\n%s", stripped(got))
	}

	// and on the screens where the errors do not belong in the drawing code at
	// all, which is all of them now
	for _, mode := range []mode{modeSearch, modeResults, modeFiles, modeHistory, modePlaying} {
		m.mode = mode
		if !strings.Contains(stripped(m.View().Content), "Sign in to confirm") {
			t.Errorf("mode %d: the failure is missing", mode)
		}
	}
}

// Removes the colour codes, so a test can look for words rather than a whole
// styled line.
func stripped(s string) string {
	var b strings.Builder
	inCode := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inCode = true
		case inCode && (r == 'm' || r == 'K'):
			inCode = false
		case !inCode:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// The player bar holds its shape: its lines are a fixed height, so a long title or
// link wrapping onto a second line would push the bottom of the screen off the
// terminal. Every line is clipped to the width instead.
func TestNothingPrintedIsAWrap(t *testing.T) {
	for _, size := range [][2]int{{200, 50}, {100, 30}, {80, 24}, {60, 20}, {40, 10}} {
		m := initialModel(nil, nil)
		m.width, m.height = size[0], size[1]
		m.track = Track{
			Title:   strings.Repeat("a very long song title ", 10),
			Stream:  true,
			PageURL: "https://youtube.com/watch?v=" + strings.Repeat("x", 100),
		}
		m.elapsed, m.length = 95*time.Second, 252*time.Second
		m.notice = strings.Repeat("a long notice ", 20)

		got := m.View().Content
		if w := lipgloss.Width(got); w > m.width {
			t.Errorf("at %dx%d: width %d overflows", m.width, m.height, w)
		}
		if h := strings.Count(got, "\n") + 1; h > m.height {
			t.Errorf("at %dx%d: height %d overflows", m.width, m.height, h)
		}
	}
}

// The first screen says the same thing every time it is drawn. The greeting used to
// be worked out while drawing, picking a random line each time, which made it
// flicker too fast to read. It is picked once now, when the model is built.
func TestGreetingDoesNotChange(t *testing.T) {
	m := initialModel(nil, nil)
	m.width, m.height = 80, 24
	m.mode = modeMenu

	if m.greeting == "" {
		t.Fatal("the model was built with no greeting")
	}

	first := m.View().Content
	for i := 0; i < 100; i++ {
		if got := m.View().Content; got != first {
			t.Fatalf("frame %d differs from the first", i)
		}
	}
}

// The greeting is not just any line: each band of the day has its own pool, and a
// line from the wrong band is as wrong as no greeting at all. The greeting is a
// random pick, so each hour is asked a hundred times and every answer has to be one
// of that hour's own lines.
func TestTimeMsgSuitsTheHour(t *testing.T) {
	hours := []int{2, 7, 12, 15, 20, 23}

	for _, hour := range hours {
		allowed := greetingPool(hour)
		if len(allowed) == 0 {
			t.Errorf("%02d:00 has no greetings at all", hour)
			continue
		}

		for i := 0; i < 100; i++ {
			got := timeMsgFor(hour)
			if !slices.Contains(allowed, got) {
				t.Errorf("%02d:00: the greeting was %q, which is not one of that hour's %d lines",
					hour, got, len(allowed))
			}
		}
	}
}

// The bands do not overlap. If two shared a line the test above would pass while
// the greeting was plainly wrong for one of them, which looks fine until someone
// runs it at the wrong time of day.
func TestEachHourHasItsOwnPool(t *testing.T) {
	// the bands, and the hour that picks each one
	hours := []int{2, 7, 12, 15, 20, 23}

	for i := 0; i < len(hours); i++ {
		for j := i + 1; j < len(hours); j++ {
			first, second := hours[i], hours[j]
			for _, line := range greetingPool(first) {
				if slices.Contains(greetingPool(second), line) {
					t.Errorf("%02d:00 and %02d:00 both use %q", first, second, line)
				}
			}
		}
	}
}

// The frame covers the whole screen: one line fewer and the bottom row is never
// drawn, so the terminal's own background shows through the player bar. That is an
// off by one rather than anything to do with colour.
//
// lipgloss counts a border as part of a style's height, so the bar's height and
// the height taken off the terminal for it have to be the same number. When they
// were not, the frame was one line short at every size.
func TestFrameFillsTheTerminalExactly(t *testing.T) {
	sizes := [][2]int{{200, 50}, {120, 40}, {100, 30}, {80, 24}, {60, 20}, {40, 10}}

	for _, size := range sizes {
		for _, mode := range []mode{modeMenu, modeSearch, modeResults, modePlaying} {
			m := initialModel(nil, nil)
			m.width, m.height = size[0], size[1]
			m.mode = mode
			m.track = Track{Title: "Midnight City"}

			got := m.View().Content
			rows := strings.Count(got, "\n") + 1
			if rows != m.height {
				t.Errorf("mode %d at %dx%d: the frame is %d lines, the terminal is %d",
					mode, m.width, m.height, rows, m.height)
			}
		}
	}
}

// The check for a transparent screen. A cell with no background of its own shows
// whatever the terminal's background is: a dark grey nobody notices on most
// terminals, the desktop on a transparent one.
//
// The border along the bottom was the row doing this, with a foreground colour for
// the line and no background around it. Checking the width of every row cannot see
// it, because the row is the right width; it has to be the colour that is checked.
func TestEveryRowHasABackground(t *testing.T) {
	// a background colour, either as 24-bit rgb or as one of the sixteen
	background := regexp.MustCompile(`(?:48;2;\d+;\d+;\d+|4[0-7];)`)

	sizes := [][2]int{{200, 50}, {120, 40}, {100, 30}, {80, 24}, {60, 20}}

	for _, size := range sizes {
		for _, mode := range []mode{modeMenu, modeSearch, modeResults, modeFiles, modeHistory, modePlaying} {
			m := initialModel(nil, nil)
			m.width, m.height = size[0], size[1]
			m.mode = mode
			m.track = Track{Title: "Born of a Star", Stream: true, PageURL: "https://youtube.com/watch?v=abc"}
			m.elapsed, m.length = 7*time.Second, 288*time.Second
			m.lastError = "Playback failed"
			m.notice = "volume 60%"
			m.spin = 3
			m.items = nil
			for i := range 9 {
				m.items = append(m.items, item{title: fmt.Sprintf("result %d", i)})
			}
			m.itemCursor = 4

			for i, row := range strings.Split(m.View().Content, "\n") {
				if !background.MatchString(row) {
					t.Errorf("mode %d at %dx%d: row %d has no background colour, "+
						"so a transparent terminal shows through it", mode, m.width, m.height, i)
				}
			}
		}
	}
}

// The menu and the queue are both on screen together, and give way in the right
// order as the terminal narrows. The menu goes first because it is what the user
// came to look at least: content is worth more than navigation, and more than a
// queue. Below that only the content is left, which beats three unusable columns.
func TestTheFrameHasThreeColumnsWhenThereIsRoom(t *testing.T) {
	cases := []struct {
		width          int
		sidebar, queue bool
		wantContent    int
	}{
		{200, true, true, 200 - sidebarWidth - queueWidth},
		{120, true, true, 120 - sidebarWidth - queueWidth},
		{84, true, true, 84 - sidebarWidth - queueWidth}, // exactly enough for all three
		{83, true, false, 83 - sidebarWidth},             // one column short: the queue goes
		{60, true, false, 60 - sidebarWidth},             // only the menu fits
		{53, false, false, 53},                           // not even the menu
		{40, false, false, 40},
	}

	for _, c := range cases {
		m := initialModel(nil, nil)
		m.width, m.height = c.width, 30

		if got := m.showSidebar(); got != c.sidebar {
			t.Errorf("width %d: showSidebar = %v, want %v", c.width, got, c.sidebar)
		}
		if got := m.showQueue(); got != c.queue {
			t.Errorf("width %d: showQueue = %v, want %v", c.width, got, c.queue)
		}
		if got := m.contentWidth(); got != c.wantContent {
			t.Errorf("width %d: contentWidth = %d, want %d", c.width, got, c.wantContent)
		}
	}
}

// The arithmetic behind the frame: the three columns have to add up to exactly the
// terminal width, or the right hand one hangs off the side and the middle one is
// not where it looks.
func TestTheColumnsAddUpToTheTerminal(t *testing.T) {
	for _, width := range []int{84, 100, 120, 160, 200, 240} {
		m := initialModel(nil, nil)
		m.width, m.height = width, 30

		total := m.contentWidth()
		if m.showSidebar() {
			total += sidebarWidth
		}
		if m.showQueue() {
			total += queueWidth
		}

		if total != width {
			t.Errorf("width %d: the columns add up to %d", width, total)
		}
	}
}

// The player bar is exactly as tall as the layout says. If the two numbers disagree
// the bottom of the screen is either left unpainted or pushed off the terminal.
func TestTheBarIsSixRowsAndFits(t *testing.T) {
	for _, width := range []int{60, 80, 100, 120, 200} {
		m := initialModel(nil, nil)
		m.width, m.height = width, 30
		m.track = Track{Title: "Born of a Star"}
		m.elapsed, m.length = 95*time.Second, 288*time.Second

		bar := playerStyle.Width(width).Height(playerBlockHeight).Render(m.drawPlayerBar())
		if got := strings.Count(bar, "\n") + 1; got != playerBlockHeight {
			t.Errorf("width %d: the bar is %d rows, playerBlockHeight is %d",
				width, got, playerBlockHeight)
		}
	}
}

// The song title sits in the middle of the bar: it is the one line about the music
// rather than the controls.
func TestTheTitleIsCentred(t *testing.T) {
	m := initialModel(nil, nil)
	m.width, m.height = 100, 30
	m.track = Track{Title: "Midnight City"}
	m.elapsed, m.length = 95*time.Second, 288*time.Second

	first := strings.Split(m.drawPlayerBar(), "\n")[0]
	plain := stripped(first)

	// the title is on the line, with the same number of spaces either side
	left := len(plain) - len(strings.TrimLeft(plain, " "))
	right := len(plain) - len(strings.TrimRight(plain, " "))

	if !strings.Contains(plain, "Midnight City") {
		t.Fatalf("the title is not on the first line: %q", plain)
	}
	// an odd number of spare columns cannot be split exactly, so being one out
	// is as centred as it gets
	if left-right > 1 || right-left > 1 {
		t.Errorf("the title is not centred: %d spaces before, %d after", left, right)
	}
}

// The bar is in the order asked for: title, progress bar, then the volume and
// transport underneath rather than sharing a line with the bar.
func TestVolumeAndTransportAreBelowTheProgressBar(t *testing.T) {
	m := initialModel(nil, nil)
	m.width, m.height = 120, 30
	m.track = Track{Title: "Midnight City"}
	m.elapsed, m.length = 95*time.Second, 288*time.Second
	m.volume = 0.6

	lines := strings.Split(stripped(m.drawPlayerBar()), "\n")

	// the progress bar and the times are on the second line
	if len(lines) < 5 {
		t.Fatalf("the bar is only %d lines", len(lines))
	}
	if !strings.Contains(lines[1], "1:35 / 4:48") {
		t.Errorf("line 1 is not the progress bar: %q", lines[1])
	}

	// the volume and the transport are on the line below it
	below := strings.Join(lines[2:], "\n")
	if !strings.Contains(below, "60%") {
		t.Errorf("the volume is not below the progress bar:\n%s", below)
	}
	if !strings.Contains(below, "prev") || !strings.Contains(below, "next") {
		t.Errorf("the transport buttons are not below the progress bar:\n%s", below)
	}

	// and the volume is on the left with the buttons on the right
	transport := lines[3]
	if strings.Index(transport, "vol") > strings.Index(transport, "prev") {
		t.Errorf("the volume is not left of the buttons: %q", transport)
	}
}

// Left and right move the playhead; up and down do not, because there is no list on
// the player screen for them to move through.
func TestArrowKeysSeek(t *testing.T) {
	// a player is needed, because seeking is the player's job and a model built
	// with none of them does nothing on any key
	player := &fakeAudio{}

	for _, c := range []struct {
		name string
		code rune
	}{
		{"left", tea.KeyLeft},
		{"right", tea.KeyRight},
	} {
		m := initialModel(nil, player)
		m.mode = modePlaying
		m.track = Track{Title: "Midnight City"}
		m.length = 288 * time.Second

		next, _ := m.onKey(tea.KeyPressMsg(tea.Key{Code: c.code}))
		if next == nil {
			t.Errorf("the %s arrow produced no model", c.name)
		}
	}

	// up and down are deliberately not bound on this screen: there is no list
	// here for them to move a cursor through, so they are two keys that would
	// silently do nothing.
	for _, code := range []rune{tea.KeyUp, tea.KeyDown} {
		m := initialModel(nil, player)
		m.mode = modePlaying
		m.track = Track{Title: "Midnight City"}
		m.length = 288 * time.Second

		before := m.startTime
		m.onKey(tea.KeyPressMsg(tea.Key{Code: code}))
		if m.startTime != before {
			t.Errorf("the up or down arrow moved the playhead")
		}
	}

	// the hint line has to say so, or nobody knows the arrows do anything
	m := initialModel(nil, player)
	hints := stripped(m.keyHints())
	if !strings.Contains(hints, "←") || !strings.Contains(hints, "→") {
		t.Errorf("the key hints do not mention the arrows: %q", hints)
	}
}

// Whatever the cover is sized from, it must not change while one track plays. It is
// drawn once and kept, keyed by track and width, so a width that moved during a
// track would throw the drawing away with nothing to replace it and the cover would
// blink out on every key press. Volume, shuffle, repeat and mute all raise a
// notice.
func TestANoticeNeverResizesTheCover(t *testing.T) {
	m := initialModel(nil, nil)
	m.width, m.height = 120, 34
	m.mode = modePlaying
	m.track = Track{ID: "stable", Title: "Midnight City"}

	width := m.artWidth()
	key := artKey(m.track, width)

	// everything a key press can do to the model that touches a message
	for _, change := range []func(*model){
		func(m *model) { m.notice = "volume 60%" },
		func(m *model) { m.notice = "volume 65%" },
		func(m *model) { m.notice = "shuffle on" },
		func(m *model) { m.notice = "repeat on" },
		func(m *model) { m.notice = "muted" },
		func(m *model) { m.notice = "" },
		func(m *model) { m.volume = 0.1 },
		func(m *model) { m.volume = 0.9 },
		func(m *model) { m.muted = true },
		func(m *model) { m.paused = true },
		func(m *model) { m.spin++ },
		func(m *model) { m.elapsed += time.Second },
	} {
		change(&m)

		if got := m.artWidth(); got != width {
			t.Errorf("the cover width moved to %d, was %d", got, width)
		}
		if got := artKey(m.track, m.artWidth()); got != key {
			t.Errorf("the cover key moved to %q, was %q", got, key)
		}
	}
}

// sgrState is in sgr.go.

// Every cell needs a background of its own; one without shows the terminal's own,
// invisible on a dark terminal and the desktop on a transparent one.
//
// Walks the frame the way a terminal walks it, tracking colour state cell by cell,
// rather than looking for a background code somewhere in each line. A line can
// contain one and still leave most of itself unpainted, which is exactly what
// happened twice here: a border with a foreground but no background, and every
// coloured run ending in a full reset that took the box's background with it.
func TestNoCellIsLeftUnpainted(t *testing.T) {
	for _, size := range [][2]int{{200, 50}, {160, 44}, {120, 36}, {100, 30}, {84, 24}, {60, 20}} {
		for _, mode := range []mode{modeMenu, modeSearch, modeResults, modeFiles, modeHistory, modePlaying} {
			m := modelWithEverything(size[0], size[1], mode)

			for row, line := range strings.Split(m.View().Content, "\n") {
				var st sgrState
				col := 0

				runes := []rune(line)
				for i := 0; i < len(runes); i++ {
					// an escape sequence
					if runes[i] == 0x1b && i+1 < len(runes) && runes[i+1] == '[' {
						end := i + 2
						for end < len(runes) && runes[end] != 'm' {
							end++
						}
						if end < len(runes) {
							st = st.apply(string(runes[i+2 : end]))
						}
						i = end
						continue
					}

					// the end of the line, not a cell
					if runes[i] == '\n' {
						continue
					}

					col++
					if !st.hasBackground() {
						t.Errorf("%dx%d mode %d: row %d column %d is unpainted, character %q",
							m.width, m.height, mode, row, col, runes[i])
						return
					}
				}
			}
		}
	}
}

// A model in the worst state the drawing code can be asked for: a track playing
// with a cover, a full queue, a long title, a notice, a failure, a long list with
// the cursor in the middle and a history to show. Every screen is then drawn with
// all of that in place, so a line that only goes wrong when everything is set is
// still covered.
func modelWithEverything(width, height int, mode mode) model {
	m := initialModel(nil, nil)
	m.width, m.height = width, height
	m.mode = mode

	m.track = Track{
		ID:       "dQw4w9WgXcQ",
		Title:    "Never Gonna Give You Up (Official Music Video) (4K Remaster)",
		Stream:   true,
		PageURL:  "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		Filename: "dQw4w9WgXcQ youtube - Never Gonna Give You Up.mp3",
	}
	m.elapsed, m.length = 95*time.Second, 213*time.Second
	m.paused = true
	m.opts.shuffle = true
	m.opts.repeat = true
	m.notice = "volume 60%"
	m.lastError = "Playback failed: Sign in to confirm you're not a bot"
	m.menuCursor = 3
	m.spin = 5
	m.searchInput = "lofi girl"
	m.searching = true
	m.itemCursor = 20
	m.index = 3

	song := Track{Title: "a song in the list", ID: "x"}
	for range 40 {
		m.items = append(m.items, item{title: song.Title, track: song})
		m.recent = append(m.recent, song)
		m.queue = append(m.queue, song)
	}

	// and a cover, as the background fetch would have left it
	next, _ := m.onArt(artMsg{key: artKey(m.track, m.artWidth()), art: coverArt(m.artWidth())})
	return next.(model)
}

// The cover is never taller than the rows reserved for it. The width is worked out
// before the picture arrives, from the shape a thumbnail is, and the picture is
// then drawn at that width. If the two disagree about how tall it comes out, the
// art is taller than its slot and the middle box
// is cut off in the middle of the picture. This is the check on that: the room
// is reserved from one function and the art is measured off the other.
func TestTheArtFitsTheRoomItWasGiven(t *testing.T) {
	for _, size := range [][2]int{{240, 70}, {200, 50}, {160, 44}, {120, 36}, {100, 30}, {84, 24}, {60, 20}, {40, 12}} {
		for _, mode := range []mode{modeMenu, modePlaying} {
			m := modelWithEverything(size[0], size[1], mode)

			reserved := m.playerArtRows()
			if mode == modeMenu {
				reserved = m.menuArtRows()
			}

			width := m.artWidth()
			if width == 0 {
				continue // too narrow for a picture, which is allowed
			}

			// what the art is actually drawn at, and how many rows that is. The
			// render ends every line with a newline, so the count is the newlines
			// and not one more than that.
			art := coverArt(width)
			drawn := strings.Count(art, "\n")

			if drawn > reserved {
				t.Errorf("%dx%d mode %d: %d columns of art is %d rows but only %d were reserved",
					m.width, m.height, mode, width, drawn, reserved)
			}

			// and the two functions that work the shape out must agree with the
			// renderer about it, or the cover overflows its slot
			if got, want := asciiart.RowsFor(width), drawn; got != want {
				t.Errorf("%dx%d mode %d: at %d columns RowsFor says %d rows and the art came out %d",
					m.width, m.height, mode, width, got, want)
			}
		}
	}
}

// The cover comes out the shape of a video thumbnail rather than a square: a
// square drawing of a 16:9 picture throws a third of it away.
func TestTheArtIsWiderThanItIsTall(t *testing.T) {
	m := modelWithEverything(150, 40, modePlaying)
	width := m.artWidth()
	if width == 0 {
		t.Fatal("no art at this size")
	}

	art := coverArt(width)
	rows := strings.Count(art, "\n")
	cols := len([]rune(strings.SplitN(art, "\n", 2)[0]))

	if rows >= cols {
		t.Errorf("the art is %d rows by %d columns, which is not a 16:9 thumbnail", rows, cols)
	}
}

// TestTheCursorColourStaysOnTheCursor checks that the accent colour is on the
// menu row under the cursor and on no other row.
//
// fg() leaves a colour open at the end of a line on purpose, so that a box's
// background survives to the end of that line. That is right within one line, but
// lipgloss word-wraps a block before styling it, and its wrapper re-states any
// style still open when it reaches a newline. An accent left open on the selected
// row was therefore carried onto every row below it, which turned the whole menu
// the colour of the cursor.
//
// The frame is walked the way a terminal walks it, as TestNoCellIsLeftUnpainted
// does, because a colour being present in a line is not the same as it being in
// force when a particular character is written.
func TestTheCursorColourStaysOnTheCursor(t *testing.T) {
	accentSGR := accentRGB()

	for _, cursor := range []int{0, 2, len(menuItems) - 1} {
		for _, size := range [][2]int{{160, 44}, {120, 34}, {100, 30}} {
			m := modelWithEverything(size[0], size[1], modeMenu)
			m.mode = modeMenu
			m.menuCursor = cursor

			for row, text := range strings.Split(m.View().Content, "\n") {
				plain, fg := plainAndForeground(text)

				if !strings.Contains(plain, menuItems[cursor]) {
					continue // a row that is not the selected one
				}
				if fg != accentSGR {
					t.Errorf("cursor %d at %dx%d: %q is on row %d but its foreground is %q, want the accent",
						cursor, size[0], size[1], menuItems[cursor], row, fg)
				}
			}
		}
	}
}

// TestNoOtherMenuRowTakesTheCursorColour is the other half of the same
// mistake: not only should the selected row be accented, no other row should be.
// It is a separate test because the failure looks different, and because the
// first one would pass if the cursor row were the only row drawn at all.
func TestNoOtherMenuRowTakesTheCursorColour(t *testing.T) {
	accentSGR := accentRGB()

	for cursor := range menuItems {
		m := modelWithEverything(120, 34, modeMenu)
		m.mode = modeMenu
		m.menuCursor = cursor

		for _, choice := range menuItems {
			if choice == menuItems[cursor] {
				continue
			}

			for row, text := range strings.Split(m.View().Content, "\n") {
				plain, fg := plainAndForeground(text)
				if !strings.Contains(plain, choice) {
					continue
				}
				if fg == accentSGR {
					t.Errorf("cursor on %q: %q on row %d is accented as well",
						menuItems[cursor], choice, row)
				}
			}
		}
	}
}

// plainAndForeground strips the escape codes out of a line and reports the
// foreground in force at the first character that is actually painted.
//
// The first painted character is the one that matters: it is the first thing a
// reader sees on the row, and it is where a leaked colour shows up first. Later
// characters on the same row can legitimately change colour.
func plainAndForeground(line string) (string, string) {
	var st sgrState
	var plain strings.Builder
	var foreground string
	seen := false

	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		if runes[i] == 0x1b && i+1 < len(runes) && runes[i+1] == '[' {
			end := i + 2
			for end < len(runes) && runes[end] != 'm' {
				end++
			}
			if end < len(runes) {
				st = st.apply(string(runes[i+2 : end]))
				i = end
				continue
			}
		}
		if unicode.IsSpace(runes[i]) {
			plain.WriteRune(runes[i])
			continue
		}
		if !seen {
			// the state is read before the character is written, because the
			// character is what the state applies to
			foreground = st.fg
			seen = true
		}
		plain.WriteRune(runes[i])
	}

	return plain.String(), foreground
}

// accentRGB is the accent colour as the numbers a terminal reads out of an
// escape sequence, in the form sgrState keeps them in. The leading 38 is the
// instruction to set a foreground and is part of what sgrState stores.
func accentRGB() string {
	n, err := strconv.ParseUint(strings.TrimPrefix(accent, "#"), 16, 32)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("38;2;%d;%d;%d", n>>16&0xFF, n>>8&0xFF, n&0xFF)
}
