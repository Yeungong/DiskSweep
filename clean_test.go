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
	for _, id := range []string{"temp_windows", "wer", "windows_logs", "wu_downloads", "delivery_opt", "prefetch", "windows_old", "vss_shadows", "win_upgrade_residue"} {
		if !admin[id] {
			t.Errorf("%s should require admin", id)
		}
	}
	// User-level caches must NOT require elevation.
	for _, id := range []string{"temp_user", "npm_cache", "pip_cache", "crash_dumps", "thumb_cache", "browser_cache", "uv_cache", "playwright_cache", "netease_cache", "thunder_cache", "douyin_cache", "blizzard_cache", "code_cache"} {
		if admin[id] {
			t.Errorf("%s should not require admin", id)
		}
	}
}

func TestNewCleanRulesCoverRealDirs(t *testing.T) {
	a := &App{}
	defs := map[string]CleanItem{}
	for _, d := range a.cleanItemDefs() {
		defs[d.ID] = d
	}
	// The cache-heavy rules added for v0.2 must exist with sensible levels.
	cases := []struct{ id, level string }{
		{"uv_cache", LevelModerate},
		{"playwright_cache", LevelModerate},
		{"netease_cache", LevelModerate},
		{"thunder_cache", LevelModerate},
		{"douyin_cache", LevelModerate},
		{"blizzard_cache", LevelModerate},
		{"tencent_cache", LevelCautious},
		{"code_cache", LevelModerate},
		{"temp_user_all", LevelSafe},
		{"vss_shadows", LevelCautious},
		{"win_upgrade_residue", LevelCautious},
		// game rules added for v0.3.x
		{"blizzard_game_cache", LevelCautious},
		{"game_crash_dumps", LevelSafe},
		{"steam_cache", LevelModerate},
		{"epic_cache", LevelModerate},
		{"riot_cache", LevelModerate},
		{"rockstar_cache", LevelModerate},
	}
	for _, c := range cases {
		item, ok := defs[c.id]
		if !ok {
			t.Errorf("missing rule %s", c.id)
			continue
		}
		if item.Level != c.level {
			t.Errorf("%s level = %q, want %q", c.id, item.Level, c.level)
		}
		if c.id != "blizzard_game_cache" && len(item.Paths) == 0 {
			t.Errorf("%s has no paths", c.id)
		}
	}
}

// TestBlizzardGameCacheProbe verifies the special numbered-folder probing:
// a game folder with numeric subdirs is detected, and non-disposable content
// (settings etc.) is NOT counted.
func TestBlizzardGameCacheProbe(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "Blizzard Entertainment", "Overwatch")
	// Numeric event folder (like "592095225" from a real Overwatch install).
	if err := os.MkdirAll(filepath.Join(base, "592095225"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "592095225", "data.bin"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	// Disposable cache dir.
	if err := os.MkdirAll(filepath.Join(base, "Cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "Cache", "c.bin"), make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}
	// Settings must NOT be counted.
	if err := os.MkdirAll(filepath.Join(base, "Settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "Settings", "config.txt"), make([]byte, 99999), 0o644); err != nil {
		t.Fatal(err)
	}
	// shop images must NOT be counted either.
	if err := os.MkdirAll(filepath.Join(base, "ShopImages"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "ShopImages", "hero.png"), make([]byte, 12345), 0o644); err != nil {
		t.Fatal(err)
	}

	// Repoint the probe at our temp root by patching localAppData is hard;
	// instead call the inner matching helpers directly and assert behavior.
	if !isNumericDirName("592095225") {
		t.Fatal("numeric dir should be recognized")
	}
	if isNumericDirName("Settings") || isNumericDirName("ShopImages") {
		t.Fatal("non-numeric dir must not be treated as event cache")
	}
	if !isBlizzardDisposableDir("Cache") || !isBlizzardDisposableDir("logs") {
		t.Fatal("cache/logs should be disposable")
	}
	if isBlizzardDisposableDir("Settings") || isBlizzardDisposableDir("ShopImages") {
		t.Fatal("settings/images must not be disposable")
	}
}

func TestParseVSSUsedBytes(t *testing.T) {
	// Simulated vssadmin output (zh-CN and en-US variants).
	zh := "已使用的空间: 5.10 GB (5476081664 字节)\n最大空间: 30.00 GB (32212254720 字节)"
	en := "Used Space: 1.50 GB (1610612736 bytes)\nMaximum Space: 10.00 GB (10737418240 bytes)"
	if got := parseVSSUsedBytes(zh); got != 5476081664 {
		t.Fatalf("zh parse = %d, want 5476081664", got)
	}
	if got := parseVSSUsedBytes(en); got != 1610612736 {
		t.Fatalf("en parse = %d, want 1610612736", got)
	}
	if got := parseVSSUsedBytes("no numbers here"); got != 0 {
		t.Fatalf("empty parse = %d, want 0", got)
	}
}
