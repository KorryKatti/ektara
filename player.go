package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/lrstanley/go-ytdlp"
	_ "github.com/mattn/go-sqlite3"
	"log"
)

// One playable thing, either a local mp3 or a YouTube stream.
type Track struct {
	// The YouTube video id, empty for local files not downloaded from YouTube.
	ID string
	// What we show the user and report to Discord.
	Title string
	// The local mp3 to decode. Empty when Stream is true.
	Filename string
	// The canonical youtube.com/watch?v=... address, stored instead of the direct
	// media url because that carries an expiring token and is useless once it
	// lapses. Empty for local files, which is also how a streamed row is told
	// apart from a downloaded one.
	PageURL string
	// Play the audio over the network instead of from disk.
	Stream bool
}

// Puts the queue in a random order, in place.
//
// Swaps each track with a random other track rather than picking a random track
// to play next, so every track is heard exactly once and none is left sitting at
// the end by accident.
func shuffleQueue(queue []Track) {
	for i := range queue {
		j := rand.Intn(len(queue))
		queue[i], queue[j] = queue[j], queue[i]
	}
}

// The playback options the user can flip with a key while a track is playing.
type options struct {
	shuffle bool // play the queue in a random order
	repeat  bool // start the queue again once it ends
}

// Flips one of the options and says what it became, so the user can see the state
// without having to remember it. Returned rather than printed, because the display
// is redrawn constantly and anything printed straight to the terminal would be
// wiped on the next frame.
func (o *options) toggle(name string, field *bool) string {
	*field = !*field
	return fmt.Sprintf("%s %s", name, onOff(*field))
}

// A short description of where the audio is coming from.
func (t Track) label() string {
	if t.Stream {
		return "stream: " + t.Title
	}
	return t.Filename
}

