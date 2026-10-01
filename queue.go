package main

// This file is playback: starting tracks, the queue, the tick that watches them,
// and the messages that come back when something slow finishes.

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"
)

// ---------------------------------------------------------------------------
// starting, stopping and moving between tracks
// ---------------------------------------------------------------------------

// playQueue is the one place a queue starts playing. Everything else ends up
// here: a single pick from a list, or the queue the user built up.
func (m model) playQueue(queue []Track) (tea.Model, tea.Cmd) {
	m.queue = queue
	m.index = 0
	m.queueWanted = 0
	m.mode = modePlaying

	// shuffle happens once, here, rather than on every track. Doing it per
	// track would keep changing the order as you listen.
	m.wasShuffled = m.opts.shuffle
	if m.opts.shuffle {
		shuffleQueue(m.queue)
	}

	return m, m.startTrack()
}

// startTrack sets the model up for the current position in the queue and goes
// off to open the audio. It does not open it here, because opening is slow and
// doing it inline would freeze the display.
func (m *model) startTrack() tea.Cmd {
	m.stopTrack()

	// the index is wrapped with the queue length, so repeat can go round again
	m.track = m.queue[m.index%len(m.queue)]

	m.startTime = time.Now()
	m.elapsed = 0
	m.length = 0
	m.paused = false
	m.muted = false
	m.loading = true
	m.notice = ""

	// Discord is told straight away rather than waiting for the audio to be
	// ready, because opening a stream takes a second and a presence that lags
	// behind the music looks broken.
	//
	// No tick here. onTick batches one of its own onto whatever this returns,
	// so the chain that Init started keeps being renewed, and starting a second
	// one here would make the display repaint twice as fast for the rest of the
	// session. The callers that are not onTick, which is the a and d keys and
	// picking a track, rely on the existing chain still running.
	return tea.Batch(openTrackCmd(m.player, m.track), m.presence.showCmd(m.track, m.startTime))
}

// stopTrack ends the audio but keeps the player, and the open audio device,
// ready for the next track. It is not a close: the one mpv instance outlives
// every track in the queue and is only torn down when the program ends.
func (m *model) stopTrack() {
	if m.player != nil {
		m.player.Stop()
	}
}

// move goes one track forwards or backwards and starts the new one.
func (m model) move(delta int) (tea.Model, tea.Cmd) {
	m.index += delta

	// past the end: repeat starts again, otherwise the queue is over
	if m.index >= len(m.queue) {
		if !m.opts.repeat {
			return m.backToMenu()
		}
		m.index = 0
	}

	// shuffle switched on part way through: shuffle what is left to play, so
	// the change takes effect now without disturbing the track already going.
	//
	// The index is wrapped, because with repeat on the index can be past the
	// end already, and handing shuffleQueue an empty slice makes it ask the
	// random number generator to pick a number out of nothing.
	if m.opts.shuffle && !m.wasShuffled {
		shuffleQueue(m.queue[m.index%len(m.queue):])
	}
	m.wasShuffled = m.opts.shuffle

	return m, m.startTrack()
}

// backToMenu stops the audio and shows the menu again.
func (m model) backToMenu() (tea.Model, tea.Cmd) {
	m.stopTrack()
	m.mode = modeMenu
	m.queue = nil
	m.queueWanted = 0
	m.loading = false

	// Discord goes to idle once, here, rather than between every pair of
	// tracks, or the presence flickers in the gap.
	//
	// No tick either, for the same reason startTrack does not return one.
	return m, m.presence.hideCmd()
}

// quit stops the audio and ends the program.
//
// The audio is stopped here, right now, before tea.Quit is returned, and not
// from inside a command. A command runs in a goroutine and would race the
// program shutting down around it, which can leave the audio device held open
// while the terminal is being put back.
func (m model) quit() (tea.Model, tea.Cmd) {
	m.stopTrack()
	return m, tea.Quit
}

func (m model) seek(delta time.Duration) (tea.Model, tea.Cmd) {
	if m.player == nil {
		return m, nil
	}
	m.player.SeekBy(delta)
	// the Discord start time moves with the seek, so the elapsed time on the
	// presence keeps matching what is actually being heard
	m.startTime = m.startTime.Add(-delta)
	return m, m.presence.showCmd(m.track, m.startTime)
}

