package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Delete error kinds.
const (
	ErrLocked     = "locked"     // file is in use
	ErrPermission = "permission" // access denied
	ErrOther      = "other"
)

// recycleBatchSize is how many paths go into one SHFileOperationW call. The
// shell pays a large fixed cost per call (it validates every path and sets up
// its own progress state), so tiny batches dominate the runtime when a rule
// matches tens of thousands of files.
const recycleBatchSize = 2000

// cleanHeartbeatInterval is how often the item currently being processed
// re-announces itself. Without it a single big item (e.g. %TEMP% with 1.2 GB
// of files) leaves the UI frozen for minutes and looks like a hang.
const cleanHeartbeatInterval = 700 * time.Millisecond

// PathError reports one failed delete.
type PathError struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Error string `json:"error"`
}

// CleanResult is the outcome of cleaning one item.
type CleanResult struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	OK      bool        `json:"ok"`
	Freed   int64       `json:"freed"`
	Errors  []PathError `json:"errors,omitempty"`
	Warning string      `json:"warning,omitempty"`
}

// ExecuteClean runs the cleanup for the given item IDs and returns per-item
// results. Every deletion moves files to the recycle bin (reversible) instead
// of permanently deleting them. Progress is emitted via "clean:progress":
// once when an item starts, a steady heartbeat while it works (so a long item
// never looks like a hang), and once when it finishes.
func (a *App) ExecuteClean(itemIDs []string) []CleanResult {
	byID := map[string]CleanItem{}
	for _, d := range a.cleanItemDefs() {
		byID[d.ID] = d
	}

	tk := &cleanTracker{app: a, total: len(itemIDs), started: time.Now()}
	stop := make(chan struct{})
	var hb sync.WaitGroup
	hb.Add(1)
	go func() {
		defer hb.Done()
		tk.heartbeat(stop)
	}()
	defer func() {
		close(stop)
		hb.Wait()
	}()

	results := make([]CleanResult, 0, len(itemIDs))
	for _, id := range itemIDs {
		item, ok := byID[id]
		if !ok {
			tk.begin(id, id)
			results = append(results, CleanResult{
				ID: id, Name: id, OK: false,
				Errors: []PathError{{Kind: ErrOther, Error: "未知清理项"}},
			})
			tk.endItem(0)
			continue
		}
		tk.begin(item.ID, item.Name)
		res := a.executeItemTracked(item, tk)
		results = append(results, res)
		tk.endItem(res.Freed)
	}
	return results
}

// CleanProgress is emitted during a cleanup batch.
type CleanProgress struct {
	Done      int    `json:"done"`            // items already finished
	Total     int    `json:"total"`           // items in this batch
	ItemID    string `json:"itemId,omitempty"`
	ItemName  string `json:"itemName,omitempty"`
	Freed     int64  `json:"freed"`           // bytes reclaimed so far in this batch
	Phase     string `json:"phase,omitempty"` // start | work | done
	Detail    string `json:"detail,omitempty"`
	SubDone   int    `json:"subDone,omitempty"`  // progress inside the current item
	SubTotal  int    `json:"subTotal,omitempty"` // 0 when the total is unknown
	ElapsedMs int64  `json:"elapsedMs,omitempty"`
}

// cleanTracker carries the live state of one ExecuteClean batch so the
// heartbeat goroutine can keep reporting while an item is still working.
// A tracker with a nil app is a no-op, which lets tests call executeItem
// without any progress plumbing.
type cleanTracker struct {
	app *App

	mu       sync.Mutex
	total    int
	done     int
	itemID   string
	itemName string
	phase    string
	detail   string
	subDone  int
	subTotal int
	bytes    int64
	started  time.Time
}

func (t *cleanTracker) begin(id, name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.itemID, t.itemName = id, name
	t.phase, t.detail = "start", ""
	t.subDone, t.subTotal = 0, 0
	t.mu.Unlock()
	t.publish()
}

func (t *cleanTracker) endItem(freed int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.done++
	t.bytes += freed
	t.phase, t.detail = "done", ""
	t.subDone, t.subTotal = 0, 0
	t.mu.Unlock()
	t.publish()
}

