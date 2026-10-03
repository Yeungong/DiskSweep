package main

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateConfig redirects config/cache paths to a temp dir for a test.
func isolateConfig(t *testing.T) (cfgPath, appDataDir string) {
	t.Helper()
	tmp := tmpDir(t)
	cfgPath = filepath.Join(tmp, "config.json")
	appDataDir = filepath.Join(tmp, "appdata")
	oldCfg, oldDir := configPathFn, defaultCacheDirFn
	configPathFn = func() string { return cfgPath }
	defaultCacheDirFn = func() string { return appDataDir }
	t.Cleanup(func() {
		configPathFn = oldCfg
		defaultCacheDirFn = oldDir
	})
	return cfgPath, appDataDir
}

func TestConfigRoundTrip(t *testing.T) {
	isolateConfig(t)
	// Default: empty config.
	if loadConfig().CacheDir != "" {
		t.Fatal("default config should have empty CacheDir")
	}
	if err := saveConfig(AppConfig{CacheDir: `D:\Cache`}); err != nil {
		t.Fatal(err)
	}
	if got := loadConfig().CacheDir; got != `D:\Cache` {
		t.Fatalf("CacheDir = %q, want D:\\Cache", got)
	}
}

func TestResolveCachePath(t *testing.T) {
	_, appData := isolateConfig(t)
	wantDefault := filepath.Join(appData, "DiskSweep", "cache.db")
	if got := resolveCachePath(); got != wantDefault {
		t.Fatalf("default resolve = %q, want %q", got, wantDefault)
	}
	_ = saveConfig(AppConfig{CacheDir: `D:\Cache`})
	if got := resolveCachePath(); got != filepath.Join(`D:\Cache`, "cache.db") {
		t.Fatalf("custom resolve = %q", got)
	}
}

func TestSetCacheLocationMigrates(t *testing.T) {
	_, _ = isolateConfig(t)

	// Seed the default location with a db containing one history entry.
	oldPath := resolveCachePath()
	s, err := openSnapshotStore(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cache: s}
	a.recordHistory(CleanItem{ID: "t", Name: "测试"}, `C:\x\tmp\a`, 42, true, "")
	s.close()

	newDir := filepath.Join(tmpDir(t), "custom")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}

	loc, err := a.SetCacheLocation(newDir)
	if err != nil {
		t.Fatalf("SetCacheLocation: %v", err)
	}
	if !loc.Custom || loc.Path != filepath.Join(newDir, "cache.db") {
		t.Fatalf("bad location: %+v", loc)
	}
	if _, err := os.Stat(loc.Path); err != nil {
		t.Fatal("new cache db not created")
	}

	// Data must have migrated: history entry readable from the new store.
	entries := a.GetCleanHistory()
	if len(entries) != 1 || entries[0].Size != 42 {
		t.Fatalf("history not migrated: %+v", entries)
	}

	// Reset back to default.
	loc2, err := a.ResetCacheLocation()
	if err != nil {
		t.Fatal(err)
	}
	if loc2.Custom || loc2.Path != oldPath {
		t.Fatalf("reset location wrong: %+v", loc2)
	}
	entries2 := a.GetCleanHistory()
	if len(entries2) != 1 {
		t.Fatal("history lost after reset")
	}
	if a.cache != nil {
		a.cache.close()
	}
}

func TestSetCacheLocationInvalid(t *testing.T) {
	_, _ = isolateConfig(t)
	a := &App{}
	if _, err := a.SetCacheLocation(filepath.Join(tmpDir(t), "nope")); err == nil {
		t.Fatal("nonexistent dir should fail")
	}
	// Not writable: a path that is a file.
	f := filepath.Join(tmpDir(t), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetCacheLocation(f); err == nil {
		t.Fatal("file path should fail")
	}
}

// TestSwitchCacheKeepsStoreOnFailure pins the recovery behaviour of a failed
// cache switch.
//
// switchCache closes the live store before touching the target, so any error on
// the way back out used to leave a.cache == nil. That silently disabled history
// and disk-trend snapshots for the rest of the session, with no hint in the UI
// that anything was wrong — a failure the user could not even diagnose.
func TestSwitchCacheKeepsStoreOnFailure(t *testing.T) {
	_, _ = isolateConfig(t)

	oldPath := resolveCachePath()
	s, err := openSnapshotStore(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cache: s}
	a.recordHistory(CleanItem{ID: "t", Name: "测试"}, `C:\x\a`, 42, true, "")
	if got := len(a.GetCleanHistory()); got != 1 {
		t.Fatalf("setup: history = %d, want 1", got)
	}
	s.close()

	// A target whose cache.db exists but is not a database: openSnapshotStore
	// must fail on it.
	badDir := tmpDir(t)
	if err := os.WriteFile(filepath.Join(badDir, "cache.db"),
		[]byte("definitely not a sqlite file"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := a.SetCacheLocation(badDir); err == nil {
		t.Fatal("switching to an unreadable db must return an error")
	}

	if a.cache == nil {
		t.Fatal("cache is nil after a failed switch: history and snapshots are dead " +
			"for the rest of the session")
	}
	if got := len(a.GetCleanHistory()); got != 1 {
		t.Fatalf("history after a failed switch = %d, want 1 (previous store lost)", got)
	}
	// The config records which db to use, so it must not have moved.
	if got := resolveCachePath(); got != oldPath {
		t.Fatalf("config points at %q after a failed switch, want %q", got, oldPath)
	}
	if a.cache != nil {
		a.cache.close()
	}
}
