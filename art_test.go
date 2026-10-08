package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestArtIsDroppedForTheWrongTrack checks a cover that turned up late is not
// shown against the wrong song.
//
// The cover is fetched over the network, so by the time it lands the user may
// have pressed d, or the window may have been resized. Either way the drawing is
// for something that is no longer on the screen, and showing it would put one
// song's picture next to another song's name.
func TestArtIsDroppedForTheWrongTrack(t *testing.T) {
	m := initialModel(nil, nil)
	m.width, m.height = 100, 30
	m.mode = modePlaying
	m.track = Track{ID: "aaaa", Title: "Song A"}

	// an answer for a different track entirely
	next, _ := m.onArt(artMsg{key: "bbbb/20", art: "THE WRONG ART"})
	got := next.(model)
	if got.art != "" {
		t.Errorf("art for another track was kept: %q", got.art)
	}
	if s := got.trackArt(); s != "" {
		t.Errorf("art for another track would be drawn: %q", s)
	}

	// an answer for this track but at a width the screen has moved on from
	stale := artKey(m.track, m.artWidth()+2)
	next, _ = m.onArt(artMsg{key: stale, art: "THE WRONG WIDTH"})
	got = next.(model)
	if got.art != "" {
		t.Errorf("art at the wrong width was kept: %q", got.art)
	}

	// the right one, and it is kept
	right := artKey(m.track, m.artWidth())
	next, _ = m.onArt(artMsg{key: right, art: "THE ART"})
	got = next.(model)
	if got.trackArt() != "THE ART" {
		t.Errorf("the right art was not kept, got %q", got.trackArt())
	}
}

// TestArtIsRefetchedOnResize checks a new window size throws the old drawing
// away and asks for a new one, rather than showing a picture drawn for the old
// size or never asking at all.
func TestArtIsRefetchedOnResize(t *testing.T) {
	m := initialModel(nil, nil)
	m.mode = modePlaying
	m.track = Track{ID: "aaaa", Title: "Song A"}
	m.width, m.height = 100, 30
	m.art = "ART DRAWN AT 100"
	m.artKey = artKey(m.track, m.artWidth())

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	got := next.(model)

	if got.art != "" {
		t.Errorf("the drawing from the old size was kept: %q", got.art)
	}
	if got.artKey != "" {
		t.Errorf("the key from the old size was kept: %q", got.artKey)
	}
	if cmd == nil {
		t.Error("resizing did not ask for a new drawing")
	}
}

// TestArtIsNotAskedForPointlessly checks the fetch is skipped when it cannot
// possibly be used: a local mp3 has no thumbnail, and a screen with no room has
// nowhere to put a picture. Asking anyway would be a download for nothing.
func TestArtIsNotAskedForPointlessly(t *testing.T) {
	// a local file has no video id, so there is no thumbnail address
	m := initialModel(nil, nil)
	m.width, m.height = 100, 30
	m.mode = modePlaying
	m.track = Track{Filename: "local.mp3", Title: "Local Song"}
	if cmd := m.wantArt(); cmd != nil {
		t.Error("a local file asked for a cover, which it cannot have")
	}

	// a screen with no room for a picture
	tiny := initialModel(nil, nil)
	tiny.width, tiny.height = 30, 6
	tiny.mode = modePlaying
	tiny.track = Track{ID: "aaaa", Title: "Song A"}
	if w := tiny.artWidth(); w != 0 {
		t.Errorf("artWidth at 30x6 = %d, expected no room for a cover", w)
	}
	if cmd := tiny.wantArt(); cmd != nil {
		t.Error("asked for a cover with no room to show it")
	}
}

// TestArtIsFetchedOnlyOnce checks a drawing that already exists is not fetched
// again.
//
// The screen is drawn ten times a second and the art is asked for on every one
// of them, so without this the same thumbnail would be downloaded over and over
// for as long as the track played.
func TestArtIsFetchedOnlyOnce(t *testing.T) {
	// an empty cache, so this test does not depend on what ran before it
	artCache.Lock()
	artCache.byKey = map[string]string{}
	artCache.Unlock()

	track := Track{ID: "test-once", Title: "Song"}

	if artCmd(track, 20) == nil {
		t.Fatal("the first request for a drawing was refused")
	}

	// pretend it arrived
	artCache.Lock()
	artCache.byKey[artKey(track, 20)] = "the drawing"
	artCache.Unlock()

	if artCmd(track, 20) != nil {
		t.Error("asked again for a drawing that already exists")
	}

	// a different width is a different drawing, so that one is still wanted
	if artCmd(track, 24) == nil {
		t.Error("a drawing at a new width was refused, but it is a new drawing")
	}
}

// TestANoticeDoesNotMoveTheCover is the bug where pressing + for the volume made
// the cover disappear.
//
// A notice used to be drawn at the bottom of the middle box, which made the box
// two rows shorter, which changed the width the cover was drawn at, which meant
// the cover on the model was the wrong shape and was thrown away. Nothing
// refetched it, because a cover that needs refetching is only noticed when the
// terminal resizes. So the cover stayed blank until the notice expired a second
// later, and then came back on its own.
//
// The cover is now sized from the part of the screen that does not move, and a
// notice is drawn in the player bar where it costs the box nothing. So neither
// the width nor the key may change when a notice comes and goes.
func TestANoticeDoesNotMoveTheCover(t *testing.T) {
	m := initialModel(nil, nil)
	m.width, m.height = 100, 30
	m.mode = modePlaying
	m.track = Track{ID: "notice-test", Title: "Midnight City"}

	// the cover arrives
	next, _ := m.onArt(artMsg{key: artKey(m.track, m.artWidth()), art: "THE COVER"})
	m = next.(model)

	if m.trackArt() == "" {
		t.Fatalf("setup: the cover should be showing, artWidth=%d", m.artWidth())
	}

	width := m.artWidth()
	key := m.artKey

	// every notice the keys raise, one after another, then gone
	for _, notice := range []string{
		"volume 60%", "volume 65%", "shuffle on", "repeat on",
		"muted", "unmuted", "volume 55%",
	} {
		m.notice = notice

		if m.artWidth() != width {
			t.Errorf("the notice %q changed the cover width from %d to %d",
				notice, width, m.artWidth())
		}
		if m.artKey != key {
			t.Errorf("the notice %q changed the cover key from %q to %q",
				notice, key, m.artKey)
		}
		if m.trackArt() == "" {
			t.Errorf("the notice %q made the cover disappear", notice)
		}
	}
}

// TestTheCoverSurvivesANotice checks the whole round trip on the real screen: a
// notice is shown, it expires, and the cover is still the cover afterwards rather
// than a blank space that never fills in.
func TestTheCoverSurvivesANotice(t *testing.T) {
	m := initialModel(nil, nil)
	m.width, m.height = 100, 30
	m.mode = modePlaying
	m.track = Track{ID: "round-trip", Title: "Midnight City"}

	next, _ := m.onArt(artMsg{key: artKey(m.track, m.artWidth()), art: "THE COVER"})
	m = next.(model)

	m.notice = "volume 60%"
	withNotice := stripped(m.View().Content)

	m.notice = ""
	after := stripped(m.View().Content)

	if !strings.Contains(withNotice, "volume 60%") {
		t.Error("the notice was not shown")
	}
	if !strings.Contains(after, "THE COVER") {
		t.Error("the cover did not survive the notice")
	}
	if !strings.Contains(withNotice, "THE COVER") {
		t.Error("the cover was not on screen while the notice was")
	}
}
