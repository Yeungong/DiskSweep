package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// DirChild is one entry (file or directory) in a scanned directory listing.
type DirChild struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsDir     bool   `json:"isDir"`
	Size      int64  `json:"size"`
	FileCount int    `json:"fileCount"`
	ModTime   int64  `json:"modTime"`
}

// ScanProgress is emitted to the frontend during a scan.
type ScanProgress struct {
	Phase     string `json:"phase"` // "enumerate" | "scan"
	DirsTotal int    `json:"dirsTotal"`
	DirsDone  int    `json:"dirsDone"`
	Files     int64  `json:"files"`
	Bytes     int64  `json:"bytes"`
}

// ScanSummary is the final result of a scan.
type ScanSummary struct {
	OK        bool   `json:"ok"`
	Cancelled bool   `json:"cancelled"`
	Root      string `json:"root"`
	Dirs      int    `json:"dirs"`
	Files     int64  `json:"files"`
	Bytes     int64  `json:"bytes"`
	Errors    int    `json:"errors"`
	Duration  int64  `json:"durationMs"`
}

type dirInfo struct {
	size      int64
	fileCount int
}

// scanManager holds the state of the current (or last) full-tree scan.
type scanManager struct {
	mu        sync.Mutex
	scanning  atomic.Bool
	cancel    atomic.Bool
	dirsTotal int
	dirsDone  int64
	files     int64
	bytes     int64
	errs      int64

	sizeMap sync.Map // path -> dirInfo

	emit func(event string, data interface{}) // injectable; defaults to runtime.EventsEmit
}

func defaultEmit(ctx context.Context) func(string, interface{}) {
	return func(event string, data interface{}) {
		if ctx != nil {
			wruntime.EventsEmit(ctx, event, data)
		}
	}
}

// --- App API ---

// StartScan launches a background full-tree scan of root, collecting the
// topN largest files. Only one scan may run at a time.
func (a *App) StartScan(root string, topN int) error {
	if a.scans.scanning.Load() {
		return errScanInProgress
	}
	a.scans.mu.Lock()
	a.scans.sizeMap = sync.Map{}
	a.scans.dirsTotal = 0
	a.scans.dirsDone = 0
	a.scans.files = 0
	a.scans.bytes = 0
	a.scans.errs = 0
	a.scans.mu.Unlock()
	a.scans.cancel.Store(false)
	a.scans.scanning.Store(true)
	a.topFiles.reset(topN)

	go a.runScan(root)
	return nil
}

// CancelScan requests cancellation of the running scan. The scan stops at the
// next safe checkpoint and emits a scan:done event with cancelled=true.
func (a *App) CancelScan() {
	a.scans.cancel.Store(true)
}

// IsScanning reports whether a scan is currently running.
func (a *App) IsScanning() bool {
	return a.scans.scanning.Load()
}

// GetDirChildren returns the immediate children of path with sizes from the
// scan cache. Files are stat'ed on demand; directories read from the cache.
func (a *App) GetDirChildren(path string) []DirChild {
	entries, err := os.ReadDir(path)
	if err != nil {
		return []DirChild{}
	}
	out := make([]DirChild, 0, len(entries))
	for _, e := range entries {
		child := DirChild{
			Name:  e.Name(),
			Path:  filepath.Join(path, e.Name()),
			IsDir: e.IsDir(),
		}
		if e.IsDir() {
			if v, ok := a.scans.sizeMap.Load(child.Path); ok {
				di := v.(dirInfo)
				child.Size = di.size
				child.FileCount = di.fileCount
			}
		} else {
			if info, err := e.Info(); err == nil {
				child.Size = info.Size()
				child.ModTime = info.ModTime().Unix()
			}
		}
		out = append(out, child)
	}
	return out
}

// --- internal scan pipeline ---

func (a *App) runScan(root string) {
	start := time.Now()
	dirs := a.enumerateDirs(root)
	if a.scans.cancel.Load() {
		a.finishScan(false, true, root, start)
		return
	}
	a.scanDirs(dirs)
	a.aggregateSizes(dirs)
	cancelled := a.scans.cancel.Load()
	a.finishScan(!cancelled, cancelled, root, start)
}

// enumerateDirs walks the tree collecting every directory path (BFS via stack).
// It does not stat files, so it is fast. Symlinks are never followed because
// os.ReadDir reports them as non-directories.
func (a *App) enumerateDirs(root string) []string {
	dirs := []string{}
	stack := []string{root}
	for len(stack) > 0 {
		if a.scans.cancel.Load() {
			break
		}
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		dirs = append(dirs, cur)
		entries, err := os.ReadDir(cur)
		if err != nil {
			atomic.AddInt64(&a.scans.errs, 1)
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				stack = append(stack, filepath.Join(cur, e.Name()))
			}
		}
	}
	a.scans.mu.Lock()
	a.scans.dirsTotal = len(dirs)
	a.scans.mu.Unlock()
	a.scans.emit("scan:progress", ScanProgress{Phase: "enumerate", DirsTotal: len(dirs)})
	return dirs
}