// step records what the current item is doing right now. subTotal == 0 means
// the total is not known yet (the bar stays indeterminate).
func (t *cleanTracker) step(detail string, subDone, subTotal int) {
	if t == nil || t.app == nil {
		return
	}
	t.mu.Lock()
	t.phase = "work"
	t.detail = detail
	t.subDone, t.subTotal = subDone, subTotal
	t.mu.Unlock()
}

func (t *cleanTracker) heartbeat(stop <-chan struct{}) {
	ticker := time.NewTicker(cleanHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			t.publish()
		}
	}
}

func (t *cleanTracker) publish() {
	if t == nil || t.app == nil || t.app.scans.emit == nil {
		return
	}
	t.mu.Lock()
	p := CleanProgress{
		Done: t.done, Total: t.total,
		ItemID: t.itemID, ItemName: t.itemName,
		Freed: t.bytes, Phase: t.phase, Detail: t.detail,
		SubDone: t.subDone, SubTotal: t.subTotal,
		ElapsedMs: time.Since(t.started).Milliseconds(),
	}
	t.mu.Unlock()
	t.app.scans.emit("clean:progress", p)
}

// executeItem runs one cleanup item without progress reporting (used by tests).
func (a *App) executeItem(item CleanItem) CleanResult {
	return a.executeItemTracked(item, nil)
}

func (a *App) executeItemTracked(item CleanItem, tk *cleanTracker) CleanResult {
	if tk == nil {
		tk = &cleanTracker{}
	}
	res := CleanResult{ID: item.ID, Name: item.Name}

	// Hard safety net, checked before anything else touches the disk.
	//
	// Probe-only targets (WinSxS, Windows\Installer, WindowsApps) exist so the
	// user can see where the space went -- they are never deletable. The UI
	// hides the action, but a stale frontend, a scripted call, or a future UI
	// change must not be able to remove them: these directories are serviced
	// only by DISM/Windows, and deleting them breaks Windows Update, program
	// uninstall/repair, and every Store app. Enforced here rather than in the
	// frontend so the guarantee holds regardless of caller.
	if item.InfoOnly || infoOnlyRuleIDs[item.ID] {
		res.OK = false
		res.Errors = append(res.Errors, PathError{
			Path:  strings.Join(item.Paths, "; "),
			Kind:  ErrOther,
			Error: "该项仅供查看空间占用，DiskSweep 不会删除它（需要 DISM/系统自带工具处理）",
		})
		return res
	}

	if item.ID == "recycle_bin" {
		tk.step("正在清空所有磁盘的回收站…", 0, 0)
		freed := a.emptyRecycleBin()
		res.OK = true
		res.Freed = freed
		return res
	}

	// System-level items executed via privileged commands. They permanently
	// delete data (no recycle-bin undo), so they must run elevated.
	switch item.ID {
	case "vss_shadows":
		return a.executeVSS(item)
	case "win_upgrade_residue":
		return a.executeUpgradeResidue(item)
	}

	paths := a.resolveItemPaths(item)
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue // path missing or already gone
		}

		if item.Match == "" && item.MaxAgeDays == 0 && item.ProtectRecentMinutes == 0 {
			// Whole path: move it to the recycle bin in one shot.
			// Deliberately not taken by rules that protect recent files: a
			// directory carries no useful mtime, so the whole-dir move cannot
			// honour that protection and would delete live files in it.
			var size int64
			var files int
			if info.IsDir() {
				size, files = walkDirSize(p)
			} else {
				size, files = info.Size(), 1
			}
			if files == 0 {
				continue // already empty (an earlier rule in this batch got it)
			}
			tk.step("正在移入回收站: "+filepath.Base(p), 0, 0)
			if err := moveToRecycleBin(p); err != nil {
				// The shell refuses to move a whole directory when it holds
				// in-use files. Fall back to moving the contents so whatever
				// can go is still recycled instead of the item failing whole.
				freed, fallbackErrs, handled := a.recycleMatchingFilesTracked(p, "", 0, 0, tk)
				if freed > 0 {
					res.Freed += freed
					a.recordHistoryBatch(item, handled)
					if len(fallbackErrs) > 0 {
						res.Warning = fmt.Sprintf("部分内容被占用，%d 项未能移入回收站（关闭相关程序后可重试）", len(fallbackErrs))
					}
					continue
				}
				res.Errors = append(res.Errors, PathError{Path: p, Kind: classifyErr(err), Error: shellDeleteErrMessage(err)})
				res.Errors = append(res.Errors, fallbackErrs...)
				continue
			}
			res.Freed += size
			a.recordHistory(item, p, size, true, "")
			continue
		}

		// Selective deletion: collect matching files, recycle them in batches.
		freed, errs, deleted := a.recycleMatchingFilesTracked(p, item.Match, item.MaxAgeDays, item.ProtectRecentMinutes, tk)
		res.Freed += freed
		res.Errors = append(res.Errors, errs...)
		a.recordHistoryBatch(item, deleted)
	}

	res.OK = len(res.Errors) == 0
	if res.OK && res.Freed == 0 {
		res.Warning = "没有可移动的内容（可能已被清理）"
	}
	return res
}

