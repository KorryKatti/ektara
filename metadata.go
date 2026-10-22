package main

// The music's own details: who made it, what it is on, what it sounds like, and
// how long it runs.
//
// Where they come from varies. yt-dlp knows the artist, the album and the song
// for most videos, from YouTube's own metadata. For the rest the artist is in
// the title, because most official uploads are titled "Artist - Song (Official
// Video)".

import (
	"regexp"
	"strings"
	"time"

	"github.com/lrstanley/go-ytdlp"
)

// The words in a title that mean the video is not the song: official video, 4K,
// remastered and the rest. None of them are ever part of an artist's name.
//
// "video", "audio" and "hd" are left out on purpose: they turn up inside real
// band names, and a title that says "Official Video" has already been caught by
// "official".
var titleNoise = regexp.MustCompile(`(?i)\b(official|music video|lyric video|lyrics|visuali[sz]er|remaster(ed)?|4k|full album|explicit)\b`)

// The same words in brackets, which is how they usually arrive.
var titleNoiseInBrackets = regexp.MustCompile(`(?i)\s*[\(\[][^()\[\]]*\b(official|music video|lyric|visuali[sz]er|remaster|4k|full album|explicit|hd|hq|audio|video)\b[^()\[\]]*[\)\]]`)

// The separators a title puts between the artist and the song.
var titleSeparators = []string{" - ", " – ", " — "}

// Splits a title into the artist and the song, when the title says.
//
// Most official uploads are titled "Artist - Song (Official Video)", and for
// those the artist is in the title and nowhere else.
func splitTitle(title string) (artist, song string) {
	title = strings.TrimSpace(title)

	// With no separator the whole title is the song.
	song = title

	for _, sep := range titleSeparators {
		i := strings.Index(title, sep)
		if i <= 0 {
			continue
		}
		artist = strings.TrimSpace(title[:i])
		song = strings.TrimSpace(title[i+len(sep):])
		if titleNoise.MatchString(artist) {
			return "", ""
		}
		break
	}

	// The words that are not the song: official video, 4K, remastered.
	return artist, strings.TrimSpace(titleNoiseInBrackets.ReplaceAllString(song, ""))
}

// The artist out of a title, when the title says.
func artistFromTitle(title string) string {
	artist, _ := splitTitle(title)
	return artist
}

// The artist out of the channel a video came from.
//
// The auto-generated music channels are named "Artist - Topic", so the suffix
// comes off, and what is left is the artist.
func artistFromUploader(uploader string) string {
	uploader = strings.TrimSpace(uploader)
	for _, suffix := range []string{" - Topic", " - topic", " - VEVO", " - vevo"} {
		uploader = strings.TrimSuffix(uploader, suffix)
	}
	return strings.TrimSpace(uploader)
}

// The music's own details, as far as they are known.
type trackMeta struct {
	Artist   string
	Album    string
	Song     string
	Genres   []string
	Duration time.Duration
}

// What yt-dlp knows about a track.
//
// The genres are only there for the videos that carry a MusicBrainz block, so
// they are usually empty. The artist, the album and the song come from
// YouTube's own metadata and are usually there.
func metaFromInfo(info *ytdlp.ExtractedInfo) trackMeta {
	var m trackMeta
	if info == nil {
		return m
	}

	if len(info.Artists) > 0 {
		m.Artist = info.Artists[0]
	}
	if info.Album != nil {
		m.Album = *info.Album
	}
	if info.Track != nil {
		m.Song = *info.Track
	}
	m.Genres = info.Genres
	if info.Duration != nil && *info.Duration > 0 {
		m.Duration = time.Duration(*info.Duration * float64(time.Second))
	}

	return m
}

// Puts the metadata on a track, keeping whatever the track already knew.
func (t *Track) apply(m trackMeta) {
	if m.Artist != "" {
		t.Artist = m.Artist
	}
	if m.Album != "" {
		t.Album = m.Album
	}
	if m.Song != "" {
		t.Song = m.Song
	}
	if len(m.Genres) > 0 {
		t.Genres = m.Genres
	}
	if m.Duration > 0 {
		t.Duration = m.Duration
	}
}

// Fills in what the title knows, when nothing else said.
//
// The title is the last place looked, because it is the least reliable: it says
// "Artist - Song (Official Video)" and nothing else.
func (t *Track) fillFromTitle() {
	artist, song := splitTitle(t.Title)

	if t.Artist == "" {
		t.Artist = artist
	}
	if t.Song == "" {
		t.Song = song
	}
}

// The genre list as one string, for the database.
func (t Track) genreString() string {
	return strings.Join(t.Genres, ",")
}

// The name of the song, which is the video title when nothing said otherwise.
func (t Track) name() string {
	if t.Song != "" {
		return t.Song
	}
	return t.Title
}
