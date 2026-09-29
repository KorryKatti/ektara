package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestXDGDIRPrefersTheEnvironment(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/somewhere/else")

	// Someone who has set this has said where they want their data to live.
	if got := xdgDir("XDG_DATA_HOME", "unused"); got != "/somewhere/else" {
		t.Errorf("got %q, want the value from the environment", got)
	}
}

func TestXDGDIRFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory on this machine: %v", err)
	}
	want := filepath.Join(home, ".local", "share")

	if got := xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share")); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLibraryDirIsCreated(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	dir, err := libraryDir()
	if err != nil {
		t.Fatalf("libraryDir: %v", err)
	}

	// It has to make its own folder. A program that assumes the directory is
	// already there fails on the first run and on every fresh machine.
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("library directory %q was not created", dir)
	}
	if filepath.Base(dir) != "ektara" {
		t.Errorf("library directory is %q, want it to end in ektara", dir)
	}
}

func TestMusicPathKeepsOnlyTheFileName(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	want, err := musicDir()
	if err != nil {
		t.Fatalf("musicDir: %v", err)
	}

	// Names come out of the database, and a database is a file a user can edit.
	// Nothing in it should be able to send the player somewhere else on disk.
	for _, name := range []string{
		"song.mp3",
		"/etc/passwd/song.mp3",
		"../../../etc/song.mp3",
		"nested/song.mp3",
	} {
		got, err := musicPath(name)
		if err != nil {
			t.Fatalf("musicPath(%q): %v", name, err)
		}
		if got != filepath.Join(want, "song.mp3") {
			t.Errorf("musicPath(%q) = %q, want %q", name, got, filepath.Join(want, "song.mp3"))
		}
	}
}

func TestAdoptLegacyDBCopiesTheOldDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// the shape of the old setup: a database in the folder it was run from
	if err := os.WriteFile(filepath.Join(dir, "songs.db"), []byte("old history"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "library", "songs.db")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}

	adoptLegacyDB(dst)

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read the adopted database: %v", err)
	}
	if string(got) != "old history" {
		t.Errorf("adopted database holds %q, want the old history", got)
	}

	// The copy is deliberate. If this goes wrong the user can delete the new
	// file and still have the original, so nothing may have been taken away.
	if _, err := os.Stat(filepath.Join(dir, "songs.db")); err != nil {
		t.Error("the original database was removed, so a bad migration cannot be undone")
	}
}

func TestAdoptLegacyDBLeavesAnExistingLibraryAlone(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "songs.db"), []byte("old history"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "library", "songs.db")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("the real one"), 0o644); err != nil {
		t.Fatal(err)
	}

	adoptLegacyDB(dst)

	// The library's own database is the live one. Overwriting it with a stale
	// copy from a folder the user happened to run from would lose real history.
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the real one" {
		t.Errorf("database now holds %q, want the library's own untouched", got)
	}
}

func TestAdoptLegacyDBDoesNothingWhenThereIsNoOldDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	dst := filepath.Join(dir, "library", "songs.db")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}

	adoptLegacyDB(dst) // must not panic, and must not create anything

	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("a database appeared out of nowhere")
	}
}
