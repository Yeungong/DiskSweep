package main

import (
	"container/heap"
	"errors"
	"sort"
	"sync"
)

// errScanInProgress is returned when StartScan is called while a scan runs.
var errScanInProgress = errors.New("已有扫描正在进行")

// TopFileEntry is one entry in the top-files list.
type TopFileEntry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
}

// topFilesCollector keeps the N largest files found during a scan using a
// bounded min-heap. Safe for concurrent use.
type topFilesCollector struct {
	mu    sync.Mutex
	limit int
	heap  topHeap
}

// topHeap is a min-heap of TopFileEntry ordered by Size.
type topHeap []TopFileEntry

func (h topHeap) Len() int           { return len(h) }
func (h topHeap) Less(i, j int) bool { return h[i].Size < h[j].Size }
func (h topHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *topHeap) Push(x interface{}) { *h = append(*h, x.(TopFileEntry)) }
func (h *topHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

func newTopFilesCollector(limit int) topFilesCollector {
	if limit <= 0 {
		limit = 200
	}
	return topFilesCollector{limit: limit}
}

// add inserts a file into the collector, keeping only the largest `limit`.
// Safe on the zero value: a collector that was never initialised gets the
// default limit instead of panicking on heap[0].
func (t *topFilesCollector) add(path string, size int64, modTime int64) {
	if size <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.limit <= 0 {
		t.limit = 200
	}
	if t.heap.Len() < t.limit {
		heap.Push(&t.heap, TopFileEntry{Path: path, Size: size, ModTime: modTime})
		return
	}
	if size > t.heap[0].Size {
		heap.Pop(&t.heap)
		heap.Push(&t.heap, TopFileEntry{Path: path, Size: size, ModTime: modTime})
	}
}

// result returns the collected files sorted by size descending.
func (t *topFilesCollector) result() []TopFileEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TopFileEntry, len(t.heap))
	copy(out, t.heap)
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out
}

// reset clears the collector and optionally sets a new limit.
func (t *topFilesCollector) reset(limit int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if limit > 0 {
		t.limit = limit
	}
	if t.limit <= 0 {
		t.limit = 200
	}
	t.heap = topHeap{}
}

// limitOr returns the configured limit, falling back to the default for a
// zero-value collector.
func (t *topFilesCollector) limitOr() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.limit <= 0 {
		return 200
	}
	return t.limit
}

// localTopFiles is a per-worker candidate buffer. Every scanned file would
// otherwise take the collector's single global mutex, which turns a scan of a
// cache directory (hundreds of thousands of tiny files) into a lock convoy.
// Workers accumulate into their own heap and merge once at the end.
type localTopFiles struct {
	limit int
	heap  topHeap
}

func newLocalTopFiles(limit int) *localTopFiles {
	if limit <= 0 {
		limit = 200
	}
	return &localTopFiles{limit: limit}
}

// add is lock-free: a worker owns its buffer exclusively.
//
// The limit guard mirrors topFilesCollector.add. A zero value would otherwise
// fall through to heap[0] on an empty heap and panic — the same bug that was
// already fixed in the shared collector, and this type should not be the one
// that reintroduces it.
func (l *localTopFiles) add(path string, size int64, modTime int64) {
	if size <= 0 {
		return
	}
	if l.limit <= 0 {
		l.limit = 200
	}
	if l.heap.Len() < l.limit {
		heap.Push(&l.heap, TopFileEntry{Path: path, Size: size, ModTime: modTime})
		return
	}
	if size > l.heap[0].Size {
		heap.Pop(&l.heap)
		heap.Push(&l.heap, TopFileEntry{Path: path, Size: size, ModTime: modTime})
	}
}

// merge folds a worker's candidates into the shared collector.
func (t *topFilesCollector) merge(l *localTopFiles) {
	if l == nil || l.heap.Len() == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.limit <= 0 {
		t.limit = 200
	}
	for _, e := range l.heap {
		if t.heap.Len() < t.limit {
			heap.Push(&t.heap, e)
			continue
		}
		if e.Size > t.heap[0].Size {
			heap.Pop(&t.heap)
			heap.Push(&t.heap, e)
		}
	}
}

// GetTopFiles returns the largest files collected by the latest scan.
func (a *App) GetTopFiles() []TopFileEntry {
	return a.topFiles.result()
}

// SnapshotInfo describes a restored scan snapshot.
type SnapshotInfo struct {
	Exists    bool           `json:"exists"`
	ScannedAt int64          `json:"scannedAt"`
	Dirs      int            `json:"dirs"`
	TopFiles  []TopFileEntry `json:"topFiles"`
}

// LoadSnapshot restores the last saved scan for root into memory, so the UI
// can show previous results immediately after launch. Call StartScan to
// refresh. Returns ok=false when no usable snapshot exists.
func (a *App) LoadSnapshot(root string) SnapshotInfo {
	info := SnapshotInfo{}
	if a.cache == nil {
		return info
	}
	sizes, top, at, ok, err := a.cache.load(root)
	if err != nil || !ok {
		return info
	}

	a.scans.mu.Lock()
	a.scans.sizeMap = sync.Map{}
	for p, di := range sizes {
		a.scans.sizeMap.Store(p, di)
	}
	a.scans.mu.Unlock()

	// Reset in place rather than assigning a fresh collector: the collector
	// embeds a mutex, so replacing it wholesale copies a lock (flagged by vet's
	// copylocks check in spirit) and, worse, swaps the object out from under a
	// scan that may still be running -- its final merge would land in an
	// orphaned collector and the results would be silently lost.
	a.topFiles.reset(200)
	for _, f := range top {
		a.topFiles.add(f.Path, f.Size, f.ModTime)
	}

	info.Exists = true
	info.ScannedAt = at.Unix()
	info.Dirs = len(sizes)
	info.TopFiles = a.topFiles.result()
	return info
}
