// tui_test.go
package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestListWindow pins down the arithmetic that keeps a long list on screen.
//
// The history can run to hundreds of rows and the terminal is about thirty
// lines, so the list has to show a slice around the cursor. The two things that
// must always hold are that the slice fits inside the list, and that the cursor
// is in it. Everything else is cosmetic.
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

// TestListRows checks the screen-height maths, including the sizes a terminal
// can actually be shrunk to.
func TestListRows(t *testing.T) {
	cases := []struct {
		height int
		want   int
	}{
		{0, 10},  // no size reported yet, so a sensible default
		{30, 18}, // the normal size
		{12, 3},  // just fits, with the minimum three rows
		{5, 3},   // absurdly small, but still three rows rather than none
	}
	for _, c := range cases {
		m := model{height: c.height}
		if got := m.listRows(); got != c.want {
			t.Errorf("listRows(height=%d) = %d, want %d", c.height, got, c.want)
		}
	}
}

// TestSpinFrame checks the busy animation cycles and wraps, which is what stops
// the counter running past the end of the list of frames.
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

// TestDrawSearchShowsSearching is the fix for the screen that used to look
// frozen: while a search is out the box has to say so, and once it is not there
// the typing hint has to come back.
func TestDrawSearchShowsSearching(t *testing.T) {
	m := initialModel(nil)
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

// TestDrawItemsWindows checks that only a screenful of a long list is drawn,
// and that the "showing x-y of n" line tells the user the rest is there.
func TestDrawItemsWindows(t *testing.T) {
	m := initialModel(nil)
	m.width, m.height = 80, 30
	m.mode = modeHistory
	for i := 0; i < 71; i++ {
		m.items = append(m.items, item{title: fmt.Sprintf("song number %02d", i)})
	}

	got := m.drawItems()
	if !strings.Contains(got, "showing 1-18 of 71") {
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

// TestOnTickAlwaysKeepsTheClockRunning checks the one rule the tick cannot
// break: every path out of it has to hand back another tick.
//
// A path that returns nil instead stops the clock for good. Nothing complains,
// the progress bar just quietly stops moving, and the program looks frozen. Only
// one clock is wanted, so startTrack and backToMenu must not start their own,
// which makes this the whole reason the rest of the function matters.
func TestOnTickAlwaysKeepsTheClockRunning(t *testing.T) {
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
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := c.build(initialModel(nil))
			_, cmd := m.onTick()
			if cmd == nil {
				t.Error("onTick returned no command, so the clock has stopped")
			}
		})
	}
}

// TestDrawMenuShowsTheLastFailure checks that a broken track leaves a reason
// behind.
//
// The notice a failure used to raise was wiped by the move to the next track, so
// the reason was never seen. lastError does not expire, so the queue can run out
// and the menu can still say why the music stopped.
func TestDrawMenuShowsTheLastFailure(t *testing.T) {
	m := initialModel(nil)
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
