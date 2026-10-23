package main

// source : lrclib.net
import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	lyricsBaseURL = "https://lrclib.net"
	lyricsClient  = &http.Client{Timeout: 15 * time.Second}
)

// lrclib asks for this
const lyricsUserAgent = "ektara (https://github.com/korrykatti/ektara)"

// respectring lrclib's request limit so pause between end of one and start of next
var lyricsLimiter = newBurstLimiter(1, 400*time.Millisecond)

// One LRCLIB search result for a song version; choose the correct match from multiple results
type lyricRecord struct {
	ID           int     `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	SyncedLyrics string  `json:"syncedLyrics"`
	PlainLyrics  string  `json:"plainLyrics"`
}

// One line of a song, and when it is sung
type lyricLine struct {
	Time time.Duration
	Text string
}

// Lyrics for one track: timed lines, plain text, or no lyrics if instrumental. Exactly one lyric format is filled
type lyrics struct {
	Synced       []lyricLine
	Plain        string
	Instrumental bool
}

// Formats a record for display, keeping timed lyrics when available or using plain text otherwise
func (r lyricRecord) asLyrics() lyrics {
	if r.Instrumental {
		return lyrics{Instrumental: true}
	}
	if lines := parseLRC(r.SyncedLyrics); len(lines) > 0 {
		return lyrics{Synced: lines}
	}
	return lyrics{Plain: r.PlainLyrics}
}

// two frields and two more that help , look needs from a track
type lyricQuery struct {
	Song     string
	Artist   string
	Duration time.Duration
}

// Builds a track search query using the song title or track name as a fallback, without the album

func lyricQueryFromTrack(t Track) lyricQuery {
	q := lyricQuery{Song: t.Song, Artist: t.Artist, Duration: t.Duration}
	if q.Song == "" {
		q.Song = t.Title
	}
	return q
}

// Searches LRCLIB for song lyrics; no results means no lyrics are available, while failures are left for the caller to handle
func searchLyrics(ctx context.Context, q lyricQuery) ([]lyricRecord, error) {
	lyricsLimiter.wait()

	form := url.Values{}
	form.Set("track_name", q.Song)
	if q.Artist != "" {
		form.Set("artist_name", q.Artist)
	}
	// whole seconds are as precise as lrclib knows
	if q.Duration > 0 {
		form.Set("duration", strconv.Itoa(int(q.Duration.Seconds()+0.5)))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lyricsBaseURL+"/api/search?"+form.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", lyricsUserAgent)

	resp, err := lyricsClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Any non-list response, including 503 errors, is treated as a failure so it isn't mistaken for missing lyrics; the caller handles retries
	if resp.StatusCode != http.StatusOK {
		//funny enough precious can do this part too
		return nil, fmt.Errorf("lrclib search: %s %s", resp.Status, q.Song)
	}

	var records []lyricRecord
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return nil, fmt.Errorf("lrclib search: %w", err)
	}
	return records, nil

}

// Selects the best lyrics match based on track duration, timing availability, and artist match
func pickRecord(records []lyricRecord, artist string, duration time.Duration) (lyricRecord, bool) {
	var best lyricRecord
	bestArtist := false
	for _, r := range records {
		if duration > 0 {
			// LRCLIB's own rule, and the only thing that tells the hundred
			// covers of a famous song apart.
			if outBy := r.Duration - duration.Seconds(); outBy > 2 || outBy < -2 {
				continue
			}
		}
		if best == (lyricRecord{}) {
			best, bestArtist = r, artistMatches(r, artist)
			continue
		}
		if beatsRecord(r, best, artist, bestArtist) {
			best, bestArtist = r, artistMatches(r, artist)
		}
	}
	if best == (lyricRecord{}) {
		return lyricRecord{}, false
	}
	return best, true
}

// says wheter canditate shold take the best-so-far's place
func beatsRecord(candidate, incumbent /*woah big english words*/ lyricRecord, artist string, incumbentArtist bool) bool {
	// synced prefer over plain
	if candidate.SyncedLyrics != "" && incumbent.SyncedLyrics == "" {
		return true
	}
	if incumbent.SyncedLyrics != "" && candidate.SyncedLyrics == "" {
		return false
	}
	// Prefer the known track artist over other artists
	return artistMatches(candidate, artist) && !incumbentArtist
	// so many functions , 4 years ago i would have never done so
}

// Matches artists loosely by checking if either name contains the other, allowing for spelling and credit variations
func artistMatches(r lyricRecord, artist string) bool {
	artist, r.ArtistName = strings.ToLower(strings.TrimSpace(artist)), strings.ToLower(strings.TrimSpace(r.ArtistName))
	if artist == "" || r.ArtistName == "" {
		return false
	}

	return strings.Contains(r.ArtistName, artist) || strings.Contains(artist, r.ArtistName)
}

// Parses LRC timed lyrics and removes metadata tags that contain no lyrics
func parseLRC(raw string) []lyricLine {
	var lines []lyricLine
	for _, row := range strings.Split(raw, "\n") {
		row = strings.TrimRight(row, "\r")

		times, text := peelTimestamps(row)
		if len(times) == 0 {
			continue
		}
		// Skips timed lines with no lyrics to avoid highlighting empty space
		if text == "" {
			continue
		}
		for _, t := range times {
			lines = append(lines, lyricLine{Time: t, Text: text})
		}
	}
	// Sorts lyric lines by timestamp to ensure they display in the correct order
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time < lines[j].Time })
	return lines
}

// Extracts timestamps and lyrics from an LRC line, supports multiple timestamps, and skips metadata tags
func peelTimestamps(row string) (times []time.Duration, text string) {
	for strings.HasPrefix(row, "[") {
		end := strings.Index(row, "]")
		if end < 0 {
			return times, ""
		}
		at, ok := parseTimestamp(row[1:end]) /*damn the functions*/
		if !ok {
			return times, ""
		}
		times = append(times, at)
		row = row[end+1:]
	}
	return times, row
}

// Parses an LRC timestamp with minutes, seconds, fractions, optional hours, and comma or dot separators
func parseTimestamp(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	whole, frac, _ := strings.Cut(s, ".")
	whole, frac, _ = strings.Cut(strings.TrimSpace(whole), ",")

	parts := strings.Split(whole, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, false
	}
	n := make([]int, len(parts))
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || v < 0 {
			return 0, false
		}
		n[i] = v
	}
	at, err := time.ParseDuration(fmt.Sprintf("%dh%dm%fs", n[0], n[1], float64(n[2])+fracSeconds(frac)))
	if err != nil {
		return 0, false
	}
	return at, true
}

// Converts the timestamp fraction into seconds, handling hundredths, milliseconds, and other fractional formats
func fracSeconds(frac string) float64 {
	switch len(frac) {
	case 1:
		return float64(atLeastZero(frac)) / 10
	case 2:
		return float64(atLeastZero(frac)) / 100
	default:
		return float64(atLeastZero(frac)) / 1000
	}
}

func atLeastZero(frac string) int {
	v, err := strconv.Atoi(frac)
	if err != nil || v < 0 {
		return 0
	}
	return v
}
