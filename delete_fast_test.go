package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSubtreeAllMatch covers the "can this whole folder go in one shot" check
// that lets a temp/cache rule recycle one directory instead of thousands of
// files. It must stay conservative: anything that has to be kept, anything
// unreadable, and anything too recent must all return false.
func TestSubtreeAllMatch(t *testing.T) {
	write := func(p string, size int, age time.Duration) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		if age > 0 {
			old := time.Now().Add(-age)
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("all match", func(t *testing.T) {
		root := tmpDir(t)
		write(filepath.Join(root, "a.tmp"), 10, 0)
		write(filepath.Join(root, "sub", "b.tmp"), 20, 0)
		ok, size, files := subtreeAllMatch(root, "ext:.tmp", 0)
		if !ok || size != 30 || files != 2 {
			t.Fatalf("got ok=%v size=%d files=%d, want true/30/2", ok, size, files)
		}
	})

	t.Run("one keeper blocks the whole subtree", func(t *testing.T) {
		root := tmpDir(t)
		write(filepath.Join(root, "a.tmp"), 10, 0)
		write(filepath.Join(root, "keep.txt"), 10, 0)
		if ok, _, _ := subtreeAllMatch(root, "ext:.tmp", 0); ok {
			t.Fatal("a non-matching file must prevent a bulk move")
		}
	})

	t.Run("nested keeper blocks the whole subtree", func(t *testing.T) {
		root := tmpDir(t)
		write(filepath.Join(root, "deep", "deeper", "a.tmp"), 10, 0)
		write(filepath.Join(root, "deep", "keep.txt"), 10, 0)
		if ok, _, _ := subtreeAllMatch(root, "ext:.tmp", 0); ok {
			t.Fatal("a non-matching file deeper in the tree must prevent a bulk move")
		}
	})

	t.Run("too recent blocks the whole subtree", func(t *testing.T) {
		root := tmpDir(t)
		write(filepath.Join(root, "old.tmp"), 10, 48*time.Hour)
		write(filepath.Join(root, "new.tmp"), 10, 0)
		cutoff := time.Now().AddDate(0, 0, -1).Unix()
		if ok, _, _ := subtreeAllMatch(root, "ext:.tmp", cutoff); ok {
			t.Fatal("a file younger than the cutoff must prevent a bulk move")
		}
	})

	t.Run("all old enough", func(t *testing.T) {
		root := tmpDir(t)
		write(filepath.Join(root, "a.tmp"), 10, 48*time.Hour)
		write(filepath.Join(root, "b.tmp"), 20, 72*time.Hour)
		cutoff := time.Now().AddDate(0, 0, -1).Unix()
		ok, size, files := subtreeAllMatch(root, "ext:.tmp", cutoff)
		if !ok || size != 30 || files != 2 {
			t.Fatalf("got ok=%v size=%d files=%d, want true/30/2", ok, size, files)
		}
	})

	t.Run("empty dir is not a bulk target", func(t *testing.T) {
		root := tmpDir(t)
		if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
			t.Fatal(err)
		}
		ok, _, files := subtreeAllMatch(root, "", 0)
		if !ok || files != 0 {
			t.Fatalf("got ok=%v files=%d, want true/0 (caller skips on 0 files)", ok, files)
		}
	})
}

// TestRecordHistoryBatch verifies the batched history writer stores every row
// with the right values, so batching cannot silently drop entries.
func TestRecordHistoryBatch(t *testing.T) {
	path := filepath.Join(tmpDir(t), "cache.db")
	s, err := openSnapshotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	a := &App{cache: s}

	item := CleanItem{ID: "temp_user", Name: "用户临时文件"}
	outs := []PathOutcome{
		{Path: `C:\Temp\a.tmp`, Size: 10},
		{Path: `C:\Temp\b.tmp`, Size: 20},
		{Path: `C:\Temp\c.tmp`, Size: 30, Err: "locked"},
	}
	a.recordHistoryBatch(item, outs)
	a.recordHistoryBatch(item, nil) // must be a no-op

	entries := a.GetCleanHistory()
	if len(entries) != 3 {
		t.Fatalf("history len = %d, want 3", len(entries))
	}
	// Newest first, so c.tmp (the failure) comes back first and must be ok=0.
	if entries[0].Path != `C:\Temp\c.tmp` || entries[0].OK || entries[0].Error != "locked" {
		t.Fatalf("failed entry wrong: %+v", entries[0])
	}
	for _, e := range entries {
		if e.ItemName != "用户临时文件" || !e.OK && e.Path != `C:\Temp\c.tmp` {
			t.Fatalf("entry wrong: %+v", e)
		}
	}
}

// TestRecordHistoryBatchSpeed logs the cost of the batched writer against the
// original one-statement-per-file loop (which also re-ran the whole-table
// prune query every time). It asserts nothing — the point is that the numbers
// show up in `go test -v` output when tuning cleanup performance.
func TestRecordHistoryBatchSpeed(t *testing.T) {
	const n = 1000

	path := filepath.Join(tmpDir(t), "legacy.db")
	sLegacy, err := openSnapshotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer sLegacy.close()
	aLegacy := &App{cache: sLegacy}
	item := CleanItem{ID: "temp_user", Name: "用户临时文件"}

	legacyStart := time.Now()
	for i := 0; i < n; i++ {
		// The pre-optimisation code path: one implicit transaction plus a
		// full-table prune per file.
		_, _ = aLegacy.cache.db.Exec(
			`INSERT INTO clean_history (time, item_id, item_name, path, size, ok, error) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			time.Now().Unix(), item.ID, item.Name, `C:\Temp\legacy.tmp`, 1, 1, "")
		_, _ = aLegacy.cache.db.Exec(
			`DELETE FROM clean_history WHERE id NOT IN (SELECT id FROM clean_history ORDER BY id DESC LIMIT 10000) OR time < ?`,
			time.Now().AddDate(0, 0, -90).Unix())
	}
	legacyMs := time.Since(legacyStart).Milliseconds()

	path2 := filepath.Join(tmpDir(t), "batched.db")
	sBatch, err := openSnapshotStore(path2)
	if err != nil {
		t.Fatal(err)
	}
	defer sBatch.close()
	aBatch := &App{cache: sBatch}
	outs := make([]PathOutcome, 0, n)
	for i := 0; i < n; i++ {
		outs = append(outs, PathOutcome{Path: `C:\Temp\batched.tmp`, Size: 1})
	}
	lastHistoryPrune.Store(0)
	batchStart := time.Now()
	aBatch.recordHistoryBatch(item, outs)
	batchMs := time.Since(batchStart).Milliseconds()

	var got int
	if err := aBatch.cache.db.QueryRow(`SELECT COUNT(*) FROM clean_history`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != n {
		t.Fatalf("batched rows = %d, want %d", got, n)
	}
	t.Logf("%d history rows: legacy per-row %d ms vs batched %d ms", n, legacyMs, batchMs)
}
