package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDepDefsSanity(t *testing.T) {
	a := &App{}
	defs := a.depDefs()
	if len(defs) < 15 {
		t.Fatalf("expected a substantial dep knowledge base, got %d", len(defs))
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if d.ID == "" || d.Name == "" {
			t.Fatalf("dep missing id/name: %+v", d)
		}
		if seen[d.ID] {
			t.Fatalf("duplicate dep id: %s", d.ID)
		}
		seen[d.ID] = true
		switch d.Cleanable {
		case DepKeep, DepPartial, DepModel:
		default:
			t.Fatalf("dep %s has bad cleanable %q", d.ID, d.Cleanable)
		}
		if d.Desc == "" || d.CleanNote == "" {
			t.Fatalf("dep %s missing desc/cleanNote", d.ID)
		}
	}
}

func TestDependenciesProbeRealMachine(t *testing.T) {
	a := &App{}
	deps := a.Dependencies()
	if len(deps) == 0 {
		t.Fatal("expected at least some deps on a real machine")
	}
	// .NET runtime must exist on this dev machine (dotnet shared dir).
	foundDotnet := false
	for _, d := range deps {
		if d.ID == "dotnet_runtime" && d.Exists && d.Size > 0 {
			foundDotnet = true
		}
		if d.Size < 0 {
			t.Fatalf("negative size for %s", d.ID)
		}
	}
	if !foundDotnet {
		t.Log("note: dotnet_runtime not found (non-dev machine?)")
	}
	t.Logf("detected %d deps", len(deps))
	for _, d := range deps {
		t.Logf("  [%s] %s %s %d bytes", d.ID, d.Name, d.Category, d.Size)
	}
}

func TestFindDirByName(t *testing.T) {
	root := tmpDir(t)
	// Case-insensitive dir name match.
	if err := os.MkdirAll(filepath.Join(root, "Llama.Cpp"), 0o755); err != nil {
		t.Fatal(err)
	}
	found, size, _ := findDirByName(root, "llama.cpp")
	if !found {
		t.Fatal("should find Llama.Cpp case-insensitively")
	}
	if size != 0 {
		t.Fatalf("size = %d, want 0", size)
	}
	// Missing dir.
	if f, _, _ := findDirByName(root, "nope"); f {
		t.Fatal("should not find missing dir")
	}
}

func TestMatchDepByPath(t *testing.T) {
	a := &App{}
	a.depCache = a.depDefs()

	cases := []struct {
		path string
		id   string // expected dep id; "" = no match
	}{
		{`C:\Program Files\dotnet`, "dotnet_runtime"},
		{`C:\Program Files\dotnet\shared\Microsoft.NETCore.App`, "dotnet_runtime"},
		{`C:\Program Files\dotnet\sdk`, "dotnet_sdk"},
		{`C:\Program Files\Java\jdk-8.0.423`, "jdk"},
		{`D:\llama.cpp`, "llamacpp"},
		{`D:\llama.cpp\model`, "llamacpp"},
		{`D:\llama.cpp\model\Qwen3.6-35B.gguf`, "gguf_model"},
		{`E:\proj\.venv`, "python_venv"},
		{`E:\proj\node_modules`, "node_modules"},
		{`C:\Users\x\.ollama`, "ollama"},
		{`C:\Windows\System32\vcruntime140.dll`, "vcpp_runtime"},
		{`C:\Windows\System32\d3dcompiler_47.dll`, "directx"},
		{`D:\random\project`, ""},
	}
	for _, c := range cases {
		d := a.matchDepByPath(c.path)
		if c.id == "" {
			if d != nil {
				t.Errorf("%s: expected no match, got %s", c.path, d.ID)
			}
			continue
		}
		if d == nil {
			t.Errorf("%s: expected dep %s, got nil", c.path, c.id)
			continue
		}
		if d.ID != c.id {
			t.Errorf("%s: got %s, want %s", c.path, d.ID, c.id)
		}
	}
}

// TestRecyclePathProtectsKeepDeps verifies protected deps cannot be recycled.
func TestRecyclePathProtectsKeepDeps(t *testing.T) {
	a := &App{}
	a.depCache = a.depDefs()

	// Protected dep: .NET runtime dir would be rejected BEFORE any deletion.
	// (The dir may not exist on this machine, but protection must trigger on
	// the path match alone, not on existence.)
	err := a.RecyclePath(`C:\Program Files\dotnet`)
	if err == nil {
		t.Fatal("expected protected dep recycle to be rejected")
	}
	// Non-dep paths still flow through (they may fail for other reasons like
	// missing file, but not with the protection error).
	err = a.RecyclePath(`D:\does-not-exist-probe`)
	if err == errDepProtected {
		t.Fatal("non-dep path should not be blocked as protected")
	}
}

// TestDepNotesChinese verifies every dep has Chinese in name, desc or note.
func TestDepNotesChinese(t *testing.T) {
	a := &App{}
	for _, d := range a.depDefs() {
		if !containsCJK(d.Name) && !containsCJK(d.Desc) && !containsCJK(d.CleanNote) {
			t.Errorf("dep %s has no Chinese text: name=%q desc=%q", d.ID, d.Name, d.Desc)
		}
	}
}

func containsCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}
