package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// schemaVersion invalidates incompatible cache formats on bump.
const schemaVersion = 1

// snapshotStore persists scan results (directory sizes + top files) to a
// SQLite database so a re-launch can restore the previous scan instantly.
type snapshotStore struct {
	db *sql.DB
}

func openSnapshotStore(path string) (*snapshotStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	_, _ = db.Exec("PRAGMA journal_mode=WAL")
	_, _ = db.Exec("PRAGMA synchronous=NORMAL")
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS dir_sizes (
			root       TEXT NOT NULL,
			path       TEXT NOT NULL,
			size       INTEGER NOT NULL,
			file_count INTEGER NOT NULL,
			PRIMARY KEY (root, path)
		);
		CREATE TABLE IF NOT EXISTS top_files (
			root     TEXT NOT NULL,
			path     TEXT NOT NULL,
			size     INTEGER NOT NULL,
			mod_time INTEGER NOT NULL,
			PRIMARY KEY (root, path)
		);
		CREATE TABLE IF NOT EXISTS scan_meta (
			root       TEXT PRIMARY KEY,
			scanned_at INTEGER NOT NULL,
			version    INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS clean_history (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			time      INTEGER NOT NULL,
			item_id   TEXT NOT NULL,
			item_name TEXT NOT NULL,
			path      TEXT NOT NULL,
			size      INTEGER NOT NULL,
			ok        INTEGER NOT NULL,
			error     TEXT NOT NULL DEFAULT '',
			restored  INTEGER NOT NULL DEFAULT 0
		);
	`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &snapshotStore{db: db}, nil
}

func (s *snapshotStore) close() {
	if s != nil && s.db != nil {
		s.db.Close()
	}
}

// load restores a snapshot for root. Returns ok=false when no usable snapshot
// exists (or the schema version changed).
func (s *snapshotStore) load(root string) (sizes map[string]dirInfo, top []TopFileEntry, scannedAt time.Time, ok bool, err error) {
	var at int64
	var version int
	err = s.db.QueryRow(`SELECT scanned_at, version FROM scan_meta WHERE root = ?`, root).
		Scan(&at, &version)
	if err == sql.ErrNoRows {
		return nil, nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, nil, time.Time{}, false, err
	}
	if version != schemaVersion {
		return nil, nil, time.Time{}, false, nil
	}

	sizes = map[string]dirInfo{}
	rows, err := s.db.Query(`SELECT path, size, file_count FROM dir_sizes WHERE root = ?`, root)
	if err != nil {
		return nil, nil, time.Time{}, false, err
	}
	for rows.Next() {
		var p string
		var size int64
		var fc int
		if err := rows.Scan(&p, &size, &fc); err != nil {
			rows.Close()
			return nil, nil, time.Time{}, false, err
		}
		sizes[p] = dirInfo{size: size, fileCount: fc}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, time.Time{}, false, err
	}

	top = []TopFileEntry{}
	rows, err = s.db.Query(
		`SELECT path, size, mod_time FROM top_files WHERE root = ? ORDER BY size DESC LIMIT 500`, root)
	if err != nil {
		return nil, nil, time.Time{}, false, err
	}
	for rows.Next() {
		var p string
		var size int64
		var mt int64
		if err := rows.Scan(&p, &size, &mt); err != nil {
			rows.Close()
			return nil, nil, time.Time{}, false, err
		}
		top = append(top, TopFileEntry{Path: p, Size: size, ModTime: mt})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, time.Time{}, false, err
	}

	return sizes, top, time.Unix(at, 0), true, nil
}

// save replaces the snapshot for root in a single transaction.
func (s *snapshotStore) save(root string, sizes map[string]dirInfo, top []TopFileEntry, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := tx.Exec(`DELETE FROM dir_sizes WHERE root = ?`, root); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM top_files WHERE root = ?`, root); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM scan_meta WHERE root = ?`, root); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`INSERT INTO dir_sizes (root, path, size, file_count) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	for p, di := range sizes {
		if _, err := stmt.Exec(root, p, di.size, di.fileCount); err != nil {
			stmt.Close()
			return err
		}
	}
	stmt.Close()

	stmt, err = tx.Prepare(`INSERT INTO top_files (root, path, size, mod_time) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	for _, f := range top {
		if _, err := stmt.Exec(root, f.Path, f.Size, f.ModTime); err != nil {
			stmt.Close()
			return err
		}
	}
	stmt.Close()

	_, err = tx.Exec(`INSERT INTO scan_meta (root, scanned_at, version) VALUES (?, ?, ?)`,
		root, at.Unix(), schemaVersion)
	if err != nil {
		return err
	}
	return tx.Commit()
}
