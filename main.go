package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bytes"
	"database/sql"
	"github.com/ebitengine/oto/v3"
	"github.com/eiannone/keyboard"
	"github.com/hajimehoshi/go-mp3"
	"github.com/lrstanley/go-ytdlp"
	_ "github.com/mattn/go-sqlite3"
	"log"
	"os/exec"

	"github.com/hugolgst/rich-go/client"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The TUI rewrite uses these. Each line is here so the file still builds
// before that package is used, and goes away as it gets used.
var (
	_ = list.New
	_ = tea.NewProgram
	_ = lipgloss.NewStyle
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

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	db, err := sql.Open("sqlite3", "songs.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Discord is optional. Without it the player still works, it just does not
	// show what is playing, so a login failure is worth a line in the log and
	// nothing more. The client leaves itself unlogged when the socket cannot
	// be opened, and quietly ignores every activity update after that, so
	// there is nothing to clean up here either.
	if err := client.Login("1553038133006704670"); err != nil {
		log.Printf("Discord rich presence unavailable: %v", err)
	}

	if err := setDiscordIdleActivity(); err != nil {
		log.Printf("update Discord activity: %v", err)
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS songs(
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		id TEXT,
		name TEXT NOT NULL
	)
	`)
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS metadata(
		id TEXT PRIMARY KEY,
		thumbnail_url TEXT NOT NULL
	)
	`)
	if err != nil {
		log.Fatal(err)
	}

	// url and title are only set for streamed tracks. An empty url is what
	// marks a row as a local file, so there is no separate flag column.
	for _, col := range []string{"url TEXT", "title TEXT"} {
		if _, err := db.Exec("ALTER TABLE songs ADD COLUMN " + col); err != nil &&
			!strings.Contains(err.Error(), "duplicate column name") {
			log.Printf("migrate songs: %v", err)
		}
	}

	// one set of options for the whole session, so switching shuffle or repeat
	// on part way through a queue carries over to the next one
	opts := &options{}

	for {
		queue, ok := chooseSong(scanner, db)
		if !ok {
			fmt.Println("Bye!")
			return
		}

		// shuffle happens once per queue, here, rather than as each track comes
		// wasShuffled remembers the setting as it was when the queue was last
		// looked at, so the loop can tell when s has been pressed since. Doing
		// the shuffle inside the loop on every track would keep the order
		// changing as you listen, so it only happens on the change.
		wasShuffled := opts.shuffle
		if opts.shuffle {
			shuffleQueue(queue)
		}

		// main holds the position so a and d can move both ways. modes 1-4 hand
		// back a queue of one, so the loop runs once and a/d do nothing.
		i := 0
		playing := true
		for playing {
			// shuffle switched on part way through: shuffle what is left to
			// play, so the change takes effect now without disturbing the
			// track that is already going.
			//
			// The index is wrapped, because with repeat on i can be past the
			// end of the queue already and the next track to play is the one
			// at the start, which leaves the whole queue to shuffle. Without
			// the wrap that hands shuffleQueue an empty slice and it panics
			// on the random number it is asked for.
			if opts.shuffle && !wasShuffled {
				shuffleQueue(queue[i%len(queue):])
			}
			wasShuffled = opts.shuffle

			// i is wrapped with the queue length so repeat can go round again,
			// and so i stays a plain count of tracks played
			track := queue[i%len(queue)]

			// one clock read per track, shared by the Discord presence and the
			// position display so both agree on when playback started
			start := time.Now()

			if err := setDiscordActivity(db, track, start); err != nil {
				log.Printf("update Discord activity: %v", err)
			}

			goWhere, playedIt := play(db, opts, track, i%len(queue), start, queue)
			if playedIt {
				logSong(db, track)
			}

			switch goWhere {
			case "next":
				i++
				// the end of the queue normally means the end of the music.
				// with repeat it just means round again, which is what the
				// wrap in the loop above is for.
				if i >= len(queue) && !opts.repeat {
					playing = false
				}
			case "prev":
				if i > 0 {
					i--
				}
			case "menu":
				playing = false
			}
		}

		// only once the whole queue is done, otherwise the presence flickers
		// to idle in the gap between two queued tracks
		if err := setDiscordIdleActivity(); err != nil {
			log.Printf("update Discord activity: %v", err)
		}
	}
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
	seq, err := res.LastInsertId()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("New row:", seq)
	fmt.Println("\nFinished:", t.label())
}

// chooseSong shows the mode menu and returns the tracks to play, in order.
// Modes 1-4 return a single track; mode 5 returns a whole queue. ok is false
// when the user quits or nothing was chosen.
func chooseSong(scanner *bufio.Scanner, db *sql.DB) ([]Track, bool) {
	fmt.Println("\nChoose mode:")
	fmt.Println("1: Online (search & download from YouTube)")
	fmt.Println("2: Offline (play mp3 files in current directory)")
	fmt.Println("3: Look at history (play queue)")
	fmt.Println("4: Stream (play from YouTube without downloading)")
	fmt.Println("5: Queue (play several tracks one after another)")
	fmt.Println("q: Quit")
	fmt.Print("> ")

	mode := ""
	if !scanner.Scan() {
		return nil, false
	}
	mode = strings.TrimSpace(scanner.Text())

	switch mode {
	case "1":
		// Install/cache yt-dlp if it isn't installed yet.
		ytdlp.MustInstall(context.TODO(), nil)

		id, title, ok := pickVideo(scanner, "download")
		if !ok {
			return nil, false
		}
		pageURL := "https://www.youtube.com/watch?v=" + id

		fmt.Println("Downloading:", pageURL)

		outputTemplate := "%(id)s %(extractor)s - %(title)s.%(ext)s"

		dl := ytdlp.New().
			ExtractAudio().
			AudioFormat("mp3").
			Output(outputTemplate).
			Print("after_move:filepath")

		res, err := dl.Run(context.TODO(), pageURL)
		if err != nil {
			fmt.Println("Download failed:", err)
			return nil, false
		}

		filename := lastNonEmptyLine(res.Stdout)
		if filename == "" {
			fmt.Println("Could not determine downloaded file path")
			return nil, false
		}

		fmt.Println("Downloaded file:", filename)
		fmt.Println("Download complete!")

		// the id is known here, so there is no need to recover it from the
		// filename the way local files have to
		return []Track{{ID: id, Title: title, Filename: filename}}, true

	case "2":
		files, err := filepath.Glob("*.mp3")
		if err != nil {
			panic(err)
		}

		if len(files) == 0 {
			fmt.Println("No mp3 files found in current directory.")
			return nil, false
		}

		fmt.Println("\nLocal mp3 files:")
		for i, f := range files {
			fmt.Printf("%d: %s\n", i+1, f)
		}

		choice, ok := askChoice(scanner, "play", len(files))
		if !ok {
			return nil, false
		}

		filename := files[choice-1]
		return []Track{{ID: parseID(filename), Title: titleFromFilename(filename), Filename: filename}}, true

	case "3":
		track, ok := historyMode(db)
		if !ok {
			return nil, false
		}
		return []Track{track}, true

	case "4":
		// Install/cache yt-dlp if it isn't installed yet.
		ytdlp.MustInstall(context.TODO(), nil)

		id, title, ok := pickVideo(scanner, "stream")
		if !ok {
			return nil, false
		}
		return []Track{{
			ID:      id,
			Title:   title,
			PageURL: "https://www.youtube.com/watch?v=" + id,
			Stream:  true,
		}}, true

	case "5":
		fmt.Print("How many tracks? ")
		if !scanner.Scan() {
			return nil, false
		}
		n, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err != nil || n < 1 {
			fmt.Println("Please enter a valid number")
			return nil, false
		}

		// each slot is an ordinary mode pick, so a queue can mix downloads,
		// local files, history entries and streams
		queue := make([]Track, 0, n)
		for i := 0; i < n; i++ {
			fmt.Printf("\n--- track %d of %d ---\n", i+1, n)
			more, ok := chooseSong(scanner, db)
			if !ok {
				// one bad pick should not throw away the tracks already
				// queued, so play whatever was built
				if len(queue) == 0 {
					return nil, false
				}
				fmt.Printf("\nQueue stopped early: %d of %d tracks\n", len(queue), n)
				break
			}
			queue = append(queue, more...)
		}
		return queue, true

	case "q", "Q":
		return nil, false

	default:
		fmt.Println("Unknown choice:", mode)
		return nil, false
	}
}

// askChoice reads a 1-based index from the user. ok is false when the user
// cancels with 0 or types something unusable.
func askChoice(scanner *bufio.Scanner, verb string, n int) (int, bool) {
	fmt.Printf("\nEnter which song would you like to %s (0 to cancel): ", verb)

	if !scanner.Scan() {
		fmt.Println("No selection provided")
		return 0, false
	}

	choice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil {
		fmt.Println("Please enter a number.")
		return 0, false
	}
	if choice == 0 {
		return 0, false
	}
	if choice < 1 || choice > n {
		fmt.Println("Invalid choice.")
		return 0, false
	}
	return choice, true
}

// pickVideo searches YouTube and asks which of the top results to use.
// ok is false when the user cancels.
func pickVideo(scanner *bufio.Scanner, verb string) (id, title string, ok bool) {
	fmt.Println("Enter name of song please:")

	var input string
	if scanner.Scan() {
		input = strings.TrimSpace(scanner.Text())
	}
	fmt.Println("You entered:", input)

	const limit = 3

	videos, _, err := ytdlp.New().
		FlatPlaylist().
		ExtractInfo(context.TODO(), fmt.Sprintf("ytsearch%d:%s", limit, input))
	if err != nil {
		fmt.Println("Search failed:", err)
		return "", "", false
	}

	if len(videos) > limit {
		videos = videos[:limit]
	}
	if len(videos) == 0 {
		fmt.Println("No videos found")
		return "", "", false
	}

	fmt.Println("\nTop Results:")
	for i, video := range videos {
		if video == nil {
			continue
		}
		title := ""
		if video.Title != nil {
			title = *video.Title
		}
		fmt.Printf("%d: ID: %s | Title: %s\n", i+1, video.ID, title)
	}

	choice, ok := askChoice(scanner, verb, len(videos))
	if !ok {
		return "", "", false
	}

	selected := videos[choice-1]
	fmt.Println("You selected:", *selected.Title)
	return selected.ID, *selected.Title, true
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

// prevSong returns the song with the largest seq less than `seq`.
func prevSong(db *sql.DB, seq int64) (songRow, error) {
	return scanSong(db.QueryRow(
		`SELECT `+songColumns+` FROM songs WHERE seq< ? ORDER BY seq DESC LIMIT 1`, seq))
}

// nextSong returns the song with the smallest seq greater than `seq`.
func nextSong(db *sql.DB, seq int64) (songRow, error) {
	return scanSong(db.QueryRow(
		`SELECT `+songColumns+` FROM songs WHERE seq > ? ORDER BY seq ASC LIMIT 1`, seq))
}

// historyMode is a full screen browser over the songs table. It opens on the
// newest row and reads keys directly rather than through the shared scanner,
// because it draws and redraws instead of asking a numbered question. ok is
// false when the user cancels.
func historyMode(db *sql.DB) (Track, bool) {
	// start at newest song
	current, err := scanSong(db.QueryRow(
		`SELECT ` + songColumns + ` FROM songs ORDER BY seq DESC LIMIT 1`))
	if err == sql.ErrNoRows {
		fmt.Println("History is empty")
		return Track{}, false
	}
	if err != nil {
		log.Printf("load history : %v", err)
		return Track{}, false
	}
	if err := keyboard.Open(); err != nil {
		panic(err)
	}
	defer keyboard.Close()

	for {
		// draw
		fmt.Print("\033[H\033[2J") // clear screen
		fmt.Println("History")
		fmt.Println()
		fmt.Printf("  > %s\n", current.Track.label())
		fmt.Println()
		fmt.Println("a:back  d:forward  Enter:play  q:cancel")

		r, code, err := keyboard.GetKey()
		if err != nil {
			return Track{}, false
		}

		// control keys (enter, esc, ...) come back with a zero rune,
		// so they have to be matched on the key code
		switch code {
		case keyboard.KeyEnter, keyboard.KeyCtrlJ:
			return current.Track, true
		case keyboard.KeyEsc:
			return Track{}, false
		}

		switch r {
		case 'a':
			prev, err := prevSong(db, current.Seq)
			if err == sql.ErrNoRows {
				// nothing older stay put
				continue
			}
			if err != nil {
				log.Printf("prev: %v", err)
				continue
			}
			current = prev

		case 'd':
			next, err := nextSong(db, current.Seq)
			if err == sql.ErrNoRows {
				// nothing newer stay put
				continue
			}
			if err != nil {
				log.Printf("next: %v", err)
				continue
			}
			current = next

		case '\r', '\n':
			return current.Track, true

		case 'q', 'Q':
			return Track{}, false
		}

	}
}

// oto allows only one context per process, so we create it once
// with the first song's sample rate and reuse it afterwards.
var (
	otoOnce sync.Once
	otoCtx  *oto.Context
	otoErr  error
	// otoRate is the rate the context was actually created with. Streams have
	// to be decoded to exactly this rate or they will play at the wrong pitch.
	otoRate int
)

// defaultSampleRate is the rate the context is created with when a stream is
// the first thing played, before any file has told us its rate.
const defaultSampleRate = 44100

// getOtoContext returns the process wide audio context, creating it with
// sampleRate the first time it is called. Every later caller gets the same one
// whatever rate it asks for, because oto allows only a single context and the
// device cannot be reconfigured underneath a playing track.
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

// inner is the width of the text inside the box, not counting its edges.
const inner = 58

// screen is everything the player display shows. It is collected first and
// drawn as one piece, so the display never flickers half updated.
type screen struct {
	title   string
	url     string
	elapsed time.Duration
	length  time.Duration
	paused  bool
	muted   bool
	volume  int
	shuffle bool
	repeat  bool
	notice  string
	// until is when the notice stops being shown. A notice with no expiry
	// would be wiped a tenth of a second after the key that made it, which
	// is too fast to read.
	until time.Time
	queue []Track
	index int
}

// clock formats a playing time as m:ss, or h:mm:ss once it is over an hour.
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Seconds())
	if hours := seconds / 3600; hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

// wide reports whether a rune is drawn two cells across, which every CJK and
// emoji rune is. Counting these as one is what makes a box with a Japanese
// title in it look broken.
func wide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // hangul jamo
		r >= 0x2E80 && r <= 0xA4CF,   // cjk radicals through yi
		r >= 0xAC00 && r <= 0xD7A3,   // hangul syllables
		r >= 0xF900 && r <= 0xFAFF,   // cjk compatibility
		r >= 0xFE30 && r <= 0xFE6F,   // cjk compatibility forms
		r >= 0xFF00 && r <= 0xFF60,   // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,   // fullwidth signs
		r >= 0x1F300 && r <= 0x1F64F, // emoji
		r >= 0x1F900 && r <= 0x1F9FF, // supplemental emoji
		r >= 0x20000 && r <= 0x3FFFD: // cjk extension planes
		return true
	}
	return false
}

