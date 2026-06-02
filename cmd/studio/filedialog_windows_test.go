//go:build windows

package main

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Win32 OPENFILENAMEW struct is 152 bytes on x64. GetOpenFileNameW validates lStructSize and
// reads fields by offset, so a wrong layout silently breaks the dialog (or worse). Pin the size.
func TestOpenFileNameLayout(t *testing.T) {
	if got := unsafe.Sizeof(openFileNameW{}); got != 152 {
		t.Fatalf("OPENFILENAMEW size = %d, want 152 (x64 ABI mismatch)", got)
	}
}

// ofnFilter must produce NUL-separated label/pattern pairs ending in a double-NUL.
func TestOFNFilter(t *testing.T) {
	got := ofnFilter("Images", "*.png", "All", "*.*")
	want := []uint16{}
	for _, s := range []string{"Images", "*.png", "All", "*.*"} {
		u, _ := windows.UTF16FromString(s)
		want = append(want, u...)
	}
	want = append(want, 0)
	if len(got) != len(want) {
		t.Fatalf("filter len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filter[%d] = %d, want %d", i, got[i], want[i])
		}
	}
	// Ends in a double-NUL (the last pattern's trailing NUL + the terminator).
	if n := len(got); got[n-1] != 0 || got[n-2] != 0 {
		t.Fatalf("filter must end in double-NUL, got ...%d,%d", got[n-2], got[n-1])
	}
}
