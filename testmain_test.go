package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain works around a machine-specific hazard: 360 (ZhuDongFangYu)
// real-time protection interferes with file I/O under the user's %TEMP%.
//
// Two distinct symptoms, same cause, both seen on this box:
//
//   - os.RemoveAll on a t.TempDir() hangs, then the package aborts with
//     "panic: test timed out" (stack in makeTempDir.func1 -> NtSetInformationFile);
//   - SQLite's WAL setup hangs inside openSnapshotStore -> Exec, because 360
//     holds the freshly created .db open while scanning it.
//
// Both disappear once temp files leave C:\Users\...\Temp, so this TestMain:
//
//  1. Builds the shared temp root on the *project* drive (E:) instead of the
//     default %TEMP%, then points TMPDIR/TMP/TEMP at it. Every test keeps its
//     own fresh subdirectory; only the parent location changes.
//
//  2. Drops the automatic cleanup Go registers for t.TempDir(), and removes the
//     tree once at the end under a short timeout that never blocks the run.
//
// Setting DS_KEEP_TMP_FILES=1 keeps the tree around for inspection.
func TestMain(m *testing.M) {
	parent, err := testTempParent()
	if err != nil {
		// Could not pick a location; fall back to the defaults so the suite
		// still runs (and just takes the intermittent hang risk).
		os.Exit(m.Run())
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		os.Exit(m.Run())
	}

	// Create this run's root before touching stale ones, so a slow reap can
	// never delay or endanger the run we are about to start.
	root, err := os.MkdirTemp(parent, "run-*")
	if err != nil {
		os.Exit(m.Run())
	}
	tmpRoot, err = filepath.Abs(root)
	if err != nil {
		tmpRoot = root
	}
	// Go's testing package derives t.TempDir() from $TMPDIR.
	os.Setenv("TMPDIR", tmpRoot)
	os.Setenv("TMP", tmpRoot)
	os.Setenv("TEMP", tmpRoot)

	// Reap leftovers from previous runs only after this run's directory is
	// safely in place, and only in the background. Deleting a few thousand
	// stale files can take minutes when 360 stalls it, and doing that
	// synchronously used to look exactly like "the suite hangs with no
	// output" -- it blocked before a single test had started.
	go reapStaleTempRoots(parent, filepath.Base(tmpRoot))

	code := m.Run()

	if os.Getenv("DS_KEEP_TMP_FILES") == "" {
		removeTempTree(tmpRoot)
	}
	os.Exit(code)
}

// testTempParent returns the directory that holds the per-run temp roots.
//
// It sits BESIDE the project, not inside it, for three separately-earned
// reasons:
//
//   - Not under %TEMP%: the user's policy is that build/test artefacts never
//     consume C: space, and it is also where 360 interferes most.
//   - Not under .workbuddy: writes there are intercepted on this box, which made
//     SQLite's WAL setup hang inside openSnapshotStore.
//   - Not inside the project at all: TestScanAccessDeniedDirDoesNotCrash plants a
//     directory with an explicit DENY ACE. While that leftover lived in
//     "tmp-test/" and then ".tmp-test/", ANY tool that walks the project root
//     tripped over it -- `go build ./...` failed with "Access is denied", and
//     wailsbindings.exe hung for minutes during `wails build`. Go's own scanner
//     skips dot-directories, but other tooling does not, so the only reliable
//     fix is to keep the tree out of the project entirely.
func testTempParent() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(wd), filepath.Base(wd)+"-test-tmp"), nil
}

// tempCleanupTimeout bounds how long we wait for temp-tree removal. 360's
// real-time protection can stall this for minutes, so the timeout is kept
// short -- a leaked temp directory is far cheaper than a slow test run.
const tempCleanupTimeout = 10 * time.Second

// removeTempTreeBestEffort deletes dir without blocking the caller for longer
// than tempCleanupTimeout, reporting whether it finished in time.
//
// Every destructive temp operation in this file goes through here: on this
// machine os.RemoveAll over a few thousand files is regularly stalled by 360,
// and an unbounded wait turns a cosmetic cleanup into a hung test run.
func removeTempTreeBestEffort(dir string) bool {
	if dir == "" {
		return true
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = os.RemoveAll(dir)
	}()
	select {
	case <-done:
		return true
	case <-time.After(tempCleanupTimeout):
		return false
	}
}

// reapStaleTempRoots removes temp roots left behind by earlier runs, skipping
// "keep". Each removal is independently time-bounded; if the filesystem stalls
// we stop early rather than pile up even more waiting.
func reapStaleTempRoots(parent, keep string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, "run-") || name == keep {
			continue
		}
		if !removeTempTreeBestEffort(filepath.Join(parent, name)) {
			return
		}
	}
}

// tmpRoot is the parent directory for every test temp dir. Empty when the
// TestMain setup failed, in which case tmpDir() falls back to the default.
var tmpRoot string

// tmpDir returns a fresh, unique temporary directory for a test.
//
// It behaves like t.TempDir() except that the directory lives under tmpRoot and
// is NOT registered for per-test cleanup, so a cleanup-time hang cannot wedge
// the run. Everything is removed once, at the end, by TestMain.
func tmpDir(t testing.TB) string {
	t.Helper()
	if tmpRoot == "" {
		// No managed root (TestMain setup failed): use the default location
		// and accept the standard cleanup behaviour.
		return t.TempDir()
	}
	dir, err := os.MkdirTemp(tmpRoot, sanitizeForPath(t.Name())+"-*")
	if err != nil {
		t.Fatalf("MkdirTemp in %s: %v", tmpRoot, err)
	}
	return dir
}

// sanitizeForPath turns a test name into a safe single path element.
//
// Subtests are named "Parent/child", and MkdirTemp rejects a pattern containing
// a path separator, so the slashes have to go. Anything else that Windows
// dislikes in a file name is folded away too, purely defensively.
func sanitizeForPath(name string) string {
	repl := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	out := repl.Replace(name)
	if out == "" {
		out = "test"
	}
	return out
}

// removeTempTree is removeTempTreeBestEffort plus a diagnostic warning: a
// leaked temp directory is a cosmetic problem, a hung suite is not.
func removeTempTree(root string) {
	if !removeTempTreeBestEffort(root) {
		fmt.Fprintf(os.Stderr,
			"warn: temp tree cleanup timed out (leftover is harmless), leaving %s\n", root)
	}
}
