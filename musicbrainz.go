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
