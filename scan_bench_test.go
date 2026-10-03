package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestScanPhaseTiming compares the old two-pass scanner (enumerate then
// scanDirs) against the new single-pass concurrent walker on the same tree.
func TestScanPhaseTiming(t *testing.T) {
	if os.Getenv("DS_BENCH_SCAN") == "" {
		t.Skip("set DS_BENCH_SCAN=1 to run the scan phase benchmark")
	}
	root := os.Getenv("DS_BENCH_ROOT")
	if root == "" {
		root = `C:\Users\yeung\AppData\Local`
	}
	t.Logf("root = %s", root)

	// --- new single-pass walker ---
	newApp := &App{}
	newApp.topFiles = newTopFilesCollector(200)
	newApp.scans.emit = func(string, interface{}) {}
	t0 := time.Now()
	newDirs := newApp.walkAndScan(root)
	newMs := time.Since(t0).Milliseconds()
	t.Logf("NEW walkAndScan: %d dirs, %d files, %.2f GB in %d ms",
		len(newDirs), newApp.scans.files, float64(newApp.scans.bytes)/1024/1024/1024, newMs)

	if os.Getenv("DS_BENCH_OLD") == "" {
		return
	}

	// --- old two-pass scanner, for comparison ---
	oldApp := &App{}
	oldApp.topFiles = newTopFilesCollector(200)
	oldApp.scans.emit = func(string, interface{}) {}
	o0 := time.Now()
	oldDirs := oldApp.enumerateDirs(root)
	enumMs := time.Since(o0).Milliseconds()
	oldApp.scanDirs(oldDirs)
	oldMs := time.Since(o0).Milliseconds()
	t.Logf("OLD enumerate:   %d dirs in %d ms", len(oldDirs), enumMs)
	t.Logf("OLD enumerate+scan total: %d dirs, %d files, %.2f GB in %d ms",
		len(oldDirs), oldApp.scans.files, float64(oldApp.scans.bytes)/1024/1024/1024, oldMs)

	if oldMs > 0 {
		t.Logf("SPEEDUP: %.2fx (%d ms -> %d ms)", float64(oldMs)/float64(newMs), oldMs, newMs)
	}
	if len(oldDirs) != len(newDirs) || oldApp.scans.files != newApp.scans.files || oldApp.scans.bytes != newApp.scans.bytes {
		t.Errorf("walkers disagree: dirs %d vs %d, files %d vs %d, bytes %d vs %d",
			len(oldDirs), len(newDirs), oldApp.scans.files, newApp.scans.files,
			oldApp.scans.bytes, newApp.scans.bytes)
	}
}

// TestWalkAndScanMatchesOldScanner verifies the new single-pass walker
// produces exactly the same directory set and totals as the two-pass version,
// on a synthetic tree (no environment dependency, always runs).
func TestWalkAndScanMatchesOldScanner(t *testing.T) {
	root := tmpDir(t)
	// Build a tree with nested dirs, empty dirs, and files with known sizes.
	for _, rel := range []string{
		"a/1.txt", "a/2.txt", "a/deep/3.txt", "a/deep/deeper/4.txt",
		"b/5.txt", "b/sub/6.txt",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, 10), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	nw := &App{}
	nw.topFiles = newTopFilesCollector(200)
	nw.scans.emit = func(string, interface{}) {}
	newDirs := nw.walkAndScan(root)
	nw.aggregateSizes(newDirs)

	old := &App{}
	old.topFiles = newTopFilesCollector(200)
	old.scans.emit = func(string, interface{}) {}
	oldDirs := old.enumerateDirs(root)
	old.scanDirs(oldDirs)
	old.aggregateSizes(oldDirs)

	if len(newDirs) != len(oldDirs) {
		t.Fatalf("dirs: new %d vs old %d", len(newDirs), len(oldDirs))
	}
	if nw.scans.files != old.scans.files || nw.scans.bytes != old.scans.bytes {
		t.Fatalf("totals: new files=%d bytes=%d vs old files=%d bytes=%d",
			nw.scans.files, nw.scans.bytes, old.scans.files, old.scans.bytes)
	}
	// Every directory must carry the same aggregated size in both walkers.
	for _, d := range oldDirs {
		nv, nok := nw.scans.sizeMap.Load(d)
		ov, ook := old.scans.sizeMap.Load(d)
		if !nok || !ook {
			t.Fatalf("dir %s missing from a walker: new=%v old=%v", d, nok, ook)
		}
		if nv.(dirInfo) != ov.(dirInfo) {
			t.Fatalf("dir %s: new %+v vs old %+v", d, nv.(dirInfo), ov.(dirInfo))
		}
	}
	if nw.topFiles.result() == nil {
		t.Fatal("top files must not be nil")
	}
}

// TestTopFilesZeroValue guards the panic that a zero-value collector used to
// hit (heap[0] on an empty heap when limit was never set).
func TestTopFilesZeroValue(t *testing.T) {
	var c topFilesCollector // never initialised
	for i := 0; i < 500; i++ {
		c.add("f", int64(i+1), 0)
	}
	got := c.result()
	if len(got) == 0 {
		t.Fatal("zero-value collector must accept entries")
	}
	if len(got) > 200 {
		t.Fatalf("zero-value collector kept %d entries, want <= 200", len(got))
	}
	// Largest first.
	if got[0].Size != 500 {
		t.Fatalf("largest = %d, want 500", got[0].Size)
	}
}

// TestLocalTopFilesMerge verifies the per-worker buffers merge correctly and
// that the merged result equals what a single shared collector would hold.
func TestLocalTopFilesMerge(t *testing.T) {
	const limit = 10
	var shared topFilesCollector
	shared.reset(limit)

	// Two "workers" with disjoint candidate sets.
	l1 := newLocalTopFiles(limit)
	l2 := newLocalTopFiles(limit)
	for i := 1; i <= 100; i++ {
		l1.add("a", int64(i*2), 0)   // even sizes 2..200
		l2.add("b", int64(i*2+1), 0) // odd sizes 3..201
		shared.add("a", int64(i*2), 0)
		shared.add("b", int64(i*2+1), 0)
	}
	var merged topFilesCollector
	merged.reset(limit)
	merged.merge(l1)
	merged.merge(l2)

	want := shared.result()
	got := merged.result()
	if len(got) != len(want) {
		t.Fatalf("merged %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Size != want[i].Size {
			t.Fatalf("entry %d: merged size %d, want %d", i, got[i].Size, want[i].Size)
		}
	}
}

// TestEnumerateCost isolates directory enumeration on a directory full of
// small files, which is what makes a cache scan feel slow.
func TestEnumerateCost(t *testing.T) {
	if os.Getenv("DS_BENCH_SCAN") == "" {
		t.Skip("set DS_BENCH_SCAN=1 to run the enumerate benchmark")
	}
	root := filepath.Clean(os.TempDir())
	t0 := time.Now()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Skipf("cannot read %s: %v", root, err)
	}
	t.Logf("os.ReadDir(%s): %d entries in %d ms", root, len(entries), time.Since(t0).Milliseconds())
}
