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

	// Close the current store to release the db lock before copying.
	if a.cache != nil {
		a.cache.close()
		a.cache = nil
	}

	// Migrate data only when the target has no db yet.
	if target != old {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			if src, err := os.Open(old); err == nil {
				dst, err := os.Create(target)
				if err == nil {
					_, _ = dst.ReadFrom(src)
					_ = dst.Close()
				}
				_ = src.Close()
			}
		}
	}

	cfg := loadConfig()
	cfg.CacheDir = targetDir
	if err := saveConfig(cfg); err != nil {
		return CacheLocation{}, err
	}

	store, err := openSnapshotStore(target)
	if err != nil {
		return CacheLocation{}, err
	}
	a.cache = store
	return a.GetCacheLocation(), nil
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
