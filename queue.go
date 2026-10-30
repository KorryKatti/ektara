package main

// Playback: starting tracks, the queue, the tick that watches them, and the
// messages that come back when something slow finishes.

import (
	"log"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Checks if genres are missing before querying MusicBrainz
func needsGenres(t Track) bool {
	return len(t.Genres) == 0
}

// The one place a queue starts playing.
func (m model) playQueue(queue []Track) (tea.Model, tea.Cmd) {
	m.queue = queue
	m.index = 0
	m.queueWanted = 0
	m.mode = modePlaying
	m.rolling = true

	// Shuffle happens once, here, rather than on every track.
	m.wasShuffled = m.opts.shuffle
	if m.opts.shuffle {
		shuffleQueue(m.queue)
	}

	return m, m.startTrack()
}

// Adds a track to the queue without interrupting playback; plays it immediately if nothing is playing

func (m model) enqueue(t Track) (tea.Model, tea.Cmd) {
	if !m.rolling {
		return m.playQueue([]Track{t})
	}
	m.queue = append(m.queue, t)
	m.say("queued: " + t.Title)
	return m, nil
}

// Sets the model up for the current position and goes off to open the audio.
func (m *model) startTrack() tea.Cmd {
	m.stopTrack()

	// wrapped with the queue length, so repeat can go round again
	m.track = m.queue[m.index%len(m.queue)]

	m.startTime = time.Now()
	m.elapsed = 0
	m.length = 0
	m.paused = false
	m.muted = false
	m.loading = true
	m.notice = ""

	// Discord is told straight away rather than waiting for the audio, because
	// opening a stream takes a second and a presence that lags looks broken.
	m.art = ""
	m.artKey = artKey(m.track, m.artWidth())

	// Fetch lyrics in advance and reset them when the track changes
	m.lyrics = lyrics{}
	m.lyricsLoading = true
	m.lyricsFailed = false
	m.lyricsFor = lyricsKey(m.track)
	m.lyricsPinned = false
	m.lyricsTop = 0
	m.lyricOffset = 0

	// onTick already renews this chain, so starting another would double the refresh rate
	cmds := []tea.Cmd{
		openTrackCmd(m.player, m.track),
		m.presence.showCmd(m.track, m.startTime),
		artCmd(m.track, m.artWidth()),
		lyricsCmd(m.db, m.track),
	}
	// Genres are only looked up when the track has none. The answer arrives
	// later and is applied by onGenres; playback never waits for it.
	if needsGenres(m.track) {
		cmds = append(cmds, genreCmd(m.db, m.track))
	}
	return tea.Batch(cmds...)
}

// Ends the audio but keeps the player, and the open device, ready for the next
// track.
func (m *model) stopTrack() {
	if m.player != nil {
		m.player.Stop()
	}
}

// Apply lyrics only if the track hasn't changed
func (m model) onLyrics(msg lyricsMsg) (tea.Model, tea.Cmd) {
	if msg.forTrack != m.lyricsFor {
		return m, nil
	}
	m.lyricsLoading = false
	if msg.err != nil {
		log.Printf("lyrics %s: %v", m.track.label(), msg.err)
		m.lyrics = lyrics{}
		m.lyricsFailed = true
		return m, nil
	}
	m.lyrics = msg.lyrics
	return m, nil
}

// Updates track and queue genres only if the track is still the same
func (m model) onGenres(msg genresMsg) (tea.Model, tea.Cmd) {
	if msg.forTrack != lyricsKey(m.track) {
		return m, nil
	}
	if msg.err != nil {
		// A failed lookup is not "no genres": nothing is cached, so the next
		// play can ask again.
		log.Printf("genres %s: %v", m.track.label(), msg.err)
		return m, nil
	}
	m.track.Genres = msg.genres
	if len(m.queue) > 0 {
		m.queue[m.index%len(m.queue)].Genres = msg.genres
	}
	return m, nil

}

func (m model) move(delta int) (tea.Model, tea.Cmd) {
	// nothing queued
	if len(m.queue) == 0 {
		return m, nil
	}
	m.index += delta

	if m.index >= len(m.queue) {
		if !m.opts.repeat {
			return m.backToMenu()
		}
		m.index = 0
	}

	// Shuffle switched on part way through: shuffle what is left, so the change
	// takes effect now without disturbing the track already going. The index is
	// wrapped, because with repeat on it can be past the end already.
	if m.opts.shuffle && !m.wasShuffled {
		shuffleQueue(m.queue[m.index%len(m.queue):])
	}
	m.wasShuffled = m.opts.shuffle

	return m, m.startTrack()
}

// Leaves the player screen without touching the audio.
func (m model) leavePlayer() (tea.Model, tea.Cmd) {
	m.mode = modeMenu
	return m, nil
}

// earlier used to stop all music , not only for queue ending or music ending
func (m model) backToMenu() (tea.Model, tea.Cmd) {
	m.stopTrack()
	m.mode = modeMenu
	m.queue = nil
	m.queueWanted = 0
	m.loading = false
	m.rolling = false

	// Discord goes to idle once, here, rather than between every pair of tracks.
	return m, m.presence.hideCmd()
}

// The audio is stopped here, right now, and not from inside a command: a command
// runs in a goroutine and would race the program shutting down around it.
func (m model) quit() (tea.Model, tea.Cmd) {
	m.stopTrack()
	return m, tea.Quit
}

func (m model) seek(delta time.Duration) (tea.Model, tea.Cmd) {
	if m.player == nil {
		return m, nil
	}
	m.player.SeekBy(delta)
	// The Discord start time moves with the seek.
	m.startTime = m.startTime.Add(-delta)
	return m, m.presence.showCmd(m.track, m.startTime)
}

// Runs ten times a second: sees whether the track has finished, copies the
// playback position into the model, and sets the next tick going.
//
// Every single path out of here returns tick(): a path returning nil kills the
// clock for good and the progress bar freezes.
func (m model) onTick() (tea.Model, tea.Cmd) {
	m.spin++
	// notices and expiries
	// Check expiry before other guards so notices don't disappear instantly or persist across screens.
	if m.notice != "" && time.Now().After(m.noticeUntil) {
		m.notice = ""
	}
	// Track playback progress , not screen mode , so the progress bar keeps updating when the user leaves the player screen ( rolling )
	if m.rolling && m.player != nil && !m.loading {
		// Use mpv's playback position directly; it already accounts for read-ahead.
		m.elapsed = m.player.Position()
		m.length = m.player.Length()
	}
	// nothing loaded or track still starting , clock on tho
	if !m.rolling || m.loading || m.player == nil {
		return m, tick()
	}
	if !m.player.Playing() && !m.paused {
		// Keep updates running when a song ends, so the display doesn't freeze
		if err := m.player.Err(); err != nil {
			// Only save tracks that finish normally, not ones that fail to play
			log.Printf("play %s: %v", m.track.label(), err)
			m.lastError = "playback failed : " + err.Error()

			// Skip the failed track to prevent the same error repeating every tick
			next, cmd := m.move(1)
			return next, tea.Batch(cmd, tick())
		}
		// The length mpv knows, so the history has it even for a local file whose
		// title said nothing. A stream has no length for mpv, so the one the
		// metadata brought is kept rather than thrown away for a zero.
		if m.length > 0 {
			m.track.Duration = m.length
		}
		logSong(m.db, m.track)
		next, cmd := m.move(1)
		return next, tea.Batch(cmd, tick())
	}
	return m, tick()
}

// A message on the display for a couple of seconds.
func (m *model) say(msg string) {
	m.notice = msg
	m.noticeUntil = time.Now().Add(2 * time.Second)
}

func (m model) onTrackOpened(msg trackOpenedMsg) (tea.Model, tea.Cmd) {
	m.loading = false

	if msg.err != nil {
		log.Printf("open %s: %v", m.track.label(), msg.err)
		m.lastError = "Could not play " + m.track.label()
		return m.move(1) // carry on rather than sitting on a dead screen
	}

	m.lastError = ""

	// The genre lookup can finish while the audio is still opening. Keep what
	// it found, or the opened track would overwrite it with the copy that has
	// no genres yet.
	if len(m.track.Genres) > 0 {
		msg.track.Genres = m.track.Genres
	}

	// What we learned about the music, on to the track playing and to the queue
	// behind it, so the next track keeps it.
	if len(m.queue) > 0 {
		m.queue[m.index%len(m.queue)] = msg.track
	}
	m.track = msg.track
	m.track.fillFromTitle()

	if m.player != nil {
		// The model's own volume, so a new track does not reset the level.
		m.player.SetVolume(m.soundVolume())
		// It loaded paused, so the sound starts now rather than a second early.
		m.player.Play()
	}

	return m, nil
}

func (m model) onSearched(msg searchedMsg) (tea.Model, tea.Cmd) {
	// The user pressed esc and left while this was out, so this answer is stale.
	if !m.searching {
		return m, nil
	}
	m.searching = false

	if msg.err != nil {
		m.say("Search failed: " + msg.err.Error())
		m.mode = modeMenu
		return m, nil
	}
	if len(msg.videos) == 0 {
		m.say("No videos found.")
		m.mode = modeMenu
		return m, nil
	}

	// A result from "Stream" is ready to play as it is; one from "Online" has to
	// be downloaded first.
	items := make([]item, 0, len(msg.videos))
	for _, v := range msg.videos {
		row := item{title: v.Title, track: Track{ID: v.ID, Title: v.Title}}
		row.track.fillFromTitle()
		row.track.ArtistHint = strings.TrimSpace(m.searchArtist)
		if m.searchWasStream {
			row.track.PageURL = "https://www.youtube.com/watch?v=" + v.ID
			row.track.Stream = true
		} else {
			row.needsDownload = true
		}
		items = append(items, row)
	}

	m.items = items
	m.itemCursor = 0
	m.mode = modeResults
	return m, nil
}

// Queues the downloaded track without interrupting playback

func (m model) onDownloaded(msg downloadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.say("download failed: " + msg.err.Error())
		m.mode = modeMenu
		return m, nil
	}
	if msg.forQueue {
		return m.enqueue(msg.track)
	}
	return m.playQueue([]Track{msg.track})
}
