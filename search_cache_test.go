package main

import (
	"database/sql"
	"testing"
	"time"
)

// testCacheDB is a database with only the search cache table in it.
func testCacheDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory database: %v", err)
	}
	// One connection, because every connection to :memory: is a different
	// database, and the table would only exist on whichever one made it first.
	db.SetMaxOpenConns(1)

	if err := initSearchCache(db); err != nil {
		t.Fatalf("create search cache: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return db
}

// ageCacheRow rewrites when an answer was fetched, so a test can make an entry
// look older than it is without waiting for it to get that way.
func ageCacheRow(t *testing.T, db *sql.DB, query string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age).Unix()
	if _, err := db.Exec(
		"UPDATE search_cache SET fetched_at = ? WHERE query = ?", when, normaliseQuery(query),
	); err != nil {
		t.Fatalf("age cache row: %v", err)
	}
}

func TestSearchCacheRoundTrip(t *testing.T) {
	db := testCacheDB(t)

	want := []video{
		{ID: "abc123", Title: "One"},
		{ID: "def456", Title: "Two"},
	}
	storeSearch(db, "hello", want)

	got, fresh := cachedSearch(db, "hello")
	if !fresh {
		t.Error("a result just stored should be fresh")
	}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("result %d is %+v, want %+v", i, got[i], v)
		}
	}
}

func TestSearchCacheIgnoresCaseAndSpacing(t *testing.T) {
	db := testCacheDB(t)

	storeSearch(db, "Hello", []video{{ID: "abc123", Title: "One"}})

	// The same search typed with different capitals or a stray space has to hit
	// the same entry, or the cache fills with near-duplicates of one search.
	got, fresh := cachedSearch(db, "  HELLO  ")
	if !fresh {
		t.Fatal("a differently typed copy of the search should still be fresh")
	}
	if len(got) != 1 || got[0].ID != "abc123" {
		t.Fatalf("got %+v, want the result stored under Hello", got)
	}
}

func TestSearchCacheStaleResultIsStillReturned(t *testing.T) {
	db := testCacheDB(t)

	storeSearch(db, "hello", []video{{ID: "abc123", Title: "One"}})
	ageCacheRow(t, db, "hello", searchCacheFreshFor+time.Hour)

	got, fresh := cachedSearch(db, "hello")

	// This is the case that matters. An old answer is no good on its own, but
	// it is what gets served when YouTube is refusing, so it has to still be
	// there.
	if fresh {
		t.Error("a result older than the fresh window should not be fresh")
	}
	if len(got) != 1 || got[0].ID != "abc123" {
		t.Fatalf("got %+v, want the stale result to still be returned", got)
	}
}

func TestSearchCacheMissIsNotAnError(t *testing.T) {
	db := testCacheDB(t)

	got, fresh := cachedSearch(db, "never searched")
	if fresh {
		t.Error("an absent result should not be fresh")
	}
	if got != nil {
		t.Errorf("got %+v, want nothing for a search never made", got)
	}
}

func TestSearchCacheOverwritesAnEarlierAnswer(t *testing.T) {
	db := testCacheDB(t)

	storeSearch(db, "hello", []video{{ID: "first", Title: "First"}})
	storeSearch(db, "hello", []video{{ID: "second", Title: "Second"}})

	got, _ := cachedSearch(db, "hello")
	if len(got) != 1 || got[0].ID != "second" {
		t.Fatalf("got %+v, want the newer answer to have replaced the old one", got)
	}
}

func TestPruneSearchCacheKeepsRecentAndDropsAncient(t *testing.T) {
	db := testCacheDB(t)

	storeSearch(db, "recent", []video{{ID: "a", Title: "A"}})
	storeSearch(db, "ancient", []video{{ID: "b", Title: "B"}})
	ageCacheRow(t, db, "ancient", searchCacheKeptFor+time.Hour)

	pruneSearchCache(db)

	if _, fresh := cachedSearch(db, "recent"); !fresh {
		t.Error("pruning threw away a recent answer")
	}
	// The ancient one is too old to fall back on, so it is the one that goes.
	if got, _ := cachedSearch(db, "ancient"); got != nil {
		t.Errorf("got %+v, want the ancient answer pruned", got)
	}
}
