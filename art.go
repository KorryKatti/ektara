package main

// This file is the cover art: getting the picture for whatever is playing, and
// turning it into text.
//
// The picture for a track is its YouTube thumbnail, whose address is a fixed
// pattern with the video id dropped in (see thumbnailURL in player.go). Nothing
// is stored about it and nothing is looked up: the address is built, fetched,
// drawn, and thrown away.
//
// Drawing is the slow part and it is done over and over, because the screen is
// redrawn ten times a second and the art changes size when the terminal does.
// So a finished drawing is kept, keyed by the track and the width it was drawn
// at, and the same one is handed back next time. A track that has been played
// before is therefore drawn once per width rather than once per frame.

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"ektara/asciiart"
)

// artTimeout is how long a thumbnail has to arrive. A picture that is slower
// than this is not worth waiting for: the screen does without it rather than
// showing an empty box, and a fresh attempt goes out the next time the track
// comes round.
const artTimeout = 5 * time.Second

// artMsg says a drawing finished and is ready to be shown. The key is the track
// and width it was drawn for, so an answer that turns up after the user has
// moved on can be thrown away instead of being shown against the wrong track.
type artMsg struct {
	key string
	art string
}

// artCache holds the finished drawings. The lock is because the fetching happens
// on other goroutines while the screen is being drawn, and a map written from
// two of them at once is a crash rather than a wrong picture.
var artCache = struct {
	sync.Mutex
	byKey map[string]string
}{byKey: map[string]string{}}

// artKey names one drawing: this track, at this width. The width is in the key
// because the same track drawn at two sizes is two drawings.
func artKey(t Track, width int) string {
	return t.ID + "/" + strconv.Itoa(width)
}

// cachedArt is a drawing that is already done, or "" if there is not one yet. It
// never fetches anything, so it is safe to call while drawing the screen.
func cachedArt(key string) string {
	artCache.Lock()
	defer artCache.Unlock()
	return artCache.byKey[key]
}

// artCmd goes off to fetch and draw a track's picture, then sends back the
// result.
//
// It is a command rather than something done while drawing because it waits on
// the network. Drawing happens ten times a second on the one goroutine that owns
// the screen, so a download done there would freeze the program for as long as
// the download took.
//
// A width below eight means there is no room for a picture, so nothing is sent
// and nothing is fetched.
func artCmd(t Track, width int) tea.Cmd {
	if width < 8 {
		return nil
	}

	key := artKey(t, width)

	// already drawn, or already on its way. Asking again would fetch the same
	// picture over and over for as long as the track played.
	if cachedArt(key) != "" {
		return nil
	}

	url := thumbnailURL(t.ID)
	if url == "" {
		// a local mp3 has no thumbnail, and there is nothing to fall back to
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), artTimeout)
		defer cancel()

		art, err := asciiart.RenderURL(ctx, url, asciiart.Options{
			Width:      width,
			Color:      true,
			Mode:       asciiart.Braille,
			Trim:       true,
			Background: contentPanel,
		})
		if err != nil {
			// a missing thumbnail is not worth interrupting anything for, and
			// nothing is said on screen: the box is simply left empty
			log.Printf("cover for %s: %v", t.ID, err)
			return nil
		}

		artCache.Lock()
		artCache.byKey[key] = art
		artCache.Unlock()

		return artMsg{key: key, art: art}
	}
}

// onArt takes the answer to a fetch.
//
// A drawing that turns up for a track that is no longer playing, or at a width
// the screen has moved on from, is dropped: it would otherwise be shown against
// the wrong song. The key is checked against the one wanted right now, and the
// key is stored alongside the drawing so the screen can check it again later
// without having to remember what it asked for.
func (m model) onArt(msg artMsg) (tea.Model, tea.Cmd) {
	want := artKey(m.track, m.artWidth())
	if msg.key != want {
		return m, nil
	}
	m.art = msg.art
	m.artKey = want
	return m, nil
}
