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
func (t *topFilesCollector) add(path string, size int64, modTime int64) {
	if size <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
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
	t.heap = topHeap{}
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

	a.topFiles = newTopFilesCollector(200)
	for _, f := range top {
		a.topFiles.add(f.Path, f.Size, f.ModTime)
	}

	info.Exists = true
	info.ScannedAt = at.Unix()
	info.Dirs = len(sizes)
	info.TopFiles = a.topFiles.result()
	return info
}
