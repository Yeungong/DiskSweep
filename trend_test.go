package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordAndQueryDiskSnapshot(t *testing.T) {
	path := filepath.Join(tmpDir(t), "test.db")
	s, err := openSnapshotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()

	drives := []DriveInfo{
		{Drive: "C:", Total: 100 << 30, Used: 85 << 30, Free: 15 << 30},
	}
	sizes := map[string]dirInfo{
		`C:\Windows`:  {size: 30 << 30, fileCount: 100},
		`C:\AppData`:  {size: 10 << 30, fileCount: 50},
		`C:\Programs`: {size: 5 << 30, fileCount: 20},
	}
	day1 := time.Date(2026, 8, 10, 10, 0, 0, 0, time.Local)
	if err := s.recordDiskSnapshot(drives, sizes, day1); err != nil {
		t.Fatal(err)
	}

	// Day 2: free dropped to 9G; Programs grew.
	drives[0].Free = 9 << 30
	drives[0].Used = 91 << 30
	sizes[`C:\Windows`] = dirInfo{size: 31 << 30, fileCount: 102}
	sizes[`C:\AppData`] = dirInfo{size: 11 << 30, fileCount: 55}
	sizes[`C:\Programs`] = dirInfo{size: 6 << 30, fileCount: 21}
	sizes[`C:\NewStuff`] = dirInfo{size: 2 << 30, fileCount: 8}
	day2 := time.Date(2026, 8, 11, 18, 0, 0, 0, time.Local)
	if err := s.recordDiskSnapshot(drives, sizes, day2); err != nil {
		t.Fatal(err)
	}

	trend, err := s.diskTrend("C:")
	if err != nil {
		t.Fatal(err)
	}
	if len(trend) != 2 {
		t.Fatalf("trend = %d rows, want 2", len(trend))
	}
	if trend[0].Day != "2026-08-10" || trend[0].Free != 15<<30 {
		t.Errorf("trend[0] = %+v", trend[0])
	}
	if trend[1].Day != "2026-08-11" || trend[1].Free != 9<<30 {
		t.Errorf("trend[1] = %+v", trend[1])
	}

	deltas, err := s.dirDiffs(0) // no threshold for the test
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]DirDelta{}
	for _, d := range deltas {
		byPath[d.Path] = d
	}
	// Windows grew by 1G
	if d, ok := byPath[`C:\Windows`]; !ok || d.Delta != 1<<30 {
		t.Errorf("Windows delta = %+v (ok=%v)", d, ok)
	}
	// NewStuff appeared (+2G)
	if d, ok := byPath[`C:\NewStuff`]; !ok || d.Delta != 2<<30 {
		t.Errorf("NewStuff delta = %+v (ok=%v)", d, ok)
	}
}

// TestRecordSameDayReplaces verifies a second scan on the same day overwrites
// the previous snapshot instead of appending.
func TestRecordSameDayReplaces(t *testing.T) {
	path := filepath.Join(tmpDir(t), "test.db")
	s, err := openSnapshotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	drives := []DriveInfo{{Drive: "C:", Total: 100 << 30, Used: 1, Free: 99 << 30}}
	at := time.Date(2026, 8, 12, 9, 0, 0, 0, time.Local)
	if err := s.recordDiskSnapshot(drives, map[string]dirInfo{}, at); err != nil {
		t.Fatal(err)
	}
	at2 := time.Date(2026, 8, 12, 21, 0, 0, 0, time.Local)
	if err := s.recordDiskSnapshot(drives, map[string]dirInfo{}, at2); err != nil {
		t.Fatal(err)
	}
	trend, _ := s.diskTrend("C:")
	if len(trend) != 1 {
		t.Fatalf("same-day scans should collapse to 1 row, got %d", len(trend))
	}
}

// TestDirDiffsNeedsTwoDays verifies no deltas when only one snapshot exists.
func TestDirDiffsNeedsTwoDays(t *testing.T) {
	path := filepath.Join(tmpDir(t), "test.db")
	s, err := openSnapshotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	drives := []DriveInfo{{Drive: "C:", Total: 1 << 30, Used: 1, Free: 1}}
	if err := s.recordDiskSnapshot(drives, map[string]dirInfo{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	deltas, err := s.dirDiffs(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 0 {
		t.Fatalf("expected no deltas with one snapshot, got %d", len(deltas))
	}
}

func TestBaseNameNormalization(t *testing.T) {
	cases := map[string]string{
		"Microsoft .NET Runtime 8.0.21":      ".NET Runtime",
		"Microsoft .NET Runtime 8.0.29 (x64)": ".NET Runtime",
		"Microsoft .NET SDK 8.0.429":         ".NET SDK",
		"Microsoft Visual C++ 2015-2022 Redistributable (x64) - 14.42.34433": "Microsoft Visual C++ Redistributable",
	}
	// Use an App with DuplicateApps's baseName via a small re-implementation
	// to keep the test hermetic (no registry access needed).
	for name, want := range cases {
		got := normalizeAppBaseName(name)
		if got != want {
			t.Errorf("%q -> %q, want %q", name, got, want)
		}
	}
}

func TestDuplicateAppsRuns(t *testing.T) {
	a := &App{}
	groups := a.DuplicateApps()
	// Should not panic and should produce some result on a real machine.
	t.Logf("duplicate groups: %d", len(groups))
	for _, g := range groups {
		t.Logf("  %s x%d (%d MB)", g.BaseName, g.Count, g.TotalSize)
	}
	_ = os.Getenv // keep import used if trimmed
}
