package main

// Where the program keeps its files.
//
// These used to be relative, so every piece landed in whatever folder it was
// started from: history depended on the launch directory, and running from two
// places gave two empty databases. The XDG Base Directory Specification says
// where each kind of file belongs, and following it means the program behaves
// the same from anywhere.
//
//	~/.local/share/ektara/songs.db   history, cover art, search cache
//	~/.local/share/ektara/music/     downloaded mp3s
//	~/.local/state/ektara/ektara.log the log
//	~/.config/ektara/                nothing yet, for settings one day

import (
	"fmt"
	"os"
	"path/filepath"
)

// One of the XDG base directories, falling back to the specification's default.
// The environment is read because someone who has set XDG_DATA_HOME has said
// where they want their data, and writing to their home anyway would be rude.
func xdgDir(envVar, fallback string) string {
	if dir := os.Getenv(envVar); dir != "" {
		return dir
	}

	home, err := os.UserHomeDir()
	if err != nil {
		// Rather than guessing somewhere, the caller gets this and says so.
		return ""
	}

	return filepath.Join(home, fallback)
}

// os.MkdirAll that names the path it was given, because "permission denied" on
// its own does not say which folder was the problem.
func makeDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return nil
}

// The data the program keeps between runs and would be sad to lose.
func libraryDir() (string, error) {
	dir := filepath.Join(xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share")), "ektara")
	if dir == "" {
		return "", fmt.Errorf("no home directory to keep the database in")
	}
	return dir, makeDir(dir)
}

// Its own folder so the database is not among the mp3s, and so "what has this
// downloaded" is one folder to look at.
func musicDir() (string, error) {
	dir := filepath.Join(xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share")), "ektara", "music")
	if dir == "" {
		return "", fmt.Errorf("no home directory to keep downloads in")
	}
	return dir, makeDir(dir)
}

// For what changes every run and is not worth keeping. Separate from the library
// because a user clearing out "caches and logs" should not take their listening
// history with them.
func stateDir() (string, error) {
	dir := filepath.Join(xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state")), "ektara")
	if dir == "" {
		return "", fmt.Errorf("no home directory to keep the log in")
	}
	return dir, makeDir(dir)
}

func logPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ektara.log"), nil
}

func dbPath() (string, error) {
	dir, err := libraryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "songs.db"), nil
}

// A Track carries the bare file name rather than a full path, on purpose. The
// name goes in the database, and a stored path stops being right the moment the
// user moves their home directory or changes XDG_DATA_HOME, which would leave the
// history pointing at nothing. The folder is worked out here instead, every
// time, from wherever the library is now.
func musicPath(name string) (string, error) {
	dir, err := musicDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(name)), nil
}
