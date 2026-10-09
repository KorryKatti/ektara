package main

// Search results are remembered, so searching twice does not ask YouTube twice
// and so the program still works when YouTube says no.
//
// That second part is the point. The failures guarded against are not random:
// they come back for hours, and they are the difference between a working player
// and one that cannot play anything. A result found once is good enough to keep
// using for a long time, because the top three videos for a song name do not
// change from one day to the next. So the cache is generous on purpose: recent
// results are used without asking, older ones when asking has failed.

import (
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// How long a cached answer is used without asking YouTube whether it is still
// right. A week is long enough that a song searched twice in a session never
// reaches the network twice.
const searchCacheFreshFor = 7 * 24 * time.Hour

// How long an answer is kept before being thrown away. Much longer than the fresh
// window on purpose: an old answer is no good on its own, but it is what falls
// back when YouTube is blocking.
const searchCacheKeptFor = 90 * 24 * time.Hour

// Adds the table the answers live in. Separate from openDB's other work because
// nothing else needs it, and a database written by an older version has no such
// table.
func initSearchCache(db *sql.DB) error {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS search_cache(
		query TEXT PRIMARY KEY,
		results TEXT NOT NULL,
		fetched_at INTEGER NOT NULL
	)
	`)
	return err
}

// Makes the same search always use the same key, so "Hello", " hello " and
// "HELLO" share one entry instead of three.
func normaliseQuery(query string) string {
	return strings.ToLower(strings.TrimSpace(query))
}

// The remembered answer for a query, and whether it was fresh enough to use
// without asking YouTube again.
//
// An error is deliberately not returned: a cache that cannot be read is not worth
// failing a search over, because the search itself may well succeed, and a missing
// cache only costs the request it was meant to save.
func cachedSearch(db *sql.DB, query string) (videos []video, fresh bool) {
	var (
		results   string
		fetchedAt int64
	)
	err := db.QueryRow(
		"SELECT results, fetched_at FROM search_cache WHERE query = ?",
		normaliseQuery(query),
	).Scan(&results, &fetchedAt)
	if err != nil {
		return nil, false
	}

	if err := json.Unmarshal([]byte(results), &videos); err != nil {
		return nil, false
	}

	return videos, time.Since(time.Unix(fetchedAt, 0)) < searchCacheFreshFor
}

// Remembers an answer. Errors are logged and otherwise ignored: a search that
// worked should not be reported as failed because it could not be written down.
func storeSearch(db *sql.DB, query string, videos []video) {
	results, err := json.Marshal(videos)
	if err != nil {
		log.Printf("cache search %q: %v", query, err)
		return
	}

	_, err = db.Exec(`
	INSERT INTO search_cache (query, results, fetched_at) VALUES (?,?,?)
	ON CONFLICT(query) DO UPDATE SET
		results = excluded.results,
		fetched_at = excluded.fetched_at
	`,
		normaliseQuery(query), string(results), time.Now().Unix(),
	)
	if err != nil {
		log.Printf("cache search %q: %v", query, err)
	}
}

// Throws away answers too old to be any use. Called after a search is stored rather
// than on a timer: there is no need to be exact, and no reason to keep a table
// growing forever.
func pruneSearchCache(db *sql.DB) {
	cutoff := time.Now().Add(-searchCacheKeptFor).Unix()
	if _, err := db.Exec("DELETE FROM search_cache WHERE fetched_at < ?", cutoff); err != nil {
		log.Printf("prune search cache: %v", err)
	}
}
