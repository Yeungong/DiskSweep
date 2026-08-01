package main

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"
	"time"
)

// waitScan waits until the background scan finishes (or times out).
func waitScan(a *App, t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for a.IsScanning() {
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish within 30s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func buildTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(rel string, size int) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("a/b/c.txt", 100)
	mk("a/d.txt", 50)
	mk("e.txt", 200)
	mk(".hidden/x.bin", 300)
	return root
}

func TestScanSizesAndChildren(t *testing.T) {
	root := buildTree(t)
	a := &App{}
	a.scans.emit = func(string, interface{}) {}

	if err := a.StartScan(root, 100); err != nil {
		t.Fatal(err)
	}
	waitScan(a, t)

	// Root totals
	if v, ok := a.scans.sizeMap.Load(root); ok {
		di := v.(dirInfo)
		if di.size != 650 {
			t.Fatalf("root size = %d, want 650", di.size)
		}
		if di.fileCount != 4 {
			t.Fatalf("root fileCount = %d, want 4", di.fileCount)
		}
	} else {
		t.Fatal("root missing from sizeMap")
	}

	// Child aggregation
	check := func(rel string, wantSize int64, wantFiles int) {
		v, ok := a.scans.sizeMap.Load(filepath.Join(root, rel))
		if !ok {
			t.Fatalf("%s missing from sizeMap", rel)
		}
		di := v.(dirInfo)
		if di.size != wantSize {
			t.Fatalf("%s size = %d, want %d", rel, di.size, wantSize)
		}
		if di.fileCount != wantFiles {
			t.Fatalf("%s fileCount = %d, want %d", rel, di.fileCount, wantFiles)
		}
	}
	check("a", 150, 2)
	check("a/b", 100, 1)
	check(".hidden", 300, 1)

	// GetDirChildren lists immediate children
	children := a.GetDirChildren(root)
	if len(children) != 3 {
		t.Fatalf("root children = %d, want 3", len(children))
	}
	for _, c := range children {
		if c.Name == "a" {
			if !c.IsDir || c.Size != 150 || c.FileCount != 2 {
				t.Fatalf("child a wrong: %+v", c)
			}
		}
		if c.Name == "e.txt" {
			if c.IsDir || c.Size != 200 {
				t.Fatalf("child e.txt wrong: %+v", c)
			}
		}
	}
}

func TestScanSymlinkNotFollowed(t *testing.T) {
	root := t.TempDir()
	// Create a target dir with a big file.
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "big.bin"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a symlink loop: root/loop -> root
	link := filepath.Join(root, "loop")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("cannot create symlink (no privilege): %v", err)
	}

	a := &App{}
	a.scans.emit = func(string, interface{}) {}
	if err := a.StartScan(root, 100); err != nil {
		t.Fatal(err)
	}
	waitScan(a, t)

	v, ok := a.scans.sizeMap.Load(root)
	if !ok {
		t.Fatal("root missing")
	}
	di := v.(dirInfo)
	// Without symlink resolution: target/ (1024) only. With a loop we would
	// either hang or double-count; the scan must terminate and size must be sane.
	if di.size != 1024 {
		t.Fatalf("root size = %d, want 1024 (symlink must not be followed)", di.size)
	}
	if di.fileCount != 1 {
		t.Fatalf("root fileCount = %d, want 1", di.fileCount)
	}
}

func TestScanMissingDirDoesNotCrash(t *testing.T) {
	a := &App{}
	a.scans.emit = func(string, interface{}) {}

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := a.StartScan(missing, 100); err != nil {
		t.Fatal(err)
	}
	waitScan(a, t)

	if a.scans.errs == 0 {
		t.Fatal("expected an error to be counted for the missing root")
	}
	if a.scans.scanning.Load() {
		t.Fatal("scan should have stopped")
	}
}

// TestScanAccessDeniedDirDoesNotCrash builds a real access-denied directory via
// icacls and verifies the scan completes without crashing and without
// over-counting the blocked subtree.
func TestScanAccessDeniedDirDoesNotCrash(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "secret.bin"), make([]byte, 999), 0o644); err != nil {
		t.Fatal(err)
	}

	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	deny := exec.Command("icacls", blocked, "/deny", u.Username+":(OI)(CI)F")
	if out, err := deny.CombinedOutput(); err != nil {
		t.Skipf("icacls deny failed (%v): %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("icacls", blocked, "/remove:d", u.Username).Run()
	})

	a := &App{}
	a.scans.emit = func(string, interface{}) {}
	if err := a.StartScan(root, 10); err != nil {
		t.Fatal(err)
	}
	waitScan(a, t)

	// Scan must terminate and never see the blocked file's 999 bytes.
	if a.scans.scanning.Load() {
		t.Fatal("scan stuck on denied directory")
	}
	if v, ok := a.scans.sizeMap.Load(root); ok {
		if di := v.(dirInfo); di.size == 999 {
			t.Fatal("blocked subtree was counted despite access denial")
		}
	}
}
