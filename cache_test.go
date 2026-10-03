package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(tmpDir(t), "cache.db")
	s, err := openSnapshotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()

	sizes := map[string]dirInfo{
		"C:\\":     {size: 100, fileCount: 2},
		"C:\\a":    {size: 60, fileCount: 1},
		"C:\\a\\b": {size: 60, fileCount: 1},
	}
	top := []TopFileEntry{
		{Path: "C:\\a\\big.bin", Size: 60, ModTime: 123},
		{Path: "C:\\med.dat", Size: 40, ModTime: 456},
	}
	at := time.Now().Truncate(time.Second)

	if err := s.save("C:\\", sizes, top, at); err != nil {
		t.Fatal(err)
	}

	gotSizes, gotTop, gotAt, ok, err := s.load("C:\\")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("load returned ok=false")
	}
	if len(gotSizes) != 3 {
		t.Fatalf("sizes len = %d, want 3", len(gotSizes))
	}
	if gotSizes["C:\\a"].size != 60 || gotSizes["C:\\a"].fileCount != 1 {
		t.Fatalf("a wrong: %+v", gotSizes["C:\\a"])
	}
	if len(gotTop) != 2 || gotTop[0].Size != 60 || gotTop[1].Path != "C:\\med.dat" {
		t.Fatalf("top wrong: %+v", gotTop)
	}
	if !gotAt.Equal(at) {
		t.Fatalf("scannedAt = %v, want %v", gotAt, at)
	}
}

func TestSnapshotNoRows(t *testing.T) {
	path := filepath.Join(tmpDir(t), "cache.db")
	s, err := openSnapshotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()

	_, _, _, ok, err := s.load("D:\\")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected ok=false for missing snapshot")
	}
}

func TestSnapshotRootIsolation(t *testing.T) {
	path := filepath.Join(tmpDir(t), "cache.db")
	s, err := openSnapshotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()

	if err := s.save("C:\\", map[string]dirInfo{"C:\\": {size: 5, fileCount: 1}}, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	sizes, _, _, ok, err := s.load("D:\\")
	if err != nil {
		t.Fatal(err)
	}
	if ok || len(sizes) != 0 {
		t.Fatal("D: must not see C: snapshot")
	}
}

func TestLoadSnapshotAppIntegration(t *testing.T) {
	path := filepath.Join(tmpDir(t), "cache.db")
	s, err := openSnapshotStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()

	sizes := map[string]dirInfo{
		"C:\\":     {size: 100, fileCount: 2},
		"C:\\Users": {size: 90, fileCount: 1},
	}
	top := []TopFileEntry{{Path: "C:\\Users\\huge.iso", Size: 90, ModTime: 7}}
	if err := s.save("C:\\", sizes, top, time.Now()); err != nil {
		t.Fatal(err)
	}

	a := &App{cache: s}
	info := a.LoadSnapshot("C:\\")
	if !info.Exists || info.Dirs != 2 {
		t.Fatalf("bad snapshot info: %+v", info)
	}
	if len(info.TopFiles) != 1 || info.TopFiles[0].Size != 90 {
		t.Fatalf("bad top files: %+v", info.TopFiles)
	}
	// Directory browsing works from restored snapshot.
	// (GetDirChildren reads the real FS; sizes come from the cache map.)
	if _, ok := a.scans.sizeMap.Load("C:\\Users"); !ok {
		t.Fatal("size map not restored")
	}
}
