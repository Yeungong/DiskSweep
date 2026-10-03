package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var (
	errNotADir     = errString("指定的路径不是目录")
	errNotWritable = errString("该目录不可写")
)

// AppConfig holds user-adjustable settings, persisted as JSON next to the
// default cache database so it can always be found regardless of where the
// cache itself lives.
type AppConfig struct {
	CacheDir string `json:"cacheDir"` // empty = default (%AppData%\DiskSweep)
}

// Injectables so tests can isolate config/cache paths from the real user dirs.
var (
	configPathFn = func() string {
		dir, err := os.UserConfigDir()
		if err != nil {
			dir = os.TempDir()
		}
		return filepath.Join(dir, "DiskSweep", "config.json")
	}
	defaultCacheDirFn = func() string {
		dir, err := os.UserConfigDir()
		if err != nil {
			dir = os.TempDir()
		}
		return dir
	}
)

func configPath() string { return configPathFn() }

func loadConfig() AppConfig {
	cfg := AppConfig{}
	data, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

func saveConfig(cfg AppConfig) error {
	if err := os.MkdirAll(filepath.Dir(configPath()), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0o644)
}

// defaultCachePath is the built-in location when no custom cache dir is set.
func defaultCachePath() string {
	return filepath.Join(defaultCacheDirFn(), "DiskSweep", "cache.db")
}

// resolveCachePath returns the cache db path honoring a custom config dir.
func resolveCachePath() string {
	if d := loadConfig().CacheDir; d != "" {
		return filepath.Join(d, "cache.db")
	}
	return defaultCachePath()
}

// CacheLocation describes where scan/history data is stored.
type CacheLocation struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Custom bool   `json:"custom"`
}

// GetCacheLocation reports the current cache db path and size.
func (a *App) GetCacheLocation() CacheLocation {
	path := resolveCachePath()
	loc := CacheLocation{Path: path, Custom: loadConfig().CacheDir != ""}
	if f, err := os.Stat(path); err == nil {
		loc.Size = f.Size()
	}
	return loc
}

// SetCacheLocation switches the cache db to dir (must exist and be writable),
// migrating the existing data when the target db does not already exist.
func (a *App) SetCacheLocation(dir string) (CacheLocation, error) {
	dir = trimSpace(dir)
	if dir == "" {
		return a.ResetCacheLocation()
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return CacheLocation{}, errNotADir
	}
	// Writability probe.
	probe := filepath.Join(dir, ".ds_write_probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		return CacheLocation{}, errNotWritable
	}
	_ = os.Remove(probe)

	return a.switchCache(dir)
}

// ResetCacheLocation restores the default (%AppData%) cache location.
func (a *App) ResetCacheLocation() (CacheLocation, error) {
	return a.switchCache("")
}

// switchCache moves the cache store to targetDir ("" = default) and migrates
// the existing db file if the target does not yet have one.
func (a *App) switchCache(targetDir string) (CacheLocation, error) {
	old := resolveCachePath() // current location (migration source)
	target := defaultCachePath()
	if targetDir != "" {
		target = filepath.Join(targetDir, "cache.db")
	}

	// Close the current store to release the db lock before copying anything.
	if a.cache != nil {
		a.cache.close()
		a.cache = nil
	}

	// Every failure path below reopens the previous store. Returning an error
	// with a.cache left nil would silently disable history and snapshots for the
	// rest of the session, and the UI gives no hint that anything went wrong.
	keepOldStore := func() {
		if a.cache == nil {
			a.cache, _ = openSnapshotStore(old)
		}
	}

	if target != old {
		if err := migrateCacheDB(old, target); err != nil {
			keepOldStore()
			return CacheLocation{}, err
		}
	}

	store, err := openSnapshotStore(target)
	if err != nil {
		keepOldStore()
		return CacheLocation{}, err
	}
	a.cache = store

	// The config is written last: it is the record of which database to use, so
	// it must not point at a location we failed to open.
	cfg := loadConfig()
	cfg.CacheDir = targetDir
	if err := saveConfig(cfg); err != nil {
		return CacheLocation{}, err
	}
	return a.GetCacheLocation(), nil
}

// migrateCacheDB copies the existing database to target when target has none
// yet. It is a no-op when there is nothing to migrate.
//
// The copy lands at a temporary name and is renamed into place only once it has
// been written and closed successfully. A truncated database left at the real
// path would be indistinguishable from a valid one until SQLite refused to open
// it, and the user would have no idea why their history vanished.
func migrateCacheDB(oldPath, target string) error {
	if _, err := os.Stat(target); err == nil || !os.IsNotExist(err) {
		return nil // target already has a db (or cannot be inspected)
	}
	src, err := os.Open(oldPath)
	if err != nil {
		return nil // nothing to migrate (first run)
	}
	defer src.Close()

	tmp := target + ".migrating"
	dst, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := dst.ReadFrom(src); err != nil {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// PickDirectory opens a native folder picker and returns the chosen directory
// ("" when cancelled).
func (a *App) PickDirectory() string {
	dir, err := wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "选择缓存目录",
	})
	if err != nil || dir == "" {
		return ""
	}
	return dir
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
