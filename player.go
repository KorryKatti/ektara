package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/oto/v3"
	"github.com/hugolgst/rich-go/client"
	"github.com/lrstanley/go-ytdlp"
	_ "github.com/mattn/go-sqlite3"
	"log"
)

// Track is one playable thing, either a local mp3 or a YouTube stream.
type Track struct {
	// ID is the YouTube video id, empty for local files that were not
	// downloaded from YouTube.
	ID string
	// Title is what we show the user and report to Discord.
	Title string
	// Filename is the local mp3 to decode. Empty when Stream is true.
	Filename string
	// PageURL is the canonical youtube.com/watch?v=... address. It is stored
	// instead of the direct media url because the media url carries an
	// expiring token and is useless once it lapses. Empty for local files,
	// which is also how a streamed row is told apart from a downloaded one.
	PageURL string
	// Stream plays the audio over the network instead of from disk.
	Stream bool
}

// shuffleQueue puts the queue in a random order, in place.
//
// It swaps each track with a random other track rather than picking a random
// track to play next, so every track is still heard exactly once and none is
// left sitting at the end of the queue by accident.
func shuffleQueue(queue []Track) {
	for i := range queue {
		j := rand.Intn(len(queue))
		queue[i], queue[j] = queue[j], queue[i]
	}
}

// options are the playback options the user can switch on and off with a key
// while a track is playing. main owns them and passes a pointer into play, so
// a key press changes what the queue loop does without play needing to know
// anything about the queue.
type options struct {
	// shuffle plays the queue in a random order
	shuffle bool
	// repeat starts the queue again once it ends
	repeat bool
}

// toggle flips one of the options and says what it became, so the user can
// see the state without having to remember it. The message is returned rather
// than printed, because the display is redrawn constantly and anything printed
// straight to the terminal would be wiped on the next frame.
func (o *options) toggle(name string, field *bool) string {
	*field = !*field
	return fmt.Sprintf("%s %s", name, onOff(*field))
}

