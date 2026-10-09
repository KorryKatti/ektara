package main

// The keys: every key press, and which screen it belongs to. Nothing here draws
// or plays anything. A handler changes the model and returns the command that
// should follow from it.

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The front door for keys: ctrl+c for every screen, then the rest to whichever
// screen is showing.
func (m model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always quits, because a key that only quits from one screen would
	// trap the user on any other.
	if msg.String() == "ctrl+c" {
		return m.quit()
	}

	switch m.mode {
	case modeMenu:
		return m.onMenuKey(msg)
	case modeSearch:
		return m.onSearchKey(msg)
	case modeResults, modeFiles, modeHistory:
		return m.onListKey(msg)
	case modePlaying:
		return m.onPlayerKey(msg)
	}

	return m, nil
}

func (m model) onMenuKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m.quit()

	case "up", "k":
		if m.menuCursor > 0 {
			m.menuCursor--
		}

	case "down", "j":
		if m.menuCursor < len(menuItems)-1 {
			m.menuCursor++
		}

	// Typing the number jumps straight to that row, so "3" is a shortcut for
	// moving down twice and pressing enter.
	case "1", "2", "3", "4", "5":
		m.menuCursor = int(msg.String()[0] - '1')
		return m.startMenuChoice()

	case "enter":
		return m.startMenuChoice()
	}

	return m, nil
}

// Acts on whichever menu row the cursor is on.
func (m model) startMenuChoice() (tea.Model, tea.Cmd) {
	switch m.menuCursor {

	case menuQuit:
		return m.quit()

	// "Online" and "Stream" both start by asking for a name to look up. Which one
	// it was is remembered, because picking a result has to know whether to save
	// it or stream it.
	case menuOnline, menuStream:
		m.mode = modeSearch
		m.searchInput = ""
		m.askingCount = false
		m.searchWasStream = m.menuCursor == menuStream
		return m, nil

	// "Queue" uses the same typing screen, but for a number.
	case menuQueue:
		m.mode = modeSearch
		m.searchInput = ""
		m.askingCount = true
		return m, nil

	case menuOffline:
		tracks, err := localFiles()
		if err != nil {
			m.say("Could not list your library: " + err.Error())
			return m, nil
		}
		if len(tracks) == 0 {
			m.say("No mp3 files in your library.")
			return m, nil
		}
		m.mode = modeFiles
		m.items = tracksToItems(tracks)
		m.itemCursor = 0
		return m, nil

	case menuHistory:
		tracks, err := historyTracks(m.db)
		if err != nil {
			m.say("Could not read the history: " + err.Error())
			return m, nil
		}
		if len(tracks) == 0 {
			m.say("Nothing has been played yet.")
			return m, nil
		}
		m.mode = modeHistory
		m.items = tracksToItems(tracks)
		m.itemCursor = 0
		return m, nil
	}

	return m, nil
}

func (m model) onSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Leaving while a search is out cancels its result: onSearched only acts
		// when searching is still true, so a late answer cannot yank the user back
		// to a screen they walked away from.
		m.searching = false
		m.mode = modeMenu
		return m, nil

	case "enter":
		// One search at a time: a second enter while the first is out would start
		// a duplicate request.
		if m.searching {
			return m, nil
		}
		if m.askingCount {
			return m.startQueue()
		}
		if strings.TrimSpace(m.searchInput) == "" {
			return m, nil
		}
		// Searching takes a network round trip, so it happens in the background.
		// searching is set so the screen shows an animation instead of looking
		// stuck.
		m.searching = true
		return m, searchCmd(m.db, m.searchInput)

	case "backspace":
		if m.searchInput != "" {
			m.searchInput = m.searchInput[:len(m.searchInput)-1]
		}
		return m, nil
	}

	// Anything else that is a plain character gets typed into the box, which is
	// ignored while searching because the box is no longer the focus.
	if !m.searching && len(msg.Key().Text) > 0 {
		m.searchInput += msg.Key().Text
	}
	return m, nil
}

