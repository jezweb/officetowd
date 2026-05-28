// Package manifest tracks the last-known state of every synced path in a
// local SQLite file at ~/.officetowd/state.db. Each row pairs a path with
// the hash + mtime + size we last saw locally AND the etag + last-modified
// we last saw remotely. The bisync algorithm uses these to detect changes
// on either side and to recognise "we already wrote this".
package manifest

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Entry is one row of the manifest. Path is relative to the configured
// LocalDir (and to the bucket prefix on the remote side).
type Entry struct {
	Path           string
	LocalHash      string // sha256 hex of local content at last sync
	LocalMtime     time.Time
	LocalSize      int64
	RemoteETag     string // R2 ETag at last sync (md5 for unmultipart, opaque for multipart)
	RemoteModified time.Time
	RemoteSize     int64
	LastSyncedAt   time.Time
}

// DB wraps a *sql.DB with the manifest schema applied.
type DB struct {
	db *sql.DB
}

// DefaultPath returns ~/.officetowd/state.db.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".officetowd", "state.db"), nil
}

// Open creates the manifest file if missing and applies the schema.
// Idempotent — safe to call repeatedly.
func Open(path string) (*DB, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir parent: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_journal=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	m := &DB{db: db}
	if err := m.applySchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return m, nil
}

// Close closes the underlying SQLite handle.
func (m *DB) Close() error {
	return m.db.Close()
}

func (m *DB) applySchema() error {
	_, err := m.db.Exec(`
CREATE TABLE IF NOT EXISTS manifest (
  path             TEXT PRIMARY KEY,
  local_hash       TEXT NOT NULL DEFAULT '',
  local_mtime      INTEGER NOT NULL DEFAULT 0,
  local_size       INTEGER NOT NULL DEFAULT 0,
  remote_etag      TEXT NOT NULL DEFAULT '',
  remote_modified  INTEGER NOT NULL DEFAULT 0,
  remote_size      INTEGER NOT NULL DEFAULT 0,
  last_synced_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS manifest_last_synced_idx ON manifest(last_synced_at);
`)
	return err
}

// Get returns the entry for a path, or (nil, nil) if missing.
func (m *DB) Get(path string) (*Entry, error) {
	row := m.db.QueryRow(`
SELECT path, local_hash, local_mtime, local_size, remote_etag, remote_modified, remote_size, last_synced_at
FROM manifest WHERE path = ?`, path)
	var e Entry
	var localMtime, remoteMod, syncedAt int64
	err := row.Scan(&e.Path, &e.LocalHash, &localMtime, &e.LocalSize, &e.RemoteETag, &remoteMod, &e.RemoteSize, &syncedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query manifest: %w", err)
	}
	e.LocalMtime = time.Unix(localMtime, 0)
	e.RemoteModified = time.Unix(remoteMod, 0)
	e.LastSyncedAt = time.Unix(syncedAt, 0)
	return &e, nil
}

// Put inserts or updates an entry.
func (m *DB) Put(e *Entry) error {
	if e.LastSyncedAt.IsZero() {
		e.LastSyncedAt = time.Now()
	}
	_, err := m.db.Exec(`
INSERT INTO manifest (path, local_hash, local_mtime, local_size, remote_etag, remote_modified, remote_size, last_synced_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET
  local_hash = excluded.local_hash,
  local_mtime = excluded.local_mtime,
  local_size = excluded.local_size,
  remote_etag = excluded.remote_etag,
  remote_modified = excluded.remote_modified,
  remote_size = excluded.remote_size,
  last_synced_at = excluded.last_synced_at
`,
		e.Path, e.LocalHash, e.LocalMtime.Unix(), e.LocalSize,
		e.RemoteETag, e.RemoteModified.Unix(), e.RemoteSize, e.LastSyncedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert manifest: %w", err)
	}
	return nil
}

// Delete removes an entry. Called when a path is gone from both sides.
func (m *DB) Delete(path string) error {
	_, err := m.db.Exec(`DELETE FROM manifest WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("delete manifest: %w", err)
	}
	return nil
}

// List returns all manifest entries, optionally filtered to paths under
// a prefix. Useful for the periodic sweep that compares against fresh
// directory walks.
func (m *DB) List(prefix string) ([]*Entry, error) {
	var rows *sql.Rows
	var err error
	if prefix == "" {
		rows, err = m.db.Query(`
SELECT path, local_hash, local_mtime, local_size, remote_etag, remote_modified, remote_size, last_synced_at
FROM manifest ORDER BY path`)
	} else {
		rows, err = m.db.Query(`
SELECT path, local_hash, local_mtime, local_size, remote_etag, remote_modified, remote_size, last_synced_at
FROM manifest WHERE path LIKE ? || '%' ORDER BY path`, prefix)
	}
	if err != nil {
		return nil, fmt.Errorf("list manifest: %w", err)
	}
	defer rows.Close()

	var out []*Entry
	for rows.Next() {
		var e Entry
		var localMtime, remoteMod, syncedAt int64
		if err := rows.Scan(&e.Path, &e.LocalHash, &localMtime, &e.LocalSize, &e.RemoteETag, &remoteMod, &e.RemoteSize, &syncedAt); err != nil {
			return nil, fmt.Errorf("scan manifest row: %w", err)
		}
		e.LocalMtime = time.Unix(localMtime, 0)
		e.RemoteModified = time.Unix(remoteMod, 0)
		e.LastSyncedAt = time.Unix(syncedAt, 0)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// AllPaths returns just the set of paths in the manifest — cheap lookup
// for "is this path known?". Used by the bisync engine.
func (m *DB) AllPaths() (map[string]struct{}, error) {
	rows, err := m.db.Query(`SELECT path FROM manifest`)
	if err != nil {
		return nil, fmt.Errorf("list paths: %w", err)
	}
	defer rows.Close()
	set := make(map[string]struct{})
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan path: %w", err)
		}
		set[p] = struct{}{}
	}
	return set, rows.Err()
}
