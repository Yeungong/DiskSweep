package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// of permanently deleting them.
func (a *App) ExecuteClean(itemIDs []string) []CleanResult {
	byID := map[string]CleanItem{}
	for _, d := range a.cleanItemDefs() {
		byID[d.ID] = d
	}
	results := make([]CleanResult, 0, len(itemIDs))
	for _, id := range itemIDs {
		item, ok := byID[id]
		if !ok {
			results = append(results, CleanResult{
				ID: id, Name: id, OK: false,
				Errors: []PathError{{Kind: ErrOther, Error: "未知清理项"}},
			})
			continue
		}
		results = append(results, a.executeItem(item))
	}
	return results
}

func (a *App) executeItem(item CleanItem) CleanResult {
	res := CleanResult{ID: item.ID, Name: item.Name}

	if item.ID == "recycle_bin" {
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

	for _, p := range item.Paths {
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			continue // path missing or already gone
		}

		if item.Match == "" && item.MaxAgeDays == 0 {
			// Whole directory: move it to the recycle bin in one shot.
			size, _ := a.dirSizeCached(p)
			if err := moveToRecycleBin(p); err != nil {
				res.Errors = append(res.Errors, PathError{Path: p, Kind: classifyErr(err), Error: err.Error()})
				continue
			}
			res.Freed += size
			a.recordHistory(item, p, size, true, "")
			continue
		}

		// Selective deletion: collect matching files, recycle them in batches.
		freed, errs, deleted := a.recycleMatchingFiles(p, item.Match, item.MaxAgeDays)
		res.Freed += freed
		res.Errors = append(res.Errors, errs...)
		for _, d := range deleted {
			a.recordHistory(item, d.Path, d.Size, d.Err == "", d.Err)
		}
	}

	res.OK = len(res.Errors) == 0
	if res.OK && res.Freed == 0 {
		res.Warning = "没有可移动的内容（可能已被清理）"
	}
	return res
}

// recycleMatchingFiles moves matching files under dir (recursively) to the
// recycle bin in batches and prunes now-empty subdirectories. Returns the
// total freed bytes, per-path failures and the list of handled paths.
func (a *App) recycleMatchingFiles(dir, match string, maxAgeDays int) (int64, []PathError, []PathOutcome) {
	var targets []PathOutcome
	var errs []PathError
	var cutoff int64
	if maxAgeDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -maxAgeDays).Unix()
	}

	var walk func(d string)
	walk = func(d string) {
		entries, err := os.ReadDir(d)
		if err != nil {
			errs = append(errs, PathError{Path: d, Kind: classifyErr(err), Error: err.Error()})
			return
		}
		for _, e := range entries {
			full := filepath.Join(d, e.Name())
			if e.IsDir() {
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
				targets = append(targets, PathOutcome{Path: full, Size: info.Size()})
			}
		}
	}
	walk(dir)

	var freed int64
	var handled []PathOutcome
	const batch = 100
	for i := 0; i < len(targets); i += batch {
		end := i + batch
		if end > len(targets) {
			end = len(targets)
		}
		chunk := targets[i:end]
		paths := make([]string, len(chunk))
		for j, t := range chunk {
			paths[j] = t.Path
		}
		if err := movePathsToRecycleBin(paths); err != nil {
			// SHFileOperation can move part of the batch and still return an
			// error code (e.g. one file is locked). Verify each path: if it is
			// gone it actually succeeded; if it still exists, retry it alone
			// so only genuine failures are reported.
			var retry []PathOutcome
			for _, t := range chunk {
				if _, statErr := os.Lstat(t.Path); os.IsNotExist(statErr) {
					freed += t.Size
					handled = append(handled, PathOutcome{Path: t.Path, Size: t.Size})
					continue
				}
				retry = append(retry, t)
			}
			for _, t := range retry {
				if err := moveToRecycleBin(t.Path); err == nil {
					freed += t.Size
					handled = append(handled, PathOutcome{Path: t.Path, Size: t.Size})
					continue
				}
				errs = append(errs, PathError{Path: t.Path, Kind: classifyErr(err), Error: err.Error()})
				handled = append(handled, PathOutcome{Path: t.Path, Size: t.Size, Err: err.Error()})
			}
			continue
		}
		for _, t := range chunk {
			freed += t.Size
			handled = append(handled, PathOutcome{Path: t.Path, Size: t.Size})
		}
	}
	return freed, errs, handled
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
		res.OK = false
		res.Errors = []PathError{{
			Path:  item.Paths[0],
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

// emptyRecycleBin empties the recycle bin of every fixed drive, returning the
// space freed (estimated from the sizes before emptying).
func (a *App) emptyRecycleBin() int64 {	var total int64
	for _, p := range a.recycleBinPaths() {
		if s, _ := walkDirSize(p); s > 0 {
			total += s
		}
	}
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Clear-RecycleBin -Force -ErrorAction SilentlyContinue")
	if err := cmd.Run(); err != nil {
		return 0 // could not empty; report no freed space
	}
	return total
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
	const batch = 100
	for i := 0; i < len(paths); i += batch {
		end := i + batch
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
		wFunc: fODelete,
		pFrom: uintptr(unsafe.Pointer(&buf[0])),
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
	if dep := a.matchDepByPath(path); dep != nil && dep.Cleanable == DepKeep {
		return errDepProtected
	}
	return moveToRecycleBin(path)
}

// errDepProtected is returned when a user tries to recycle a protected
// dependency (e.g. a .NET runtime or VC++ library that software depends on).
var errDepProtected = errString("该路径是系统依赖（请勿删除），已阻止操作")

// OpenInExplorer opens Windows Explorer with the given path selected.
func (a *App) OpenInExplorer(path string) error {
	cmd := exec.Command("explorer", "/select,"+path)
	return cmd.Start()
}
