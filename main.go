package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/lrstanley/go-ytdlp"
	"github.com/raitonoberu/ytsearch"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)

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

	dl := ytdlp.New().
		ExtractAudio().
		AudioFormat("mp3").
		Output("%(extractor)s - %(title)s.%(ext)s")

	_, err = dl.Run(context.TODO(), url)
	if err != nil {
		panic(err)
	}

	fmt.Println("Download complete!")
}
