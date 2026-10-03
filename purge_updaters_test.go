package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestPurgeUpdaterCaches permanently deletes the "*-updater" installer caches
// under %LOCALAPPDATA%. These are re-downloadable update packages, so unlike
// normal cleanup they are removed outright rather than moved to the recycle
// bin — moving them there would still occupy C: (the recycle bin lives on the
// same volume), which is precisely the trap we hit before.
//
// Gated behind DS_PURGE_UPDATERS=1.
func TestPurgeUpdaterCaches(t *testing.T) {
	if os.Getenv("DS_PURGE_UPDATERS") == "" {
		t.Skip("set DS_PURGE_UPDATERS=1 to purge updater caches")
	}

	base := localAppData()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}

	type victim struct {
		name string
		size int64
	}
	var targets []victim
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), "-updater") {
			continue
		}
		p := filepath.Join(base, e.Name())
		size, _ := walkDirSize(p)
		if size == 0 {
			continue // empty shell; nothing to reclaim
		}
		targets = append(targets, victim{e.Name(), size})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].size > targets[j].size })

	var total int64
	for _, v := range targets {
		p := filepath.Join(base, v.name)
		if err := os.RemoveAll(p); err != nil {
			t.Logf("FAIL %-42s %8.1f MB: %v", v.name, float64(v.size)/1024/1024, err)
			continue
		}
		// RemoveAll reports success even when some files were locked, so
		// verify what actually went away.
		left, _ := walkDirSize(p)
		freed := v.size - left
		total += freed
		t.Logf("OK   %-42s %8.1f MB freed", v.name, float64(freed)/1024/1024)
	}
	fmt.Println()
	t.Logf("TOTAL freed: %.2f GB (%d dirs)", float64(total)/1024/1024/1024, len(targets))
}
