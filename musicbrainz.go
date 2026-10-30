package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

var (
	musicbrainzBaseURL = "https://musicbrainz.org"
	musicbrainzClient  = &http.Client{Timeout: 15 * time.Second}
)

const musicbrainzUserAgent = "ektara (https://github.com/korrykatti/ektara)"

// dont spam , blocks last hours
var musicbrainzLimiter = newBurstLimiter(1, time.Second)

// cache key prefix
const genreCachePrefix = "genre:"

// -- JSON shapes --
type mbArtist struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type mbArtistCredit struct {
	// name as credited on this recording
	Name   string   `json:"name"`
	Artist mbArtist `json:"artist"`
}

type mbReleaseGroup struct {
	ID             string   `json:"id"`
	PrimaryType    string   `json:"primary-type"`
	SecondaryTypes []string `json:"secondary-types"`
}

type mbRelease struct {
	ReleaseGroup mbReleaseGroup `json:"release-group"`
}

type mbRecording struct {
	ID           string           `json:"id"`
	Title        string           `json:"title"`
	Length       int              `json:"length"` // in ms
	Video        bool             `json:"video"`  // idek why this , but musicBrainz has it
	ArtistCredit []mbArtistCredit `json:"artist-credit"`
	Releases     []mbRelease      `json:"releases"`
}

type mbGenre struct {
	Name string `json:"name"`
	// how strongly music brainz associates this genere with entity
	// for oredering not confidence
	Count int `json:"count"`
}

// Both /release-group/<mbid> and /artist/<mbid> with inc=genres answer with this shape
type mbGenreAnswer struct {
	Genres []mbGenre `json:"genres"`
}

// -- json shapes helpers --

// Combines all credited names into one string to support multi-artist matching
func (r mbRecording) credit() string {
	names := make([]string, 0, len(r.ArtistCredit))
	for _, c := range r.ArtistCredit {
		names = append(names, c.Name)
	}
	return strings.Join(names, ", ")
}

// Uses the first credited artist when the release has no genres
func (r mbRecording) primaryArtistID() string {
	if len(r.ArtistCredit) == 0 {
		return ""
	}
	return r.ArtistCredit[0].Artist.ID
}

