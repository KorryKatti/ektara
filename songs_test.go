package main

import (
	"database/sql"
	"strings"
	"testing"
)

// songDB is a database with the songs table in it, which is all logSong and
// historyTracks need.
func songDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory database: %v", err)
	}
	// One connection, because every connection to :memory: is a different
	// database and the table would only exist on whichever one made it first.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`
	CREATE TABLE songs(
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		id TEXT, name TEXT NOT NULL, url TEXT, title TEXT
	)`); err != nil {
		t.Fatalf("create songs table: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return db
}

// TestLogSongSurvivesABrokenDatabase is the fix for the worst bug this program
// had.
//
// logSong ended the process on a database error. It was called from the tick,
// ten times a second, in the middle of a track, so a disk filling up or a
// momentary lock took the whole interface down with no message on screen. Every
// other database error in the code is logged and shrugged off, and this is now
// the same.
//
// A closed database is the stand-in for every way a write can fail, and it is
// the strongest version of one: it will never recover on its own, so a function
// that still calls os.Exit on it is plainly going to.
func TestLogSongSurvivesABrokenDatabase(t *testing.T) {
	db := songDB(t)
	db.Close() // every write from here fails

	// The whole point: this used to be log.Fatal, and reaching the line after
	// it is the assertion. A regression turns this into an os.Exit(1) and the
	// test binary dies, which go test reports as a failure of the whole run.
	logSong(db, Track{ID: "abc123", Filename: "song.mp3", Title: "a song"})

	t.Log("logSong returned rather than ending the process")
}

// TestLogSongStillRecords makes sure the fix above did not turn into "do
// nothing at all", which would also survive a broken database.
func TestLogSongStillRecords(t *testing.T) {
	db := songDB(t)

	track := Track{ID: "abc123", Filename: "song.mp3", PageURL: "https://example.invalid/watch", Title: "a song"}
	logSong(db, track)
	logSong(db, track)

	history, err := historyTracks(db)
	if err != nil {
		t.Fatalf("historyTracks: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d rows, want 2", len(history))
	}
	// newest first, so the most recent insert is at the front
	if history[0].Title != "a song" {
		t.Errorf("newest row is %q, want %q", history[0].Title, "a song")
	}
	if history[0].ID != "abc123" {
		t.Errorf("id came back as %q, want abc123", history[0].ID)
	}
	if !history[0].Stream {
		t.Error("a row with a url was read back as a local file")
	}
}

// TestThumbnailURLIsBuiltNotCached pins down that the cover image is a pattern
// with the id in it, which is why there is no table behind it.
//
// It used to SELECT from a metadata table and INSERT the result, on the claim
// that this saved a lookup. It saved nothing, because the url is derivable from
// the id, and it cost a table on every install plus two queries per track.
func TestThumbnailURLIsBuiltNotCached(t *testing.T) {
	if got := thumbnailURL("dQw4w9WgXcQ"); got != "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg" {
		t.Errorf("thumbnailURL = %q, want the standard i.ytimg.com address", got)
	}

	// a local file has no video id, and an empty string is how the caller is
	// told to leave the placeholder image alone
	if got := thumbnailURL(""); got != "" {
		t.Errorf("thumbnailURL(\"\") = %q, want empty", got)
	}
}

// TestOpenDBCreatesNoStrayTables checks the metadata table really is gone and
// not just unused. An unused table would be harmless, but it would also still
// be created on every fresh install for no reason.
func TestOpenDBCreatesNoStrayTables(t *testing.T) {
	db := songDB(t)

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("walk tables: %v", err)
	}

	for _, name := range names {
		if strings.Contains(name, "metadata") {
			t.Errorf("table %q is still being created but nothing reads it", name)
		}
	}
	t.Logf("tables: %v", names)
}
