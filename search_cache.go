package main

// Search results are remembered, so searching twice does not ask YouTube twice
// and so the program still works when YouTube says no.
//
// The cache is generous on purpose: recent results are used without asking, older
// ones when asking has failed, because a block lasts for hours and an old answer
// beats no answer.

import (
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// How long a cached answer is used without asking YouTube whether it is still
// right.
const searchCacheFreshFor = 7 * 24 * time.Hour

// How long an answer is kept before being thrown away. Much longer than the fresh
// window: an old answer is what falls back when YouTube is blocking.
const searchCacheKeptFor = 90 * 24 * time.Hour

// Adds the table the answers live in.
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

// Makes the same search always use the same key.
func normaliseQuery(query string) string {
	return strings.ToLower(strings.TrimSpace(query))
}

// The remembered answer for a query, and whether it was fresh enough to use
// without asking YouTube again. A cache that cannot be read is not worth failing
// a search over.
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

// Throws away answers too old to be any use.
func pruneSearchCache(db *sql.DB) {
	cutoff := time.Now().Add(-searchCacheKeptFor).Unix()
	if _, err := db.Exec("DELETE FROM search_cache WHERE fetched_at < ?", cutoff); err != nil {
		log.Printf("prune search cache: %v", err)
	}
}
