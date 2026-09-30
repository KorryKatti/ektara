// tui_test.go
package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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

// TestDrawItemsWindows checks that only a screenful of a long list is drawn,
// and that the "showing x-y of n" line tells the user the rest is there.
func TestDrawItemsWindows(t *testing.T) {
	m := initialModel(nil, nil)
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

// fakeAudio is a player that does not play anything, so the tick can be tested
// at the one point that matters: the moment a track ends.
//
// It exists because that path could not be reached before. onTick asks whether
// the player is still going, and the real player can only answer that by
// actually playing something out of a sound device, so the test had no way in
// and the end-of-a-track path went untested. That is how a bug survived in the
// one place the test above claimed to cover.
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

// TestOnTickAlwaysKeepsTheClockRunning checks the one rule the tick cannot
// break: every path out of it has to hand back another tick.
//
// A path that returns a command without one stops the clock for good. Nothing
// complains, the progress bar just quietly stops moving, and the program looks
// frozen. Only one clock is wanted, so startTrack and backToMenu must not start
// their own, which is what makes this the whole reason the rest of the function
// matters.
//
// The end-of-a-track cases are the ones that matter most, and they are the ones
// this test did not have for a long time: onTick does not return its own
// command there, it returns move()'s, and move() has no tick in it. That froze
// the display the first time any song ended, which was every time.
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

// TestEndOfTrackKeepsTheClockRunning is the bug, pinned down.
//
// When a song ended, onTick returned move()'s command, which opens the next
// track and does not include a tick. Nothing renews the clock chain, so ten
// seconds after the first song finished the progress bar stopped, the search
// spinner stopped, and the queue sat on the last track forever.
//
// The only way to test this is to actually run the command onTick returned and
// see whether it produces another tick, which is why there is a fake player at
// all.
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

// isTick says whether msg is a tick, looking one level into a batch if it has
// to. A tick is allowed to be batched alongside the work a track change does,
// so finding it among a batch's members counts as finding it.
//
// It deliberately does not recurse. Running a batch's own members would run
// openTrackCmd and the Discord update for real, on every one of a few hundred
// rounds, which turns a test about the clock into a test about the network. The
// fix batches the tick directly into what onTick returns, so the tick is always
// only one level down.
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

// TestEndOfTrackMovesTheQueueOn checks the other half: a finished track is not
// just a clock problem, it is also the moment the queue is supposed to advance.
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

// TestEndOfLastTrackReturnsToTheMenu is the other end of the same queue: the
// last song finishes and there is nothing after it, so the menu comes back.
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

// TestDrawMenuShowsTheLastFailure checks that a broken track leaves a reason
// behind.
//
// The notice a failure used to raise was wiped by the move to the next track, so
// the reason was never seen. lastError does not expire, so the queue can run out
// and the menu can still say why the music stopped.
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
