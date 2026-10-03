package main

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// TestExecuteReportedCleanTargets actually runs the cleanup for the item IDs
// reported to the user as safe, and reports per-item results. This is the
// end-to-end verification that the new rules resolve to real, recycled paths.
//
// Gated behind an env var so a normal `go test ./...` never touches the user's
// files: set DS_EXEC_CLEAN=1 to run it.
func TestExecuteReportedCleanTargets(t *testing.T) {
	if os.Getenv("DS_EXEC_CLEAN") == "" {
		t.Skip("set DS_EXEC_CLEAN=1 to run the real cleanup")
	}

	a := &App{}
	a.scans.emit = func(event string, data interface{}) {
		if p, ok := data.(CleanProgress); ok && p.Phase == "done" {
			t.Logf("  done: %s -> %.1f MB (%.1fs)", p.ItemName, float64(p.Freed)/1024/1024, float64(p.ElapsedMs)/1000)
		}
	}

	ids := []string{
		"nvidia_cache",
		"updater_cache",
		"go_build_cache",
		"pnpm_cache",
		"npm_cache",
		"pip_cache",
		"playwright_cache",
		"crash_dumps",
		"temp_user_all",
	}

	start := time.Now()
	results := a.ExecuteClean(ids)
	var total int64
	for _, r := range results {
		total += r.Freed
		status := "OK "
		if !r.OK {
			status = "FAIL"
		}
		t.Logf("%-4s %-34s %9.1f MB  errs=%d %s",
			status, r.Name, float64(r.Freed)/1024/1024, len(r.Errors), r.Warning)
		for _, e := range r.Errors {
			t.Logf("        ! %s (%s)", e.Error, e.Kind)
		}
	}
	t.Logf("TOTAL freed in %.1fs: %.2f GB", time.Since(start).Seconds(), float64(total)/1024/1024/1024)
	fmt.Println()
}
