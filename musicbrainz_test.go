package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The real limiter waits a second between requests. Tests cannot afford that,
// and they are not testing the limiter anyway (limiter_test.go does), so swap
// in a bucket with a thousand tokens.
func fastMusicBrainzLimiter(t *testing.T) {
	t.Helper()
	old := musicbrainzLimiter
	musicbrainzLimiter = newBurstLimiter(1000, time.Millisecond)
	t.Cleanup(func() { musicbrainzLimiter = old })
}

// testMusicBrainzServer stands a local server in for musicbrainz.org and points
// the client at it. No request leaves the machine.
func testMusicBrainzServer(t *testing.T, handler http.Handler) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	old := musicbrainzBaseURL
	musicbrainzBaseURL = server.URL
	t.Cleanup(func() { musicbrainzBaseURL = old })
}

// The search answer for a clean "Never Gonna Give You Up" hit: right title,
// right artist, one album release group.
const mbSearchOne = `{
	"recordings": [{
		"id": "rec-1",
		"title": "Never Gonna Give You Up",
		"length": 213000,
		"video": false,
		"artist-credit": [{
			"name": "Rick Astley",
			"artist": {"id": "artist-1", "name": "Rick Astley"}
		}],
		"releases": [{
			"release-group": {"id": "rg-1", "primary-type": "Album", "secondary-types": []}
		}]
	}]
}`

// A mux that answers the three calls a successful lookup makes.
func musicBrainzMux(t *testing.T, search string) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/ws/2/recording", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != musicbrainzUserAgent {
			t.Errorf("User-Agent %q, want the project introduction", got)
		}
		if got := r.URL.Query().Get("fmt"); got != "json" {
			t.Errorf("fmt %q, want json", got)
		}
		_, _ = w.Write([]byte(search))
	})

	mux.HandleFunc("/ws/2/release-group/rg-1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"genres":[
			{"name":"dance-pop","count":3},
			{"name":"new wave","count":1}
		]}`))
	})

	mux.HandleFunc("/ws/2/artist/artist-1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"genres":[{"name":"pop","count":5}]}`))
	})

	return mux
}

func TestFindGenresBringsBackReleaseGroupGenres(t *testing.T) {
	fastMusicBrainzLimiter(t)
	db := testCacheDB(t)
	testMusicBrainzServer(t, musicBrainzMux(t, mbSearchOne))

	track := Track{
		ID:       "dQw4w9WgXcQ",
		Song:     "Never Gonna Give You Up",
		Artist:   "Rick Astley",
		Duration: 213 * time.Second,
	}

	got, err := findGenres(context.TODO(), db, track)
	if err != nil {
		t.Fatalf("find genres: %v", err)
	}

	// Higher count first, as mergeGenres promises.
	if want := "dance-pop,new wave"; strings.Join(got, ",") != want {
		t.Errorf("got %v, want %q", got, want)
	}
}

func TestFindGenresFallsBackToTheArtist(t *testing.T) {
	fastMusicBrainzLimiter(t)
	db := testCacheDB(t)

	// Same recording, but its release group carries no genres.
	search := strings.Replace(mbSearchOne, `"releases"`, `"releases"`, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/2/recording", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(search))
	})
	mux.HandleFunc("/ws/2/release-group/rg-1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"genres":[]}`))
	})
	mux.HandleFunc("/ws/2/artist/artist-1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"genres":[{"name":"pop","count":5}]}`))
	})
	testMusicBrainzServer(t, mux)

	track := Track{ID: "x", Song: "Never Gonna Give You Up", Artist: "Rick Astley", Duration: 213 * time.Second}

	got, err := findGenres(context.TODO(), db, track)
	if err != nil || len(got) != 1 || got[0] != "pop" {
		t.Errorf("got %v err %v, want the artist's pop", got, err)
	}
}

