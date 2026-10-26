package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	// media url because that carries an expiring token. Empty for local files,
	// which is also how a streamed row is told apart from a downloaded one.
	PageURL string
	// Play the audio over the network instead of from disk.
	Stream bool

	// What the music is, as far as we know: who made it, what it is on, what it
	// sounds like and how long it runs. Empty when nothing said.
	Artist string
	// The artist typed in the search box, used only when nothing else names one.
	ArtistHint string
	Album      string
	// The name of the song, which is not always the name of the video.
	Song     string
	Genres   []string
	Duration time.Duration
}

// Puts the queue in a random order, in place. Each track is swapped with a random
// other one, so every track is heard exactly once.
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
// without having to remember it.
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
	// from. Bring one across if the library has none.
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

	// url and title are only set for streamed tracks. An empty url marks a row as
	// a local file, so there is no separate flag column. The rest is what we know
	// about the music itself.
	for _, col := range []string{"url TEXT", "title TEXT", "artist TEXT", "album TEXT", "song TEXT", "genres TEXT", "duration INTEGER"} {
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
// Copies rather than moves, so a migration that goes wrong can be undone.
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

// The cover image for a YouTube video, or "" when there is no id to build one
// from. Nothing is fetched here and nothing is stored.
func thumbnailURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"
}

// Records a track in the history, called after it has played rather than before,
// so the history shows what was listened to rather than what was queued. A
// failure is logged and nothing more.
func logSong(db *sql.DB, t Track) {
	if _, err := db.Exec(
		"INSERT INTO songs (id,name,url,title,artist,album,song,genres,duration) VALUES (?,?,?,?,?,?,?,?,?)",
		t.ID,
		t.Filename,
		t.PageURL,
		t.Title,
		t.Artist,
		t.Album,
		t.Song,
		t.genreString(),
		t.Duration.Milliseconds(),
	); err != nil {
		log.Printf("log song %s: %v", t.label(), err)
	}
}

// A downloaded mp3's filename turned into something worth showing a human, with
// the video id and the "youtube - " prefix stripped off.
func titleFromFilename(filename string) string {
	title := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	if id := parseID(filename); id != "" {
		title = strings.TrimSpace(strings.TrimPrefix(title, id))
	}
	return strings.TrimSpace(strings.TrimPrefix(title, "youtube - "))
}

// Looks a name up on YouTube and returns the top few results.
//
// Only goes to YouTube when there is no recent answer to reuse. When YouTube
// refuses, an older answer is used in place of an error.
func searchYouTube(db *sql.DB, query string) ([]video, error) {
	if remembered, fresh := cachedSearch(db, query); fresh {
		return remembered, nil
	}

	// yt-dlp might not be installed yet, and installing it takes a while, so this
	// happens before the search rather than being the user's problem
	ytdlp.MustInstall(context.TODO(), nil)

	const limit = 3

	// spaced out here rather than inside yt-dlp, to space out whole searches
	youtubeLimiter.wait()

	found, _, err := ytdlp.New().
		Retries("3").
		FlatPlaylist().
		ExtractInfo(context.TODO(), fmt.Sprintf("ytsearch%d:%s", limit, query))
	if err != nil {
		// YouTube is saying no. Something remembered beats an error.
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
		// the embedded client, because the default web client gets the bot check
		ExtractorArgs("youtube:player_client=web_embedded").
		SetWorkDir(dir).
		// the tags go into the file, so the music carries its own details with it
		// and other programs can read them
		EmbedMetadata().
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
	track := Track{ID: id, Title: title, Filename: filepath.Base(filename)}
	track.fillFromTitle()
	return track, nil
}

// One YouTube search result.
type video struct {
	ID    string
	Title string
}

// The mp3s in the library folder, as tracks ready to play. The title is worked out
// from the filename, because a downloaded file carries the real title in its name.
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
		// the library ever moves.
		base := filepath.Base(name)
		track := Track{
			ID:       parseID(base),
			Title:    titleFromFilename(base),
			Filename: base,
		}
		// The artist is usually only in the title, and the title is all a local
		// file has.
		track.fillFromTitle()
		tracks = append(tracks, track)
	}

	return tracks, nil
}

// One row of the songs table, carrying enough to play it again.
type songRow struct {
	Seq   int64
	Track Track
}

const songColumns = `seq,id,name,url,title,artist,album,song,genres,duration`

func scanSong(rows interface{ Scan(...any) error }) (songRow, error) {
	var (
		row                       songRow
		id, name                  sql.NullString
		url, title, artist, album sql.NullString
		song                      sql.NullString
		genres                    sql.NullString
		duration                  sql.NullInt64
	)
	// Every column is nullable in sqlite's eyes, so they are all read as nullable.
	err := rows.Scan(&row.Seq, &id, &name, &url, &title, &artist, &album, &song, &genres, &duration)
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
	row.Track.Artist = artist.String
	row.Track.Album = album.String
	row.Track.Song = song.String
	if genres.String != "" {
		row.Track.Genres = strings.Split(genres.String, ",")
	}
	row.Track.Duration = time.Duration(duration.Int64) * time.Millisecond
	return row, nil
}

// Every song that has been played, newest first.
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

// The final non empty line of s with its whitespace trimmed: yt-dlp reports the
// file it wrote as the last non empty line of its output.
func lastNonEmptyLine(s string) string {
	var last string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			last = line
		}
	}
	return last
}