// resolveItemPaths returns the paths an item should actually operate on.
// Rules with a dynamic target list (Blizzard event caches) compute their
// targets here so the cleaned set always matches what the UI measured.
func (a *App) resolveItemPaths(item CleanItem) []string {
	if item.ID == "blizzard_game_cache" {
		return blizzardGameCachePaths()
	}
	return item.Paths
}

// deleteCutoff returns the newest modification time that is still deletable,
// combining the max-age rule with recent-activity protection. 0 means "no
// constraint".
//
// When both apply the stricter one wins, i.e. the later timestamp: with
// MaxAgeDays=7 and ProtectRecentMinutes=30 a file must be older than 7 days,
// and with only protection set it must be older than 30 minutes.
func deleteCutoff(maxAgeDays, protectRecentMinutes int, now time.Time) int64 {
	var cutoff int64
	if maxAgeDays > 0 {
		cutoff = now.AddDate(0, 0, -maxAgeDays).Unix()
	}
	if protectRecentMinutes > 0 {
		if c := now.Add(-time.Duration(protectRecentMinutes) * time.Minute).Unix(); c > cutoff {
			cutoff = c
		}
	}
	return cutoff
}

// recycleMatchingFiles moves matching files under dir (recursively) to the
// recycle bin in batches and prunes now-empty subdirectories. Returns the
// total freed bytes, per-path failures and the list of handled paths.
func (a *App) recycleMatchingFiles(dir, match string, maxAgeDays int) (int64, []PathError, []PathOutcome) {
	return a.recycleMatchingFilesTracked(dir, match, maxAgeDays, 0, nil)
}