// ---------------------------------------------------------------------------
// the tick
// ---------------------------------------------------------------------------

// onTick runs ten times a second. It does a few cheap things: nudges the busy
// animation along, sees whether the track has finished, copies the playback
// position into the model so the display can show it, and sets the next tick
// going.
//
// Every single path out of here returns tick(). That is the whole point of the
// function. A path that returns nil instead kills the clock for good: no more
// ticks arrive, the progress bar freezes, and nothing complains about it. The
// program starts on the menu where there is no audio to look at, so that early
// exit is not a corner case, it is the first thing that happens every run.
func (m model) onTick() (tea.Model, tea.Cmd) {
	// the tick also drives the little "busy" animation, so it is counted here
	// before any of the early exits below
	m.spin++

	// nothing to watch unless a track is actually open
	if m.mode != modePlaying || m.loading || m.player == nil {
		return m, tick()
	}

	// the track finished, or it broke
	if !m.player.Playing() && !m.paused {
		// Both branches below hand back what move() returned, so the tick has
		// to be added here rather than trusted to be in there already. move()
		// returns a command to open the next track, or one to shut the presence
		// down, and neither of those is a tick. Returning it unadorned was the
		// bug this comment used to explain away: the clock chain that Init
		// started is still the only one, and onTick is what renews it, so the
		// first time a song ended the whole display froze. The progress bar
		// stopped, the search spinner stopped, and the queue never advanced.
		//
		// The tick has to be batched in rather than started separately, so that
		// there is still exactly one chain of them. Two chains both renewing
		// themselves is how the display ends up repainting twice as fast for the
		// rest of the session.
		if err := m.player.Err(); err != nil {
			// A track that broke stops the player with an error on it, which
			// is not the same as reaching the end of the track. Only a clean
			// finish earns a history row, or every track that failed to stream
			// would end up in it.
			log.Printf("play %s: %v", m.track.label(), err)
			m.lastError = "Playback failed: " + err.Error()

			// Move on rather than sit here. Leaving the stopped player in the
			// model means the next tick arrives, finds the same stopped player
			// carrying the same error, and does all of this again: ten times a
			// second, for as long as the app is left open.
			next, cmd := m.move(1)
			return next, tea.Batch(cmd, tick())
		}
		// it played all the way through, so it goes in the history.
		logSong(m.db, m.track)
		next, cmd := m.move(1)
		return next, tea.Batch(cmd, tick())
	}

	// copy the position in. mpv is the one playing, so it knows exactly where
	// it is, including any read-ahead, and there is nothing to subtract.
	m.elapsed = m.player.Position()
	m.length = m.player.Length()

	// a notice with no expiry would be wiped a tenth of a second after the key
	// that made it, which is too fast to read
	if m.notice != "" && time.Now().After(m.noticeUntil) {
		m.notice = ""
	}

	return m, tick()
}

// say puts a message on the display for a couple of seconds, which is long
// enough to read and short enough not to sit there for the rest of the track.
func (m *model) say(msg string) {
	m.notice = msg
	m.noticeUntil = time.Now().Add(2 * time.Second)
}

// ---------------------------------------------------------------------------
// the results of the slow commands
// ---------------------------------------------------------------------------

func (m model) onTrackOpened(msg trackOpenedMsg) (tea.Model, tea.Cmd) {
	m.loading = false

	if msg.err != nil {
		log.Printf("open %s: %v", m.track.label(), msg.err)
		m.lastError = "Could not play " + m.track.label()
		// carry on to the next one rather than sitting on a dead screen
		return m.move(1)
	}

	// it opened, so whatever went wrong last is no longer what is happening
	m.lastError = ""

	// the volume is the model's own number rather than whatever mpv happens to
	// be at, so a new track does not reset the level
	if m.player != nil {
		m.player.SetVolume(m.soundVolume())
		// it loaded paused, so the sound starts now that the model has caught
		// up, rather than a second early while the network was still answering
		m.player.Play()
	}

	return m, nil
}

func (m model) onSearched(msg searchedMsg) (tea.Model, tea.Cmd) {
	// the user pressed esc and left while this search was still out, so this
	// answer is stale and must not reopen the screen
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

	// each result becomes a list row. A result from "Stream" is ready to play
	// as it is; one from "Online" has to be downloaded first.
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
