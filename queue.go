package main

// Playback: starting tracks, the queue, the tick that watches them, and the
// messages that come back when something slow finishes.

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The one place a queue starts playing. Everything else ends up here: a single
// pick from a list, or the queue the user built up.
func (m model) playQueue(queue []Track) (tea.Model, tea.Cmd) {
	m.queue = queue
	m.index = 0
	m.queueWanted = 0
	m.mode = modePlaying

	// Shuffle happens once, here, rather than on every track: doing it per track
	// would keep changing the order as you listen.
	m.wasShuffled = m.opts.shuffle
	if m.opts.shuffle {
		shuffleQueue(m.queue)
	}

	return m, m.startTrack()
}

// Sets the model up for the current position and goes off to open the audio. It
// does not open it here, because opening is slow and would freeze the display.
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
	// opening a stream takes a second and a presence that lags looks broken. The
	// cover is asked for here for the same reason: it is a download, and it goes
	// out with everything else rather than when the screen happens to redraw.
	m.art = ""
	m.artKey = artKey(m.track, m.artWidth())

	// No tick here. onTick batches one of its own onto whatever this returns, so
	// the chain Init started keeps being renewed; starting a second one here would
	// make the display repaint twice as fast for the rest of the session. The
	// callers that are not onTick, which is a, d and picking a track, rely on the
	// existing chain still running.
	return tea.Batch(
		openTrackCmd(m.player, m.track),
		m.presence.showCmd(m.track, m.startTime),
		artCmd(m.track, m.artWidth()),
	)
}

// Ends the audio but keeps the player, and the open device, ready for the next
// track. Not a close: the mpv instance outlives every track in the queue.
func (m *model) stopTrack() {
	if m.player != nil {
		m.player.Stop()
	}
}

func (m model) move(delta int) (tea.Model, tea.Cmd) {
	m.index += delta

	if m.index >= len(m.queue) {
		if !m.opts.repeat {
			return m.backToMenu()
		}
		m.index = 0
	}

	// Shuffle switched on part way through: shuffle what is left, so the change
	// takes effect now without disturbing the track already going. The index is
	// wrapped, because with repeat on it can be past the end already, and handing
	// shuffleQueue an empty slice makes it ask the random number generator for a
	// number out of nothing.
	if m.opts.shuffle && !m.wasShuffled {
		shuffleQueue(m.queue[m.index%len(m.queue):])
	}
	m.wasShuffled = m.opts.shuffle

	return m, m.startTrack()
}

func (m model) backToMenu() (tea.Model, tea.Cmd) {
	m.stopTrack()
	m.mode = modeMenu
	m.queue = nil
	m.queueWanted = 0
	m.loading = false

	// Discord goes to idle once, here, rather than between every pair of tracks,
	// or the presence flickers in the gap.
	return m, m.presence.hideCmd()
}

// The audio is stopped here, right now, before tea.Quit is returned, and not from
// inside a command: a command runs in a goroutine and would race the program
// shutting down around it, which can leave the audio device held open while the
// terminal is being put back.
func (m model) quit() (tea.Model, tea.Cmd) {
	m.stopTrack()
	return m, tea.Quit
}

func (m model) seek(delta time.Duration) (tea.Model, tea.Cmd) {
	if m.player == nil {
		return m, nil
	}
	m.player.SeekBy(delta)
	// The Discord start time moves with the seek, so its elapsed time keeps
	// matching what is actually being heard.
	m.startTime = m.startTime.Add(-delta)
	return m, m.presence.showCmd(m.track, m.startTime)
}

// Runs ten times a second: nudges the busy animation along, sees whether the
// track has finished, copies the playback position into the model, and sets the
// next tick going.
//
// Every single path out of here returns tick(). That is the whole point of the
// function: a path returning nil kills the clock for good, no more ticks arrive,
// the progress bar freezes, and nothing complains. The program starts on the menu
// where there is no audio to look at, so that early exit is not a corner case,
// it is the first thing that happens every run.
func (m model) onTick() (tea.Model, tea.Cmd) {
	m.spin++

	if m.mode != modePlaying || m.loading || m.player == nil {
		return m, tick()
	}

	if !m.player.Playing() && !m.paused {
		// The tick has to be batched in here rather than trusted to be in what
		// move() returned: move returns a command to open the next track, or one
		// to shut the presence down, and neither is a tick. Returning it unadorned
		// froze the whole display the first time a song ended. Batched rather than
		// started separately, so there is still exactly one chain of them.
		if err := m.player.Err(); err != nil {
			// A track that broke is not the same as reaching the end of one. Only a
			// clean finish earns a history row, or every track that failed to
			// stream would end up in it.
			log.Printf("play %s: %v", m.track.label(), err)
			m.lastError = "Playback failed: " + err.Error()

			// Move on rather than sit here: leaving the stopped player in the model
			// means the next tick finds the same error and does all of this again,
			// ten times a second, for as long as the app is left open.
			next, cmd := m.move(1)
			return next, tea.Batch(cmd, tick())
		}
		logSong(m.db, m.track)
		next, cmd := m.move(1)
		return next, tea.Batch(cmd, tick())
	}

	// mpv is the one playing, so it knows exactly where it is, read-ahead included,
	// and there is nothing to subtract.
	m.elapsed = m.player.Position()
	m.length = m.player.Length()

	// a notice with no expiry would be wiped a tenth of a second after the key
	// that made it, too fast to read
	if m.notice != "" && time.Now().After(m.noticeUntil) {
		m.notice = ""
	}

	return m, tick()
}

// A message on the display for a couple of seconds: long enough to read, short
// enough not to sit there for the rest of the track.
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

	if m.player != nil {
		// The model's own volume rather than whatever mpv happens to be at, so a
		// new track does not reset the level.
		m.player.SetVolume(m.soundVolume())
		// It loaded paused, so the sound starts now that the model has caught up,
		// rather than a second early while the network was still answering.
		m.player.Play()
	}

	return m, nil
}

func (m model) onSearched(msg searchedMsg) (tea.Model, tea.Cmd) {
	// The user pressed esc and left while this was out, so this answer is stale
	// and must not reopen the screen.
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

func (m model) onDownloaded(msg downloadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.say("Download failed: " + msg.err.Error())
		m.mode = modeMenu
		return m, nil
	}
	return m.playQueue([]Track{msg.track})
}