// recycleMatchingFilesTracked is recycleMatchingFiles plus live progress
// reporting. tk may be nil (tests). protectRecentMinutes > 0 shields entries
// that were modified very recently (see CleanItem.ProtectRecentMinutes).
func (a *App) recycleMatchingFilesTracked(dir, match string, maxAgeDays, protectRecentMinutes int, tk *cleanTracker) (int64, []PathError, []PathOutcome) {
	var dirTargets []PathOutcome // whole subtrees chosen for a one-shot move
	var fileTargets []PathOutcome
	var errs []PathError
	// A single cutoff covers both rules: age says "must be at least N days old",
	// protection says "must not have been touched in the last M minutes". Both
	// are "skip if newer than X", so the stricter timestamp is the right one.
	cutoff := deleteCutoff(maxAgeDays, protectRecentMinutes, time.Now())

	scanned := 0
	var walk func(d string)
	walk = func(d string) {
		entries, err := os.ReadDir(d)
		if err != nil {
			errs = append(errs, PathError{Path: d, Kind: classifyErr(err), Error: err.Error()})
			return
		}
		scanned++
		found := len(dirTargets) + len(fileTargets)
		tk.step(fmt.Sprintf("正在扫描目录（已查 %d 个目录 · 已找到 %d 项可清理）", scanned, found), 0, 0)
		for _, e := range entries {
			full := filepath.Join(d, e.Name())
			if e.IsDir() {
				// A subtree whose contents all match can be recycled as a
				// single entry instead of thousands of individual files — the
				// biggest win on temp/cache rules.
				if ok, size, files := subtreeAllMatch(full, match, cutoff); ok && files > 0 {
					dirTargets = append(dirTargets, PathOutcome{Path: full, Size: size})
					continue
				}
				walk(full)
				removeEmptyDir(full, &errs)
				continue
			}
			if !matchesName(e.Name(), match) {
				continue
			}
			if cutoff > 0 {
				if info, err := e.Info(); err != nil || info.ModTime().Unix() > cutoff {
					continue // too recent
				}
			}
			if info, err := e.Info(); err == nil {
				fileTargets = append(fileTargets, PathOutcome{Path: full, Size: info.Size()})
			}
		}
	}
	walk(dir)

	var freed int64
	var handled []PathOutcome

	// Subtrees first, one shell call each. The shell refuses a whole
	// directory when something inside it is in use, so on failure descend into
	// it: most of its contents can still be recycled and leaving the whole
	// subtree behind is exactly the "清理了很久但没释放多少" complaint.
	for _, t := range dirTargets {
		tk.step("正在移入回收站: "+filepath.Base(t.Path), 0, 0)
		mErr := moveToRecycleBin(t.Path)
		if mErr == nil {
			freed += t.Size
			handled = append(handled, t)
			continue
		}
		f, e, h := a.recycleMatchingFilesTracked(t.Path, match, maxAgeDays, protectRecentMinutes, tk)
		freed += f
		errs = append(errs, e...)
		handled = append(handled, h...)
		if f == 0 && len(e) == 0 {
			msg := shellDeleteErrMessage(mErr)
			errs = append(errs, PathError{Path: t.Path, Kind: classifyErr(mErr), Error: msg})
			handled = append(handled, PathOutcome{Path: t.Path, Size: t.Size, Err: msg})
		}
	}

	// Loose files, in large batches (the shell only moves the ones it can and
	// reports the rest, so partial success is free).
	total := len(fileTargets)
	for i := 0; i < total; i += recycleBatchSize {
		end := i + recycleBatchSize
		if end > total {
			end = total
		}
		tk.step(fmt.Sprintf("正在移入回收站（%d/%d）", i, total), i, total)
		f, e, h := a.recycleChunk(fileTargets[i:end])
		freed += f
		errs = append(errs, e...)
		handled = append(handled, h...)
		tk.step(fmt.Sprintf("正在移入回收站（%d/%d）", end, total), end, total)
	}
	return freed, errs, handled
}

// recycleChunk moves one batch of paths to the recycle bin.
//
// SHFileOperationW reports an error even when only part of the batch failed
// (e.g. one locked file), so after a failure every path is checked: anything
// that vanished really was moved and counts as freed, and only the survivors
// are retried — individually, so each genuine failure is attributed to the
// path that caused it.
func (a *App) recycleChunk(chunk []PathOutcome) (int64, []PathError, []PathOutcome) {
	paths := make([]string, len(chunk))
	for i, t := range chunk {
		paths[i] = t.Path
	}
	err := movePathsToRecycleBin(paths)
	if err == nil {
		var freed int64
		for _, t := range chunk {
			freed += t.Size
		}
		return freed, nil, append([]PathOutcome{}, chunk...)
	}

	var freed int64
	var handled []PathOutcome
	var survivors []PathOutcome
	for _, t := range chunk {
		if _, statErr := os.Lstat(t.Path); os.IsNotExist(statErr) {
			freed += t.Size
			handled = append(handled, t)
			continue
		}
		survivors = append(survivors, t)
	}

	// The whole batch was rejected (in-use files, no recycle bin on the
	// volume, ...). Retrying thousands of paths one at a time would cost far
	// more than the batch we just tried, so report it as one failure.
	//
	// Path is left empty on purpose: the failure belongs to the batch, and
	// naming chunk[0] would blame an arbitrary file that may be perfectly fine.
	if len(survivors) == len(chunk) && len(chunk) > 32 {
		for _, t := range survivors {
			handled = append(handled, PathOutcome{Path: t.Path, Size: t.Size, Err: err.Error()})
		}
		return freed, []PathError{{
			Kind:  classifyErr(err),
			Error: fmt.Sprintf("%s（该批次 %d 项均未移动）", shellDeleteErrMessage(err), len(chunk)),
		}}, handled
	}

	var errs []PathError
	for _, t := range survivors {
		mErr := moveToRecycleBin(t.Path)
		if mErr == nil {
			freed += t.Size
			handled = append(handled, t)
			continue
		}
		msg := shellDeleteErrMessage(mErr)
		errs = append(errs, PathError{Path: t.Path, Kind: classifyErr(mErr), Error: msg})
		handled = append(handled, PathOutcome{Path: t.Path, Size: t.Size, Err: msg})
	}
	return freed, errs, handled
}

