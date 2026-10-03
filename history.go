package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf16"
)

// joinPath is filepath.Join (kept as a helper so the restore logic reads
// clearly and is easy to unit test against path cases).
func joinPath(elem ...string) string { return filepath.Join(elem...) }

// dirName is filepath.Dir with an empty-string guard (returns "" for empty).
func dirName(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Dir(p)
}

// HistoryEntry records one path moved to the recycle bin by a cleanup.
type HistoryEntry struct {
	ID       int64  `json:"id"`
	Time     int64  `json:"time"`
	ItemID   string `json:"itemId"`
	ItemName string `json:"itemName"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Restored bool   `json:"restored"`
}

// recordHistory appends one entry to the clean history and prunes old
// entries (kept: newest 10000, or within 90 days). Non-fatal: history is a
// convenience, and tests may run without a cache store.
func (a *App) recordHistory(item CleanItem, path string, size int64, ok bool, errMsg string) {
	if a.cache == nil || a.cache.db == nil {
		return
	}
	_, _ = a.cache.db.Exec(
		`INSERT INTO clean_history (time, item_id, item_name, path, size, ok, error) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), item.ID, item.Name, path, size, boolToInt(ok), errMsg,
	)
	a.pruneHistory()
}

// recordHistoryBatch records every path handled by one rule in a single
// transaction and prunes once. One-row-per-statement mattered a lot in
// practice: a rule such as "用户临时文件" can touch tens of thousands of files,
// and each write also re-ran the whole-table prune query.
func (a *App) recordHistoryBatch(item CleanItem, outs []PathOutcome) {
	if len(outs) == 0 || a.cache == nil || a.cache.db == nil {
		return
	}
	fallback := func() {
		for _, o := range outs {
			a.recordHistory(item, o.Path, o.Size, o.Err == "", o.Err)
		}
	}
	tx, err := a.cache.db.Begin()
	if err != nil {
		fallback()
		return
	}
	stmt, err := tx.Prepare(
		`INSERT INTO clean_history (time, item_id, item_name, path, size, ok, error) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		fallback()
		return
	}
	now := time.Now().Unix()
	for _, o := range outs {
		_, _ = stmt.Exec(now, item.ID, item.Name, o.Path, o.Size, boolToInt(o.Err == ""), o.Err)
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		return
	}
	a.pruneHistory()
}

// lastHistoryPrune throttles pruning: the prune query scans the entire history
// table, so running it after every insert is pure overhead inside a big batch.
var lastHistoryPrune atomic.Int64

// pruneHistory keeps the newest 10000 entries and anything within 90 days,
// refreshing at most once every 10 seconds.
func (a *App) pruneHistory() {
	if a.cache == nil || a.cache.db == nil {
		return
	}
	now := time.Now().Unix()
	for {
		last := lastHistoryPrune.Load()
		if last != 0 && now-last < 10 {
			return
		}
		if lastHistoryPrune.CompareAndSwap(last, now) {
			break
		}
	}
	cutoff := time.Now().AddDate(0, 0, -90).Unix()
	_, _ = a.cache.db.Exec(
		`DELETE FROM clean_history WHERE id NOT IN (SELECT id FROM clean_history ORDER BY id DESC LIMIT 10000) OR time < ?`,
		cutoff,
	)
}

// GetCleanHistory returns recent cleanup history, newest first.
func (a *App) GetCleanHistory() []HistoryEntry {
	out := []HistoryEntry{}
	if a.cache == nil || a.cache.db == nil {
		return out
	}
	rows, err := a.cache.db.Query(
		`SELECT id, time, item_id, item_name, path, size, ok, error, restored
		 FROM clean_history ORDER BY id DESC LIMIT 500`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var h HistoryEntry
		var okI, restI int
		if err := rows.Scan(&h.ID, &h.Time, &h.ItemID, &h.ItemName, &h.Path, &h.Size, &okI, &h.Error, &restI); err != nil {
			continue
		}
		h.OK = okI != 0
		h.Restored = restI != 0
		out = append(out, h)
	}
	return out
}

// RestoreResult is the outcome of a restore attempt.
type RestoreResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// RestoreHistory restores one history entry from the recycle bin back to its
// original path. Returns ok=false with a message when the file is no longer
// in the recycle bin (e.g. it was emptied) or already restored.
func (a *App) RestoreHistory(id int64) RestoreResult {
	if a.cache == nil || a.cache.db == nil {
		return RestoreResult{OK: false, Message: "历史记录不可用"}
	}

	var h HistoryEntry
	var okI, restI int
	err := a.cache.db.QueryRow(
		`SELECT id, time, item_id, item_name, path, size, ok, error, restored
		 FROM clean_history WHERE id = ?`, id).
		Scan(&h.ID, &h.Time, &h.ItemID, &h.ItemName, &h.Path, &h.Size, &okI, &h.Error, &restI)
	if err != nil {
		return RestoreResult{OK: false, Message: "记录不存在"}
	}
	h.Restored = restI != 0
	if h.Restored {
		return RestoreResult{OK: false, Message: "该记录已恢复过"}
	}
	if _, err := os.Stat(h.Path); err == nil {
		return RestoreResult{OK: false, Message: "原位置已存在同名文件，无需恢复"}
	}

	if err := restoreFromRecycleBin(h.Path); err != nil {
		return RestoreResult{OK: false, Message: "恢复失败: " + err.Error()}
	}

	// Verify the path is back; otherwise report it as gone from the bin.
	if _, err := os.Stat(h.Path); err != nil {
		return RestoreResult{OK: false, Message: "回收站中已找不到该文件（可能已被清空）"}
	}

	_, _ = a.cache.db.Exec(`UPDATE clean_history SET restored = 1 WHERE id = ?`, id)
	return RestoreResult{OK: true, Message: "已恢复到原位置"}
}

// restoreFromRecycleBin finds the item in the recycle bin whose original path
// matches target and silently moves it back to its original location.
//
// Instead of invoking the shell "restore" verb (which pops a blue explorer
// progress dialog), we parse the recycle-bin $I metadata files directly to
// find the physical $R file, then move it back with os.Rename (same-volume
// rename, instant, no window).
func restoreFromRecycleBin(target string) error {
	// Windows stores the original path in $I* metadata next to the $R* data.
	// We search every drive's $Recycle.Bin\<SID> folder.
	for _, root := range recycleRoots() {
		infos, err := os.ReadDir(root)
		if err != nil {
			continue // drive absent or no permission
		}
		for _, sid := range infos {
			if !sid.IsDir() {
				continue
			}
			bin := joinPath(root, sid.Name())
			err := restoreFromRecycleDir(bin, target)
			if err == nil {
				return nil // restored
			}
			if err != errNotInRecycleBin {
				return err
			}
		}
	}
	return errNotInRecycleBin
}

// recycleRoots returns the $Recycle.Bin root of every present fixed drive.
func recycleRoots() []string {
	roots := []string{}
	for _, d := range listFixedDrives() {
		roots = append(roots, d+`\$Recycle.Bin`)
	}
	return roots
}

// listFixedDrives returns drive letters (e.g. "C:") of all present drives by
// probing for the drive root directory.
func listFixedDrives() []string {
	out := []string{}
	for letter := 'A'; letter <= 'Z'; letter++ {
		root := string(letter) + `:\`
		if _, err := os.Stat(root); err == nil {
			out = append(out, string(letter)+":")
		}
	}
	return out
}

// restoreFromRecycleDir scans one $Recycle.Bin\<SID> directory for the entry
// whose original path equals target, then moves its $R file back.
func restoreFromRecycleDir(bin, target string) error {
	entries, err := os.ReadDir(bin)
	if err != nil {
		return errNotInRecycleBin
	}
	for _, e := range entries {
		name := e.Name()
		if len(name) < 2 || name[0] != '$' || (name[1] != 'I' && name[1] != 'i') {
			continue
		}
		metaPath := joinPath(bin, name)
		orig, _, err := parseRecycleMeta(metaPath)
		if err != nil || !strings.EqualFold(orig, target) {
			continue
		}
		// Found: the data file is $R + the same suffix.
		dataName := "$R" + name[2:]
		dataPath := joinPath(bin, dataName)
		if _, err := os.Stat(dataPath); err != nil {
			return errNoRestoreVerb // metadata without data (shouldn't happen)
		}
		// Ensure the destination directory exists, then rename back.
		dir := dirName(target)
		if dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		if err := os.Rename(dataPath, target); err != nil {
			return err
		}
		// Remove the metadata file (the recycle-bin entry is gone).
		_ = os.Remove(metaPath)
		return nil
	}
	return errNotInRecycleBin
}

// parseRecycleMeta reads a $I metadata file and returns the original path and
// file size. Win10 format: 8-byte magic, 8-byte size, 8-byte FILETIME,
// 4-byte path length, then UTF-16LE original path.
func parseRecycleMeta(path string) (string, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	if len(data) < 28 {
		return "", 0, errString("回收站元数据格式异常")
	}
	size := int64(binary.LittleEndian.Uint64(data[8:16]))
	u := make([]uint16, 0, 512)
	for i := 28; i+1 < len(data); i += 2 {
		c := binary.LittleEndian.Uint16(data[i : i+2])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u)), size, nil
}

var (
	errNotInRecycleBin = errString("回收站中不存在该文件")
	errNoRestoreVerb   = errString("未找到还原操作")
)

type errString string

func (e errString) Error() string { return string(e) }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