func TestFindGenresTreatsNoRecordingsAsAnAnswer(t *testing.T) {
	fastMusicBrainzLimiter(t)
	db := testCacheDB(t)
	testMusicBrainzServer(t, musicBrainzMux(t, `{"recordings":[]}`))

	track := Track{ID: "no-hit", Song: "Some Song", Artist: "Somebody"}

	got, err := findGenres(context.TODO(), db, track)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v err %v, want nothing and no error", got, err)
	}

	// Empty is an answer, so it must be remembered rather than re-asked.
	if _, ok := cachedGenres(db, lyricsKey(track)); !ok {
		t.Error("empty answer was not cached")
	}
}

func TestFindGenresIgnoresAMismatchedTitle(t *testing.T) {
	fastMusicBrainzLimiter(t)
	db := testCacheDB(t)

	// The search is fuzzy and offered a live version. Same artist, same length,
	// but the title is not the title.
	search := `{"recordings":[{
		"id":"rec-2",
		"title":"Never Gonna Give You Up (Live)",
		"length":213000,
		"artist-credit":[{"name":"Rick Astley","artist":{"id":"artist-1","name":"Rick Astley"}}],
		"releases":[{"release-group":{"id":"rg-1","primary-type":"Album"}}]
	}]}`
	testMusicBrainzServer(t, musicBrainzMux(t, search))

	track := Track{ID: "x", Song: "Never Gonna Give You Up", Artist: "Rick Astley"}

	got, err := findGenres(context.TODO(), db, track)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v err %v, want the live version refused", got, err)
	}
}

func TestFindGenresRetriesA503AndCachesNothing(t *testing.T) {
	fastMusicBrainzLimiter(t)
	db := testCacheDB(t)

	var calls int32
	testMusicBrainzServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	track := Track{ID: "busy", Song: "Some Song", Artist: "Somebody"}

	got, err := findGenres(context.TODO(), db, track)
	if err == nil {
		t.Fatalf("got %v, want the 503 carried back as an error", got)
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Errorf("%d requests, want 3 (one try, two retries)", n)
	}
	if _, ok := cachedGenres(db, lyricsKey(track)); ok {
		t.Error("a 503 was cached as if it were an answer")
	}
}

func TestFindGenresUsesTheCacheWithoutNetwork(t *testing.T) {
	fastMusicBrainzLimiter(t)
	db := testCacheDB(t)

	var calls int32
	testMusicBrainzServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))

	track := Track{ID: "cached", Song: "Anything", Artist: "Anyone"}
	storeGenres(db, track, []string{"pop"})

	got, err := findGenres(context.TODO(), db, track)
	if err != nil || len(got) != 1 || got[0] != "pop" {
		t.Errorf("got %v err %v, want the cached pop", got, err)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("%d network calls, want none", n)
	}
}

func TestPruneSearchCacheKeepsGenres(t *testing.T) {
	// Genres share the search cache's table but never go stale, exactly like
	// lyrics.
	db := testCacheDB(t)
	track := Track{ID: "keep-genre"}

	storeGenres(db, track, []string{"pop"})
	if _, err := db.Exec(
		"UPDATE search_cache SET fetched_at = ? WHERE query = ?",
		time.Now().Add(-365*24*time.Hour).Unix(), genreCachePrefix+lyricsKey(track),
	); err != nil {
		t.Fatalf("age genre row: %v", err)
	}

	pruneSearchCache(db)

	if _, ok := cachedGenres(db, lyricsKey(track)); !ok {
		t.Error("pruning took the genres with it")
	}
}

func TestMergeGenresDedupesAndCaps(t *testing.T) {
	// "Pop" and "pop" are the same genre; counts add up; only three survive.
	got := mergeGenres(
		[]mbGenre{{Name: "Pop", Count: 1}, {Name: "rock", Count: 4}},
		[]mbGenre{{Name: "POP", Count: 5}, {Name: "disco", Count: 3}, {Name: "soul", Count: 2}},
	)

	if want := "pop,rock,disco"; strings.Join(got, ",") != want {
		t.Errorf("got %v, want %q", got, want)
	}
}