// subtreeAllMatch walks dir once and reports whether every file inside it
// matches the rule and is old enough to clean, together with the subtree size
// and file count. It is deliberately conservative — an unreadable directory, a
// mismatching file or a file that is too recent all return false — so a
// subtree is only recycled wholesale when nothing in it has to be kept. It
// bails out at the first mismatch, which makes the common case almost free.
func subtreeAllMatch(dir, match string, cutoff int64) (bool, int64, int) {
	var size int64
	var files int
	all := true
	var walk func(d string)
	walk = func(d string) {
		if !all {
			return
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			all = false
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				walk(filepath.Join(d, e.Name()))
				if !all {
					return
				}
				continue
			}
			if !matchesName(e.Name(), match) {
				all = false
				return
			}
			info, err := e.Info()
			if err != nil {
				all = false
				return
			}
			if cutoff > 0 && info.ModTime().Unix() > cutoff {
				all = false
				return
			}
			size += info.Size()
			files++
		}
	}
	walk(dir)
	if !all {
		return false, 0, 0
	}
	return true, size, files
}

// PathOutcome records one file handled by a selective cleanup.
type PathOutcome struct {
	Path string
	Size int64
	Err  string
}

// deleteDirContents is kept for tests only; production cleanup uses the
// recycle-bin path (recycleMatchingFiles / moveToRecycleBin).
func (a *App) deleteDirContents(dir, match string, maxAgeDays int) (int64, []PathError) {
	var freed int64
	var errs []PathError
	var cutoff int64
	if maxAgeDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -maxAgeDays).Unix()
	}
	a.walkDelete(dir, match, cutoff, &freed, &errs)
	return freed, errs
}

func (a *App) walkDelete(dir, match string, cutoff int64, freed *int64, errs *[]PathError) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		*errs = append(*errs, PathError{Path: dir, Kind: classifyErr(err), Error: err.Error()})
		return
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			a.walkDelete(full, match, cutoff, freed, errs)
			removeEmptyDir(full, errs)
			continue
		}
		if !matchesName(e.Name(), match) {
			continue
		}
		if cutoff > 0 {
			if info, err := e.Info(); err != nil || info.ModTime().Unix() > cutoff {
				continue // too recent to delete
			}
		}
		if f, err := deleteFile(full); err != nil {
			*errs = append(*errs, PathError{Path: full, Kind: classifyErr(err), Error: err.Error()})
		} else {
			*freed += f
		}
	}
}

// matchesName applies a CleanItem.Match rule to a file name.
// Unknown rule prefixes return false (conservative: better to skip a file
// than to delete everything when a rule is mistyped).
func matchesName(name, match string) bool {
	switch {
	case match == "":
		return true
	case strings.HasPrefix(match, "glob:"):
		ok, _ := filepath.Match(strings.TrimPrefix(match, "glob:"), name)
		return ok
	case strings.HasPrefix(match, "ext:"):
		return strings.EqualFold(filepath.Ext(name), strings.TrimPrefix(match, "ext:"))
	default:
		return false
	}
}

// deleteFile removes one file, clearing the read-only attribute first and
// retrying transient locks a few times. Returns the freed bytes.
func deleteFile(p string) (int64, error) {
	info, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	size := info.Size()

	// Windows read-only files fail to delete; clear the attribute.
	if info.Mode().Perm()&0o222 == 0 {
		_ = os.Chmod(p, 0o666)
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		err = os.Remove(p)
		if err == nil {
			return size, nil
		}
		lastErr = err
		if isLockedErr(err) || isPermissionErr(err) {
			time.Sleep(80 * time.Millisecond)
			continue
		}
		break
	}
	return 0, lastErr
}

