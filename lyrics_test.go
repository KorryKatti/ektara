package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseLRCReadsTimestampsAndWords(t *testing.T) {
	raw := "[00:09.14] Jeffrey\n[01:02.03] Jeffrey Bezos\n[02:10.40] Jeff Bezos"

	got := parseLRC(raw)

	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3", len(got))
	}
	if got[0].Time != 9140*time.Millisecond {
		t.Errorf("first line at %v, want 9.14s", got[0].Time)
	}
	if got[1].Time != 62030*time.Millisecond {
		t.Errorf("second line at %v, want 1:02.03", got[1].Time)
	}
	if got[2].Text != "Jeff Bezos" {
		t.Errorf("third line %q, want \"Jeff Bezos\"", got[2].Text)
	}
}

func TestParseLRCPutsLinesInTimeOrder(t *testing.T) {
	// A tidy file is in order already, but the follow-along presumes the times
	// go up, so a messy one is put right rather than taken at its word.
	raw := "[00:30.00] late\n[00:10.00] early\n[00:20.00] middle"

	got := parseLRC(raw)

	if len(got) != 3 {
		t.Fatalf("got %d lines, want 3", len(got))
	}
	for i, want := range []string{"early", "middle", "late"} {
		if got[i].Text != want {
			t.Errorf("line %d is %q, want %q", i, got[i].Text, want)
		}
	}
}

func TestParseLRCDropsTagsAndEmptyWords(t *testing.T) {
	// [ti:] and friends carry no time, and a timed line with no words highlights
	// empty space. Neither has anything to show.
	raw := "[ti:Bezos III]\n[ar:Bo Burnham]\n[00:05.00]\n\n[00:09.14] Jeffrey"

	got := parseLRC(raw)

	if len(got) != 1 || got[0].Text != "Jeffrey" {
		t.Fatalf("got %+v, want one line \"Jeffrey\"", got)
	}
}

func TestParseLRCHandlesHourCommaAndRepeatedLine(t *testing.T) {
	raw := "[01:01:00.50] long song\n[00:00,10] comma\n[00:01.00][00:05.00] twice"

	got := parseLRC(raw)

	if len(got) != 4 {
		t.Fatalf("got %d lines, want 4: %+v", len(got), got)
	}
	if got[0].Time != 100*time.Millisecond {
		t.Errorf("comma time %v, want 100ms", got[0].Time)
	}
	if got[1].Time != time.Second || got[2].Time != 5*time.Second {
		t.Errorf("repeated line at %v and %v, want 1s and 5s", got[1].Time, got[2].Time)
	}
	if got[3].Time != 3660*time.Second+500*time.Millisecond {
		t.Errorf("hour time %v, want 1:01:00.50", got[3].Time)
	}
}

func TestPickRecordTakesTheRunningLength(t *testing.T) {
	// Duration is the only thing that tells the hundred covers of a famous song
	// apart, so it is the first and hardest cut. ±2 seconds, LRCLIB's own rule.
	mine := lyricRecord{TrackName: "x", Duration: 171.0, PlainLyrics: "plain"}
	covers := []lyricRecord{
		{TrackName: "x", Duration: 237.0, SyncedLyrics: "[00:01.00] no"},
		{TrackName: "x", Duration: 222.0, SyncedLyrics: "[00:01.00] no"},
		mine,
	}

	got, ok := pickRecord(covers, "somebody", 172*time.Second)

	if !ok {
		t.Fatal("no record picked, want mine at 171s within ±2s of 172s")
	}
	if got != mine {
		t.Errorf("picked %+v, want %+v", got, mine)
	}
}

func TestPickRecordPrefersSyncedOverPlain(t *testing.T) {
	plain := lyricRecord{Duration: 171.0, PlainLyrics: "plain"}
	synced := lyricRecord{Duration: 172.0, SyncedLyrics: "[00:01.00] timed"}

	got, ok := pickRecord([]lyricRecord{plain, synced}, "", 0)

	if !ok || got != synced {
		t.Errorf("picked %+v ok=%v, want the synced record", got, ok)
	}
}

func TestPickRecordPrefersTheArtistsOwn(t *testing.T) {
	// Both are within a second, both timed. The track was already known to be
	// by Bo Burnham, so that candidate wins over the other singer's.
	other := lyricRecord{Duration: 171.0, SyncedLyrics: "[00:01.00] no", ArtistName: "Someone Else"}
	own := lyricRecord{Duration: 171.5, SyncedLyrics: "[00:01.00] yes", ArtistName: "Bo Burnham"}

	got, ok := pickRecord([]lyricRecord{other, own}, "bo burnham", 171*time.Second)

	if !ok || got != own {
		t.Errorf("picked %+v ok=%v, want the artist's own record", got, ok)
	}
}

