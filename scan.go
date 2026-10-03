package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// DirChild is one entry (file or directory) in a scanned directory listing.
type DirChild struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	IsDir       bool   `json:"isDir"`
	Size        int64  `json:"size"`
	FileCount   int    `json:"fileCount"`
	ModTime     int64  `json:"modTime"`
	Inaccessible bool   `json:"inaccessible"` // true: dir exists but cannot be read (no permission)
	// DepNote is a Chinese annotation when this entry is a recognized
	// dependency (e.g. ".NET 运行时 · 请勿删除"). Empty when not matched.
	DepNote  string `json:"depNote,omitempty"`
	DepID    string `json:"depId,omitempty"`
	DepClean string `json:"depClean,omitempty"` // "keep" | "partial" | "model"
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

	// Inaccessible lists directories that exist but could not be read
	// (typically permission-denied). Their sizes are NOT included in Bytes,
	// so they may hide significant disk usage.
	InaccessibleDirs   int      `json:"inaccessibleDirs"`
	InaccessibleSample []string `json:"inaccessibleSample,omitempty"` // first few paths
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

	sizeMap      sync.Map // path -> dirInfo
	inaccessible sync.Map // path -> struct{} (dir exists but unreadable)

	last ScanSummary // most recent finished scan summary (guarded by mu)

	emit func(event string, data interface{}) // injectable; defaults to runtime.EventsEmit
}