// removeEmptyDir removes dir if it is now empty (non-fatal on failure).
func removeEmptyDir(p string, errs *[]PathError) {
	entries, err := os.ReadDir(p)
	if err != nil || len(entries) > 0 {
		return
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		*errs = append(*errs, PathError{Path: p, Kind: classifyErr(err), Error: err.Error()})
	}
}

// executeVSS deletes all system restore points (shadow copies) via vssadmin.
// This is a permanent deletion - shadow copies cannot go to the recycle bin.
// Requires an elevated process; a non-admin attempt is reported as a
// permission failure.
func (a *App) executeVSS(item CleanItem) CleanResult {
	res := CleanResult{ID: item.ID, Name: item.Name}
	if !a.IsAdmin() {
		res.OK = false
		res.Errors = []PathError{{
			Path:  `C:\System Volume Information`,
			Kind:  ErrPermission,
			Error: "需要管理员权限删除系统还原点",
		}}
		return res
	}
	before := item.Size
	cmd := exec.Command("vssadmin", "delete", "shadows", "/all", "/quiet")
	if err := cmd.Run(); err != nil {
		res.OK = false
		res.Errors = []PathError{{
			Path:  `vssadmin`,
			Kind:  ErrOther,
			Error: "删除卷影副本失败: " + err.Error(),
		}}
		return res
	}
	// Best-effort estimate of freed space from the pre-delete probe.
	res.OK = true
	res.Freed = before
	if res.Freed == 0 {
		res.Warning = "卷影副本已删除（释放空间量未知，以磁盘剩余空间变化为准）"
	}
	a.recordHistory(item, "系统还原点（全部卷影副本）", before, true, "")
	return res
}

// executeUpgradeResidue removes Windows upgrade leftovers. These directories
// are owned by TrustedInstaller, so even admins usually need takeown/icacls
// before deletion. We attempt the delete and report per-path outcomes.
func (a *App) executeUpgradeResidue(item CleanItem) CleanResult {
	res := CleanResult{ID: item.ID, Name: item.Name}
	if !a.IsAdmin() {
		where := `C:\$WINDOWS.~BT`
		if len(item.Paths) > 0 {
			where = item.Paths[0]
		}
		res.OK = false
		res.Errors = []PathError{{
			Path:  where,
			Kind:  ErrPermission,
			Error: "需要管理员权限删除升级残留",
		}}
		return res
	}
	for _, p := range item.Paths {
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			continue // already gone
		}
		// Estimate size before deletion (best effort).
		size, _ := walkDirSize(p)
		// Grant current admin full control, then delete permanently.
		if err := takeownAndDelete(p); err != nil {
			res.Errors = append(res.Errors, PathError{Path: p, Kind: classifyErr(err), Error: err.Error()})
			continue
		}
		res.Freed += size
		a.recordHistory(item, p, size, true, "")
	}
	res.OK = len(res.Errors) == 0
	if res.OK && res.Freed == 0 {
		res.Warning = "没有可删除的升级残留（可能已清理）"
	}
	return res
}

// takeownAndDelete takes ownership of a directory tree and permanently
// deletes it. Needed for TrustedInstaller-owned upgrade leftovers.
func takeownAndDelete(p string) error {
	// takeown /F <path> /R /D Y takes ownership recursively (prompts suppressed).
	t := exec.Command("takeown", "/F", p, "/R", "/D", "Y")
	if out, err := t.CombinedOutput(); err != nil {
		_ = out
		// Some subpaths may still fail; continue to icacls anyway.
	}
	i := exec.Command("icacls", p, "/grant", "Administrators:F", "/T", "/Q", "/C")
	if out, err := i.CombinedOutput(); err != nil {
		_ = out
	}
	return os.RemoveAll(p)
}

// recycleBinBytes is the total physical size of the recycle bin on every drive.
//
// Note this counts what is really on disk, which is deliberately not the same as
// what Explorer reports: Explorer lists only entries still present in the bin's
// $I index, so payloads left behind by a failed or partial empty (orphaned $R
// folders) are invisible there while still consuming space. Measured on this
// box: ~594 MB on disk vs ~13 MB shown by the Shell namespace.
func (a *App) recycleBinBytes() int64 {
	var total int64
	for _, p := range a.recycleBinPaths() {
		if s, _ := walkDirSize(p); s > 0 {
			total += s
		}
	}
	return total
}