// scanDirs stats every directory's direct files using a worker pool, recording
// per-directory sizes into the sizeMap and reporting progress.
func (a *App) scanDirs(dirs []string) {
	total := len(dirs)
	workers := runtime.NumCPU()
	if workers > 16 {
		workers = 16
	}
	if total < workers {
		workers = total
	}
	if workers < 1 {
		workers = 1
	}

	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range jobs {
				a.scanOneDir(d)
				done := atomic.AddInt64(&a.scans.dirsDone, 1)
				if done%100 == 0 || done == int64(total) {
					a.scans.emit("scan:progress", ScanProgress{
						Phase:     "scan",
						DirsTotal: total,
						DirsDone:  int(done),
						Files:     a.scans.files,
						Bytes:     a.scans.bytes,
					})
				}
			}
		}()
	}
	for _, d := range dirs {
		if a.scans.cancel.Load() {
			break
		}
		jobs <- d
	}
	close(jobs)
	wg.Wait()
}

// scanOneDir records the total size and file count of a directory's direct
// files (children directories are aggregated later in aggregateSizes).
func (a *App) scanOneDir(dir string) {
	var size int64
	var files int
	entries, err := os.ReadDir(dir)
	if err != nil {
		atomic.AddInt64(&a.scans.errs, 1)
		a.scans.sizeMap.Store(dir, dirInfo{0, 0})
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			atomic.AddInt64(&a.scans.errs, 1)
			continue
		}
		size += info.Size()
		files++
		a.collectTopFile(filepath.Join(dir, e.Name()), info)
	}
	atomic.AddInt64(&a.scans.files, int64(files))
	atomic.AddInt64(&a.scans.bytes, size)
	a.scans.sizeMap.Store(dir, dirInfo{size, files})
}

// collectTopFile is a hook for the top-files collector (implemented in the
// next step).
func (a *App) collectTopFile(path string, info os.FileInfo) {
	a.topFiles.add(path, info.Size(), info.ModTime().Unix())
}

// aggregateSizes bubbles child directory sizes up into their parents. Dirs are
// processed deepest-first so a parent's children are final before it is
// aggregated.
func (a *App) aggregateSizes(dirs []string) {
	sorted := make([]string, len(dirs))
	copy(sorted, dirs)
	sort.Slice(sorted, func(i, j int) bool {
		return pathDepth(sorted[i]) > pathDepth(sorted[j])
	})
	for _, d := range sorted {
		v, ok := a.scans.sizeMap.Load(d)
		if !ok {
			continue
		}
		di := v.(dirInfo)
		parent := filepath.Dir(d)
		if parent == d {
			continue // drive root
		}
		if pv, ok := a.scans.sizeMap.Load(parent); ok {
			pi := pv.(dirInfo)
			a.scans.sizeMap.Store(parent, dirInfo{pi.size + di.size, pi.fileCount + di.fileCount})
		}
	}
}

func (a *App) finishScan(ok, cancelled bool, root string, start time.Time) {
	a.scans.mu.Lock()
	summary := ScanSummary{
		OK:        ok,
		Cancelled: cancelled,
		Root:      root,
		Dirs:      a.scans.dirsTotal,
		Files:     a.scans.files,
		Bytes:     a.scans.bytes,
		Errors:    int(a.scans.errs),
		Duration:  time.Since(start).Milliseconds(),
	}
	a.scans.mu.Unlock()
	a.scans.scanning.Store(false)
	a.scans.emit("scan:done", summary)
	if ok && a.cache != nil {
		go a.saveSnapshot(root)
	}
}

// saveSnapshot persists the current size map and top files to the cache.
// Runs asynchronously; cache failures are non-fatal.
func (a *App) saveSnapshot(root string) {
	a.scans.mu.Lock()
	sizes := make(map[string]dirInfo, 4096)
	a.scans.sizeMap.Range(func(k, v interface{}) bool {
		sizes[k.(string)] = v.(dirInfo)
		return true
	})
	top := a.topFiles.result()
	a.scans.mu.Unlock()

	if err := a.cache.save(root, sizes, top, time.Now()); err != nil {
		// Cache is a convenience; ignore save errors.
		return
	}
}

// pathDepth counts path separators (Windows style).
func pathDepth(p string) int {
	return strings.Count(p, "\\")
}