func TestPickRecordWithoutADurationStillPicks(t *testing.T) {
	// No duration to cut by, so the timed, artist-matching one wins on the
	// remaining senses alone.
	plain := lyricRecord{Duration: 171.0, PlainLyrics: "plain"}
	synced := lyricRecord{Duration: 999.0, SyncedLyrics: "[00:01.00] timed", ArtistName: "Bo Burnham"}

	got, ok := pickRecord([]lyricRecord{plain, synced}, "bo burnham", 0)

	if !ok || got != synced {
		t.Errorf("picked %+v ok=%v, want the synced record", got, ok)
	}
}

func TestPickRecordSaysNothingWhenNothingMatches(t *testing.T) {
	// Every candidate runs far longer than the track does. Nothing to pick. An
	// empty answer is a real answer, and the screen says so.
	got, ok := pickRecord([]lyricRecord{{Duration: 999.0}}, "artist", 171*time.Second)

	if ok || got != (lyricRecord{}) {
		t.Errorf("picked %+v ok=%v, want no record", got, ok)
	}
}

func TestPickRecordKeepsAnInstrumental(t *testing.T) {
	// A track with no words is an answer, not a miss, so it is picked like any
	// other record and the flag is carried along.
	instrumental := lyricRecord{Duration: 171.0, Instrumental: true, ArtistName: "Bird"}

	got, ok := pickRecord([]lyricRecord{instrumental}, "bird", 171*time.Second)

	if !ok || !got.Instrumental {
		t.Errorf("picked %+v ok=%v, want the instrumental record", got, ok)
	}
}

func TestLyricQueryFromTrackPrefersTheSongName(t *testing.T) {
	// The song name is the search key; the video title is only what to fall
	// back to when the song was never learned.
	track := Track{Title: "youtube - Never Gonna Give You Up", Song: "Never Gonna Give You Up",
		Artist: "Rick Astley", Duration: 213 * time.Second}

	got := lyricQueryFromTrack(track)

	if got.Song != "Never Gonna Give You Up" || got.Artist != "Rick Astley" {
		t.Errorf("got %+v, want the song and the artist", got)
	}
	if got.Duration != 213*time.Second {
		t.Errorf("duration %v, want 213s", got.Duration)
	}

	track.Song = ""
	if got := lyricQueryFromTrack(track); got.Song != "youtube - Never Gonna Give You Up" {
		t.Errorf("got %+v, want the title as the fallback", got)
	}
}

func TestLyricRecordAsLyricsKeepsWhatItShould(t *testing.T) {
	timed := lyricRecord{SyncedLyrics: "[00:01.00] one\n[00:02.00] two"}
	got := timed.asLyrics()
	if len(got.Synced) != 2 || got.Plain != "" {
		t.Errorf("timed came out %+v, want two synced lines", got)
	}

	plain := lyricRecord{PlainLyrics: "just words"}
	got = plain.asLyrics()
	if len(got.Synced) != 0 || got.Plain != "just words" {
		t.Errorf("plain came out %+v, want the plain block", got)
	}

	// An instrumental is an answer, not a miss, and it says so.
	got = lyricRecord{Instrumental: true}.asLyrics()
	if !got.Instrumental || got.Plain != "" || len(got.Synced) != 0 {
		t.Errorf("instrumental came out %+v, want just the flag", got)
	}
}

// fastLyricsLimiter swaps the shared limiter for one that never blocks, so the
// tests do not spend four hundred milliseconds each on lrclib's pacing.
func fastLyricsLimiter(t *testing.T) {
	t.Helper()
	old := lyricsLimiter
	lyricsLimiter = newBurstLimiter(1000, time.Millisecond)
	t.Cleanup(func() { lyricsLimiter = old })
}

