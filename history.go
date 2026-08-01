package main

import (
	"os"
	"os/exec"
	"strings"
	"time"
)

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
	// Prune: keep the newest 10000 rows and anything within 90 days.
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
// matches target and invokes its "restore" verb (localized), via PowerShell.
// The recycle-bin item's Path property is the internal physical path; the
// original location comes from System.Recycle.DeletedFrom + Name.
func restoreFromRecycleBin(target string) error {
	escaped := strings.ReplaceAll(target, "'", "''")
	ps := `
$shell = New-Object -ComObject Shell.Application
$rb = $shell.NameSpace(0x0a)
$target = '` + escaped + `'
foreach ($item in $rb.Items()) {
  $del = $item.ExtendedProperty('System.Recycle.DeletedFrom')
  if ($del -and ((Join-Path $del $item.Name) -eq $target)) {
    foreach ($v in $item.Verbs()) {
      if ($v.Name -match '还原|restore') { $v.DoIt(); exit 0 }
    }
    exit 2
  }
}
exit 1
`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = out
		code := cmd.ProcessState.ExitCode()
		switch code {
		case 1:
			return errNotInRecycleBin
		case 2:
			return errNoRestoreVerb
		default:
			return err
		}
	}
	return nil
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
