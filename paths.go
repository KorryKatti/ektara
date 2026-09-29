package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Where the program keeps its files.
//
// These used to be relative paths, so every piece of it landed in whatever
// folder the program happened to be started from: the database, the log, and
// every downloaded song. That meant history depended on the folder you launched
// from, and running the program from two different places gave two different
// empty databases. The XDG Base Directory Specification says where each kind of
// file belongs, and following it means the program behaves the same from
// anywhere.
//
// https://specifications.freedesktop.org/basedir-spec/latest/
//
// The layout is:
//
//	~/.local/share/ektara/songs.db   history, cover art, search cache
//	~/.local/share/ektara/music/     downloaded mp3s
//	~/.local/state/ektara/ektara.log the log
//	~/.config/ektara/                nothing yet, for settings one day

// xdgDir returns one of the XDG base directories, falling back to the default
// the specification names when the environment variable is not set.
//
// The environment is read rather than ignored because someone who has set
// XDG_DATA_HOME has said where they want their data to live, and a program that
// quietly writes to their home directory anyway is being rude to them.
func xdgDir(envVar, fallback string) string {
	if dir := os.Getenv(envVar); dir != "" {
		return dir
	}

	home, err := os.UserHomeDir()
	if err != nil {
		// There is no home directory to fall back to. Rather than guessing
		// somewhere, the caller gets this and says so in plain language.
		return ""
	}

	return filepath.Join(home, fallback)
}

// makeDir is os.MkdirAll that names the path it was given, because "permission
// denied" on its own does not say which folder was the problem.
func makeDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return nil
}

// libraryDir is where the database lives: the data the program keeps between
// runs and would be sad to lose.
func libraryDir() (string, error) {
	dir := filepath.Join(xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share")), "ektara")
	if dir == "" {
		return "", fmt.Errorf("no home directory to keep the database in")
	}
	return dir, makeDir(dir)
}

// musicDir is where downloads are written. It is a folder of its own so the
// database file is not sitting in among the mp3s, and so "what has this program
// downloaded" is one folder to look at.
func musicDir() (string, error) {
	dir := filepath.Join(xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share")), "ektara", "music")
	if dir == "" {
		return "", fmt.Errorf("no home directory to keep downloads in")
	}
	return dir, makeDir(dir)
}

// stateDir is for the things that change on every run and are not worth
// keeping: the log. It is separate from the library because these are two
// different kinds of file, and a user clearing out "caches and logs" should not
// take their listening history with them.
func stateDir() (string, error) {
	dir := filepath.Join(xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state")), "ektara")
	if dir == "" {
		return "", fmt.Errorf("no home directory to keep the log in")
	}
	return dir, makeDir(dir)
}

// logPath is where the log is written.
func logPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ektara.log"), nil
}

// dbPath is where the database lives.
func dbPath() (string, error) {
	dir, err := libraryDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "songs.db"), nil
}

// musicPath turns a song's name into somewhere it can actually be opened from.
//
// A Track carries the bare file name rather than a full path, on purpose. The
// name goes in the database, and a path written into the database stops being
// right the moment the user moves their home directory or changes
// XDG_DATA_HOME, which would leave the history pointing at nothing. The folder
// is worked out here instead, every time, from wherever the library is now.
func musicPath(name string) (string, error) {
	dir, err := musicDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(name)), nil
}