// testLyricsServer stands a local server in for lrclib.net and points the
// client at it. No network leaves the machine.
func testLyricsServer(t *testing.T, body string, status int) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			t.Errorf("asked for %q, want /api/search", r.URL.Path)
		}
		if r.Header.Get("User-Agent") != lyricsUserAgent {
			t.Errorf("User-Agent %q, want to introduce ourselves", r.Header.Get("User-Agent"))
		}
		if got := r.URL.Query().Get("track_name"); got != "Never Gonna Give You Up" {
			t.Errorf("track_name %q, want the song", got)
		}
		if got := r.URL.Query().Get("duration"); got != "213" {
			t.Errorf("duration %q, want whole seconds", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	old := lyricsBaseURL
	lyricsBaseURL = server.URL
	t.Cleanup(func() { lyricsBaseURL = old })
}

func TestSearchLyricsReadsTheAnswer(t *testing.T) {
	fastLyricsLimiter(t)
	const answer = `[{"id":1,"trackName":"Never Gonna Give You Up","artistName":"Rick Astley",
		"duration":213.0,"syncedLyrics":"[00:10.00] Never gonna give you up","plainLyrics":"Never gonna give you up"}]`
	testLyricsServer(t, answer, http.StatusOK)

	got, err := searchLyrics(context.TODO(), lyricQuery{
		Song:     "Never Gonna Give You Up",
		Duration: 213 * time.Second,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 || got[0].ArtistName != "Rick Astley" {
		t.Errorf("got %+v, want one record by its artist", got)
	}
}

func TestSearchLyricsSaysAnEmptyAnswerIsAnAnswer(t *testing.T) {
	fastLyricsLimiter(t)
	testLyricsServer(t, "[]", http.StatusOK)

	got, err := searchLyrics(context.TODO(), lyricQuery{Song: "Never Gonna Give You Up", Duration: 213 * time.Second})

	// Empty, but not an error: there being no lyrics is a real state, not a
	// failed lookup.
	if err != nil || len(got) != 0 {
		t.Errorf("got %+v err %v, want an empty answer with no error", got, err)
	}
}

func TestSearchLyricsKeepsARefusalAnError(t *testing.T) {
	fastLyricsLimiter(t)
	testLyricsServer(t, `{"name":"ServerOverloaded"}`, http.StatusServiceUnavailable)

	_, err := searchLyrics(context.TODO(), lyricQuery{Song: "Never Gonna Give You Up", Duration: 213 * time.Second})

	// The caller decides whether to retry; what must not happen is the refusal
	// being mistaken for an empty answer.
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("err %v, want the server refusal carried back", err)
	}
}

func TestLyricsCacheRoundTripsTimedLines(t *testing.T) {
	db := testCacheDB(t)
	track := Track{ID: "dQw4w9WgXcQ", Song: "Never Gonna Give You Up", Artist: "Rick Astley", Duration: 213 * time.Second}
	want := lyrics{Synced: []lyricLine{{Time: 10 * time.Second, Text: "Never gonna give you up"}}}

	storeLyrics(db, track, want)
	got, ok := cachedLyrics(db, track.ID)

	if !ok {
		t.Fatal("nothing remembered, want the answer back")
	}
	if len(got.Synced) != 1 || got.Synced[0] != want.Synced[0] || got.Plain != "" || got.Instrumental {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestLyricsCacheRoundTripsPlainAndInstrumental(t *testing.T) {
	db := testCacheDB(t)
	plainTrack := Track{ID: "aaaaaaaaaaa"}
	instrumentalTrack := Track{ID: "bbbbbbbbbbb"}

	storeLyrics(db, plainTrack, lyrics{Plain: "just words"})
	storeLyrics(db, instrumentalTrack, lyrics{Instrumental: true})

	plain, ok := cachedLyrics(db, plainTrack.ID)
	if !ok || plain.Plain != "just words" || len(plain.Synced) != 0 {
		t.Errorf("plain came back %+v ok=%v", plain, ok)
	}
	instrumental, ok := cachedLyrics(db, instrumentalTrack.ID)
	if !ok || !instrumental.Instrumental {
		t.Errorf("instrumental came back %+v ok=%v", instrumental, ok)
	}
}

func TestLyricsCacheRemembersAnEmptyAnswer(t *testing.T) {
	// An empty list is a real answer: there are no lyrics, and we have looked.
	// It must not read as a miss, or the same song would be asked about again
	// and again.
	db := testCacheDB(t)
	track := Track{ID: "ccccccccccc"}

	storeLyrics(db, track, lyrics{})

	got, ok := cachedLyrics(db, track.ID)
	if !ok || got.Plain != "" || len(got.Synced) != 0 || got.Instrumental {
		t.Errorf("got %+v ok=%v, want an empty answer that is remembered", got, ok)
	}
}

func TestLyricsCacheMissesAnUnknownTrack(t *testing.T) {
	db := testCacheDB(t)

	if _, ok := cachedLyrics(db, "ddddddddddd"); ok {
		t.Error("found an answer for a track that was never looked up")
	}
}

func TestLyricsCacheIgnoresATrackWithNoID(t *testing.T) {
	// A hand named local file has no video id to key on, so there is nowhere to
	// put its lyrics.
	db := testCacheDB(t)

	storeLyrics(db, Track{Filename: "song.mp3"}, lyrics{Plain: "words"})

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM search_cache").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("%d rows stored, want none", count)
	}
}

func TestPruneSearchCacheKeepsLyrics(t *testing.T) {
	// Lyrics share the search cache's table but never go stale, so pruning old
	// searches must not take them with it.
	db := testCacheDB(t)
	track := Track{ID: "eeeeeeeeeee"}

	storeLyrics(db, track, lyrics{Plain: "words"})
	if _, err := db.Exec(
		"UPDATE search_cache SET fetched_at = ? WHERE query = ?",
		time.Now().Add(-365*24*time.Hour).Unix(), lyricsCachePrefix+track.ID,
	); err != nil {
		t.Fatalf("age lyrics row: %v", err)
	}

	storeSearch(db, "old search", []video{{ID: "x", Title: "x"}})
	if _, err := db.Exec(
		"UPDATE search_cache SET fetched_at = ? WHERE query = ?",
		time.Now().Add(-365*24*time.Hour).Unix(), normaliseQuery("old search"),
	); err != nil {
		t.Fatalf("age search row: %v", err)
	}

	pruneSearchCache(db)

	if _, ok := cachedLyrics(db, track.ID); !ok {
		t.Error("pruning took the lyrics with it")
	}
	if _, fresh := cachedSearch(db, "old search"); fresh {
		t.Error("pruning kept the ancient search")
	}
}
