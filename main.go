package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"bytes"

	"github.com/ebitengine/oto/v3"
	"github.com/eiannone/keyboard"
	"github.com/hajimehoshi/go-mp3"
	"github.com/lrstanley/go-ytdlp"
	"github.com/raitonoberu/ytsearch"
	"time"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("Choose mode:")
	fmt.Println("1: Online (search & download from YouTube)")
	fmt.Println("2: Offline (play mp3 files in current directory)")
	fmt.Print("> ")

	mode := ""
	if scanner.Scan() {
		mode = strings.TrimSpace(scanner.Text())
	}

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

		search := ytsearch.VideoSearch(input)

		result, err := search.Next()
		if err != nil {
			panic(err)
		}

		if len(result.Videos) == 0 {
			fmt.Println("No videos found")
			return
		}

		// Don't try to take 3 elements if fewer than 3 were returned.
		if len(result.Videos) < limit {
		}

		topThreeSlice := result.Videos
		if len(topThreeSlice) > limit {
			topThreeSlice = topThreeSlice[:limit]
		}

		fmt.Println("\nTop Results:")

		for i, video := range topThreeSlice {
			if video != nil {
				fmt.Printf("%d: ID: %s | Title: %s\n",
					i+1,
					video.ID,
					video.Title,
				)
			}
		}

		fmt.Print("\nEnter which song would you like to download: ")

		if !scanner.Scan() {
			fmt.Println("No selection provided")
			return
		}

		choice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err != nil {
			fmt.Println("Please enter a number.")
			return
		}

		if choice < 1 || choice > len(topThreeSlice) {
			fmt.Println("Invalid choice.")
			return
		}

		selectedVideo := topThreeSlice[choice-1]

		fmt.Println("You selected:", selectedVideo.Title)

		url := fmt.Sprintf(
			"https://www.youtube.com/watch?v=%s",
			selectedVideo.ID,
		)

		fmt.Println("Downloading:", url)

		outputTemplate := "%(extractor)s - %(title)s.%(ext)s"

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
			return
		}

		fmt.Println("\nLocal mp3 files:")
		for i, f := range files {
			fmt.Printf("%d: %s\n", i+1, f)
		}

		fmt.Print("\nEnter which song would you like to play: ")

		if !scanner.Scan() {
			fmt.Println("No selection provided")
			return
		}

		choice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err != nil {
			fmt.Println("Please enter a number.")
			return
		}

		if choice < 1 || choice > len(files) {
			fmt.Println("Invalid choice.")
			return
		}

		filename = files[choice-1]

	default:
		fmt.Println("Unknown choice:", mode)
		return
	}

	// music player
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
	// play all our sounds. Its configuration can't be changed later
	// what ???
	op := &oto.NewContextOptions{}
	// usually 44100 or 48000. Other values might cause distortions
	op.SampleRate = 44100

	// 2 - stereo sound
	op.ChannelCount = 2

	// format of soruce, go-mp3's format is signed 16bit integers.
	op.Format = oto.FormatSignedInt16LE

	// Remember that you should **not** create more than one context
	// i guess bro
	otoCtx, readyChan, err := oto.NewContext(op)
	if err != nil {
		panic("oto.NewContext failed: " + err.Error())
	}
	// it might take a biut for hardware audio devvices to be ready , so wait on the channel
	<-readyChan
	if err := otoCtx.Err(); err != nil {
		panic("oto initialization failed : " + err.Error())
	}

	// create a new player that will handle our sound , paused by default
	player := otoCtx.NewPlayer(decodedMp3)
	//play starts playing the sound and returns wtihout waiting for it ( its async)

	commands := make(chan string)

	start := time.Now()
	player.Play()

	err = keyboard.Open()
	if err != nil {
		panic(err)
	}

	// Always close keyboard properly when program ends.
	defer keyboard.Close()

	// Start a goroutine that listens for keys.
	go func() {

		for {

			// Wait for a single key press.
			//
			// key = the character pressed
			// _,  = other information we don't need
			// err = possible error
			key, _, err := keyboard.GetKey()

			if err != nil {
				return
			}

			// Send the key to our main loop.
			commands <- string(key)
		}

	}()

	for {
		select {
		case command := <-commands:
			switch command {
			case "p":
				if player.IsPlaying() {

					player.Pause()
					fmt.Println("Paused")
				} else {
					player.Play()
					fmt.Println("Playing")
				}
			case "q":
				player.Close()
				return
			}

		default:
			if player.IsPlaying() {
				elapsed := time.Since(start).Round(time.Second)
				fmt.Printf("\rCurrent position: %v", elapsed)
			}
			time.Sleep(100 * time.Millisecond)
		}

	}
	fmt.Println("\nFinished:", filename)

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
