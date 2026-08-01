package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanItemDefsSanity(t *testing.T) {
	a := &App{}
	defs := a.cleanItemDefs()
	if len(defs) < 10 {
		t.Fatalf("expected a substantial rule set, got %d", len(defs))
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if d.ID == "" || d.Name == "" {
			t.Fatalf("rule missing id/name: %+v", d)
		}
		if seen[d.ID] {
			t.Fatalf("duplicate rule id: %s", d.ID)
		}
		seen[d.ID] = true
		if len(d.Paths) == 0 {
			t.Fatalf("rule %s has no paths", d.ID)
		}
		if d.Level != LevelSafe && d.Level != LevelModerate && d.Level != LevelCautious {
			t.Fatalf("rule %s has bad level %q", d.ID, d.Level)
		}
	}
	// Levels must span all three buckets.
	levels := map[string]bool{}
	for _, d := range defs {
		levels[d.Level] = true
	}
	for _, l := range []string{LevelSafe, LevelModerate, LevelCautious} {
		if !levels[l] {
			t.Fatalf("missing level %q in rules", l)
		}
	}
}

func TestWalkDirSize(t *testing.T) {
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
	mk("a/b/c.bin", 100)
	mk("a/d.bin", 50)
	mk("e.bin", 200)

	s, f := walkDirSize(root)
	if s != 350 || f != 3 {
		t.Fatalf("walk = %d bytes / %d files, want 350/3", s, f)
	}
	// Missing dir returns zeroes.
	if s2, f2 := walkDirSize(filepath.Join(root, "nope")); s2 != 0 || f2 != 0 {
		t.Fatalf("missing dir walk = %d/%d, want 0/0", s2, f2)
	}
}

func TestCleanupItemsProbesRealMachine(t *testing.T) {	a := &App{}
	// Probe only the fast, always-present paths (a full CleanupItems() walk of
	// e.g. a multi-GB .codex directory would be too slow for a unit test).
	temp := CleanItem{Paths: []string{filepath.Join(localAppData(), "Temp")}}
	a.probeItem(&temp)
	if !temp.Exists {
		t.Fatal("TEMP dir must exist on Windows")
	}

	rb := CleanItem{Paths: a.recycleBinPaths()}
	a.probeItem(&rb)
	if !rb.Exists {
		t.Fatal("recycle bin must exist")
	}
	if rb.Size < 0 {
		t.Fatal("negative recycle bin size")
	}
}

func TestProbeItemWithCache(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "cache")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "x.bin"), make([]byte, 77), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &App{}
	// Prime the scan cache for sub (live walk fallback would also work, but we
	// want to exercise the cached path).
	s, fc := walkDirSize(sub)
	a.scans.sizeMap.Store(sub, dirInfo{size: s, fileCount: fc})

	item := CleanItem{Paths: []string{sub}}
	a.probeItem(&item)
	if !item.Exists || item.Size != 77 || item.FileCount != 1 {
		t.Fatalf("probe wrong: %+v", item)
	}

	// Missing path keeps Exists=false.
	item2 := CleanItem{Paths: []string{filepath.Join(root, "missing")}}
	a.probeItem(&item2)
	if item2.Exists {
		t.Fatal("missing path reported as existing")
	}
}

func TestRequiresAdminFlags(t *testing.T) {
	a := &App{}
	admin := map[string]bool{}
	for _, d := range a.cleanItemDefs() {
		if d.RequiresAdmin {
			admin[d.ID] = true
		}
	}
	// System-level paths must request elevation.
	for _, id := range []string{"temp_windows", "wer", "windows_logs", "wu_downloads", "delivery_opt", "prefetch", "windows_old"} {
		if !admin[id] {
			t.Errorf("%s should require admin", id)
		}
	}
	// User-level caches must NOT require elevation.
	for _, id := range []string{"temp_user", "npm_cache", "pip_cache", "crash_dumps", "thumb_cache", "browser_cache"} {
		if admin[id] {
			t.Errorf("%s should not require admin", id)
		}
	}
}
