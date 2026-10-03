package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type eventCapture struct {
	mu       sync.Mutex
	events   []string
	progress []ScanProgress
	done     *ScanSummary
}

func (c *eventCapture) capture(event string, data interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	switch event {
	case "scan:progress":
		c.progress = append(c.progress, data.(ScanProgress))
	case "scan:done":
		s := data.(ScanSummary)
		c.done = &s
	}
}

func (c *eventCapture) snapshot() ([]string, []ScanProgress, *ScanSummary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.events, c.progress, c.done
}

// buildBigTree creates a tree of `dirs` directories each holding one 3-byte file.
func buildBigTree(t *testing.T, dirs int) string {
	t.Helper()
	root := tmpDir(t)
	for i := 0; i < dirs; i++ {
		p := filepath.Join(root, fmt.Sprintf("d%03d", i%100), fmt.Sprintf("s%03d", i/100))
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "f.bin"), []byte{1, 2, 3}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestScanEmitsProgressEvents(t *testing.T) {
	// buildBigTree(t, 260) builds root/d{i%100}/s{i/100}/f.bin for i in [0,260):
	//   - dNNN dirs: d000..d099 (i%100 cycles)                        -> 100
	//   - sNNN dirs: i/100 is 0,1,2, but only i<260 exists, so
	//       d000..d059 get s000,s001,s002 and d060..d099 get s000,s001
	//       60*3 + 40*2                                              -> 260
	//   - root                                                         ->   1
	//                                                             total = 361
	// Each leaf holds one 3-byte file, so 260 files / 780 bytes.
	const wantDirs = 361
	const wantBytes = 260 * 3

	root := buildBigTree(t, 260)
	cap := &eventCapture{}
	a := &App{}
	a.scans.emit = cap.capture

	if err := a.StartScan(root, 10); err != nil {
		t.Fatal(err)
	}
	waitScan(a, t)

	events, progress, done := cap.snapshot()
	if !contains(events, "scan:progress") || done == nil {
		t.Fatalf("missing progress or done: events=%v done=%v", events, done)
	}
	if done.OK != true || done.Cancelled {
		t.Fatalf("unexpected done: %+v", done)
	}
	if done.Bytes != wantBytes {
		t.Fatalf("done.Bytes = %d, want %d", done.Bytes, wantBytes)
	}

	// The scan is a single concurrent pass, so there is no separate
	// "enumerate" phase any more. Every progress event must be a scan event,
	// and because directories are discovered while the walk proceeds:
	//   - DirsDone only ever moves forward,
	//   - DirsTotal only ever grows (it is the count known *so far*),
	//   - a directory is appended to the list before it is counted as done,
	//     so DirsDone must never exceed DirsTotal.
	var lastDone, lastTotal int
	for i, p := range progress {
		if p.Phase != "scan" {
			t.Fatalf("progress[%d] has phase %q, want \"scan\" (single-pass scan)", i, p.Phase)
		}
		if p.DirsDone < lastDone {
			t.Fatalf("DirsDone went backwards: %d -> %d", lastDone, p.DirsDone)
		}
		if p.DirsTotal < lastTotal {
			t.Fatalf("DirsTotal shrank: %d -> %d", lastTotal, p.DirsTotal)
		}
		if p.DirsDone > p.DirsTotal {
			t.Fatalf("DirsDone %d exceeds DirsTotal %d", p.DirsDone, p.DirsTotal)
		}
		lastDone, lastTotal = p.DirsDone, p.DirsTotal
	}
	if lastDone == 0 {
		t.Fatal("no meaningful scan progress reported")
	}

	// The final counters are set from the completed traversal.
	a.scans.mu.Lock()
	finalTotal, finalDone := a.scans.dirsTotal, a.scans.dirsDone
	a.scans.mu.Unlock()
	if finalTotal != wantDirs {
		t.Fatalf("dirsTotal = %d, want %d", finalTotal, wantDirs)
	}
	if finalDone != wantDirs {
		t.Fatalf("dirsDone = %d, want %d", finalDone, wantDirs)
	}
}

func TestCancelScan(t *testing.T) {
	root := buildBigTree(t, 3000)
	cap := &eventCapture{}
	a := &App{}
	a.scans.emit = cap.capture

	if err := a.StartScan(root, 10); err != nil {
		t.Fatal(err)
	}

	// Wait until the scan-phase progress begins (enumerate done, scan started),
	// then cancel so we interrupt an in-flight scan.
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, progress, _ := cap.snapshot()
		started := false
		for _, p := range progress {
			if p.Phase == "scan" && p.DirsDone > 0 {
				started = true
				break
			}
		}
		if started {
			break
		}
		if !a.IsScanning() {
			t.Fatal("scan finished before we could cancel")
		}
		if time.Now().After(deadline) {
			t.Fatal("scan-phase progress never arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.CancelScan()
	waitScan(a, t)

	_, _, done := cap.snapshot()
	if done == nil {
		t.Fatal("no done event after cancel")
	}
	if !done.Cancelled {
		t.Fatalf("expected cancelled scan, got %+v", done)
	}
	if done.Bytes >= 3000*3 {
		t.Fatalf("cancelled scan reported full byte count: %+v", done)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
