package main

import (
	"bufio"
	"fmt"
	"github.com/raitonoberu/ytsearch"
	"os"
	"strconv"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("Enter name of song please:")

	var input string
	if scanner.Scan() {
		input = scanner.Text()
	}

	const limit int = 3
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

	topThreeSlice := result.Videos[:limit]

	fmt.Println("\nTop Results:")
	for i, video := range topThreeSlice {
		if video != nil {
			fmt.Printf("%d: ID: %s | Title: %s\n", i+1, video.ID, video.Title)
		}
	}
	fmt.Println("Enter which song would u like to download : ")
	var choice string
	if scanner.Scan() {
		choice = scanner.Text()
		strconv.Atoi(choice)
	}
	fmt.Println("you selected : ", choice)

}
