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
		CREATE TABLE IF NOT EXISTS disk_snapshots (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			drive      TEXT NOT NULL,
			day        TEXT NOT NULL,
			total      INTEGER NOT NULL,
			free       INTEGER NOT NULL,
			used       INTEGER NOT NULL,
			scanned_at INTEGER NOT NULL,
			UNIQUE (drive, day)
		);
		CREATE TABLE IF NOT EXISTS snapshot_dirs (
			snapshot_id INTEGER NOT NULL,
			path        TEXT NOT NULL,
			size        INTEGER NOT NULL,
			file_count  INTEGER NOT NULL,
			PRIMARY KEY (snapshot_id, path)
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

// ---------- disk trend snapshots ----------

// DiskSnapshot is one day's recorded drive usage for a drive.
type DiskSnapshot struct {
	Drive string `json:"drive"` // e.g. "C:"
	Day   string `json:"day"`   // "2026-08-12"
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
	Used  int64  `json:"used"`
}

// DirDelta is one directory's size change between two snapshots.
type DirDelta struct {
	Path      string `json:"path"`
	SizeNow   int64  `json:"sizeNow"`
	SizeThen  int64  `json:"sizeThen"`
	Delta     int64  `json:"delta"` // positive = grew (space lost), negative = shrank
	FileCount int    `json:"fileCount"`
}

// recordDiskSnapshot writes today's drive usage (one row per drive per day,
// later scans on the same day replace the earlier one). dirSizes is the
// per-directory size map of this scan, stored keyed by the snapshot row so a
// later diff can show which dirs changed.
func (s *snapshotStore) recordDiskSnapshot(drives []DriveInfo, dirSizes map[string]dirInfo, at time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	day := at.Format("2006-01-02")
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, d := range drives {
		if d.Total == 0 {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM disk_snapshots WHERE drive = ? AND day = ?`, d.Drive, day); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM snapshot_dirs WHERE snapshot_id IN (
				SELECT id FROM disk_snapshots WHERE drive = ? AND day = ?)`, d.Drive, day); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO disk_snapshots (drive, day, total, free, used, scanned_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			d.Drive, day, int64(d.Total), int64(d.Free), int64(d.Used), at.Unix())
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		// Store dir sizes under the C: snapshot (dir scans are drive-wide).
		if d.Drive == `C:` {
			stmt, err := tx.Prepare(`INSERT INTO snapshot_dirs (snapshot_id, path, size, file_count) VALUES (?, ?, ?, ?)`)
			if err != nil {
				return err
			}
			for p, di := range dirSizes {
				if _, err := stmt.Exec(id, p, di.size, di.fileCount); err != nil {
					stmt.Close()
					return err
				}
			}
			stmt.Close()
		}
	}
	return tx.Commit()
}

// diskTrend returns the per-day free-space history for a drive, newest last.
func (s *snapshotStore) diskTrend(drive string) ([]DiskSnapshot, error) {
	rows, err := s.db.Query(
		`SELECT drive, day, total, free, used FROM disk_snapshots
		 WHERE drive = ? ORDER BY day ASC`, drive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiskSnapshot{}
	for rows.Next() {
		var d DiskSnapshot
		if err := rows.Scan(&d.Drive, &d.Day, &d.Total, &d.Free, &d.Used); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// dirDiffs compares the two most recent C: snapshots and returns directories
// whose size changed, sorted by absolute delta descending. minDelta filters
// out noise (default 50 MB).
func (s *snapshotStore) dirDiffs(minDelta int64) ([]DirDelta, error) {
	rows, err := s.db.Query(`
		SELECT day FROM disk_snapshots WHERE drive = 'C:' ORDER BY day DESC LIMIT 2`)
	if err != nil {
		return nil, err
	}
	days := []string{}
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			rows.Close()
			return nil, err
		}
		days = append(days, day)
	}
	rows.Close()
	if len(days) < 2 {
		return nil, nil // need at least two days to diff
	}
	// Join the two most recent snapshots' dir sizes (works on older SQLite).
	q := `
		SELECT COALESCE(cur.path, prev.path) AS path,
		       COALESCE(cur.size, 0) AS size_now,
		       COALESCE(prev.size, 0) AS size_then,
		       COALESCE(cur.file_count, 0) AS fc
		FROM (
			SELECT sd.path, sd.size, sd.file_count FROM snapshot_dirs sd
			JOIN disk_snapshots ds ON ds.id = sd.snapshot_id
			WHERE ds.drive = 'C:' AND ds.day = ?
		) cur
		LEFT JOIN (
			SELECT sd.path, sd.size, sd.file_count FROM snapshot_dirs sd
			JOIN disk_snapshots ds ON ds.id = sd.snapshot_id
			WHERE ds.drive = 'C:' AND ds.day = ?
		) prev ON cur.path = prev.path
		UNION ALL
		SELECT prev.path,
		       0,
		       prev.size,
		       0
		FROM (
			SELECT sd.path, sd.size, sd.file_count FROM snapshot_dirs sd
			JOIN disk_snapshots ds ON ds.id = sd.snapshot_id
			WHERE ds.drive = 'C:' AND ds.day = ?
		) prev
		WHERE NOT EXISTS (
			SELECT 1 FROM snapshot_dirs sd2
			JOIN disk_snapshots ds2 ON ds2.id = sd2.snapshot_id
			WHERE ds2.drive = 'C:' AND ds2.day = ? AND sd2.path = prev.path
		)
	`
	rows, err = s.db.Query(q, days[0], days[1], days[1], days[0])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DirDelta{}
	for rows.Next() {
		var path string
		var now, then int64
		var fc int
		if err := rows.Scan(&path, &now, &then, &fc); err != nil {
			return nil, err
		}
		delta := now - then
		if delta < 0 {
			delta = -delta
		}
		if delta < minDelta {
			continue
		}
		out = append(out, DirDelta{Path: path, SizeNow: now, SizeThen: then, Delta: now - then, FileCount: fc})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Sort by absolute delta descending.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := abs64(out[j].Delta), abs64(out[j-1].Delta)
			if a > b {
				out[j], out[j-1] = out[j-1], out[j]
			} else {
				break
			}
		}
	}
	return out, nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
