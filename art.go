package main

// Cover art: fetching the picture for whatever is playing and turning it into text.

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"ektara/asciiart"
)

// How long a thumbnail has to arrive before the screen gives up on it.
const artTimeout = 5 * time.Second

// A finished drawing, with the track and width it was drawn for.
type artMsg struct {
	key string
	art string
}

// Finished drawings, keyed by track and width. Locked because fetching happens on
// other goroutines while the screen is drawn.
var artCache = struct {
	sync.Mutex
	byKey map[string]string
}{byKey: map[string]string{}}

// Names one drawing: this track at this width.
func artKey(t Track, width int) string {
	return t.ID + "/" + strconv.Itoa(width)
}

// A drawing that is already done, or "" if there is not one yet.
func cachedArt(key string) string {
	artCache.Lock()
	defer artCache.Unlock()
	return artCache.byKey[key]
}

// Fetches and draws a track's picture in the background. A width below eight means
// no room for a picture, so nothing is sent and nothing is fetched.
func artCmd(t Track, width int) tea.Cmd {
	if width < 8 {
		return nil
	}

	key := artKey(t, width)

	// Already drawn, or already on its way.
	if cachedArt(key) != "" {
		return nil
	}

	url := thumbnailURL(t.ID)
	if url == "" {
		return nil // a local mp3 has no thumbnail
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
			// A missing thumbnail is not worth interrupting anything for.
			log.Printf("cover for %s: %v", t.ID, err)
			return nil
		}

		artCache.Lock()
		artCache.byKey[key] = art
		artCache.Unlock()

		return artMsg{key: key, art: art}
	}
}

// Takes the answer to a fetch, dropping it if the track or width has moved on.
func (m model) onArt(msg artMsg) (tea.Model, tea.Cmd) {
	want := artKey(m.track, m.artWidth())
	if msg.key != want {
		return m, nil
	}
	m.art = msg.art
	m.artKey = want
	return m, nil
}