// emptyRecycleBin empties every drive's recycle bin and returns the bytes that
// were ACTUALLY freed.
//
// It used to return the size measured before clearing, which is how the tool
// came to claim it freed 10 GB while only ~500 MB ever disappeared: the shell
// only removes indexed entries, so anything orphaned stays on disk but was
// still reported as freed. Measuring again afterwards makes the number
// self-verifying instead of an assumption.
func (a *App) emptyRecycleBin() int64 {
	before := a.recycleBinBytes()

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Clear-RecycleBin -Force -ErrorAction SilentlyContinue")
	if err := cmd.Run(); err != nil {
		return 0 // could not empty; report no freed space
	}

	after := a.recycleBinBytes()
	if freed := before - after; freed > 0 {
		return freed
	}
	return 0
}

// --- error classification ---

func classifyErr(err error) string {
	switch {
	case err == nil:
		return ""
	case isLockedErr(err):
		return ErrLocked
	case isPermissionErr(err):
		return ErrPermission
	default:
		return ErrOther
	}
}

// shellDeleteErrMessage turns a raw SHFileOperationW return code into
// something actionable. The shell answers with its own DE_* codes (0x71-0x7C)
// which collide with unrelated Win32 error numbers, so the plain syscall text
// is actively misleading: 0x7C surfaces as "The system call level is not
// correct." (ERROR_INVALID_LEVEL) even though the real cause is a directory
// full of in-use files.
func shellDeleteErrMessage(err error) string {
	n, ok := errnoOf(err)
	if !ok {
		return err.Error()
	}
	switch uint32(n) {
	case 0x71: // DE_SAMEFILE
		return "源路径与目标路径相同"
	case 0x77, 0x05: // DE_ACCESSDENIEDSRC / ERROR_ACCESS_DENIED
		return "访问被拒绝：文件被占用或权限不足（关闭相关程序后重试）"
	case 0x78, 0x7A, 0x7C: // DE_PATHTOODEEP / DE_INVALIDFILES / DE_FLDDESTISFILE
		return fmt.Sprintf("无法移入回收站（Shell 错误码 0x%X）：目录内通常有正在使用的文件，请关闭对应程序（浏览器 / 资源管理器 / 编辑器）后重试", uint32(n))
	default:
		return fmt.Sprintf("无法移入回收站（Shell 错误码 0x%X）：%s", uint32(n), err.Error())
	}
}

// Windows error numbers used for delete-failure classification.
const (
	errnoSharingViolation = syscall.Errno(32)
	errnoLockViolation    = syscall.Errno(33)
	errnoAccessDenied     = syscall.Errno(5)
	errnoInvalidLevel     = syscall.Errno(124) // SHFileOperation reports this for in-use files
)

// errnoOf extracts the Windows error number from an error, which may be a
// *os.PathError wrapping a syscall.Errno, or a bare syscall.Errno (as
// returned by SHFileOperationW).
func errnoOf(err error) (syscall.Errno, bool) {
	switch e := err.(type) {
	case *os.PathError:
		n, ok := e.Err.(syscall.Errno)
		return n, ok
	case syscall.Errno:
		return e, true
	}
	return 0, false
}

func isLockedErr(err error) bool {
	n, ok := errnoOf(err)
	if !ok {
		return false
	}
	return n == errnoSharingViolation || n == errnoLockViolation || n == errnoInvalidLevel
}

func isPermissionErr(err error) bool {
	n, ok := errnoOf(err)
	if !ok {
		return false
	}
	return n == errnoAccessDenied || n == syscall.EACCES
}

// --- recycle-bin moves (SHFileOperationW with FOF_ALLOWUNDO) ---

const (
	fODelete       = 0x0003
	fofSilent      = 0x0004
	fofNoConfirm   = 0x0010
	fofAllowUndo   = 0x0040
	fofNoErrorUI   = 0x0400
)

type shfileopstructW struct {
	hwnd                uintptr
	wFunc               uint32
	pFrom               uintptr
	pTo                 uintptr
	fFlags              uint16
	fAnyOperationsAbort int32
	hNameMappings       uintptr
	lpszProgressTitle   uintptr
}

