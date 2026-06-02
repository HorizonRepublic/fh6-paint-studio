//go:build !windows

package main

// The window icon is set through Win32 (WM_SETICON); on other platforms this is a no-op so the
// package still builds.
func applyWindowIcon(any) {}
