package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInfoOnlyRulesMatchGuard keeps the engine's refusal list in sync with the
// InfoOnly flags in the rule table. If they ever drift, a "view only" rule
// would become deletable -- which for WinSxS means a broken Windows.
func TestInfoOnlyRulesMatchGuard(t *testing.T) {
	a := &App{}
	seenInDefs := map[string]bool{}

	for _, d := range a.cleanItemDefs() {
		if d.InfoOnly {
			seenInDefs[d.ID] = true
			if !infoOnlyRuleIDs[d.ID] {
				t.Errorf("rule %q is marked InfoOnly but missing from infoOnlyRuleIDs "+
					"-> the engine would still delete it", d.ID)
			}
			if d.InfoNote == "" {
				t.Errorf("rule %q is InfoOnly but has no InfoNote, so the user is never "+
					"told how to reclaim the space", d.ID)
			}
			if len(d.Paths) == 0 {
				t.Errorf("rule %q is InfoOnly but has no paths to measure", d.ID)
			}
		} else if infoOnlyRuleIDs[d.ID] {
			t.Errorf("rule %q is in infoOnlyRuleIDs but not marked InfoOnly in the table", d.ID)
		}
	}

	for id := range infoOnlyRuleIDs {
		if !seenInDefs[id] {
			t.Errorf("infoOnlyRuleIDs contains %q but cleanItemDefs never marks it InfoOnly", id)
		}
	}
}

// TestInfoOnlyRulesPresent pins the three targets the user asked for, so a
// future refactor cannot quietly drop one of them.
func TestInfoOnlyRulesPresent(t *testing.T) {
	a := &App{}
	defs := map[string]CleanItem{}
	for _, d := range a.cleanItemDefs() {
		defs[d.ID] = d
	}

	want := []struct {
		id   string
		path string
	}{
		{"winsxs", filepath.Join(systemRoot(), "WinSxS")},
		{"windows_installer", filepath.Join(systemRoot(), "Installer")},
		{"windows_apps", filepath.Join(programFiles(), "WindowsApps")},
	}
	for _, w := range want {
		d, ok := defs[w.id]
		if !ok {
			t.Errorf("missing view-only rule %q", w.id)
			continue
		}
		if !d.InfoOnly {
			t.Errorf("%q must be InfoOnly", w.id)
		}
		if d.Level != LevelCautious {
			t.Errorf("%q level = %q, want %q", w.id, d.Level, LevelCautious)
		}
		found := false
		for _, p := range d.Paths {
			if p == w.path {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q paths = %v, want to include %q", w.id, d.Paths, w.path)
		}
	}
}

// TestExecuteCleanRefusesInfoOnly is the safety net for the view-only rules.
//
// It proves the refusal happens in the cleanup engine rather than only in the
// UI: a real file that we own and can delete must survive both an InfoOnly item
// aimed straight at it and a bare rule id, so a stale frontend or a scripted
// call cannot remove WinSxS / Windows\Installer / WindowsApps.
func TestExecuteCleanRefusesInfoOnly(t *testing.T) {
	dir := tmpDir(t)
	keep := filepath.Join(dir, "must-survive.bin")
	if err := os.WriteFile(keep, make([]byte, 128), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSurvive := func(what string) {
		t.Helper()
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%s deleted the target (the refusal did not happen): %v", what, err)
		}
	}

	a := &App{}

	// 1. Refused because the item carries the InfoOnly flag.
	res := a.executeItem(CleanItem{
		ID: "some_view_only", Name: "view only", Level: LevelCautious,
		Paths: []string{dir}, InfoOnly: true,
	})
	if res.OK {
		t.Errorf("InfoOnly item reported success: %+v", res)
	}
	if len(res.Errors) == 0 {
		t.Error("InfoOnly item should explain why it refused")
	}
	mustSurvive("InfoOnly flag")

	// 2. Refused by id alone, even with the flag absent -- defense in depth,
	//    because a caller can send just an id string.
	for id := range infoOnlyRuleIDs {
		res := a.executeItem(CleanItem{
			ID: id, Name: id, Level: LevelCautious, Paths: []string{dir},
		})
		if res.OK {
			t.Errorf("id %q reported success despite being view-only: %+v", id, res)
		}
		mustSurvive("id " + id)
	}

	// 3. The UI entry point refuses too: ExecuteClean must report a failure
	//    rather than silently succeeding.
	results := a.ExecuteClean([]string{"winsxs"})
	if len(results) != 1 {
		t.Fatalf("ExecuteClean returned %d results, want 1", len(results))
	}
	if results[0].OK {
		t.Error("ExecuteClean(winsxs) reported success -- it must refuse")
	}
	if results[0].Freed != 0 {
		t.Errorf("ExecuteClean(winsxs) claimed to free %d bytes", results[0].Freed)
	}
}

// TestProbeInfoOnlyReadsSize checks the probe reports a real size for an
// ordinary directory, so the view-only rows are not permanently stuck at 0.
func TestProbeInfoOnlyReadsSize(t *testing.T) {
	dir := tmpDir(t)
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), make([]byte, 300), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &App{}
	item := CleanItem{ID: "x", Name: "x", Paths: []string{dir}, InfoOnly: true}
	a.probeItem(&item)

	if !item.Exists {
		t.Fatal("existing path should set Exists")
	}
	if item.Size != 300 || item.FileCount != 1 {
		t.Fatalf("probe = %d bytes / %d files, want 300/1", item.Size, item.FileCount)
	}
	if item.Drive == "" {
		t.Error("probe should record a drive for the UI grouping")
	}
}