// openDB opens the songs database and makes sure the tables are there.
func openDB() (*sql.DB, error) {
	path, err := dbPath()
	if err != nil {
		return nil, err
	}

	// Older versions kept the database in whichever folder the program was started
	// from. Bring one across if the library has none, so the history is not lost.
	adoptLegacyDB(path)

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS songs(
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		id TEXT,
		name TEXT NOT NULL
	)
	`)
	if err != nil {
		db.Close()
		return nil, err
	}

	// url and title are only set for streamed tracks. An empty url marks a row as a
	// local file, so there is no separate flag column.
	for _, col := range []string{"url TEXT", "title TEXT"} {
		if _, err := db.Exec("ALTER TABLE songs ADD COLUMN " + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column name") {
			log.Printf("migrate songs: %v", err)
		}
	}

	if err := initSearchCache(db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// Brings across a songs.db left in the working directory by an older version, but
// only when the library has no database of its own.
//
// Copies rather than moves: the original stays where it was, so a migration that
// goes wrong can be undone by deleting the new file and nothing the user cared
// about has been taken away.
func adoptLegacyDB(dst string) {
	if _, err := os.Stat(dst); err == nil {
		return // not a first run, so the old one has nothing to teach us
	}

	const legacy = "songs.db"
	data, err := os.ReadFile(legacy)
	if err != nil {
		return // nothing there to adopt, which is the normal case
	}

	if err := os.WriteFile(dst, data, 0o644); err != nil {
		log.Printf("move old database: %v", err)
		return
	}
	log.Printf("copied the old database from %s to %s", legacy, dst)
}

// The cover image for a YouTube video, or "" when there is no id to build one from.
//
// Nothing is fetched here and nothing is stored, which is the point: Discord
// downloads the image itself from this address. This used to consult a metadata
// table to save a lookup on repeat plays, but the url is a fixed pattern with the
// id dropped in, so the table only remembered a string that could be rebuilt for
// free: two queries instead of one Sprintf, plus a table to create and migrate on
// every install. When there is a real lookup to save, this is the seam for it.
func thumbnailURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"
}

// Records a track in the history, called after it has played rather than before,
// so the history shows what was listened to rather than what was queued.
//
// A failure is logged and nothing more. It used to be fatal, which was wrong
// twice over: the interface was mid-track, and a database problem is exactly the
// sort of thing that comes back on its own a moment later. Ending the program
// threw away the session over a hiccup, signalled only by a log line nowhere near
// the screen.
func logSong(db *sql.DB, t Track) {
	if _, err := db.Exec(
		"INSERT INTO songs (id,name,url,title) VALUES (?,?,?,?)",
		t.ID,
		t.Filename,
		t.PageURL,
		t.Title,
	); err != nil {
		log.Printf("log song %s: %v", t.label(), err)
	}
}

// A downloaded mp3's filename turned into something worth showing a human, with
// the video id and the "youtube - " prefix that yt-dlp's output template adds
// stripped off.
func titleFromFilename(filename string) string {
	title := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	if id := parseID(filename); id != "" {
		title = strings.TrimSpace(strings.TrimPrefix(title, id))
	}
	return strings.TrimSpace(strings.TrimPrefix(title, "youtube - "))
}

// Looks a name up on YouTube and returns the top few results.
//
// Only goes to YouTube when there is no recent answer to reuse, and waits its
// turn first, so a burst of searching cannot arrive as a burst of requests. When
// YouTube refuses, an older answer is used in place of an error, so an address it
// has decided to block still plays the songs found before it started refusing.
//
// Shells out to yt-dlp and waits for a network round trip, so it is a tea.Cmd: the
// display stays alive and can show a "searching" line while it runs.
func searchYouTube(db *sql.DB, query string) ([]video, error) {
	if remembered, fresh := cachedSearch(db, query); fresh {
		return remembered, nil
	}

	// yt-dlp might not be installed yet, and installing it takes a while, so this
	// happens before the search rather than being the user's problem
	ytdlp.MustInstall(context.TODO(), nil)

	const limit = 3

	// spaced out here rather than inside yt-dlp because the aim is to space out
	// whole searches, not the requests inside one of them
	youtubeLimiter.wait()

	found, _, err := ytdlp.New().
		Retries("3").
		FlatPlaylist().
		ExtractInfo(context.TODO(), fmt.Sprintf("ytsearch%d:%s", limit, query))
	if err != nil {
		// YouTube is saying no. Something remembered beats an error, so a search
		// done before still answers now.
		if remembered, ok := cachedSearch(db, query); ok && len(remembered) > 0 {
			return remembered, nil
		}
		return nil, err
	}

	videos := []video{}
	for _, v := range found {
		if v == nil || v.ID == "" {
			continue
		}
		title := v.ID
		if v.Title != nil {
			title = *v.Title
		}
		videos = append(videos, video{ID: v.ID, Title: title})
		if len(videos) == limit {
			break
		}
	}

	storeSearch(db, query, videos)
	pruneSearchCache(db)

	return videos, nil
}

// Saves one YouTube video as an mp3 in the library and returns the track to play.
// Like searchYouTube this is slow, so it is a cmd.
func downloadVideo(id, title string) (Track, error) {
	ytdlp.MustInstall(context.TODO(), nil)

	pageURL := "https://www.youtube.com/watch?v=" + id

	dir, err := musicDir()
	if err != nil {
		return Track{}, err
	}

	youtubeLimiter.wait()

	res, err := ytdlp.New().
		Retries("3").
		// the same embedded client as streamURL, and for the same reason: the
		// default web client gets the bot check
		ExtractorArgs("youtube:player_client=web_embedded").
		SetWorkDir(dir).
		ExtractAudio().
		AudioFormat("mp3").
		Output("%(id)s %(extractor)s - %(title)s.%(ext)s").
		Print("after_move:filepath").
		Run(context.TODO(), pageURL)
	if err != nil {
		return Track{}, err
	}

	filename := lastNonEmptyLine(res.Stdout)
	if filename == "" {
		return Track{}, fmt.Errorf("could not work out where the download went")
	}

	// The bare name, not the path yt-dlp printed: the library can move, the name
	// cannot.
	return Track{ID: id, Title: title, Filename: filepath.Base(filename)}, nil
}

// One YouTube search result.
type video struct {
	ID    string
	Title string
}

// The mp3s in the library folder, as tracks ready to play. The title is worked out
// from the filename, because a file downloaded from YouTube carries the real title
// in its name.
func localFiles() ([]Track, error) {
	dir, err := musicDir()
	if err != nil {
		return nil, err
	}

	names, err := filepath.Glob(filepath.Join(dir, "*.mp3"))
	if err != nil {
		return nil, err
	}

	tracks := []Track{}
	for _, name := range names {
		// just the file name, not the whole path: a path in the database breaks if
		// the library ever moves, and the folder is worked out again when the file
		// is opened.
		base := filepath.Base(name)
		tracks = append(tracks, Track{
			ID:       parseID(base),
			Title:    titleFromFilename(base),
			Filename: base,
		})
	}

	return tracks, nil
}

// One row of the songs table, carrying enough to play it again.
type songRow struct {
	Seq   int64
	Track Track
}

const songColumns = `seq,id,name,url,title`

func scanSong(rows interface{ Scan(...any) error }) (songRow, error) {
	var (
		row        songRow
		id, name   sql.NullString
		url, title sql.NullString
	)
	// Every column is nullable in sqlite's eyes: rows written before url and title
	// existed have NULL there, and id is nullable in the schema.
	err := rows.Scan(&row.Seq, &id, &name, &url, &title)
	if err != nil {
		return songRow{}, err
	}
	row.Track.ID = id.String
	row.Track.Filename = name.String
	row.Track.PageURL = url.String
	row.Track.Stream = row.Track.PageURL != ""
	row.Track.Title = title.String
	if row.Track.Title == "" {
		row.Track.Title = titleFromFilename(row.Track.Filename)
	}
	return row, nil
}

// Every song that has been played, newest first, so the display can show them as
// a list to walk through with the arrow keys instead of asking for a number.
func historyTracks(db *sql.DB) ([]Track, error) {
	rows, err := db.Query(`SELECT ` + songColumns + ` FROM songs ORDER BY seq DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tracks := []Track{}
	for rows.Next() {
		row, err := scanSong(rows)
		if err != nil {
			log.Printf("history: %v", err)
			continue
		}
		tracks = append(tracks, row.Track)
	}
	return tracks, rows.Err()
}

// The YouTube video id out of a downloaded filename, which yt-dlp writes as
// "<id> <extractor> - <title>.<ext>". "" when the first field is not shaped like an
// id, which is how a hand named mp3 is recognised as having nothing to recover.
func parseID(filename string) string {
	fields := strings.Fields(filepath.Base(strings.TrimSpace(filename)))
	if len(fields) == 0 {
		return ""
	}

	id := fields[0]
	if len(id) != 11 {
		return ""
	}

	for _, char := range id {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '_' && char != '-' {
			return ""
		}
	}

	return id
}

// The final line of s with its whitespace trimmed, or "" when s holds no such
// line: yt-dlp reports the file it wrote as the last non empty line of its output.
func lastNonEmptyLine(s string) string {
	var last string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			last = line
		}
	}
	return last
}
