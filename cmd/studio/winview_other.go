//go:build !windows

package main

// Off Windows there is no HWND and no taskbar flash — the helpers are no-ops so the loop needs no
// platform guards.
func viewHWND(any) uintptr { return 0 }
func flashWindow(uintptr)  {}
