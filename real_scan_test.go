//go:build integration

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRealScanSmoke runs a real full-tree scan over the user's TEMP directory
// and sanity-checks the pipeline end to end. Run explicitly with:
//
//	go test -tags integration -run TestRealScanSmoke -v
func TestRealScanSmoke(t *testing.T) {
	root := filepath.Join(os.Getenv("LOCALAPPDATA"), "Temp")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("TEMP dir unavailable: %v", err)
	}

	a := &App{}
	a.scans.emit = func(string, interface{}) {}

	start := time.Now()
	if err := a.StartScan(root, 50); err != nil {
		t.Fatal(err)
	}
	waitScan(a, t)
	elapsed := time.Since(start)
	t.Logf("scanned %q in %v", root, elapsed)

	summary := struct {
		Dirs   int
		Files  int64
		Bytes  int64
		Errors int
	}{a.scans.dirsTotal, a.scans.files, a.scans.bytes, int(a.scans.errs)}
	if summary.Dirs == 0 || summary.Files == 0 || summary.Bytes == 0 {
		t.Fatalf("scan produced no data: %+v", summary)
	}

	children := a.GetDirChildren(root)
	if len(children) == 0 {
		t.Fatal("GetDirChildren returned nothing for real dir")
	}

	top := a.GetTopFiles()
	if len(top) == 0 {
		t.Fatal("no top files collected")
	}
	for i := 1; i < len(top); i++ {
		if top[i-1].Size < top[i].Size {
			t.Fatalf("top files not sorted descending at %d", i)
		}
	}
	if top[0].Size <= 0 || top[0].Path == "" {
		t.Fatalf("bad top file: %+v", top[0])
	}
	t.Logf("top file: %s (%d bytes)", top[0].Path, top[0].Size)
	t.Logf("dirs=%d files=%d bytes=%d errors=%d", summary.Dirs, summary.Files, summary.Bytes, summary.Errors)
}

// TestAIToolRulesDetected probes the AI-tool rules against the real machine to
// confirm the paths users actually have are discovered.
func TestAIToolRulesDetected(t *testing.T) {
	a := &App{}
	aiIDs := map[string]bool{
		"codex_data": true, "qoder_data": true, "claude_data": true,
		"claude_cli_node": true, "mimocode_data": true, "cherry_studio": true,
		"kimi_data": true, "openai_desktop": true, "cline_data": true,
		"copilot_data": true, "coze_data": true, "doubao_data": true,
		"opencode_data": true, "cc_switch": true, "arkcli_data": true,
		"dreamina_cli": true, "openchatcut": true, "ai_canvas": true,
		"streamlit_cache": true, "hf_cache": true,
	}
	found := 0
	for _, d := range a.cleanItemDefs() {
		if !aiIDs[d.ID] {
			continue
		}
		a.probeItem(&d)
		t.Logf("%-16s exists=%-5v size=%-10d paths=%v", d.ID, d.Exists, d.Size, d.Paths)
		if d.Exists {
			found++
		}
	}
	if found == 0 {
		t.Log("no AI tool data detected on this machine")
	}
}
func TestEndToEndCleanCycle(t *testing.T) {
	root := t.TempDir()
	junk := filepath.Join(root, "junk-cache")
	if err := os.MkdirAll(filepath.Join(junk, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(junk, "a.tmp"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(junk, "sub", "b.tmp"), make([]byte, 200), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(junk, "keep.bin"), make([]byte, 50), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &App{}
	a.scans.emit = func(string, interface{}) {}

	// 1. Scan the tree.
	if err := a.StartScan(root, 10); err != nil {
		t.Fatal(err)
	}
	waitScan(a, t)
	if v, ok := a.scans.sizeMap.Load(junk); !ok || v.(dirInfo).size != 350 {
		t.Fatalf("junk size after scan = %+v, want 350", v)
	}

	// 2. Drill down into junk via the same API the UI uses.
	children := a.GetDirChildren(junk)
	names := map[string]bool{}
	for _, c := range children {
		names[c.Name] = true
	}
	if !names["a.tmp"] || !names["sub"] || !names["keep.bin"] {
		t.Fatalf("drill-down missing entries: %v", names)
	}

	// 3. Clean: delete *.tmp recursively (300 bytes), keep keep.bin.
	res := a.executeItem(CleanItem{
		ID: "t", Name: "测试", Level: LevelSafe,
		Paths: []string{junk}, Match: "ext:.tmp",
	})
	if !res.OK || res.Freed != 300 {
		t.Fatalf("clean result: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(junk, "keep.bin")); err != nil {
		t.Fatal("keep.bin must survive cleanup")
	}

	// 4. Rescan and verify the freed space is reflected.
	if err := a.StartScan(root, 10); err != nil {
		t.Fatal(err)
	}
	waitScan(a, t)
	if v, ok := a.scans.sizeMap.Load(junk); !ok || v.(dirInfo).size != 50 {
		t.Fatalf("junk size after clean+rescan = %+v, want 50", v)
	}
}

// TestCleanupProbeMatchesScanCache verifies that after a scan, probing a
// cleanup rule whose path is in the scan cache reports exactly the scanned
// size (not stale/estimated data).
func TestCleanupProbeMatchesScanCache(t *testing.T) {
	root := filepath.Join(os.Getenv("LOCALAPPDATA"), "Temp")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("TEMP dir unavailable: %v", err)
	}

	a := &App{}
	a.scans.emit = func(string, interface{}) {}
	if err := a.StartScan(root, 50); err != nil {
		t.Fatal(err)
	}
	waitScan(a, t)

	defs := a.cleanItemDefs()
	for i := range defs {
		if defs[i].ID != "temp_user" {
			continue
		}
		before := defs[i].Size
		a.probeItem(&defs[i])
		if !defs[i].Exists {
			t.Fatal("temp_user should exist")
		}
		v, ok := a.scans.sizeMap.Load(root)
		if !ok {
			t.Fatal("Temp path missing from scan cache")
		}
		if defs[i].Size != v.(dirInfo).size {
			t.Fatalf("probe size %d != scan cache %d", defs[i].Size, v.(dirInfo).size)
		}
		if before != 0 {
			t.Fatal("unprobed rule should start at zero size")
		}
		t.Logf("temp_user probe matches cache: %d bytes", defs[i].Size)
	}
}
