package main

import "testing"

// Release builds replace version with -ldflags; an unset build must still say something.
func TestVersionHasADefault(t *testing.T) {
	if version == "" {
		t.Fatal("version must not be empty")
	}
}
