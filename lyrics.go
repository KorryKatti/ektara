package main

// Where the words of a song come from, and when they are sung.
//
// The words come from https://lrclib.net, a free and open lyrics database. No
// key and no registration. Two things happen here: the words are looked up over
// the network, and the timestamps inside them are read out, so they can follow
// the music later.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The address the lyrics are fetched from, and the client that does the
// fetching. Vars rather than consts so the tests can point both at a local
// server, which is how the client is tested without any network.
var (
	lyricsBaseURL = "https://lrclib.net"
	lyricsClient  = &http.Client{Timeout: 15 * time.Second}
)

// A contact address, so they know who to find if something goes wrong. It is
// not enforced, but it is how lrclib.net asks to be used.
const lyricsUserAgent = "ektara (https://github.com/ribo/ektara)"

// One request at a time, with a pause between the end of one and the start of
// the next, which is what lrclib.net's usage note asks for. Its own limiter, not
// YouTube's, so a lyrics lookup never stalls a search and the other way round.
var lyricsLimiter = newBurstLimiter(1, 400*time.Millisecond)

// Store lyrics in the search cache using prefixed keys to avoid conflicts and prevent pruning
const lyricsCachePrefix = "lyrics:"

// One record of the search answer: what LRCLIB says about one version of the
// song. Search is fuzzy, so there are often several, and picking the right one
// is the work below.
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

// One line of a song, and when it is sung.
type lyricLine struct {
	Time time.Duration
	Text string
}

// Lyrics for one track: either the timed lines, or the plain block, and the
// knowledge that there are no words at all. Exactly one of the first two is
// filled, and neither is when a track is instrumental.
type lyrics struct {
	Synced       []lyricLine
	Plain        string
	Instrumental bool
}

// Turns a record into the shape the screen wants. Records that carry timings
// keep them; records without fall back to the plain block.
func (r lyricRecord) asLyrics() lyrics {
	if r.Instrumental {
		return lyrics{Instrumental: true}
	}
	if lines := parseLRC(r.SyncedLyrics); len(lines) > 0 {
		return lyrics{Synced: lines}
	}
	return lyrics{Plain: r.PlainLyrics}
}

// What a lookup needs from a track: two fields, and two more that help.
type lyricQuery struct {
	Song     string
	Artist   string
	Duration time.Duration
}

// lyricQueryFromTrack builds the lookup for one track. The song title is the
// search key; the track's own name is what to use if the song name was never
// learned. The album is not sent: LRCLIB's search does not want it, and we
// rarely have it.
func lyricQueryFromTrack(t Track) lyricQuery {
	q := lyricQuery{Song: t.Song, Artist: t.Artist, Duration: t.Duration}
	if q.Song == "" {
		q.Song = t.Title
	}
	return q
}

// searchLyrics asks LRCLIB for the versions of a song it knows about. An empty
// slice with no error is a real answer: it means nobody has edited in lyrics
// for that track and there is nothing more to find.
//
// There is no retry here. A failed attempt stays a failed attempt; the caller
// decides whether it is worth trying again.
func searchLyrics(ctx context.Context, q lyricQuery) ([]lyricRecord, error) {
	lyricsLimiter.wait()

	form := url.Values{}
	form.Set("track_name", q.Song)
	if q.Artist != "" {
		form.Set("artist_name", q.Artist)
	}
	// Whole seconds are as precise as LRCLIB knows.
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

	// Everything that is not a list is a problem, including 503, which the
	// server throws about as often as once in ten. The caller retries; it only
	// matters here that a 503 is not mistaken for "there are no lyrics".
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lrclib search: %s %s", resp.Status, q.Song)
	}

	var records []lyricRecord
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return nil, fmt.Errorf("lrclib search: %w", err)
	}
	return records, nil
}

// pickRecord picks the candidate to sing along to, or says there is none. Three
// senses in order: the version that runs as long as the track does, the version
// with timings over the version without, and the version by the same artist as
// the track was already known for.
func pickRecord(records []lyricRecord, artist string, duration time.Duration) (lyricRecord, bool) {
	var best lyricRecord
	found, bestArtist := false, false
	for _, r := range records {
		if duration > 0 {
			// LRCLIB's own rule, and the only thing that tells the hundred
			// covers of a famous song apart.
			if outBy := r.Duration - duration.Seconds(); outBy > 2 || outBy < -2 {
				continue
			}
		}
		if !found {
			best, found, bestArtist = r, true, artistMatches(r, artist)
			continue
		}
		if beatsRecord(r, best, artist, bestArtist) {
			best, bestArtist = r, artistMatches(r, artist)
		}
	}
	if !found {
		return lyricRecord{}, false
	}
	return best, true
}

// beatsRecord says whether candidate should take the best-so-far's place.
func beatsRecord(candidate, incumbent lyricRecord, artist string, incumbentArtist bool) bool {
	// Timed lyrics can follow the music. Plain ones cannot, so they lose.
	if candidate.SyncedLyrics != "" && incumbent.SyncedLyrics == "" {
		return true
	}
	if incumbent.SyncedLyrics != "" && candidate.SyncedLyrics == "" {
		return false
	}
	// The same artist as the track was already known for, over anybody else.
	return artistMatches(candidate, artist) && !incumbentArtist
}

