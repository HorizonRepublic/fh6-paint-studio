//go:build !windows

package main

// playDoneSound is a no-op off Windows (the success chime is Win32-only).
func playDoneSound() {}
