package main

import (
	"fmt"
	"testing"
)

// TestNewRulesCoverReportedTargets is a diagnostic run: it prints what the
// newly added rules actually resolve to on this machine, so the numbers shown
// to the user can be verified against the report instead of assumed.
func TestNewRulesCoverReportedTargets(t *testing.T) {
	a := &App{}
	items := a.CleanupItems()
	want := map[string]bool{
		"nvidia_cache":       false,
		"updater_cache":      false,
		"go_build_cache":     false,
		"pnpm_cache":         false,
		"npm_cache":          false,
		"crash_dumps":        false,
		"playwright_cache":   false,
		"dx_shader":          false,
		"temp_user_all":      false,
		"nvidia_installer":   false,
		"pip_cache":          false,
		"game_crash_dumps":   false,
	}
	var covered int64
	for _, it := range items {
		if _, ok := want[it.ID]; ok {
			want[it.ID] = true
		}
		if it.ID == "nvidia_cache" || it.ID == "updater_cache" || it.ID == "go_build_cache" ||
			it.ID == "pnpm_cache" || it.ID == "npm_cache" || it.ID == "temp_user_all" ||
			it.ID == "crash_dumps" || it.ID == "playwright_cache" || it.ID == "pip_cache" {
			covered += it.Size
		}
	}
	for _, it := range items {
		switch it.ID {
		case "nvidia_cache", "updater_cache", "go_build_cache", "pnpm_cache", "npm_cache",
			"temp_user_all", "crash_dumps", "playwright_cache", "pip_cache", "dx_shader":
			t.Logf("%-18s %8.1f MB  %s  paths=%d  first=%v",
				it.ID, float64(it.Size)/1024/1024, it.Name, len(it.Paths), first2(it.Paths))
		}
	}
	for id, found := range want {
		if !found {
			t.Logf("MISS  %s (rule did not match anything on this machine)", id)
		}
	}
	t.Logf("total detected for the reported zero/low-risk set: %.2f GB", float64(covered)/1024/1024/1024)
}

func first2(p []string) []string {
	if len(p) <= 2 {
		return p
	}
	return []string{p[0], p[1], fmt.Sprintf("...+%d", len(p)-2)}
}
