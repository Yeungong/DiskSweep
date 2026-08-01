package main

import (
	"testing"
)

func TestIsAdmin(t *testing.T) {
	a := &App{}
	// Must not panic and must return a bool. Result depends on the environment;
	// on a normal dev machine it should be false (standard token).
	got := a.IsAdmin()
	_ = got
}