// cells is how many columns s takes up on screen, which is not the same as
// how many runes it has.
func cells(s string) int {
	n := 0
	for _, r := range s {
		if wide(r) {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// clip shortens s until it fits in n columns, ending in an ellipsis when it
// had to cut, so a long title cannot break the shape of the box.
func clip(s string, n int) string {
	if cells(s) <= n {
		return s
	}
	// one column is kept for the ellipsis itself
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := 1
		if wide(r) {
			w = 2
		}
		if used+w > n-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + "…"
}

// say puts a message on the display for a couple of seconds, which is long
// enough to read and short enough not to sit there for the rest of the track.
func (s *screen) say(msg string) {
	s.notice = msg
	s.until = time.Now().Add(2 * time.Second)
}

// onOff says whether a switch is on, in as few characters as possible because
// the key hints have to fit on one line.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// muted marks the volume as silent, because the volume number stays where it
// was while mute is on, so the number alone would say otherwise.
func muted(b bool) string {
	if b {
		return " (muted)"
	}
	return ""
}

// line puts text between the left and right edges of the box, padded so every
// line is the same width and the box stays a rectangle.
func line(s string) string {
	s = clip(s, inner)
	return "│ " + s + strings.Repeat(" ", inner-cells(s)) + " │"
}

// seekBar is the progress bar, with a pacman at the playback position. The
// pacman opens and closes its mouth as it goes, which is what makes the
// movement easy to follow while the track plays.
//
// The two mouth shapes are Canadian aboriginal syllabics, which most terminal
// fonts carry. A terminal without them shows a box instead of a pacman, which
// is a poor look but not a broken one.
func seekBar(elapsed, length time.Duration, width int) string {
	// how far through, as a fraction between 0 and 1
	done := 0.0
	if length > 0 {
		done = float64(elapsed) / float64(length)
	}
	if done < 0 {
		done = 0
	}
	if done > 1 {
		done = 1
	}

	// the cell the pacman sits in, and whether its mouth is open right now
	at := int(done * float64(width))
	if at > width-1 {
		at = width - 1
	}
	pacman := 'ᗣ' // mouth closed
	if int(elapsed/(250*time.Millisecond))%2 == 0 {
		pacman = 'ᗧ' // mouth open, mid-chomp
	}

	bar := make([]rune, width)
	for i := range bar {
		switch {
		case i < at:
			bar[i] = '█' // already played
		case i == at:
			bar[i] = pacman
		default:
			bar[i] = '░' // still to come
		}
	}
	return string(bar)
}

// queueWindow picks the part of the queue to show, at most max entries, with
// the track playing kept inside it so a and d move the list with the music.
func queueWindow(queue []Track, index, max int) (start int, rows []Track) {
	if len(queue) <= max {
		return 0, queue
	}
	start = index - max/2
	if start < 0 {
		start = 0
	}
	if start > len(queue)-max {
		start = len(queue) - max
	}
	return start, queue[start : start+max]
}

// draw builds the whole display: the box, the key hints, and the queue.
func (s screen) draw() string {
	var b strings.Builder

	// the box: title at the top, the url under it, and the seek bar at the
	// bottom
	// the border is inner columns of text plus one column of space either side,
	// which is what line adds. Counting the dashes as inner+4 instead makes
	// the top and bottom two columns wider than everything between them.
	b.WriteString("╭" + strings.Repeat("─", inner+2) + "╮\n")
	b.WriteString(line("♪ "+s.title) + "\n")
	b.WriteString(line(s.url) + "\n")

	// a paused marker, so the display says why there is no sound. It goes on
	// the end of the seek line rather than on a row of its own, which keeps
	// the box three lines tall for a one track queue.
	state := ""
	if s.paused {
		state = "[paused]"
	}

	// the seek row: elapsed on the left, the total on the right, and the
	// pacman between them at the position playback has reached
	bar := seekBar(s.elapsed, s.length, 30)
	b.WriteString(line(fmt.Sprintf("%7s %s %7s  %s",
		clock(s.elapsed), bar, clock(s.length), state)) + "\n")
	b.WriteString("╰" + strings.Repeat("─", inner+2) + "╯\n")

	// the keys, with s and r showing their state, because a key that does
	// nothing visible is a key nobody presses twice. The lines are kept to the
	// width of the box, so the keys read as part of the display rather than as
	// something spilled out of it.
	b.WriteString(line(fmt.Sprintf("h/k seek  a prev  d next  p pause  s shuffle %v",
		onOff(s.shuffle))) + "\n")
	b.WriteString(line(fmt.Sprintf("r repeat %v  m mute  q back to menu  vol %d%%%s",
		onOff(s.repeat), s.volume, muted(s.muted))) + "\n")

	// a short lived message, for things like a key having been pressed
	if s.notice != "" {
		b.WriteString(" " + s.notice + "\n")
	}

	// the queue, when there is one, capped at five rows so it cannot push the
	// display off the screen
	if len(s.queue) > 1 {
		b.WriteString(fmt.Sprintf(" queue (%d):\n", len(s.queue)))
		start, rows := queueWindow(s.queue, s.index, 5)
		for i, t := range rows {
			marker := "  "
			if start+i == s.index {
				marker = "▸ "
			}
			b.WriteString(fmt.Sprintf("  %s%d. %s\n", marker, start+i+1, clip(t.Title, inner-6)))
		}
	}

	return b.String()
}

// redraw paints the display over the top of the last one, and puts the cursor
// back at the top left so the next paint starts from the same place.
func redraw(s screen) {
	// home, then erase down. erasing everything is simpler but makes the
	// whole terminal flash
	fmt.Print("\033[H\033[J" + s.draw())
}

// clearScreen wipes the display, so whatever the main loop prints next starts
// on a clean screen rather than on top of the box.
func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

// play plays one track and returns two things.
//
// The first is which way to go next:
//
//	"next" - the track played out, or the user pressed d
//	"prev" - the user pressed a
//	"menu" - the user pressed q
//
// The second is true when the track played all the way through. It is false
// when the track was skipped or could not be played, so skipped and broken
// tracks stay out of the history.
// The queue is passed in as well as the track, only so the display can show
// what is coming next. It is not read or changed here.
func play(db *sql.DB, opts *options, t Track, index int, start time.Time, queue []Track) (string, bool) {
	var (
		src audioSource
		err error
	)
	if t.Stream {
		fmt.Printf("Streaming %s\n", t.Title)
		src, err = openStream(context.Background(), t)
	} else {
		src, err = openLocal(t)
	}
	if err != nil {
		log.Printf("play %s: %v", t.label(), err)
		fmt.Printf("Could not play %s: %v\n", t.label(), err)
		return "next", false
	}
	// releases the file handle, or kills ffmpeg, which is what stops a stream
	// being downloaded in the background after the user has moved on
	defer src.Stop()

	// prepare an Oto context ( this will use your default audio device)
	// Remember that you should **not** create more than one context,
	// so the context is created once and reused for every song.
	otoCtx, err := getOtoContext(src.Rate())
	if err != nil {
		log.Printf("play %s: oto initialization failed: %v", t.label(), err)
		fmt.Printf("Could not play %s: audio device error: %v\n", t.label(), err)
		return "next", false
	}

	// create a new player that will handle our sound. the source is seekable,
	// which is what lets h and k rewind, because oto's Seek only works on a
	// source that implements io.Seeker.
	player := otoCtx.NewPlayer(src)
	defer player.Close()

	player.Play()

	if err := keyboard.Open(); err != nil {
		panic(err)
	}
	defer keyboard.Close()

	// channel that receives key presses. buffered so the goroutine can
	// hand the key over and go back to waiting, which lets keyboard.Close()
	// cancel it instead of leaving it stuck on the send forever.
	keys := make(chan string, 1)

	// Start a goroutine that listens for keys.
	go func() {
		for {
			key, _, err := keyboard.GetKey()
			if err != nil {
				return
			}
			keys <- string(key)
		}
	}()

	paused := false
	muted := false
	var current_volume float64 = 0

	// the display is painted from scratch on every tick and on every key, so
	// anything shown here is whatever is true right now
	view := screen{
		title:   t.Title,
		url:     t.PageURL,
		queue:   queue,
		index:   index,
		shuffle: opts.shuffle,
		repeat:  opts.repeat,
	}
	if !t.Stream {
		// a local file has no page url, so the file it came from is the
		// useful second line instead
		view.url = t.Filename
	}
	defer clearScreen()
	clearScreen()

	for {
		select {
		case key := <-keys:
			switch key {
			case "p":
				if paused {
					player.Play()
					paused = false
					// pass the original start so elapsed time keeps
					// counting from when the track first began
					if err := setDiscordActivity(db, t, start); err != nil {
						log.Printf("update Discord activity: %v", err)
					}
				} else {
					player.Pause()
					paused = true
					if err := setDiscordIdleActivity(); err != nil {
						log.Printf("update Discord activity: %v", err)
					}
				}
			case "h":
				seekBy(db, player, src, t, &start, -5*time.Second)
			case "k":
				seekBy(db, player, src, t, &start, 5*time.Second)

			case "s":
				view.say(opts.toggle("shuffle", &opts.shuffle))
			case "r":
				view.say(opts.toggle("repeat", &opts.repeat))

			case "a":
				// no wrap: the first track has nowhere to go back to
				if index > 0 {
					return "prev", false
				}
			case "d":
				return "next", false

			case "q":
				player.PauseAndStopReading()
				return "menu", true

			case "m":
				if !muted {
					// the level is remembered rather than remembered as
					// zero, so unmuting gives back the volume that was set
					current_volume = player.Volume()
					player.SetVolume(0.0)
					muted = true
					view.say("muted")
				} else {
					player.SetVolume(current_volume)
					muted = false
					view.say("unmuted")
				}
			case "+":
				if current_volume+0.01 > 1 {
					current_volume = 1.0
				} else {
					current_volume += 0.01
				}
				player.SetVolume(current_volume)
				view.say(fmt.Sprintf("volume %.0f%%", current_volume*100.0))
			case "-":
				if current_volume-0.01 < 0 {
					current_volume = 0
				} else {
					current_volume -= 0.01
				}
				player.SetVolume(current_volume)
				view.say(fmt.Sprintf("volume %.0f%%", current_volume*100.0))
			}

		default:
			// song ended on its own -> on to the next one
			if !player.IsPlaying() && !paused {
				// a source that broke stops the player with an error on it,
				// which is not the same as reaching the end of the track. Only
				// a clean finish earns a history row, or every track that
				// failed to stream would end up in it.
				if err := player.Err(); err != nil {
					log.Printf("play %s: %v", t.label(), err)
					// the message goes on the display rather than being
					// printed, or the redraw would wipe it immediately
					view.say(fmt.Sprintf("Playback failed: %v", err))
					redraw(view)
					// a moment to read it before the next track wipes it
					time.Sleep(3 * time.Second)
					return "next", false
				}
				return "next", true
			}

			// the notice goes away once its time is up
			if time.Now().After(view.until) {
				view.notice = ""
			}
			view.paused = paused
			view.muted = muted
			view.volume = int(current_volume*100.0 + 0.5)
			view.elapsed = playedDuration(src, player)
			view.length = src.Length()
			view.shuffle = opts.shuffle
			view.repeat = opts.repeat
			redraw(view)

			time.Sleep(100 * time.Millisecond)
		}
	}
}

// seekBy moves playback by delta, and moves the Discord start time with it, so
// the elapsed time on the presence keeps matching what is actually being heard.
//
// It works by asking oto to seek the source. A local mp3 rewinds by seeking the
// file, which is instant. A stream has to kill ffmpeg and start a new one at
// the right place, and pick up a fresh media url, which takes a second or two.
func seekBy(db *sql.DB, player *oto.Player, src audioSource, t Track, start *time.Time, delta time.Duration) {
	const bytesPerFrame = 2 * 2 // stereo, signed 16-bit

	// where we are now, in pcm bytes
	from, err := src.Seek(0, io.SeekCurrent)
	if err != nil {
		log.Printf("seek: %v", err)
		return
	}

	// delta is a whole number of seconds, so this is a whole number of frames
	to := from + int64(delta/time.Second)*int64(src.Rate())*bytesPerFrame

	newPos, err := player.Seek(to, io.SeekStart)
	if err != nil {
		log.Printf("seek: %v", err)
		return
	}

	// a seek forward means more audio has been heard, which means the presence
	// has to start earlier to show the same elapsed time
	*start = start.Add(-pcmDuration(newPos-from, src.Rate()))

	if err := setDiscordActivity(db, t, *start); err != nil {
		log.Printf("update Discord activity: %v", err)
	}
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
	const bytesPerFrame = 2 * 2 // stereo, signed 16-bit
	return time.Duration(bytes/bytesPerFrame) * time.Second / time.Duration(rate)
}

// audioSource is the audio the player reads: signed 16-bit stereo PCM that can
// be rewound and knows how to be shut down. Both sources have to be seekable,
// because that is the only way oto's Seek works.
type audioSource interface {
	io.ReadSeeker
	// Rate is the sample rate of the PCM this produces
	Rate() int
	// Length is how long the whole track is, or 0 when it is not known
	Length() time.Duration
	// Stop releases the file handle, or kills ffmpeg
	Stop()
}

// localSource plays a local mp3 with go-mp3. Nothing external is involved.
//
// Seeking is exact and instant rather than a restart of anything: the decoder's
// Seek takes an offset in PCM bytes and seeks the real file underneath, which
// is the same unit the position display counts in.
type localSource struct {
	*mp3.Decoder // brings Read and SampleRate straight from the decoder
	file         *os.File

	// atEnd records that a seek landed on the end of the track, so Read can
	// report the end plainly
	atEnd bool
}

func (s *localSource) Rate() int {
	return s.Decoder.SampleRate()
}

// Read comes from the decoder and hands over signed 16-bit PCM, whatever
// channels the file has.
func (s *localSource) Read(p []byte) (int, error) {
	// A seek to the very end leaves the decoder holding half of the last frame,
	// which it then reports as an unexpected EOF rather than a clean one. oto
	// treats any error other than io.EOF as fatal and stops the player, so the
	// track would look broken instead of finished. The audio really has run out
	// here, so say the plain thing.
	if s.atEnd {
		return 0, io.EOF
	}

	return s.Decoder.Read(p)
}

// Seek clamps the offset before handing it to the decoder.
//
// The decoder finds a frame by indexing a table of frame offsets with the
// position, and a position past the end of the file runs off the end of that
// table and panics. Clamping is also the sane answer: k held down near the end
// just means the track is over.
func (s *localSource) Seek(offset int64, whence int) (int64, error) {
	// being asked where we are happens every time the position is drawn, and
	// has to move nothing
	if offset == 0 && whence == io.SeekCurrent {
		return s.Decoder.Seek(0, io.SeekCurrent)
	}

	// the decoder needs a table of frame offsets to seek with, and only builds
	// one for a seekable file. Ours always is, so this should never trip.
	length := s.Decoder.Length()
	if length <= 0 {
		return 0, fmt.Errorf("cannot seek %q, its length is unknown", s.file.Name())
	}

	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		pos, err := s.Decoder.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, err
		}
		offset += pos
	default:
		// seeking from the end is not offered, so the position is never a
		// guess about how long the track is
		return 0, fmt.Errorf("cannot seek with whence %d", whence)
	}

	if offset < 0 {
		offset = 0
	}
	if offset > length {
		offset = length
	}
	s.atEnd = offset >= length

	// oto hands the offset straight to the source without checking it against
	// the length, so the clamp above is what keeps the decoder from panicking
	landed, err := s.Decoder.Seek(offset, io.SeekStart)

	// asking for the very end is reported as EOF, because there is no frame
	// there to read. That is not a failure, it is the end of the track: the
	// next Read runs out of audio and the player stops, which is what k held
	// down at the end should do.
	if err == io.EOF && offset >= length {
		return length, nil
	}

	return landed, err
}

// Length shadows the decoder's own Length, which counts pcm bytes rather than
// returning a time.
func (s *localSource) Length() time.Duration {
	return pcmDuration(s.Decoder.Length(), s.Rate())
}

func (s *localSource) Stop() {
	s.file.Close()
}

// openLocal prepares a local mp3 for playback.
func openLocal(t Track) (audioSource, error) {
	f, err := os.Open(t.Filename)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", t.Filename, err)
	}

	dec, err := mp3.NewDecoder(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("decode %q: %w", t.Filename, err)
	}

	// the device rate is fixed by whatever plays first, and there is no
	// resampler, so say so rather than let a mismatched file sound wrong
	if rate := dec.SampleRate(); otoRate != 0 && rate != otoRate {
		fmt.Printf("Warning: %s is %dHz but the audio device is %dHz, it will sound off pitch\n",
			t.Filename, rate, otoRate)
	}

	// ponytail: a mono mp3 will play at half speed and sound terrible, because
	// the decoder emits one channel while oto is opened for two. go-mp3 does
	// not expose the channel count and its frame reader is internal, so there
	// is no cheap way to spot one. Widen every sample into both channels here
	// if mono files ever need to work.

	return &localSource{Decoder: dec, file: f}, nil
}

