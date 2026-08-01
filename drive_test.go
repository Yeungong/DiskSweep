package main

import "testing"

func TestGetDrives(t *testing.T) {
	a := &App{}
	drives := a.GetDrives()
	if len(drives) == 0 {
		t.Fatal("expected at least one drive")
	}
	foundC := false
	for _, d := range drives {
		if d.Drive == "C:" {
			foundC = true
			if d.Total == 0 {
				t.Fatal("C: total is zero")
			}
			if d.Used > d.Total {
				t.Fatalf("C: used %d > total %d", d.Used, d.Total)
			}
		}
	}
	if !foundC {
		t.Fatal("C: drive not reported")
	}
}