// artistMatches is deliberately loose: the search is fuzzy, and spelling and
// credit shorthand vary. Either name sitting inside the other is near enough.
func artistMatches(r lyricRecord, artist string) bool {
	artist, r.ArtistName = strings.ToLower(strings.TrimSpace(artist)), strings.ToLower(strings.TrimSpace(r.ArtistName))
	if artist == "" || r.ArtistName == "" {
		return false
	}
	return strings.Contains(r.ArtistName, artist) || strings.Contains(artist, r.ArtistName)
}

// parseLRC reads an LRC file into its lines. LRC is "[mm:ss.xx] line", the xx
// being hundredths of a second. Tags that carry metadata, like [ti:...], carry
// no time and no words worth showing, so their lines are dropped.
func parseLRC(raw string) []lyricLine {
	var lines []lyricLine
	for _, row := range strings.Split(raw, "\n") {
		row = strings.TrimRight(row, "\r")

		times, text := peelTimestamps(row)
		if len(times) == 0 {
			continue
		}
		// A line with a time and no words is a silence in the song, and a
		// highlight over empty space says nothing. Dropped.
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		for _, t := range times {
			lines = append(lines, lyricLine{Time: t, Text: text})
		}
	}
	// Honest as the file may be about its order, the follow-along presumes the
	// times go up, so they are put in order regardless.
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time < lines[j].Time })
	return lines
}

// peelTimestamps takes the bracketed times off the front of one LRC row and
// says what is left. There may be several, which is how the same line is made
// to appear twice. When the bracket is something else, like a [ti:...] tag, the
// row is dropped along with everything in it.
func peelTimestamps(row string) (times []time.Duration, text string) {
	for strings.HasPrefix(row, "[") {
		end := strings.Index(row, "]")
		if end < 0 {
			return times, ""
		}
		at, ok := parseTimestamp(row[1:end])
		if !ok {
			return times, ""
		}
		times = append(times, at)
		row = row[end+1:]
	}
	return times, row
}

// parseTimestamp reads one time of an LRC row: minutes, seconds and a fraction
// of a second. A comma may stand in for the dot, and there may be an hour part.
func parseTimestamp(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	// A comma stands in for the dot in some files.
	s = strings.ReplaceAll(s, ",", ".")

	whole, frac, _ := strings.Cut(s, ".")
	parts := strings.Split(whole, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}

	var n [3]int // hours, minutes, seconds
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || v < 0 {
			return 0, false
		}
		n[3-len(parts)+i] = v
	}

	at := time.Duration(n[0])*time.Hour +
		time.Duration(n[1])*time.Minute +
		time.Duration(n[2])*time.Second
	return at + time.Duration(fracSeconds(frac)*float64(time.Second)), true
}

// fracSeconds turns the text after the dot into a part of one second. One digit
// is tenths, two are hundredths, as LRC writes them, and three or more are
// thousandths.
func fracSeconds(frac string) float64 {
	if frac == "" {
		return 0
	}
	v, err := strconv.Atoi(frac)
	if err != nil || v < 0 {
		return 0
	}
	if len(frac) < 3 {
		return float64(v) / float64(pow10(len(frac)))
	}
	return float64(v) / 1000
}

// pow10 is ten to the n, for turning a count of digits into its place value.
func pow10(n int) int {
	v := 1
	for ; n > 0; n-- {
		v *= 10
	}
	return v
}

// Tracks cached lyrics, including empty results; failed lookups are not cached
func cachedLyrics(db *sql.DB, trackID string) (lyrics, bool) {
	// wihtot and id there is no key , and nothing was ever stored
	if trackID == "" {
		return lyrics{}, false
	}

	var payload string
	err := db.QueryRow(
		"SELECT results FROM search_cache WHERE query = ?",
		lyricsCachePrefix+trackID,
	).Scan(&payload)
	if err != nil {
		return lyrics{}, false
	}
	var remembered lyrics
	if err := json.Unmarshal([]byte(payload), &remembered); err != nil {
		return lyrics{}, false
	}
	return remembered, true
}

// Caches track lyrics; cache errors are logged without affecting successful lookups
func storeLyrics(db *sql.DB, t Track, found lyrics) {
	if t.ID == "" {
		return // Manually named files have no ID to use as a key
	}

	payload, err := json.Marshal(found)
	if err != nil {
		log.Printf("cache lyrics %s: %v", t.label(), err)
		return
	}

	_, err = db.Exec(`
	INSERT INTO search_cache (query, results, fetched_at) VALUES (?,?,?)
	ON CONFLICT(query) DO UPDATE SET
		results = excluded.results,
		fetched_at = excluded.fetched_at
	`,
		lyricsCachePrefix+t.ID, string(payload), time.Now().Unix(),
	)
	if err != nil {
		log.Printf("cache lyrics %s: %v", t.label(), err)
	}
}

// Identifies the track by video ID or local file name to reject stale lyrics
func lyricsKey(t Track) string {
	return t.ID + "|" + t.Filename
}