// resolveFunc hands ffmpeg something to read and says how long the result is.
// A stream resolves to a fresh media url every time it is called, because the
// url carries a token that expires and a seek needs one that still works.
type resolveFunc func(ctx context.Context) (input string, duration time.Duration, err error)

// ffmpegSource is a stream of signed 16-bit stereo PCM that can be rewound.
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
	// length is how long the track is, 0 when yt-dlp could not say
	length time.Duration

	cmd    *exec.Cmd
	r      io.ReadCloser
	stderr *bytes.Buffer

	// pos is where we are in the track, in pcm bytes. A pipe cannot be asked,
	// so it is counted here. It is atomic because the player reads on oto's
	// goroutine while the position display reads it from the main one.
	pos atomic.Int64
}

// newFFmpegSource builds a source that plays whatever resolve hands over,
// resampled to the rate the audio device is locked to.
func newFFmpegSource(ctx context.Context, resolve resolveFunc) (*ffmpegSource, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg is required to stream: %w", err)
	}

	// the rate has to be settled before ffmpeg can be told what to produce,
	// and the first thing played is what settles it
	if _, err := getOtoContext(defaultSampleRate); err != nil {
		return nil, fmt.Errorf("audio device: %w", err)
	}

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

	f.stderr = &bytes.Buffer{}
	f.cmd = exec.CommandContext(f.ctx, "ffmpeg", args...)
	pipe, err := f.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	f.cmd.Stderr = f.stderr

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
	// ffmpeg is silent unless something went wrong, so anything here is worth
	// showing
	if msg := strings.TrimSpace(f.stderr.String()); msg != "" {
		log.Printf("ffmpeg: %s", msg)
	}
}