// Reads the number that was typed and starts collecting that many tracks. A
// queue of one is the same as no queue at all, so it is not treated as one.
func (m model) startQueue() (tea.Model, tea.Cmd) {
	n := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(m.searchInput), "%d", &n); err != nil || n < 1 {
		m.mode = modeMenu
		m.say("That is not a number of tracks.")
		return m, nil
	}

	if n == 1 {
		m.queueWanted = 0
		m.mode = modeMenu
		return m, nil
	}

	m.queueWanted = n
	m.queue = nil
	m.mode = modeMenu
	m.menuCursor = 0
	return m, nil
}

func (m model) onListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.mode = modeMenu
		m.askingCount = false
		return m, nil

	case "up", "k":
		if m.itemCursor > 0 {
			m.itemCursor--
		}

	case "down", "j":
		if m.itemCursor < len(m.items)-1 {
			m.itemCursor++
		}

	case "enter":
		if m.itemCursor >= len(m.items) {
			return m, nil
		}
		picked := m.items[m.itemCursor]

		// A search result has to be saved to disk before it can play, so picking
		// one starts the download and plays it when it lands.
		if picked.needsDownload {
			m.say("Downloading...")
			return m, downloadCmd(picked.track.ID, picked.track.Title)
		}

		// Still collecting for a queue: add this one, and if it was the last asked
		// for, play what has been collected.
		if m.queueWanted > 0 {
			m.queue = append(m.queue, picked.track)
			m.queueWanted--
			if m.queueWanted == 0 {
				return m.playQueue(m.queue)
			}
			m.mode = modeMenu
			return m, nil
		}

		return m.playQueue([]Track{picked.track})
	}

	return m, nil
}

func (m model) onPlayerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// While a track is starting, the only key that does anything is one that gives
	// up and goes back, so a broken track cannot trap the user here.
	if m.loading {
		if msg.String() == "esc" {
			return m.backToMenu()
		}
		return m, nil
	}

	switch msg.String() {
	case "p":
		if m.paused {
			m.paused = false
			if m.player != nil {
				m.player.Play()
			}
			// Discord gets the original start time again, so the time it counts
			// matches what has actually been heard.
			return m, m.presence.showCmd(m.track, m.startTime)
		}
		m.paused = true
		if m.player != nil {
			m.player.Pause()
		}
		return m, m.presence.hideCmd()

	// The arrows seek, which is what a play/pause bar with a progress line in it
	// makes you expect. h and l still work: they were here first and there is no
	// reason to take them away.
	//
	// Up and down are unbound here because this screen has no list to move a
	// cursor through, so they would be two keys that do nothing.
	case "left", "h":
		return m.seek(-5 * time.Second)
	case "right", "l":
		return m.seek(5 * time.Second)

	case "a":
		// no wrap: the first track has nowhere to go back to
		if m.index > 0 {
			return m.move(-1)
		}
		return m, nil
	case "d":
		return m.move(1)

	case "s":
		m.say(m.opts.toggle("shuffle", &m.opts.shuffle))
		return m, nil
	case "r":
		m.say(m.opts.toggle("repeat", &m.opts.repeat))
		return m, nil

	case "m":
		if m.muted {
			m.muted = false
		} else {
			m.muted = true
		}
		if m.player != nil {
			m.player.SetVolume(m.soundVolume())
		}
		if m.muted {
			m.say("muted")
		} else {
			m.say("unmuted")
		}
		return m, nil

	// = as well as +, because + needs shift and terminals disagree about what
	// shift and equals sends.
	case "+", "=":
		m.volume = clampVolume(m.volume + 0.05)
		if m.player != nil {
			m.player.SetVolume(m.soundVolume())
		}
		m.say(fmt.Sprintf("volume %d%%", int(m.volume*100+0.5)))
		return m, nil
	case "-", "_":
		m.volume = clampVolume(m.volume - 0.05)
		if m.player != nil {
			m.player.SetVolume(m.soundVolume())
		}
		m.say(fmt.Sprintf("volume %d%%", int(m.volume*100+0.5)))
		return m, nil

	case "q":
		return m.backToMenu()
	}

	return m, nil
}

// What to hand the player: zero while muted, otherwise the volume. The volume
// number stays where it was while muted, which is what makes unmuting give back
// the level that was set rather than silence.
func (m model) soundVolume() float64 {
	if m.muted {
		return 0
	}
	return m.volume
}

func clampVolume(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
