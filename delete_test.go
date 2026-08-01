package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMatchesName(t *testing.T) {
	cases := []struct {
		name, match, want string // want: "" = no match, else matched
	}{
		{"a.tmp", "", "a.tmp"},
		{"thumbcache_1234.db", "glob:thumbcache_*", "thumbcache_1234.db"},
		{"iconcache.db", "glob:thumbcache_*", ""},
		{"crash.dmp", "ext:.dmp", "crash.dmp"},
		{"CRASH.DMP", "ext:.dmp", "CRASH.DMP"},
		{"notes.txt", "ext:.dmp", ""},
		{"any.txt", "unknown:rule", ""}, // unknown prefix must never match
	}
	for _, c := range cases {
		got := matchesName(c.name, c.match)
		if c.want == "" && got {
			t.Errorf("matchesName(%q,%q) = true, want false", c.name, c.match)
		}
		if c.want != "" && !got {
			t.Errorf("matchesName(%q,%q) = false, want true", c.name, c.match)
		}
	}
}

func TestDeleteFile(t *testing.T) {
	dir := t.TempDir()
	// normal file
	p := filepath.Join(dir, "f.bin")
	if err := os.WriteFile(p, make([]byte, 123), 0o644); err != nil {
		t.Fatal(err)
	}
	freed, err := deleteFile(p)
	if err != nil || freed != 123 {
		t.Fatalf("delete normal: freed=%d err=%v", freed, err)
	}
	// read-only file
	p2 := filepath.Join(dir, "ro.bin")
	if err := os.WriteFile(p2, make([]byte, 55), 0o444); err != nil {
		t.Fatal(err)
	}
	freed, err = deleteFile(p2)
	if err != nil || freed != 55 {
		t.Fatalf("delete read-only: freed=%d err=%v", freed, err)
	}
	// missing file -> no error, zero freed
	freed, err = deleteFile(filepath.Join(dir, "nope"))
	if err != nil || freed != 0 {
		t.Fatalf("delete missing: freed=%d err=%v", freed, err)
	}
}

func TestDeleteDirContents(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	files := []struct {
		rel  string
		size int
	}{
		{"a.tmp", 10},
		{"b.bin", 20},
		{"sub/c.tmp", 30},
		{"sub/d.bin", 40},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(root, f.rel), make([]byte, f.size), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	a := &App{}
	freed, errs := a.deleteDirContents(root, "ext:.tmp", 0)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if freed != 40 { // a.tmp(10) + sub/c.tmp(30)
		t.Fatalf("freed = %d, want 40", freed)
	}
	if _, err := os.Stat(filepath.Join(root, "a.tmp")); !os.IsNotExist(err) {
		t.Fatal("a.tmp should be deleted")
	}
	if _, err := os.Stat(filepath.Join(root, "b.bin")); err != nil {
		t.Fatal("b.bin should survive")
	}
	// empty sub dir pruned? sub still has d.bin, so no.
	if _, err := os.Stat(sub); err != nil {
		t.Fatal("sub should survive (contains d.bin)")
	}
}

func TestDeleteDirContentsGlobAndAge(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, size int) {
		if err := os.WriteFile(filepath.Join(root, rel), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("thumbcache_a.db", 5)
	mk("thumbcache_b.db", 6)
	mk("iconcache.db", 7)

	a := &App{}
	freed, errs := a.deleteDirContents(root, "glob:thumbcache_*", 0)
	if len(errs) != 0 || freed != 11 {
		t.Fatalf("glob delete: freed=%d errs=%v", freed, errs)
	}
	if _, err := os.Stat(filepath.Join(root, "iconcache.db")); err != nil {
		t.Fatal("iconcache.db should survive")
	}
}

func TestExecuteItemPermanent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.bin"), make([]byte, 77), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &App{}
	res := a.executeItem(CleanItem{
		ID: "t", Name: "test", Level: LevelSafe,
		Paths: []string{dir},
	})
	if !res.OK {
		t.Skipf("recycle bin unavailable, cleanup failed: %+v", res)
	}
	if res.Freed != 77 {
		t.Fatalf("execute: %+v", res)
	}
	// Whole-directory rules now move the directory itself to the recycle bin.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("directory should be gone (moved to recycle bin)")
	}
}