// Lowercases and trims titles for exact matching, so "Song" won't match "Song (Live)"
func normaliseTitle(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// Matches by title and artist first; uses duration only to choose between valid matches
func pickRecording(records []mbRecording, song, artist string, duration time.Duration) (mbRecording, bool) {
	want := normaliseTitle(song)
	who := strings.ToLower(strings.TrimSpace(artist))
	if want == "" || who == "" {
		return mbRecording{}, false
	}
	var (
		best      mbRecording
		found     bool
		bestDelta time.Duration
	)

	for _, r := range records {
		if r.Video {
			continue // concert recording or MV
		}
		if normaliseTitle(r.Title) != want {
			continue // isn't it normalize not normalise
		}
		// Matches the artist name even when credits include extra names or use shorter names
		credit := strings.ToLower(r.credit())
		if credit == "" {
			continue
		}
		if !strings.Contains(credit, who) && !strings.Contains(who, credit) {
			continue
		}
		// how feasible may it be to self host musicbrainz ?
		delta := time.Duration(0)
		if duration > 0 && r.Length > 0 {
			delta = duration - time.Duration(r.Length)*time.Millisecond
			if delta < 0 {
				delta = -delta
			}
		}
		// first match win default ; later match take over if more length closer
		if !found || (r.Length > 0 && delta < bestDelta) {
			best, found, bestDelta = r, true, delta
		}
	}
	return best, found
}

// Prefers albums, EPs, and singles because compilations and DJ mixes often have mixed genres
func (rg mbReleaseGroup) preferred() bool {
	switch strings.ToLower(rg.PrimaryType) {
	case "album", "ep", "single":
	default:
		return false
	}
	for _, secondary := range rg.SecondaryTypes {
		switch strings.ToLower(secondary) {
		case "compilation", "dj-mix":
			return false
		}
	}
	return true
}

// Prefers a suitable album, EP, or single, otherwise uses the first available release group
func pickReleaseGroup(r mbRecording) (string, bool) {
	fallback := ""
	for _, release := range r.Releases {
		id := release.ReleaseGroup.ID
		if id == "" {
			continue
		}
		if fallback == "" {
			fallback = id
		}
		if release.ReleaseGroup.preferred() {
			return id, true
		}
	}
	return fallback, fallback != ""
}

// --- merging the genres ---
// Combines genres, removes case-insensitive duplicates, and keeps the top three by count, breaking ties alphabetically
func mergeGenres(groups ...[]mbGenre) []string {
	counts := map[string]int{}
	names := map[string]string{}

	for _, group := range groups {
		for _, genre := range group {
			name := strings.TrimSpace(genre.Name)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			counts[key] += genre.Count
			if _, seen := names[key]; !seen {
				names[key] = name
			}
		}
	}
	keys := make([]string, 0, len(counts))
	// i hope you leave
	for key := range counts {
		keys = append(keys, key)
		// and don't come back
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	genres := make([]string, 0, 3)
	for _, key := range keys {
		genres = append(genres, names[key])
		if len(genres) == 3 {
			break
		}
	}
	return genres
}

// musicBrainz talking

func musicbrainzGet(ctx context.Context, path string, out any) error {
	const attempts = 3
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		musicbrainzLimiter.wait()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, musicbrainzBaseURL+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", musicbrainzUserAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := musicbrainzClient.Do(req)
		if err != nil {
			// connection failure != refusal to answer
			return err
		}
		if resp.StatusCode == http.StatusServiceUnavailable {
			resp.Body.Close()
			lastErr = fmt.Errorf("Musicbrainz: %s", resp.Status)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("Musicbrainz: %s for %s", resp.Status, path)
		}
		err = json.NewDecoder(resp.Body).Decode(out)
		resp.Body.Close()
		if err != nil {
			return err
		}
		return nil
	}
	return lastErr
}

// quoteLucene wraps a term in quotes. Titles contain "+", ":", "-" and other
// characters Lucene treats as operators; quoting makes them words again.
func quoteLucene(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// asks for five most likely recordings for this song and artist
func searchRecordings(ctx context.Context, song, artist string) ([]mbRecording, error) {
	query := "recording:" + quoteLucene(song) // i have no idea how or what lucene does whatever it does , it was just suggested to me
	if artist != "" {
		query += "  AND artist:" + quoteLucene(artist)
	}
	path := "/ws/2/recording?" + url.Values{
		"query": {query},
		"fmt":   {"json"},
		"limit": {"5"},
	}.Encode()

	var answer struct {
		Recordings []mbRecording `json:"recordings"`
	}
	if err := musicbrainzGet(ctx, path, &answer); err != nil {
		return nil, err
	}
	return answer.Recordings, nil
}

func releaseGroupGenres(ctx context.Context, id string) ([]mbGenre, error) {
	var answer mbGenreAnswer
	path := "/ws/2/release-group/" + /* walking out the door with your bags */ url.PathEscape(id) + "?inc=genres&fmt=json"
	if err := musicbrainzGet(ctx, path, &answer); err != nil {
		return nil, err
	}
	return answer.Genres, nil
}

func artistGenres(ctx context.Context, id string) ([]mbGenre, error) {
	var answer mbGenreAnswer
	path := "/ws/2/artist/" + url.PathEscape(id) + "?inc=genres&fmt=json"
	if err := musicbrainzGet(ctx, path, &answer); err != nil {
		return nil, err
	}
	return answer.Genres, nil
}

// --- resolver ---
// returns the genres for a song or an error if the lookup fails
func findGenres(ctx context.Context, db *sql.DB, t Track) ([]string, error) {
	key := lyricsKey(t)
	if cached, ok := cachedGenres(db, key); ok {
		return cached, nil
	}
	q := lyricQueryFromTrack(t)
	// yt-dlp sometimes puts "(Remastered 2009)" or "(Official Audio)" in the
	// song name. That is title noise, not part of the title, and MusicBrainz
	// does not file it that way; the rest of the program already strips it.
	q.Song = strings.TrimSpace(titleNoiseInBrackets.ReplaceAllString(q.Song, ""))
	if q.Song == "" || q.Artist == "" {
		return nil, nil
	}
	records, err := searchRecordings(ctx, q.Song, q.Artist)
	if err != nil {
		return nil, err
	}

	rec, ok := pickRecording(records, q.Song, q.Artist, q.Duration)
	if !ok {
		// song not in musicbrainz so cache and save it
		storeGenres(db, t, nil)
		return nil, nil
	}

	var groups [][]mbGenre
	if rg, ok := pickReleaseGroup(rec); ok {
		genres, err := releaseGroupGenres(ctx, rg)
		if err != nil {
			return nil, err
		}
		groups = append(groups, genres)
	}
	// Uses artist genres only when release group genres are unavailabl
	if len(groups) == 0 || len(groups[0]) == 0 {
		if id := rec.primaryArtistID(); id != "" {
			genres, err := artistGenres(ctx, id)
			if err != nil {
				return nil, err
			}
			groups = append(groups, genres)
		}
	}
	found := mergeGenres(groups...)
	storeGenres(db, t, found)
	return found, nil
}

// --- cache ---

// cachedGenres returns cached genres and indicates whether a lookup was performed
func cachedGenres(db *sql.DB, trackKey string) ([]string, bool) {
	if db == nil || trackKey == "|" {
		return nil, false
	}
	var payload string
	err := db.QueryRow(
		"SELECT results FROM search_cache WHERE query = ?",
		genreCachePrefix+trackKey,
	).Scan(&payload)
	// man my back hurts so bad rn
	if err != nil {
		return nil, false
	}

	var genres []string
	if err := json.Unmarshal([]byte(payload), &genres); err != nil {
		return nil, false
	}
	return genres, true
}

// i think the issue with current times that our work and fun both are behind the screen
// so u think u are getting a rest but no
// more outside is needed
// i think i will go play tmrw
// wont commit this today
// will commit when done
// man

// caches the genres, logging any storage errors
func storeGenres(db *sql.DB, t Track, genres []string) {
	key := lyricsKey(t)
	// good morning , the pain is still there
	if db == nil || key == "|" {
		return
	}
	// i woke up an hour earlire today
	if genres == nil {
		// Ensure empty genres are stored as [] instead of null
		genres = []string{}
	}
	// screens shoulnt be the firs thign in the mornign
	payload, err := json.Marshal(genres)
	// ofc apart from alarm
	if err != nil {
		log.Printf("cache genres %s: %v", t.label(), err)
		return
	}
	// i thikn i should start by going out for running in the morining
	// even walking is fine
	_, err = db.Exec(`
	INSERT INTO search_cache (query, results, fetched_at) VALUES (?,?,?)
	ON CONFLICT(query) DO UPDATE SET
		results = excluded.results,
		fetched_at = excluded.fetched_at
	`,
		genreCachePrefix+key, string(payload), time.Now().Unix(),
	)
	if err != nil {
		log.Printf("cache grens %s: %v", t.label(), err)
	}
	// i hope the tea is good today

}
