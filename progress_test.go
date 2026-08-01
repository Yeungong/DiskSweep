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
	root := t.TempDir()
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
	if done.Bytes != 260*3 {
		t.Fatalf("done.Bytes = %d, want %d", done.Bytes, 260*3)
	}

	// Phase sequence: enumerate first, then scan events, monotonic DirsDone.
	sawScan := false
	var lastDone int
	var enumTotal int
	for _, p := range progress {
		if p.Phase == "enumerate" {
			if sawScan {
				t.Fatal("enumerate progress emitted after scan phase")
			}
			if p.DirsTotal < 260 {
				t.Fatalf("enumerate DirsTotal = %d, want >= 260", p.DirsTotal)
			}
			enumTotal = p.DirsTotal
			continue
		}
		if p.Phase == "scan" {
			sawScan = true
			if p.DirsTotal != enumTotal {
				t.Fatalf("scan DirsTotal %d != enumerate %d", p.DirsTotal, enumTotal)
			}
			if p.DirsDone < lastDone {
				t.Fatal("DirsDone not monotonic")
			}
			lastDone = p.DirsDone
		}
	}
	if !sawScan {
		t.Fatal("no scan-phase progress events")
	}
	if lastDone != enumTotal {
		t.Fatalf("final DirsDone = %d, want %d", lastDone, enumTotal)
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
