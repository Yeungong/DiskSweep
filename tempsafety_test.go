package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDeleteCutoff pins the rule that keeps cleanup away from live files.
//
// Two independent constraints feed one cutoff: "only entries older than N days"
// (MaxAgeDays) and "never touch anything modified in the last M minutes"
// (ProtectRecentMinutes). Both mean "skip if newer than X", so the stricter
// timestamp must win.
func TestDeleteCutoff(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

	if got := deleteCutoff(0, 0, now); got != 0 {
		t.Errorf("no policy: got %d, want 0 (no constraint)", got)
	}
	if got, want := deleteCutoff(7, 0, now), now.AddDate(0, 0, -7).Unix(); got != want {
		t.Errorf("age only: got %d, want %d", got, want)
	}
	if got, want := deleteCutoff(0, 30, now), now.Add(-30*time.Minute).Unix(); got != want {
		t.Errorf("protection only: got %d, want %d", got, want)
	}

	// Both set: 30 minutes ago is later than 7 days ago, so the protection is
	// the stricter (later) bound and must be what comes back. A file that is one
	// day old satisfies the age rule but must still be protected.
	got := deleteCutoff(7, 30, now)
	if want := now.Add(-30 * time.Minute).Unix(); got != want {
		t.Errorf("both set: got %d, want %d (protection must dominate)", got, want)
	}
	if got >= now.Unix() {
		t.Error("cutoff must always be in the past")
	}
}

// TestRecentFileSurvivesProtectedCutoff is the regression test for the incident
// where cleaning %TEMP% deleted a live WorkBuddy session's in-flight config
// file (acc-product-config-*.json.2.tmp) and the session died with ENOENT.
//
// It exercises the real selection gate: subtreeAllMatch is what decides whether
// a directory may be recycled as a whole, and it must reject any subtree
// containing a freshly written file.
func TestRecentFileSurvivesProtectedCutoff(t *testing.T) {
	root := tmpDir(t)
	freshDir := filepath.Join(root, "in-flight")
	idleDir := filepath.Join(root, "idle")
	for _, d := range []string{freshDir, idleDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "x.tmp"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The "idle" subtree is what a program has genuinely finished with.
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(idleDir, "x.tmp"), past, past); err != nil {
		t.Fatal(err)
	}

	cutoff := deleteCutoff(0, 30, time.Now())

	// A file written moments ago is the atomic-save window: it may be renamed
	// into place at any instant, so deleting it breaks the owner.
	if ok, _, _ := subtreeAllMatch(freshDir, "", cutoff); ok {
		t.Error("subtree containing a just-written file must not be eligible -- " +
			"this is exactly the case that killed a live session")
	}

	// The idle subtree must still be selected, otherwise the rule would free
	// nothing at all.
	ok, size, files := subtreeAllMatch(idleDir, "", cutoff)
	if !ok || files != 1 || size != 1 {
		t.Errorf("idle subtree = (ok=%v size=%d files=%d), want (true, 1, 1)", ok, size, files)
	}
}

// TestTempUserAllProtectsLiveFiles guards the specific rule that caused the
// damage. Without per-entry protection it takes the whole-directory path and
// recycles all of %TEMP% in one move, which cannot honour any per-file
// judgement and deletes the working files of running programs.
func TestTempUserAllProtectsLiveFiles(t *testing.T) {
	a := &App{}
	var rule CleanItem
	found := false
	for _, d := range a.cleanItemDefs() {
		if d.ID == "temp_user_all" {
			rule, found = d, true
			break
		}
	}
	if !found {
		t.Fatal("temp_user_all rule is missing")
	}

	if rule.ProtectRecentMinutes <= 0 {
		t.Fatal("temp_user_all must set ProtectRecentMinutes, otherwise it deletes " +
			"the in-flight files of running programs")
	}
	if !rule.NoAutoCheck {
		t.Error("temp_user_all must not be pre-selected: it is the most aggressive " +
			"rule and must be an explicit choice")
	}
	if rule.Match == "" && rule.MaxAgeDays == 0 && rule.ProtectRecentMinutes == 0 {
		t.Fatal("temp_user_all would take the whole-directory fast path, which by " +
			"design cannot protect recent files")
	}
}

// TestNoRuleDeletesAllOfTempWithoutProtection generalises the incident: any
// rule pointed at %TEMP% must carry an age or recency constraint. A rule that
// takes %TEMP% with no per-entry policy moves the whole directory in one shell
// call and will delete whatever is running.
func TestNoRuleDeletesAllOfTempWithoutProtection(t *testing.T) {
	tempDir := filepath.Join(localAppData(), "Temp")

	a := &App{}
	for _, d := range a.cleanItemDefs() {
		pointsAtTemp := false
		for _, p := range d.Paths {
			if filepath.Clean(p) == filepath.Clean(tempDir) {
				pointsAtTemp = true
				break
			}
		}
		if !pointsAtTemp {
			continue
		}
		if d.Match == "" && d.MaxAgeDays == 0 && d.ProtectRecentMinutes == 0 {
			t.Errorf("rule %q targets all of %%TEMP%% with no age/recency guard: "+
				"it will delete files that running programs are writing", d.ID)
		}
	}
}
