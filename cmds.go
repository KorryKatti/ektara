package main

// Commands: work too slow to do on the goroutine that draws. Each runs in the
// background and hands back a message.

import (
	"context"
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

// Opens a track. A stream asks yt-dlp to resolve a media url first, so it takes
// seconds and happens off the display's goroutine.
//
// The track comes back with whatever was learned about it on the way.
func openTrackCmd(a audio, t Track) tea.Cmd {
	return func() tea.Msg {
		track, err := openTrack(a, t)
		return trackOpenedMsg{err: err, track: track}
	}
}

func searchCmd(db *sql.DB, query string) tea.Cmd {
	return func() tea.Msg {
		videos, err := searchYouTube(db, query)
		return searchedMsg{videos: videos, err: err}
	}
}

// Reads the history in the background, so the first screen has something real on
// it rather than made up entries.
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

// Downloads a YouTube video as MP3 and returns the track
// forQueue indicates whether to queue or play the track

func downloadCmd(id, title string, forQueue bool) tea.Cmd {
	return func() tea.Msg {
		track, err := downloadVideo(id, title)
		return downloadedMsg{track: track, err: err, forQueue: forQueue}
	}
}

// Fetches lyrics with retries, caches found results (including no lyrics), and retries failures later
func lyricsCmd(db *sql.DB, t Track) tea.Cmd {
	return func() tea.Msg {
		msg := lyricsMsg{forTrack: lyricsKey(t)}
		if db != nil {
			if remembered, ok := cachedLyrics(db, t.ID); ok {
				msg.lyrics = remembered
				return msg
			}
		}
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			time.Sleep(time.Duration(attempt) * time.Second)

			records, err := searchLyrics(context.TODO(), lyricQueryFromTrack(t))
			if err != nil {
				lastErr = err
				continue
			}
			found, ok := pickRecord(records, t.Artist, t.Duration)
			if ok {
				msg.lyrics = found.asLyrics()
			}
			// Cache valid responses even when no lyrics are found
			if db != nil {
				storeLyrics(db, t, msg.lyrics)
			}
			return msg
		}
		msg.err = lastErr
		return msg
	}
}
