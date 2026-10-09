package main

// Entry point: set up the log, open the library, create the one player, hand the
// model to Bubble Tea. The interface is in model.go, keys.go, queue.go, cmds.go
// and draw.go.

import (
	"fmt"
	"io"
	"log"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
)

const discordAppID = "1553038133006704670"

// Fast enough for a smooth progress bar, slow enough to cost nothing.
const redrawEvery = 100 * time.Millisecond

func main() {
	// The interface owns the terminal, so nothing may write a log line to it: a
	// line lands in the middle of the drawing and stays until the next redraw.
	// A missing log is a nuisance, a garbled screen is a bug, so if the file
	// cannot be opened the log is thrown away instead.
	if path, err := logPath(); err == nil {
		if lf, err := tea.LogToFile(path, ""); err == nil {
			defer lf.Close()
		} else {
			log.SetOutput(io.Discard)
		}
	} else {
		log.SetOutput(io.Discard)
	}

	db, err := openDB()
	if err != nil {
		fmt.Println("Could not open songs.db:", err)
		os.Exit(1)
	}
	defer db.Close()

	// Created before the interface starts: it opens the audio device, which is
	// slow and pointless if we never play anything. Worth printing rather than
	// logging, because nothing has been drawn yet and it is the only way the user
	// finds out.
	audio, err := newPlayer()
	if err != nil {
		fmt.Println("Could not open the audio player:", err)
		os.Exit(1)
	}
	defer audio.Close()

	// Discord is optional and nothing connects here. The model owns it, because a
	// connection has to be retried for the whole run rather than attempted once:
	// a Discord that is not running yet, or that restarts mid-song, used to leave
	// the status dead for the rest of the session without a word. Without Discord
	// the player still works, so it is never a reason to stop.
	p := tea.NewProgram(initialModel(db, audio))
	if _, err := p.Run(); err != nil {
		fmt.Println("Alas, there's been an error:", err)
		os.Exit(1)
	}
}
