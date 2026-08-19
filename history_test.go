package main

import (
	"encoding/binary"
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

// TestParseRecycleMeta verifies the $I metadata parser on a synthetic file
// using the real Windows 10 layout (magic, size, FILETIME, pathlen, path).
func TestParseRecycleMeta(t *testing.T) {
	dir := t.TempDir()
	metaPath := filepath.Join(dir, "$I000001")
	origPath := `C:/Users/yeung/AppData/Local/Temp/test.bin`
	pathBytes := []byte{}
	for _, r := range origPath {
		pathBytes = append(pathBytes, byte(r), byte(r>>8))
	}
	pathBytes = append(pathBytes, 0, 0)
	head := make([]byte, 28)
	head[0] = 2 // magic
	binary.LittleEndian.PutUint64(head[8:16], 12345)
	binary.LittleEndian.PutUint32(head[24:28], uint32(len(origPath)))
	data := append(head, pathBytes...)
	if err := os.WriteFile(metaPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, size, err := parseRecycleMeta(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != origPath {
		t.Errorf("path = %q, want %q", got, origPath)
	}
	if size != 12345 {
		t.Errorf("size = %d, want 12345", size)
	}
}

// TestRestoreFromRecycleDir simulates a recycle-bin folder: $I metadata +
// $R data file; the data file must be moved back to the original location.
func TestRestoreFromRecycleDir(t *testing.T) {
	bin := t.TempDir()
	origDir := filepath.Join(t.TempDir(), "sub")
	if err := os.MkdirAll(origDir, 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := filepath.Join(origDir, "report.txt")
	// Build $I metadata pointing at origPath.
	metaPath := filepath.Join(bin, "$IABC123")
	pathBytes := []byte{}
	for _, r := range origPath {
		pathBytes = append(pathBytes, byte(r), byte(r>>8))
	}
	pathBytes = append(pathBytes, 0, 0)
	head := make([]byte, 28)
	head[0] = 2
	binary.LittleEndian.PutUint32(head[24:28], uint32(len(origPath)))
	data := append(head, pathBytes...)
	if err := os.WriteFile(metaPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// $R data file with content.
	dataPath := filepath.Join(bin, "$RABC123")
	if err := os.WriteFile(dataPath, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := restoreFromRecycleDir(bin, origPath); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	// Original file restored.
	if _, err := os.Stat(origPath); err != nil {
		t.Fatal("file not restored to original path")
	}
	// Metadata removed, data gone from bin.
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Error("metadata should be removed after restore")
	}
	if _, err := os.Stat(dataPath); !os.IsNotExist(err) {
		t.Error("data file should be moved out of the bin")
	}
}

// TestRestoreFromRecycleDirNotFound verifies a non-matching target returns
// the not-in-bin sentinel and leaves files untouched.
func TestRestoreFromRecycleDirNotFound(t *testing.T) {
	bin := t.TempDir()
	if err := restoreFromRecycleDir(bin, `C:/no/such/file.txt`); err != errNotInRecycleBin {
		t.Fatalf("err = %v, want errNotInRecycleBin", err)
	}
}