var (
	shFileOpW = shell32.NewProc("SHFileOperationW")
)

// moveToRecycleBin moves a single file or directory to the recycle bin.
func moveToRecycleBin(p string) error {
	return movePathsToRecycleBin([]string{p})
}

// movePathsToRecycleBin moves many files/directories to the recycle bin in
// batches using one SHFileOperationW call per batch (multi-path pFrom).
func movePathsToRecycleBin(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	for i := 0; i < len(paths); i += recycleBatchSize {
		end := i + recycleBatchSize
		if end > len(paths) {
			end = len(paths)
		}
		if err := movePathsChunk(paths[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func movePathsChunk(paths []string) error {
	// Build a double-null-terminated, NUL-separated multi-path string.
	// Note: UTF16FromString already appends a trailing NUL, so strip it and
	// add our own single separator per path.
	buf := make([]uint16, 0, 4096)
	for _, p := range paths {
		u, err := syscall.UTF16FromString(p)
		if err != nil {
			return err
		}
		buf = append(buf, u[:len(u)-1]...)
		buf = append(buf, 0) // path separator
	}
	buf = append(buf, 0) // final terminator

	op := shfileopstructW{
		wFunc:  fODelete,
		pFrom:  uintptr(unsafe.Pointer(&buf[0])),
		fFlags: fofAllowUndo | fofNoConfirm | fofSilent | fofNoErrorUI,
	}
	ret, _, _ := shFileOpW.Call(uintptr(unsafe.Pointer(&op)))
	if ret != 0 {
		return syscall.Errno(ret)
	}
	return nil
}

// moveDirToRecycleBin is kept for tests; production uses moveToRecycleBin /
// movePathsToRecycleBin directly.
func (a *App) moveDirToRecycleBin(p string) (int64, error) {
	size, _ := a.dirSizeCached(p)
	if err := moveToRecycleBin(p); err != nil {
		return 0, err
	}
	return size, nil
}

// RecyclePath moves a single file or directory to the recycle bin. Used by the
// analyze view for individual large files. Recognized dependencies that must
// be kept are rejected to prevent accidental deletion of runtime/model files.
func (a *App) RecyclePath(path string) error {
	// The view-only guard has to be enforced here too, not just in
	// executeItemTracked. The analyser lets the user recycle an individual file,
	// so browsing into WinSxS (or Windows\Installer, or WindowsApps) and using
	// that button would otherwise do exactly what the rule-level refusal exists
	// to prevent -- and the refusal would look broken rather than protective.
	if a.isInfoOnlyPath(path) {
		return errInfoOnlyPath
	}
	if dep := a.matchDepByPath(path); dep != nil && dep.Cleanable == DepKeep {
		return errDepProtected
	}
	return moveToRecycleBin(path)
}

// errDepProtected is returned when a user tries to recycle a protected
// dependency (e.g. a .NET runtime or VC++ library that software depends on).
var errDepProtected = errString("该路径是系统依赖（请勿删除），已阻止操作")

// errInfoOnlyPath is returned when the target lies inside a view-only area.
var errInfoOnlyPath = errString("该路径属于「仅供查看」的系统目录（WinSxS / Windows\\Installer / WindowsApps），DiskSweep 不会删除它")

// isInfoOnlyPath reports whether path is, or lies inside, one of the view-only
// targets. Prefix matching includes the separator so "C:\Windows\InstallerBackup"
// is not mistaken for "C:\Windows\Installer".
func (a *App) isInfoOnlyPath(path string) bool {
	lp := strings.ToLower(filepath.Clean(path))
	for _, d := range a.cleanItemDefs() {
		if !d.InfoOnly {
			continue
		}
		for _, p := range d.Paths {
			pp := strings.ToLower(filepath.Clean(p))
			if lp == pp || strings.HasPrefix(lp, pp+`\`) {
				return true
			}
		}
	}
	return false
}

// OpenInExplorer opens Windows Explorer with the given path selected.
func (a *App) OpenInExplorer(path string) error {
	cmd := exec.Command("explorer", "/select,"+path)
	return cmd.Start()
}