// lastSummary returns the most recent finished scan summary, or nil if no
// scan has completed yet.
func (a *App) lastSummary() *ScanSummary {
	a.scans.mu.Lock()
	defer a.scans.mu.Unlock()
	if a.scans.last.Root == "" && a.scans.last.Dirs == 0 && a.scans.last.Bytes == 0 {
		return nil
	}
	s := a.scans.last
	return &s
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
	a.scans.inaccessible = sync.Map{}
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
		// Annotate recognized dependencies (e.g. dotnet, llama.cpp, venv).
		if dep := a.matchDepByPath(child.Path); dep != nil {
			child.DepID = dep.ID
			child.DepClean = dep.Cleanable
			child.DepNote = dep.Name + " · " + dep.CleanNote
		}
		if e.IsDir() {
			if _, bad := a.scans.inaccessible.Load(child.Path); bad {
				child.Inaccessible = true
			}
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
	// One concurrent pass does both jobs: it walks the tree (enumerate) and
	// records each directory's direct-file sizes as it goes. The old code
	// walked the tree twice — once to enumerate, once to stat — which meant
	// paying the whole directory-read cost twice, and the first pass was
	// serial while the fastest part (small cache dirs) waits on it.
	dirs := a.walkAndScan(root)
	if a.scans.cancel.Load() {
		a.finishScan(false, true, root, start)
		return
	}
	a.aggregateSizes(dirs)
	cancelled := a.scans.cancel.Load()
	a.finishScan(!cancelled, cancelled, root, start)
}

// walkAndScan enumerates every directory under root and records each one's
// direct-file size in a single concurrent traversal.
//
// This is the scan's hot path. The old scanner walked the tree twice — once
// to enumerate directories, once to stat their files — paying the whole
// directory-read cost twice, and the enumerate pass was serial so the actual
// file work could not start until it finished.
//
// Concurrency model: a semaphore bounds how many workers may be inside the
// traversal at once. A worker that finds subdirectories spawns a goroutine per
// subdirectory and waits for them; because the wait happens *after* releasing
// nothing and the semaphore is acquired before spawning, a deep tree simply
// runs with fewer concurrent workers rather than deadlocking. Only directory
// counts are mutex-protected (one lock per directory, versus one per file).
func (a *App) walkAndScan(root string) []string {
	workers := runtime.NumCPU() * 2
	if workers > 32 {
		workers = 32
	}
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)

	var (
		mu      sync.Mutex
		dirs    []string
		scanned int64
	)
	localHeap := newLocalTopFiles(a.topFiles.limitOr())

	var walk func(dir string)
	walk = func(dir string) {
		if a.scans.cancel.Load() {
			return
		}

		var size int64
		var files int
		entries, err := os.ReadDir(dir)
		if err != nil {
			atomic.AddInt64(&a.scans.errs, 1)
			a.scans.inaccessible.Store(dir, struct{}{})
			a.scans.sizeMap.Store(dir, dirInfo{0, 0})
			mu.Lock()
			dirs = append(dirs, dir)
			mu.Unlock()
			return
		}

		subdirs := make([]string, 0, 8)
		for _, e := range entries {
			if e.IsDir() {
				subdirs = append(subdirs, filepath.Join(dir, e.Name()))
				continue
			}
			info, err := e.Info()
			if err != nil {
				atomic.AddInt64(&a.scans.errs, 1)
				continue
			}
			size += info.Size()
			files++
			localHeap.add(filepath.Join(dir, e.Name()), info.Size(), info.ModTime().Unix())
		}
		atomic.AddInt64(&a.scans.files, int64(files))
		atomic.AddInt64(&a.scans.bytes, size)
		a.scans.sizeMap.Store(dir, dirInfo{size, files})

		mu.Lock()
		dirs = append(dirs, dir)
		mu.Unlock()

		done := atomic.AddInt64(&scanned, 1)
		if done%200 == 0 || done == 1 {
			mu.Lock()
			total := len(dirs)
			mu.Unlock()
			a.scans.emit("scan:progress", ScanProgress{
				Phase:     "scan",
				DirsTotal: total,
				DirsDone:  int(done),
				Files:     a.scans.files,
				Bytes:     a.scans.bytes,
			})
		}

		if len(subdirs) == 0 {
			return
		}

		// Fan out over subdirectories. We take a slot for each child before
		// spawning; when the pool is saturated we walk the child inline on
		// this goroutine, which keeps the traversal making progress without
		// ever blocking on a full channel.
		var wg sync.WaitGroup
		for _, sd := range subdirs {
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func(child string) {
					defer wg.Done()
					defer func() { <-sem }()
					walk(child)
				}(sd)
			default:
				walk(sd) // inline: pool is full
			}
		}
		wg.Wait()
	}

	walk(root)

	mu.Lock()
	out := dirs
	mu.Unlock()

	a.topFiles.merge(localHeap)
	a.scans.mu.Lock()
	a.scans.dirsTotal = len(out)
	a.scans.dirsDone = int64(len(out))
	a.scans.mu.Unlock()
	return out
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
			a.scans.inaccessible.Store(cur, struct{}{})
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
		a.scans.inaccessible.Store(dir, struct{}{})
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
//
// Ordering uses a bucket sort by path depth instead of sort.Slice: on a tree
// with tens of thousands of directories the comparison sort (plus a pathDepth
// scan per comparison) was pure overhead, while bucketing is a single O(n)
// pass. Path depth is monotonic with the dependency order we need — a child is
// always one level deeper than its parent — so processing buckets from deepest
// to shallowest is equivalent and does not depend on the order walkAndScan
// happened to append directories in.
func (a *App) aggregateSizes(dirs []string) {
	if len(dirs) == 0 {
		return
	}
	maxDepth := 0
	depths := make([]int, len(dirs))
	for i, d := range dirs {
		depth := stringDepth(d)
		depths[i] = depth
		if depth > maxDepth {
			maxDepth = depth
		}
	}
	buckets := make([][]string, maxDepth+1)
	for i, d := range dirs {
		buckets[depths[i]] = append(buckets[depths[i]], d)
	}
	for depth := maxDepth; depth >= 0; depth-- {
		for _, d := range buckets[depth] {
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
}

// stringDepth is pathDepth with the trailing-separator quirk removed: Windows
// roots like "C:\" carry the same depth as "C:" so a root still sorts above
// its children.
func stringDepth(p string) int {
	n := pathDepth(p)
	if n > 0 && (p[len(p)-1] == '\\' || p[len(p)-1] == '/') {
		n--
	}
	return n
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
	// Collect unreadable directories (no lock needed: sync.Map).
	var sample []string
	a.scans.inaccessible.Range(func(k, _ interface{}) bool {
		summary.InaccessibleDirs++
		if len(sample) < 20 {
			sample = append(sample, k.(string))
		}
		return true
	})
	summary.InaccessibleSample = sample
	a.scans.last = summary
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
	// Record today's drive-usage snapshot for the disk-trend view.
	if drives := a.GetDrives(); len(drives) > 0 {
		_ = a.cache.recordDiskSnapshot(drives, sizes, time.Now())
	}
}

// pathDepth counts path separators (Windows style).
func pathDepth(p string) int {
	return strings.Count(p, "\\")
}
