package main

// This file is the commands: the work that is too slow to do on the goroutine
// that draws. Each one runs in the background and hands back a message.

import (
	"database/sql"
	"log"
	"time"

	tea "charm.land/bubbletea/v2"
)

// ---------------------------------------------------------------------------
// commands: work that happens in the background
// ---------------------------------------------------------------------------

// tick waits a moment and then sends a tickMsg, which makes the model look at
// the audio again.
func tick() tea.Cmd {
	return tea.Tick(redrawEvery, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// openTrackCmd starts a track's audio in the background and sends back whether
// it worked. Opening a local file is quick, but opening a stream asks yt-dlp to
// resolve a media url first, which takes a second or two, so this happens off
// the display's goroutine.
func openTrackCmd(a audio, t Track) tea.Cmd {
	return func() tea.Msg {
		return trackOpenedMsg{err: openTrack(a, t)}
	}
}

// searchCmd runs a YouTube search in the background.
func searchCmd(db *sql.DB, query string) tea.Cmd {
	return func() tea.Msg {
		videos, err := searchYouTube(db, query)
		return searchedMsg{videos: videos, err: err}
	}
}

// recentCmd reads the history in the background, so the first screen has
// something real on it rather than made up entries.
func recentCmd(db *sql.DB) tea.Cmd {
	return func() tea.Msg {
		tracks, err := historyTracks(db)
		if err != nil {
			// no history is not a reason to refuse to start, it is just an empty
			// list on the screen
			log.Printf("recent: %v", err)
			return recentMsg{}
		}
		return recentMsg{tracks: tracks}
	}
}

// downloadCmd saves a video as an mp3 in the background.
func downloadCmd(id, title string) tea.Cmd {
	return func() tea.Msg {
		track, err := downloadVideo(id, title)
		return downloadedMsg{track: track, err: err}
	}
}
