package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeApp creates one WindowsApps-style package directory.
func writeApp(t *testing.T, root, dir, displayName string, payload int) {
	t.Helper()
	full := filepath.Join(root, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `<?xml version="1.0" encoding="UTF-8"?>
<Package><Properties><DisplayName>` + displayName + `</DisplayName></Properties></Package>`
	if err := os.WriteFile(filepath.Join(full, "AppxManifest.xml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "payload.bin"), make([]byte, payload), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWindowsAppsBreakdownGroupsVariants covers the two things that make this
// breakdown correct rather than merely plausible:
//
//   - one app owns several directories (payload + resource variants) and must be
//     reported once with the sum, not once per directory;
//   - a localised package stores "ms-resource:..." which cannot be resolved
//     here, so it must fall back to the package identity instead of showing an
//     unusable token.
func TestWindowsAppsBreakdownGroupsVariants(t *testing.T) {
	root := tmpDir(t)

	// App.One: a real payload plus a scale-400 resource variant.
	writeApp(t, root, "App.One_1.0.0.0_x64__abcdef", "App One", 1000)
	writeApp(t, root, "App.One_1.0.0.0_neutral_split.scale-400_abcdef", "App One", 300)
	// App.One: a "~" language variant with no payload of its own.
	writeApp(t, root, "App.One_1.0.0.0_neutral_~_abcdef", "App One", 50)

	// Two.Other: localised name we cannot resolve.
	writeApp(t, root, "Two.Other_2.0.0.0_x64__ghijkl", "ms-resource:AppName", 500)

	kids, count, total, files, err := windowsAppsBreakdown(root)
	if err != nil {
		t.Fatalf("breakdown: %v", err)
	}

	if count != 2 {
		t.Fatalf("appCount = %d, want 2 (variants must not count separately)", count)
	}
	// Each directory holds a manifest plus a payload, so 4 dirs -> 8 files.
	if files != 8 {
		t.Errorf("files = %d, want 8", files)
	}

	// Derive the expected sizes from the fixture rather than hardcoding byte
	// counts: the manifests have names of different lengths, so any literal
	// total is wrong the moment the fixture changes.
	wantTotal := int64(0)
	for _, dir := range []string{
		"App.One_1.0.0.0_x64__abcdef",
		"App.One_1.0.0.0_neutral_split.scale-400_abcdef",
		"App.One_1.0.0.0_neutral_~_abcdef",
		"Two.Other_2.0.0.0_x64__ghijkl",
	} {
		s, _ := walkDirSize(filepath.Join(root, dir))
		wantTotal += s
	}
	if total != wantTotal {
		t.Errorf("total = %d, want %d (sum of every directory)", total, wantTotal)
	}

	byName := map[string]int64{}
	for _, c := range kids {
		byName[c.Name] = c.Size
	}
	// The payload directory plus both resource variants must be folded into one
	// row, so one app never appears three times.
	wantAppOne := int64(0)
	for _, dir := range []string{
		"App.One_1.0.0.0_x64__abcdef",
		"App.One_1.0.0.0_neutral_split.scale-400_abcdef",
		"App.One_1.0.0.0_neutral_~_abcdef",
	} {
		s, _ := walkDirSize(filepath.Join(root, dir))
		wantAppOne += s
	}
	if got := byName["App One"]; got != wantAppOne {
		t.Errorf("App One = %d, want %d (all three directories summed)", got, wantAppOne)
	}
	// Unresolvable localised name falls back to the identity.
	if _, ok := byName["Two.Other"]; !ok {
		t.Errorf("expected fallback name %q, got %v", "Two.Other", byName)
	}
	if _, ok := byName["ms-resource:AppName"]; ok {
		t.Error("an unresolvable ms-resource token must not be shown to the user")
	}

	// Largest first, so the UI leads with what matters.
	if len(kids) != 2 || kids[0].Size < kids[1].Size {
		t.Errorf("children must be sorted largest-first: %+v", kids)
	}
}

// TestWindowsAppsBreakdownRollsUpTheTail: the list is capped, and the parts that
// do not fit must still be represented so the displayed sizes add up.
func TestWindowsAppsBreakdownRollsUpTheTail(t *testing.T) {
	root := tmpDir(t)
	total := windowsAppsDetailLimit + 5
	for i := 0; i < total; i++ {
		// Distinct names, descending sizes so ordering is unambiguous.
		writeApp(t, root, string(rune('A'+i%26))+string(rune('a'+i/26))+"_1.0.0.0_x64__h",
			"", 10)
	}

	kids, count, _, _, err := windowsAppsBreakdown(root)
	if err != nil {
		t.Fatal(err)
	}
	if count != total {
		t.Errorf("appCount = %d, want %d", count, total)
	}
	if len(kids) != windowsAppsDetailLimit+1 {
		t.Fatalf("children = %d, want %d (top %d plus one rollup)",
			len(kids), windowsAppsDetailLimit+1, windowsAppsDetailLimit)
	}
	last := kids[len(kids)-1]
	if last.Name != "其他 5 个应用" {
		t.Errorf("last row = %q, want the rollup of the 5 remaining apps", last.Name)
	}
}

// TestPreferManifestDir picks a payload directory over a resource variant, since
// only the payload carries a usable manifest.
func TestPreferManifestDir(t *testing.T) {
	payload := "App.One_1.0.0.0_x64__abcdef"
	scale := "App.One_1.0.0.0_neutral_split.scale-400_abcdef"
	lang := "App.One_1.0.0.0_neutral_~_abcdef"

	if !preferManifestDir(payload, scale) {
		t.Error("a payload dir must win over a scale variant")
	}
	if !preferManifestDir(payload, lang) {
		t.Error("a payload dir must win over a language variant")
	}
	if preferManifestDir(scale, payload) {
		t.Error("a scale variant must not win over a payload dir")
	}
	if !isResourceVariantDir(scale) || !isResourceVariantDir(lang) {
		t.Error("resource variants not recognised")
	}
	if isResourceVariantDir(payload) {
		t.Error("a payload dir was misclassified as a resource variant")
	}
}
