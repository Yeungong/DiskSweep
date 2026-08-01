package main

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateConfig redirects config/cache paths to a temp dir for a test.
func isolateConfig(t *testing.T) (cfgPath, appDataDir string) {
	t.Helper()
	tmp := t.TempDir()
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

	newDir := filepath.Join(t.TempDir(), "custom")
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
	if _, err := a.SetCacheLocation(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("nonexistent dir should fail")
	}
	// Not writable: a path that is a file.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetCacheLocation(f); err == nil {
		t.Fatal("file path should fail")
	}
}