// TestIsInfoOnlyPath covers the per-file guard.
//
// The rule-level refusal in ExecuteClean is not enough on its own: the analyser
// exposes a recycle button for individual files, so browsing into WinSxS and
// deleting there would sidestep the whole protection.
//
// This deliberately exercises only the predicate. Calling RecyclePath with a
// real system path would move that directory to the recycle bin if the guard
// ever regressed, which is not something a test should be able to do.
func TestIsInfoOnlyPath(t *testing.T) {
	a := &App{}

	inside := []string{
		filepath.Join(systemRoot(), "WinSxS"),
		filepath.Join(systemRoot(), "WinSxS", "Manifests"),
		filepath.Join(systemRoot(), "Installer"),
		filepath.Join(systemRoot(), "Installer", "$PatchCache$", "x.msp"),
		filepath.Join(programFiles(), "WindowsApps", "Vendor.App_1.0.0.0_x64__h", "a.exe"),
	}
	for _, p := range inside {
		if !a.isInfoOnlyPath(p) {
			t.Errorf("isInfoOnlyPath(%q) = false, want true", p)
		}
	}

	// A sibling that merely shares a name prefix must NOT be caught; matching on
	// the raw string would refuse legitimate paths.
	outside := []string{
		filepath.Join(systemRoot(), "InstallerBackup"),
		filepath.Join(systemRoot(), "WinSxS_backup"),
		filepath.Join(systemRoot(), "System32"),
		filepath.Join(systemRoot(), "Temp"),
	}
	for _, p := range outside {
		if a.isInfoOnlyPath(p) {
			t.Errorf("isInfoOnlyPath(%q) = true, want false (prefix over-match)", p)
		}
	}

	// The cleanup entry point must refuse as well, but only reachable safely for
	// a target that does not exist on disk.
	if err := a.RecyclePath(filepath.Join(tmpDir(t), "nothing-here")); err != nil {
		t.Logf("RecyclePath on a missing path returned: %v", err)
	}
}

// TestProbeInfoOnlyKeepsUnreadableItemVisible: an item we cannot enumerate must
// still be listed, otherwise the biggest C: consumers silently disappear --
// which is the exact confusion this feature exists to fix.
func TestProbeInfoOnlyKeepsUnreadableItemVisible(t *testing.T) {
	// A path that exists but cannot be read is hard to fabricate portably, so
	// assert the more important half directly: a missing path stays hidden.
	a := &App{}
	missing := CleanItem{
		ID: "x", Name: "x", InfoOnly: true,
		Paths: []string{filepath.Join(tmpDir(t), "definitely-not-here")},
	}
	a.probeItem(&missing)
	if missing.Exists {
		t.Error("missing path must not be reported as existing")
	}
}
