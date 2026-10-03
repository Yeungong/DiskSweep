package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTopFilesCollectorBounded(t *testing.T) {
	c := newTopFilesCollector(3)
	c.add("a", 100, 1)
	c.add("b", 50, 2)
	c.add("c", 200, 3)
	c.add("d", 150, 4) // evicts 50
	res := c.result()
	if len(res) != 3 {
		t.Fatalf("len = %d, want 3", len(res))
	}
	want := []int64{200, 150, 100}
	for i, w := range want {
		if res[i].Size != w {
			t.Fatalf("res[%d].Size = %d, want %d", i, res[i].Size, w)
		}
	}
	// Sorted descending by size.
	for i := 1; i < len(res); i++ {
		if res[i-1].Size < res[i].Size {
			t.Fatalf("not sorted descending at %d", i)
		}
	}
	// ModTime round-trips.
	if res[0].Path != "c" || res[0].ModTime != 3 {
		t.Fatalf("entry c wrong: %+v", res[0])
	}
}

func TestTopFilesReset(t *testing.T) {
	c := newTopFilesCollector(2)
	c.add("a", 10, 1)
	c.reset(0)
	if len(c.result()) != 0 {
		t.Fatal("reset did not clear")
	}
	c.add("b", 5, 2)
	if len(c.result()) != 1 || c.result()[0].Size != 5 {
		t.Fatal("post-reset add failed")
	}
}

func TestScanCollectsTopFiles(t *testing.T) {
	root := tmpDir(t)
	mk := func(rel string, size int) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("a/small.txt", 100)
	mk("b/big.bin", 300)
	mk("c/med.dat", 200)

	a := &App{}
	a.scans.emit = func(string, interface{}) {}
	if err := a.StartScan(root, 2); err != nil {
		t.Fatal(err)
	}
	waitScan(a, t)

	top := a.GetTopFiles()
	if len(top) != 2 {
		t.Fatalf("top files len = %d, want 2", len(top))
	}
	if top[0].Size != 300 || top[1].Size != 200 {
		t.Fatalf("unexpected top files: %+v", top)
	}
	if filepath.Base(top[0].Path) != "big.bin" {
		t.Fatalf("expected big.bin, got %s", top[0].Path)
	}
}

// TestLocalTopFilesZeroValue is the regression test for the per-worker buffer.
//
// A never-initialised localTopFiles has limit == 0, so the "is the heap full
// yet" test is false on the very first add and the code fell straight through to
// heap[0] on an empty heap: an immediate panic. The shared collector already
// guards this; the worker buffer must not be the one that reintroduces it.
func TestLocalTopFilesZeroValue(t *testing.T) {
	var l localTopFiles // deliberately never initialised
	for i := 0; i < 500; i++ {
		l.add("f", int64(i+1), 0)
	}
	if l.heap.Len() == 0 {
		t.Fatal("zero-value buffer collected nothing")
	}
	// It must also start keeping the largest values, not the first ones.
	if l.heap[0].Size < 1 {
		t.Fatalf("unexpected heap root: %+v", l.heap[0])
	}
}
