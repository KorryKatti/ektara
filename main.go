package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"bytes"
	"database/sql"
	"github.com/ebitengine/oto/v3"
	"github.com/eiannone/keyboard"
	"github.com/hajimehoshi/go-mp3"
	"github.com/lrstanley/go-ytdlp"
	_ "github.com/mattn/go-sqlite3"
	"log"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	db, err := sql.Open("sqlite3", "songs.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

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

	for {
		filename := chooseSong(scanner, db)
		if filename == "" {
			fmt.Println("Bye!")
			return
		}
		// db insertion
		dBresult, err := db.Exec(
			"INSERT INTO songs (id,name) VALUES (?,?)",
			parseID(filename),
			filename,
		)
		if err != nil {
			log.Fatal(err)
		}
		seq, err := dBresult.LastInsertId()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("New row:", seq)

		if !play(filename) {
			return
		}

		fmt.Println("\nFinished:", filename)
	}
}

// chooseSong shows the mode menu and returns the selected file,
// or "" if the user wants to quit / no file was chosen.
func chooseSong(scanner *bufio.Scanner, db *sql.DB) string {
	fmt.Println("\nChoose mode:")
	fmt.Println("1: Online (search & download from YouTube)")
	fmt.Println("2: Offline (play mp3 files in current directory)")
	fmt.Println("q: Quit")
	fmt.Print("> ")

	mode := ""
	if !scanner.Scan() {
		return ""
	}
	mode = strings.TrimSpace(scanner.Text())

	var filename string

	switch mode {
	case "1":
		// Install/cache yt-dlp if it isn't installed yet.
		ytdlp.MustInstall(context.TODO(), nil)

		fmt.Println("Enter name of song please:")

		var input string
		if scanner.Scan() {
			input = strings.TrimSpace(scanner.Text())
		}

		const limit = 3

		fmt.Println("You entered:", input)

		videos, _, err := ytdlp.New().
			FlatPlaylist().
			ExtractInfo(context.TODO(), fmt.Sprintf("ytsearch%d:%s", limit, input))
		if err != nil {
			panic(err)
		}

		if len(videos) == 0 {
			fmt.Println("No videos found")
			return ""
		}

		topThreeSlice := videos
		if len(topThreeSlice) > limit {
			topThreeSlice = topThreeSlice[:limit]
		}

		fmt.Println("\nTop Results:")

		for i, video := range topThreeSlice {
			if video != nil {
				title := ""
				if video.Title != nil {
					title = *video.Title
				}
				fmt.Printf("%d: ID: %s | Title: %s\n",
					i+1,
					video.ID,
					title,
				)
			}
		}

		fmt.Print("\nEnter which song would you like to download (0 to cancel): ")

		if !scanner.Scan() {
			fmt.Println("No selection provided")
			return ""
		}

		choice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err != nil {
			fmt.Println("Please enter a number.")
			return ""
		}

		if choice == 0 {
			return ""
		}

		if choice < 1 || choice > len(topThreeSlice) {
			fmt.Println("Invalid choice.")
			return ""
		}

		selectedVideo := topThreeSlice[choice-1]
		selectedTitle := ""
		if selectedVideo.Title != nil {
			selectedTitle = *selectedVideo.Title
		}

		fmt.Println("You selected:", selectedTitle)

		url := fmt.Sprintf(
			"https://www.youtube.com/watch?v=%s",
			selectedVideo.ID,
		)

		fmt.Println("Downloading:", url)

		outputTemplate := "%(id)s %(extractor)s - %(title)s.%(ext)s"

		dl := ytdlp.New().
			ExtractAudio().
			AudioFormat("mp3").
			Output(outputTemplate).
			Print("after_move:filepath")

		res, err := dl.Run(context.TODO(), url)
		if err != nil {
			panic(err)
		}

		filename = lastNonEmptyLine(res.Stdout)
		if filename == "" {
			panic("could not determine downloaded file path")
		}

		fmt.Println("Downloaded file:", filename)
		fmt.Println("Download complete!")

	case "2":
		files, err := filepath.Glob("*.mp3")
		if err != nil {
			panic(err)
		}

		if len(files) == 0 {
			fmt.Println("No mp3 files found in current directory.")
			return ""
		}

		fmt.Println("\nLocal mp3 files:")
		for i, f := range files {
			fmt.Printf("%d: %s\n", i+1, f)
		}

		fmt.Print("\nEnter which song would you like to play (0 to cancel): ")

		if !scanner.Scan() {
			fmt.Println("No selection provided")
			return ""
		}

		choice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err != nil {
			fmt.Println("Please enter a number.")
			return ""
		}

		if choice == 0 {
			return ""
		}

		if choice < 1 || choice > len(files) {
			fmt.Println("Invalid choice.")
			return ""
		}

		filename = files[choice-1]

	case "q", "Q":
		return ""

	default:
		fmt.Println("Unknown choice:", mode)
		return ""
	}

	return filename
}

// oto allows only one context per process, so we create it once
// with the first song's sample rate and reuse it afterwards.
var (
	otoOnce sync.Once
	otoCtx  *oto.Context
	otoErr  error
)

func getOtoContext(sampleRate int) (*oto.Context, error) {
	otoOnce.Do(func() {
		op := &oto.NewContextOptions{
			SampleRate:   sampleRate,
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

// play plays one song. Returns true when the song ended on its own
// (so the menu loop should continue), false when the user quit the app.
func play(filename string) bool {
	// read mp3 into memory
	fileBytes, err := os.ReadFile(filename)
	if err != nil {
		panic(fmt.Sprintf("reading %q failed: %v", filename, err))
	}
	// Convert the pure bytes into a reader object that can be used with the mp3 decoder
	fileBytesReader := bytes.NewReader(fileBytes)

	// decode file
	decodedMp3, err := mp3.NewDecoder(fileBytesReader)
	if err != nil {
		panic("mp3.NewDecoder failed: " + err.Error())
	}

	// prepare an Oto context ( this will use your default audio device)
	// Remember that you should **not** create more than one context,
	// so the context is created once and reused for every song.
	otoCtx, err := getOtoContext(decodedMp3.SampleRate())
	if err != nil {
		panic("oto initialization failed: " + err.Error())
	}

	// create a new player that will handle our sound
	player := otoCtx.NewPlayer(decodedMp3)
	defer player.Close()

	start := time.Now()
	player.Play()

	if err := keyboard.Open(); err != nil {
		panic(err)
	}
	defer keyboard.Close()

	// channel that receives key presses
	keys := make(chan string)

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

	for {
		select {
		case key := <-keys:
			switch key {
			case "p":
				if paused {
					player.Play()
					paused = false
					fmt.Println("Playing")
				} else {
					player.Pause()
					paused = true
					fmt.Println("Paused")
				}
			case "q":
				player.PauseAndStopReading()
				return true // back to menu
			}
		default:
			// song ended on its own -> back to the menu
			if !player.IsPlaying() && !paused {
				return true
			}
			elapsed := time.Since(start).Round(time.Second)
			fmt.Printf("\rCurrent position: %v  (p:pause  q:menu)", elapsed)
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func parseID(filename string) string {
	fields := strings.Fields(filename)
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

func lastNonEmptyLine(s string) string {
	var last string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			last = line
		}
	}
	return last
}
