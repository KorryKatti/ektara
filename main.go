package main

// Entry point: set up the log, open the library, create the one player, hand the
// model to Bubble Tea. The interface is in model.go, keys.go, queue.go, cmds.go
// and draw.go.

import (
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"time"

	tea "charm.land/bubbletea/v2"
)

// What this binary thinks it is. build.sh stamps both in from git with
// -ldflags, so a tag is the only thing that changes them; a plain go build
// leaves the defaults, which are still readable rather than blank.
var (
	version = "dev"
	commit  = "unknown"
)

const discordAppID = "1553038133006704670"

// Fast enough for a smooth progress bar, slow enough to cost nothing.
const redrawEvery = 100 * time.Millisecond

// The one line --version prints. Everything a bug report needs to match a
// binary to a build: the name, the git version, the commit, and the Go and
// platform it was built with.
func versionLine() string {
	return fmt.Sprintf("ektara %s (commit %s, %s, %s/%s)",
		version, commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func main() {
	// --version answers and leaves, before the log file and the database are
	// touched: neither is needed to say what this is.
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(versionLine())
		return
	}

	// Nothing may write a log line to the terminal: the interface owns it, and a
	// line lands in the middle of the drawing. A missing log is a nuisance, a
	// garbled screen is a bug, so it is thrown away instead.
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
	// slow and pointless if we never play anything.
	audio, err := newPlayer()
	if err != nil {
		fmt.Println("Could not open the audio player:", err)
		os.Exit(1)
	}
	defer audio.Close()

	// Discord is optional and nothing connects here. The model owns it, because a
	// connection has to be retried for the whole run rather than attempted once.
	p := tea.NewProgram(initialModel(db, audio))
	if _, err := p.Run(); err != nil {
		fmt.Println("Alas, there's been an error:", err)
		os.Exit(1)
	}
}