// label is a short description of where the audio is coming from.
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

	// Older versions kept the database in whichever folder the program was
	// started from. If there is one there and the library has none, bring it
	// across so the history is not simply lost.
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

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS metadata(
		id TEXT PRIMARY KEY,
		thumbnail_url TEXT NOT NULL
	)
	`)
	if err != nil {
		db.Close()
		return nil, err
	}

	// url and title are only set for streamed tracks. An empty url is what
	// marks a row as a local file, so there is no separate flag column.
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

// adoptLegacyDB brings across a songs.db left in the working directory by an
// older version, but only when the library has no database of its own.
//
// It copies rather than moves. The original stays exactly where it was, so a
// migration that goes wrong can be undone by deleting the new file and nothing
// the user cared about has been taken away.
func adoptLegacyDB(dst string) {
	if _, err := os.Stat(dst); err == nil {
		// The library already has a database, so this is not a first run and
		// the old one has nothing to teach us.
		return
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

// setDiscordActivity shows t as the thing being played, with its cover and a
// link back to YouTube. start is when playback began, not when the presence was
// written, so the elapsed time Discord shows survives a pause and resume.
func setDiscordActivity(db *sql.DB, t Track, start time.Time) error {
	song := t.Title
	largeImage := "playing"

	var buttons []*client.Button
	if t.ID != "" {
		thumbnailURL, err := thumbnailURLForID(db, t.ID)
		if err != nil {
			return fmt.Errorf("load thumbnail metadata: %w", err)
		}
		if thumbnailURL != "" {
			largeImage = thumbnailURL
		}
		pageURL := t.PageURL
		if pageURL == "" {
			pageURL = "https://www.youtube.com/watch?v=" + t.ID
		}
		buttons = []*client.Button{
			{
				Label: "Watch on YouTube",
				Url:   pageURL,
			},
		}
	}

	return client.SetActivity(client.Activity{
		State:      "Listening to music",
		Details:    song,
		LargeImage: largeImage,
		LargeText:  song,
		SmallImage: "ektara",
		SmallText:  "Ektara",
		Timestamps: &client.Timestamps{
			Start: &start,
		},
		Buttons: buttons,
	})
}

// thumbnailURLForID returns the cover image for a YouTube video, remembering
// the answer in the metadata table so repeat plays do not hit the database's
// default construction twice.
func thumbnailURLForID(db *sql.DB, id string) (string, error) {
	if id == "" {
		return "", nil
	}

	var thumbnailURL string
	err := db.QueryRow(
		"SELECT thumbnail_url FROM metadata WHERE id = ?",
		id,
	).Scan(&thumbnailURL)
	if err == nil {
		return thumbnailURL, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}

	thumbnailURL = fmt.Sprintf("https://i.ytimg.com/vi/%s/hqdefault.jpg", id)
	_, err = db.Exec(
		"INSERT OR IGNORE INTO metadata (id, thumbnail_url) VALUES (?, ?)",
		id,
		thumbnailURL,
	)
	if err != nil {
		return "", err
	}

	return thumbnailURL, nil
}

// setDiscordIdleActivity clears the playing song and shows nothing instead. It
// is written whenever audio stops, so a stale presence cannot outlive the
// track it names.
func setDiscordIdleActivity() error {
	return client.SetActivity(client.Activity{
		State:      "Idle",
		Details:    "No song playing",
		LargeImage: "ektara",
		LargeText:  "Ektara",
		SmallImage: "playing",
		SmallText:  "Idle",
	})
}

// logSong records a track in the history. It is called after the track has
// played rather than before, so the history shows what was listened to rather
// than what was queued.
func logSong(db *sql.DB, t Track) {
	res, err := db.Exec(
		"INSERT INTO songs (id,name,url,title) VALUES (?,?,?,?)",
		t.ID,
		t.Filename,
		t.PageURL,
		t.Title,
	)
	if err != nil {
		log.Fatal(err)
	}
	// nothing is printed here: the display owns the terminal, so anything
	// written straight to stdout would land in the middle of the player box.
	if _, err := res.LastInsertId(); err != nil {
		log.Printf("log song: %v", err)
	}
}

// titleFromFilename turns a downloaded mp3's filename into something worth
// showing a human, stripping the video id and the "youtube - " prefix that
// yt-dlp's output template adds.
func titleFromFilename(filename string) string {
	title := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	if id := parseID(filename); id != "" {
		title = strings.TrimSpace(strings.TrimPrefix(title, id))
	}
	return strings.TrimSpace(strings.TrimPrefix(title, "youtube - "))
}

// searchYouTube looks a name up on YouTube and returns the top few results.
//
// It only goes to YouTube when there is no recent answer to reuse, and it waits
// its turn first, so a burst of searching cannot arrive as a burst of requests.
// When YouTube refuses, an older answer is used in place of an error, so an
// address YouTube has decided to block still gets to play the songs that were
// found before it started refusing.
//
// This shells out to yt-dlp and waits for a network round trip, so it is a
// tea.Cmd: the display stays alive and can show a "searching" line while it runs.
func searchYouTube(db *sql.DB, query string) ([]video, error) {
	if remembered, fresh := cachedSearch(db, query); fresh {
		return remembered, nil
	}

	// yt-dlp might not be installed yet, and installing it takes a while, so
	// this happens before the search rather than being the user's problem
	ytdlp.MustInstall(context.TODO(), nil)

	const limit = 3

	// the wait is here rather than inside yt-dlp because it is meant to space
	// out whole searches, not the requests inside one of them
	youtubeLimiter.wait()

	found, _, err := ytdlp.New().
		Retries("3").
		FlatPlaylist().
		ExtractInfo(context.TODO(), fmt.Sprintf("ytsearch%d:%s", limit, query))
	if err != nil {
		// YouTube is saying no. Something remembered is better than an error,
		// so a search done before still answers now.
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

// downloadVideo saves one YouTube video as an mp3 in the working directory and
// returns the track to play. Like searchYouTube this is slow, so it is a cmd.
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

	// The bare name, not the path yt-dlp printed, for the same reason the
	// history stores bare names: the library can move and the name cannot.
	return Track{ID: id, Title: title, Filename: filepath.Base(filename)}, nil
}

// video is one YouTube search result.
type video struct {
	ID    string
	Title string
}

// localFiles returns the mp3s in the library folder, as tracks ready to play.
// The title is worked out from the filename, because a file that was
// downloaded from YouTube carries the real title in its name.
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
		// just the file name, not the whole path: this is what gets written to
		// the history, and a path in the database breaks if the library ever
		// moves. The folder is worked out again when the file is opened.
		base := filepath.Base(name)
		tracks = append(tracks, Track{
			ID:       parseID(base),
			Title:    titleFromFilename(base),
			Filename: base,
		})
	}

	return tracks, nil
}

// songRow is one row of the songs table, carrying enough to play it again.
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
	// every column is nullable in sqlite's eyes: rows written before url and
	// title existed have NULL there, and id is nullable in the schema
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

// historyTracks returns every song that has been played, newest first, so the
// display can show them as a list the user can walk through with the arrow
// keys instead of asking for a number.
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

// oto allows only one context per process, and one context has one sample rate,
// so the rate is ours to choose rather than the first track's.
var (
	otoOnce sync.Once
	otoCtx  *oto.Context
	otoErr  error
	// otoRate is the rate the context was created with. Every source has to
	// produce audio at exactly this rate, because oto does no resampling of its
	// own: feed it audio recorded at a different rate and it plays at the wrong
	// speed, and there is no way to notice from here.
	otoRate int
)

// defaultSampleRate is the rate the context is created with. It is fixed for
// the whole process, so it is a constant rather than whatever the first track
// happened to be.
//
// 48000 is the rate to fix it at. Most of what this plays is YouTube, which
// serves 48kHz Opus, and 48kHz is what PipeWire, PulseAudio and Bluetooth all
// default to, so the common case needs no conversion anywhere. A 44100 file, or
// a device that wants 44100, is handled by the sound server below us.
const defaultSampleRate = 48000

// bytesPerFrame is how many bytes of PCM make up one frame of audio: two
// channels of signed 16-bit. A frame is the smallest thing a position can move
// by, so every conversion between a byte offset and a playing time counts frames
// rather than bytes.
const bytesPerFrame = 2 * 2

// getOtoContext returns the process wide audio context, creating it with
// sampleRate the first time it is called. Every later caller gets the same one
// whatever rate it asks for, because oto allows only a single context and the
// device cannot be reconfigured underneath a playing track. That is why every
// source is resampled to defaultSampleRate rather than the other way round.
func getOtoContext(sampleRate int) (*oto.Context, error) {
	otoOnce.Do(func() {
		otoRate = sampleRate
		op := &oto.NewContextOptions{SampleRate: sampleRate,
			ChannelCount: 2,
			Format:       oto.FormatSignedInt16LE,
		}
		ctx, readyChan, err := oto.NewContext(op)
		if err != nil {
			otoErr = err
			return
		}
		// it might take a bit for hardware audio devices to be ready
		<-readyChan
		otoCtx = ctx
		otoErr = ctx.Err()
	})
	return otoCtx, otoErr
}

// seekBy moves playback by delta, and moves the Discord start time with it, so
// the elapsed time on the presence keeps matching what is actually being heard.
//
// It works by asking oto to seek the source. A local mp3 rewinds by seeking the
// file, which is instant. A stream has to kill ffmpeg and start a new one at
// the right place, and pick up a fresh media url, which takes a second or two.
func seekBy(player *oto.Player, src audioSource, start time.Time, delta time.Duration) time.Time {

	// where we are now, in pcm bytes
	from, err := src.Seek(0, io.SeekCurrent)
	if err != nil {
		log.Printf("seek: %v", err)
		return start
	}

	// delta is a whole number of seconds, so this is a whole number of frames
	to := from + int64(delta/time.Second)*int64(src.Rate())*bytesPerFrame

	newPos, err := player.Seek(to, io.SeekStart)
	if err != nil {
		log.Printf("seek: %v", err)
		return start
	}

	// a seek forward means more audio has been heard, which means the presence
	// has to start earlier to show the same elapsed time
	return start.Add(-pcmDuration(newPos-from, src.Rate()))
}

// openTrack starts the audio for one track and hands back the source and the
// player that are reading it.
//
// This is the slow half of playing: it asks ffprobe how long a local file is,
// spawns ffmpeg, and for a stream resolves a fresh media url first, which takes
// a second or two. That is why it is a tea.Cmd and not a plain call, so the
// display can keep repainting while it happens.
func openTrack(t Track) (audioSource, *oto.Player, error) {
	ctx := context.Background()

	var (
		src audioSource
		err error
	)
	if t.Stream {
		src, err = openStream(ctx, t)
	} else {
		src, err = openLocal(ctx, t)
	}
	if err != nil {
		return nil, nil, err
	}

	// oto allows only one context per process, so this returns the same one
	// however many tracks are played.
	otoCtx, err := getOtoContext(src.Rate())
	if err != nil {
		src.Stop()
		return nil, nil, fmt.Errorf("audio device: %w", err)
	}

	// the player reads from the source, so the source has to be seekable for
	// h and k to rewind. Both kinds of source are.
	player := otoCtx.NewPlayer(src)
	player.Play()

	return src, player, nil
}

// playedDuration reports how much of the song has actually been heard.
//
// It counts bytes the player has pulled rather than reading the wall clock, so
// it never includes the delay before audio starts, and it does not advance
// while paused: oto stops reading the source when the player is paused.
func playedDuration(src audioSource, player *oto.Player) time.Duration {
	// ask the source where it is, rather than counting bytes, so that a seek
	// moves the displayed position too
	pos, err := src.Seek(0, io.SeekCurrent)
	if err != nil || pos <= 0 {
		return 0
	}

	// the player reads ahead of the audio hardware, so discount whatever is
	// still sitting in its buffer to get the sample that is audible right now
	if buffered := int64(player.BufferedSize()); buffered < pos {
		pos -= buffered
	} else {
		pos = 0
	}

	return pcmDuration(pos, src.Rate())
}

// pcmDuration turns a count of PCM bytes into a playing time.
func pcmDuration(bytes int64, rate int) time.Duration {
	return time.Duration(bytes/bytesPerFrame) * time.Second / time.Duration(rate)
}

// audioSource is the audio the player reads: signed 16-bit stereo PCM that can
// be rewound and knows how to be shut down. It has to be seekable, because
// that is the only way oto's Seek works.
//
// The rate it produces is not its own to choose. It has to be the rate the
// device was opened at, because oto hands the samples straight to the sound
// server as they are and will not fix a rate that does not match.
type audioSource interface {
	io.ReadSeeker
	// Rate is the sample rate of the PCM this produces
	Rate() int
	// Length is how long the whole track is, or 0 when it is not known
	Length() time.Duration
	// Stop releases the file handle, or kills ffmpeg
	Stop()
}

// warner is implemented by a source that has something to say to the player that
// does not fit through Read and Seek — currently only "you seeked past the end
// of the download, so this is about to stop".
//
// It is a separate interface rather than another method on audioSource because
// only one kind of source has anything to say. The player asks whether the
// source it was handed is one of those, and a source that is not is silent: a
// local file has nothing to report, and giving it a channel that never carries
// anything would be the same as saying nothing with more code.
//
// Seek runs on oto's goroutine and the display belongs to the goroutine running
// play, so neither can touch the other's. The channel is how a value crosses
// between them, and the player reads it without waiting, so a source that is
// slow to say something is a source that is not holding up playback.
type warner interface {
	Warnings() <-chan string
}

// openLocal prepares a local file for playback.
//
// ffmpeg does the decoding here too, for the same reason it does it for a
// stream: the audio device is locked to one sample rate for the whole process,
// so whatever comes out of here has to be at that rate whatever the file was
// recorded at. ffmpeg is told which rate to produce and resamples on the way
// there. A file decoded in process could not do that, and would play slow or
// fast whenever its rate was not the one the device was locked to.
//
// ponytail: this means a local file needs ffmpeg, which it did not before. That
// is the price of not being wrong about the rate, and ffmpeg is already needed
// for every stream.

func openLocal(ctx context.Context, t Track) (audioSource, error) {
	// A Track carries the bare file name, so the library folder is worked out
	// here. Doing it at the moment of opening is what lets the whole library
	// move without the history in the database going stale.
	path, err := musicPath(t.Filename)
	if err != nil {
		return nil, err
	}

	// Ask for the length before anything is spawned, so a file that is missing
	// or is not audio at all is reported as itself rather than turning into a
	// track that silently finishes at once. It is worked out once here because
	// every seek starts ffmpeg again, and ffprobe costs a process of its own.
	length, err := localDuration(path)
	if err != nil {
		return nil, err
	}

	return newFFmpegSource(ctx, func(context.Context) (string, time.Duration, error) {
		return path, length, nil
	})
}

// localDuration asks ffprobe how long a file is.
//
// It is a separate program rather than a guess from the file size, because an
// mp3 header carries a bitrate and dividing the size by that is only right for
// a constant bitrate file. ffprobe reads the headers properly, and it ships in
// the same package as ffmpeg.
//
// A file ffprobe can make no sense of is an error, but a file whose length it
// cannot pin down is not: that is only worth losing the progress bar's total
// over, so the track plays and the bar counts up without one.
func localDuration(filename string) (time.Duration, error) {
	args := []string{
		"-v", "error",
		// ask for the container's own idea of how long it is
		"-show_entries", "format=duration",
		// print the bare number, so there is nothing to pick out of the output
		"-of", "default=noprint_wrappers=1:nokey=1",
		filename,
	}

	out, err := exec.Command("ffprobe", args...).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe %q: %w", filename, err)
	}

	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, nil
	}

	return time.Duration(seconds * float64(time.Second)), nil
}

// resolveFunc hands ffmpeg something to read and says how long the result is.
// A stream resolves to a fresh media url every time it is called, because the
// url carries a token that expires and a seek needs one that still works.
type resolveFunc func(ctx context.Context) (input string, duration time.Duration, err error)

// ffmpegSource is a stream of signed 16-bit stereo PCM that can be rewound.
// Everything that plays goes through one of these, a local file as much as a
// YouTube stream, because everything has to come out at the same rate.
//
// ffmpeg pours audio out of a pipe like water through a hose: once a byte has
// been read it is gone, so a pipe cannot be rewound. Seek therefore kills the
// running ffmpeg and starts a new one with -ss, which is what makes this look
// seekable from oto's point of view.
type ffmpegSource struct {
	ctx     context.Context
	resolve resolveFunc

	// rate is what ffmpeg was told to produce, which is the rate the audio
	// device is locked to for the whole process
	rate int
	// length is how long the track is, 0 when nothing could say
	length time.Duration

	cmd *exec.Cmd
	r   io.ReadCloser

	// pos is where we are in the track, in pcm bytes. A pipe cannot be asked,
	// so it is counted here. It is atomic because the player reads on oto's
	// goroutine while the position display reads it from the main one.
	pos atomic.Int64
}

// newFFmpegSource builds a source that plays whatever resolve hands over,
// resampled to the rate the audio device is locked to.
func newFFmpegSource(ctx context.Context, resolve resolveFunc) (*ffmpegSource, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg is required to play anything: %w", err)
	}

	// the rate has to be settled before ffmpeg can be told what to produce, and
	// it is settled here rather than by the track because it cannot change once
	// the device is open
	if _, err := getOtoContext(defaultSampleRate); err != nil {
		return nil, fmt.Errorf("audio device: %w", err)
	}

	// whatever rate the context ended up at is the rate ffmpeg has to produce,
	// which is the same number whether or not something got here first
	f := &ffmpegSource{ctx: ctx, resolve: resolve, rate: otoRate}
	if err := f.start(0); err != nil {
		return nil, err
	}
	return f, nil
}

// start kills whatever ffmpeg is running and begins a new one at seekTo.
func (f *ffmpegSource) start(seekTo time.Duration) error {
	f.Stop()

	input, duration, err := f.resolve(f.ctx)
	if err != nil {
		return err
	}
	f.length = duration

	args := []string{"-hide_banner", "-loglevel", "error"}
	if seekTo > 0 {
		args = append(args, "-ss", strconv.FormatFloat(seekTo.Seconds(), 'f', 3, 64))
	}
	args = append(args,
		"-i", input,
		"-vn", // drop any video stream
		"-f", "s16le", "-ar", strconv.Itoa(f.rate), "-ac", "2", "-",
	)

	f.cmd = exec.CommandContext(f.ctx, "ffmpeg", args...)
	pipe, err := f.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// ffmpeg's own complaints are thrown away. With -loglevel error it only
	// speaks when something is wrong, but the one thing it says during a seek
	// is not wrong at all: for a stream, the temporary file is briefly missing
	// while the new ffmpeg starts, which is expected, and the audio carries on.
	// Letting that through would put it on the screen in the middle of the
	// interface, so it goes to the null device instead. A track that really
	// cannot play still reaches the player, which reports it properly.
	f.cmd.Stderr = io.Discard

	if err := f.cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	f.r = pipe
	return nil
}

func (f *ffmpegSource) Read(p []byte) (int, error) {
	// a failed start leaves no pipe, and a nil f.r would be a crash rather than
	// a track that could not play
	if f.r == nil {
		return 0, io.EOF
	}

	n, err := f.r.Read(p)

	// count what was read, because a pipe cannot be asked where it is. This is
	// what makes the position display move, so it cannot be left out.
	f.pos.Add(int64(n))

	return n, err
}

// Seek implements io.Seeker, which is the only thing oto's Seek needs: it
// type-asserts its source to an io.Seeker and forwards to it.
//
// oto has already stopped reading and thrown away the buffered audio by the
// time this runs, so f.r can be swapped out without a lock.
func (f *ffmpegSource) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		// a pipe cannot report where it is, so the position is kept alongside
		// the source instead
		offset += f.pos.Load()
	default:
		// seeking from the end would need the length in bytes, and the length
		// is not known until the next resolve, so it is not offered
		return 0, fmt.Errorf("cannot seek with whence %d", whence)
	}
	if offset < 0 {
		offset = 0
	}

	// being asked where we are happens every time the position is drawn, and
	// must not throw away the ffmpeg that is playing
	if offset == f.pos.Load() {
		return offset, nil
	}

	// a seek past the end makes ffmpeg produce nothing at all, the pipe closes
	// and the player reports the track finished, which is the behaviour we want
	if err := f.start(pcmDuration(offset, f.rate)); err != nil {
		return 0, err
	}
	f.pos.Store(offset)
	return offset, nil
}

// restartAtCurrent throws away the running ffmpeg and starts a new one at the
// position already reached, without moving that position.
//
// Seek cannot do this. It is asked where we are every time the position is
// drawn, and it leaves the ffmpeg alone when the answer is where it already is.
// This is the other thing: pick the audio up again from the same place, which a
// source reading a file that is still growing needs whenever it reaches the end
// of what has arrived.
func (f *ffmpegSource) restartAtCurrent() error {
	return f.start(pcmDuration(f.pos.Load(), f.rate))
}

func (f *ffmpegSource) Rate() int {
	return f.rate
}

func (f *ffmpegSource) Length() time.Duration {
	return f.length
}

// Stop kills the running ffmpeg and waits for it, so nothing is left behind
// and a stream is not still downloading after the user has moved on.
func (f *ffmpegSource) Stop() {
	if f.cmd == nil {
		return
	}
	_ = f.cmd.Process.Kill()
	_ = f.cmd.Wait()
	f.cmd = nil
	f.r = nil
}

// parseID pulls the YouTube video id out of a downloaded filename, which
// yt-dlp writes as "<id> <extractor> - <title>.<ext>". It returns "" when the
// first field is not shaped like an id, which is how a hand named mp3 is
// recognised as having nothing to recover.
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

// lastNonEmptyLine returns the final line of s with its whitespace trimmed,
// or "" when s holds no such line. yt-dlp reports the file it wrote as the
// last non empty line of its output.
func lastNonEmptyLine(s string) string {
	var last string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			last = line
		}
	}
	return last
}
