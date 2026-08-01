package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryRecordAndList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	s, err := openSnapshotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	a := &App{cache: s}

	item := CleanItem{ID: "temp_user", Name: "用户临时文件"}
	a.recordHistory(item, `C:\Users\x\Temp\a.tmp`, 123, true, "")
	a.recordHistory(item, `C:\Users\x\Temp\b.tmp`, 456, false, "locked")

	entries := a.GetCleanHistory()
	if len(entries) != 2 {
		t.Fatalf("history len = %d, want 2", len(entries))
	}
	// Newest first.
	if entries[0].Path != `C:\Users\x\Temp\b.tmp` || entries[0].OK || entries[0].Error != "locked" {
		t.Fatalf("newest entry wrong: %+v", entries[0])
	}
	if entries[1].Size != 123 || entries[1].ItemName != "用户临时文件" {
		t.Fatalf("older entry wrong: %+v", entries[1])
	}
}

func TestHistoryNilCache(t *testing.T) {
	a := &App{} // no cache store
	a.recordHistory(CleanItem{ID: "x", Name: "x"}, "C:\\nope", 1, true, "")
	if got := a.GetCleanHistory(); len(got) != 0 {
		t.Fatal("nil cache must yield empty history")
	}
}

// TestRestoreHistory does a full round trip on the real recycle bin:
// record -> move to bin -> restore from bin -> file is back.
func TestRestoreHistory(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "important.txt")
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "cache.db")
	s, err := openSnapshotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	a := &App{cache: s}

	a.recordHistory(CleanItem{ID: "t", Name: "测试"}, p, 4, true, "")
	entries := a.GetCleanHistory()
	if len(entries) == 0 {
		t.Fatal("no history entry recorded")
	}
	id := entries[0].ID

	if err := moveToRecycleBin(p); err != nil {
		t.Skipf("recycle bin unavailable: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("file should be gone after move to recycle bin")
	}

	res := a.RestoreHistory(id)
	if !res.OK {
		t.Fatalf("restore failed: %+v", res)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("restored file missing at original path")
	}
	// Restoring twice should report it as already restored.
	res2 := a.RestoreHistory(id)
	if res2.OK {
		t.Fatal("second restore should not succeed")
	}
}

// TestRestoreHistoryMissingInBin restores a record whose file is no longer in
// the recycle bin (simulated: never moved there).
func TestRestoreHistoryMissingInBin(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "ghost.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "cache.db")
	s, err := openSnapshotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	a := &App{cache: s}

	a.recordHistory(CleanItem{ID: "t", Name: "测试"}, p, 1, true, "")
	id := a.GetCleanHistory()[0].ID

	res := a.RestoreHistory(id)
	if res.OK {
		t.Fatal("restore must fail: file was never moved to the recycle bin")
	}
	if res.Message == "" {
		t.Fatal("expected a message explaining the failure")
	}
}

func TestMovePathsToRecycleBinBatch(t *testing.T) {
	root := t.TempDir()
	var paths []string
	for i := 0; i < 5; i++ {
		p := filepath.Join(root, fmt.Sprintf("f%d.tmp", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if err := movePathsToRecycleBin(paths); err != nil {
		t.Skipf("recycle bin unavailable: %v", err)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("file still exists after batch recycle: %s", p)
		}
	}
}
