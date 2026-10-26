// Package store persists stories and feed caching headers in SQLite.
package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"nuevo-rss/internal/cluster"
)

type Store struct {
	db *sql.DB
}

type FeedState struct {
	ETag         string
	LastModified string
}

const schema = `
CREATE TABLE IF NOT EXISTS stories (
	id         TEXT PRIMARY KEY,
	feed_id    TEXT NOT NULL,
	publisher  TEXT NOT NULL,
	source     TEXT NOT NULL,
	categories TEXT NOT NULL,
	title      TEXT NOT NULL,
	link       TEXT NOT NULL,
	summary    TEXT NOT NULL,
	published  INTEGER NOT NULL,
	seen       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS stories_published ON stories(published);
CREATE TABLE IF NOT EXISTS feed_state (
	feed_id       TEXT PRIMARY KEY,
	etag          TEXT NOT NULL,
	last_modified TEXT NOT NULL
);`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite has one writer; one connection avoids lock contention
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// Migrations for databases created by older versions.
	for _, m := range []string{`ALTER TABLE stories ADD COLUMN guessed INTEGER NOT NULL DEFAULT 0`} {
		if _, err := db.Exec(m); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SaveStories(ctx context.Context, stories []cluster.Story) error {
	if len(stories) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO stories (id, feed_id, publisher, source, categories, title, link, summary, published, seen, guessed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET categories = excluded.categories, summary = excluded.summary, guessed = excluded.guessed`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, st := range stories {
		if _, err := stmt.ExecContext(ctx, st.ID, st.FeedID, st.Publisher, st.Source, strings.Join(st.Categories, ","),
			st.Title, st.Link, st.Summary, st.Published.UnixMilli(), st.Seen.UnixMilli(), st.Guessed); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Known returns which of ids are already stored.
func (s *Store) Known(ctx context.Context, ids []string) (map[string]bool, error) {
	known := make(map[string]bool)
	for start := 0; start < len(ids); start += 500 {
		chunk := ids[start:min(start+500, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		q := "SELECT id FROM stories WHERE id IN (?" + strings.Repeat(",?", len(chunk)-1) + ")"
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			known[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return known, nil
}

// LoadSince returns stories published after t, in the order they were first seen.
func (s *Store) LoadSince(ctx context.Context, t time.Time) ([]*cluster.Story, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, feed_id, publisher, source, categories, title, link, summary, published, seen, guessed
		FROM stories WHERE published >= ? ORDER BY seen, published`, t.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*cluster.Story
	for rows.Next() {
		var st cluster.Story
		var cats string
		var pub, seen int64
		if err := rows.Scan(&st.ID, &st.FeedID, &st.Publisher, &st.Source, &cats, &st.Title, &st.Link, &st.Summary, &pub, &seen, &st.Guessed); err != nil {
			return nil, err
		}
		st.Categories = strings.Split(cats, ",")
		st.Published = time.UnixMilli(pub)
		st.Seen = time.UnixMilli(seen)
		out = append(out, &st)
	}
	return out, rows.Err()
}

func (s *Store) FeedStates(ctx context.Context) (map[string]FeedState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT feed_id, etag, last_modified FROM feed_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]FeedState)
	for rows.Next() {
		var id string
		var fs FeedState
		if err := rows.Scan(&id, &fs.ETag, &fs.LastModified); err != nil {
			return nil, err
		}
		out[id] = fs
	}
	return out, rows.Err()
}

func (s *Store) SaveFeedState(ctx context.Context, feedID string, fs FeedState) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO feed_state (feed_id, etag, last_modified) VALUES (?, ?, ?)
		ON CONFLICT(feed_id) DO UPDATE SET etag = excluded.etag, last_modified = excluded.last_modified`,
		feedID, fs.ETag, fs.LastModified)
	return err
}

// Prune deletes stories published before t.
func (s *Store) Prune(ctx context.Context, t time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM stories WHERE published < ?`, t.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