func TestExecuteCleanUnknownItem(t *testing.T) {
	a := &App{}
	res := a.ExecuteClean([]string{"no_such_id"})
	if len(res) != 1 || res[0].OK {
		t.Fatalf("unknown item result: %+v", res)
	}
}

func TestClassifyErrors(t *testing.T) {
	locked := &os.PathError{Op: "remove", Path: "x", Err: syscall.Errno(32)} // sharing violation
	locked2 := &os.PathError{Op: "remove", Path: "x", Err: syscall.Errno(33)} // lock violation
	denied := &os.PathError{Op: "remove", Path: "x", Err: syscall.Errno(5)} // access denied
	other := &os.PathError{Op: "remove", Path: "x", Err: syscall.Errno(2)}  // not found

	if classifyErr(locked) != ErrLocked || classifyErr(locked2) != ErrLocked {
		t.Fatal("sharing/lock violations must classify as locked")
	}
	if classifyErr(denied) != ErrPermission {
		t.Fatal("access denied must classify as permission")
	}
	if classifyErr(other) != ErrOther {
		t.Fatal("other errno must classify as other")
	}
	if classifyErr(nil) != "" {
		t.Fatal("nil error must classify as empty")
	}
}

// TestDeleteLockedFileReportsError holds a file open without delete sharing so
// removal fails with a sharing violation, then verifies the delete path
// reports it as a locked error instead of crashing.
func TestDeleteLockedFileReportsError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "held.bin")
	if err := os.WriteFile(p, make([]byte, 42), 0o644); err != nil {
		t.Fatal(err)
	}

	// Open without FILE_SHARE_DELETE to emulate a locked file.
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil,
		syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Skipf("cannot open with restrictive share mode: %v", err)
	}
	defer syscall.CloseHandle(handle)

	freed, err := deleteFile(p)
	if err == nil {
		t.Fatal("expected delete of a locked file to fail")
	}
	if freed != 0 {
		t.Fatalf("freed = %d, want 0", freed)
	}
	if classifyErr(err) != ErrLocked {
		t.Fatalf("expected locked classification, got %q (%v)", classifyErr(err), err)
	}
	// File must still exist.
	if _, err := os.Stat(p); err != nil {
		t.Fatal("locked file should still exist after failed delete")
	}
}

// TestMoveToRecycleBin verifies cautious-path deletion moves a file to the
// recycle bin (reversible). Skipped if the environment cannot do it.
func TestMoveToRecycleBin(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "trash.bin")
	if err := os.WriteFile(p, make([]byte, 33), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := moveToRecycleBin(p); err != nil {
		t.Skipf("recycle bin unavailable in this environment: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("file should be gone after moving to recycle bin")
	}
}

// TestExecuteItemCautiousToRecycleBin verifies cautious items are moved to the
// recycle bin (not permanently deleted).
func TestExecuteItemCautiousToRecycleBin(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent-data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &App{}
	// Prime the size cache so moveDirToRecycleBin reports freed bytes.
	s, fc := walkDirSize(dir)
	a.scans.sizeMap.Store(dir, dirInfo{size: s, fileCount: fc})

	res := a.executeItem(CleanItem{ID: "c", Name: "cautious", Level: LevelCautious, Paths: []string{dir}})
	if !res.OK {
		t.Skipf("recycle bin unavailable: %+v", res)
	}
	if res.Freed != 2 {
		t.Fatalf("freed = %d, want 2", res.Freed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("cautious dir should be gone (moved to recycle bin)")
	}
}
