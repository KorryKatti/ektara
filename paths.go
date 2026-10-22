package main

// Where the program keeps its files, following the XDG Base Directory
// Specification so it behaves the same from anywhere:
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

// os.MkdirAll that names the path it was given.
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

// Its own folder so the database is not among the mp3s.
func musicDir() (string, error) {
	dir := filepath.Join(xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share")), "ektara", "music")
	if dir == "" {
		return "", fmt.Errorf("no home directory to keep downloads in")
	}
	return dir, makeDir(dir)
}

// For what changes every run and is not worth keeping.
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

// The full path of a track, worked out from wherever the library is now.
func musicPath(name string) (string, error) {
	dir, err := musicDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(name)), nil
}