// openStream resolves the YouTube url to a direct media url and plays it.
// Nothing touches the disk. The resolution is kept as a function because every
// seek needs a fresh url.
func openStream(ctx context.Context, t Track) (audioSource, error) {
	return newFFmpegSource(ctx, func(ctx context.Context) (string, time.Duration, error) {
		return resolveStream(ctx, t.PageURL)
	})
}

// resolveStream asks yt-dlp for the direct, expiring media url behind a youtube
// watch url, plus how long the track is. It runs as late as possible because
// the url stops working once its token expires.
//
// Sometimes YouTube hands back a url that is already dead, and it reports 403
// the moment anything tries to read it. A dead url cannot be told apart from a
// live one by looking at it, so it is fetched for a few bytes and asked again
// if that fails. A live url answers 206, a dead one answers 403.
func resolveStream(ctx context.Context, pageURL string) (mediaURL string, duration time.Duration, err error) {
	const attempts = 3

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		var url string
		url, duration, lastErr = resolveOnce(ctx, pageURL)
		if lastErr != nil {
			continue
		}

		dead, err := urlIsDead(ctx, url)
		if err != nil {
			lastErr = fmt.Errorf("check %q: %w", pageURL, err)
			continue
		}
		if dead {
			log.Printf("resolve %q: attempt %d gave a dead url, asking again", pageURL, attempt)
			lastErr = fmt.Errorf("resolve %q: got a url that is already dead", pageURL)
			continue
		}

		return url, duration, nil
	}

	return "", 0, fmt.Errorf("resolve %q after %d attempts: %w", pageURL, attempts, lastErr)
}

// resolveOnce is a single yt-dlp lookup.
func resolveOnce(ctx context.Context, pageURL string) (mediaURL string, duration time.Duration, err error) {
	infos, _, err := ytdlp.New().
		Format("bestaudio").
		ExtractInfo(ctx, pageURL)
	if err != nil {
		return "", 0, fmt.Errorf("resolve %q: %w", pageURL, err)
	}
	if len(infos) == 0 || infos[0].URL == nil || *infos[0].URL == "" {
		return "", 0, fmt.Errorf("resolve %q: no audio stream found", pageURL)
	}
	if d := infos[0].Duration; d != nil {
		duration = time.Duration(*d * float64(time.Second))
	}
	return *infos[0].URL, duration, nil
}

// urlIsDead asks the server for the first kilobyte of the stream. Asking for a
// range rather than the whole thing keeps this cheap, and a few bytes are
// enough to be told yes or no.
func urlIsDead(ctx context.Context, mediaURL string) (bool, error) {
	const probe = "bytes=0-1023"

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Range", probe)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	// the body has to be drained and closed so the connection can be reused
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	// 403 is the one YouTube gives for a url it has decided to stop honouring.
	// Anything else, including a 206, is treated as working.
	return resp.StatusCode == http.StatusForbidden, nil
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
