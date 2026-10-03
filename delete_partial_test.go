package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRecycleBatchPartialLocked verifies the partial-failure handling: when a
// batch recycle move fails because one file is locked, the other files that
// were actually moved are counted as freed and NOT reported as errors, while
// the locked one is retried individually and reported as locked.
//
// Gated behind DS_SHELL_TESTS=1, matching the other tests that drive the real
// shell/filesystem (see DS_EXEC_CLEAN, DS_PURGE_UPDATERS).
//
// It is off by default because SHFileOperationW is documented to require a
// thread with a message pump. In a headless `go test` process there is none, so
// the call blocks forever instead of returning -- which surfaced as
// "panic: test timed out" with the stack parked in syscall.(*LazyProc).Call.
// The production app is unaffected: it runs inside the Wails event loop.
func TestRecycleBatchPartialLocked(t *testing.T) {
	if os.Getenv("DS_SHELL_TESTS") != "1" {
		t.Skip("set DS_SHELL_TESTS=1 to run tests that drive the shell recycle bin")
	}
	dir := tmpDir(t)
	names := []string{"f1.dat", "f2.dat", "f3.dat"}
	for i, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), make([]byte, 100*(i+1)), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	// Lock f3 exclusively so SHFileOperation cannot move it.
	lockedPath := filepath.Join(dir, "f3.dat")
	h, err := os.OpenFile(lockedPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	a := &App{}
	freed, errs, handled := a.recycleMatchingFiles(dir, "", 0)

	if freed != 300 { // f1+f2 moved (100+200); f3 stays
		t.Errorf("freed = %d, want 300", freed)
	}
	for _, e := range errs {
		if filepath.Base(e.Path) == "f3.dat" {
			if e.Kind != ErrLocked {
				t.Errorf("f3 kind = %q, want %q (err=%v)", e.Kind, ErrLocked, e.Error)
			}
		} else {
			t.Errorf("unexpected error for %s: %+v", e.Path, e)
		}
	}
	// f1/f2 must be gone (moved to recycle bin), f3 still present.
	if _, err := os.Lstat(filepath.Join(dir, "f1.dat")); !os.IsNotExist(err) {
		t.Errorf("f1.dat still exists after recycle (err=%v)", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "f2.dat")); !os.IsNotExist(err) {
		t.Errorf("f2.dat still exists after recycle (err=%v)", err)
	}
	if _, err := os.Lstat(lockedPath); err != nil {
		t.Errorf("f3.dat should still exist (err=%v)", err)
	}
	_ = handled
}
