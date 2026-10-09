package main

// Commands: work too slow to do on the goroutine that draws. Each runs in the
// background and hands back a message.

import (
	"database/sql"
	"log"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Waits a moment, then says "look at the audio again".
func tick() tea.Cmd {
	return tea.Tick(redrawEvery, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Opening a local file is quick, but opening a stream asks yt-dlp to resolve a
// media url first, which takes seconds, so this happens off the display's
// goroutine.
func openTrackCmd(a audio, t Track) tea.Cmd {
	return func() tea.Msg {
		return trackOpenedMsg{err: openTrack(a, t)}
	}
}

func searchCmd(db *sql.DB, query string) tea.Cmd {
	return func() tea.Msg {
		videos, err := searchYouTube(db, query)
		return searchedMsg{videos: videos, err: err}
	}
}

// Read in the background, so the first screen has something real on it rather
// than made up entries.
func recentCmd(db *sql.DB) tea.Cmd {
	return func() tea.Msg {
		tracks, err := historyTracks(db)
		if err != nil {
			// no history is not a reason to refuse to start
			log.Printf("recent: %v", err)
			return recentMsg{}
		}
		return recentMsg{tracks: tracks}
	}
}

func downloadCmd(id, title string) tea.Cmd {
	return func() tea.Msg {
		track, err := downloadVideo(id, title)
		return downloadedMsg{track: track, err: err}
	}
}
