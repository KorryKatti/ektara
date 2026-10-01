package main

// This file is the entry point: set up the log, open the library, create the
// one player, and hand the model to Bubble Tea. The interface itself is in
// model.go, keys.go, queue.go, cmds.go and draw.go.

import (
	"fmt"
	"io"
	"log"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
)

// discordAppID is this program's Discord application.
const discordAppID = "1553038133006704670"

// redrawEvery is how often the player looks at the audio and redraws. Ten times
// a second is fast enough for a progress bar to look smooth and slow enough to
// cost nothing.
const redrawEvery = 100 * time.Millisecond

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	// The interface owns the terminal, so nothing may write a log line to it. A
	// log goes to stderr, which is the same screen, and the line lands in the
	// middle of the drawing and stays there until the next full redraw. The log
	// therefore goes to a file in the state directory. If that file cannot be
	// opened the log is thrown away instead: a missing log is a nuisance, but a
	// garbled screen is a bug.
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

	// The player is created once, before the interface starts, because it opens
	// the audio device and there is no point paying for that if the program is
	// about to be closed again, and none at all if the user never plays
	// anything. A failure here is worth printing rather than logging, because
	// nothing has been drawn yet and it is the only way the user finds out.
	audio, err := newPlayer()
	if err != nil {
		fmt.Println("Could not open the audio player:", err)
		os.Exit(1)
	}
	defer audio.Close()

	// Discord is optional, and nothing about connecting to it happens here. The
	// model owns that, because a connection has to be able to be retried for
	// the whole run rather than attempted once and forgotten: a Discord that is
	// not running yet, or that restarts mid-song, used to leave the status
	// dead for the rest of the session without a word. Without Discord the
	// player still works, so none of it is ever a reason to stop.
	p := tea.NewProgram(initialModel(db, audio))
	if _, err := p.Run(); err != nil {
		fmt.Println("Alas, there's been an error:", err)
		os.Exit(1)
	}
}
