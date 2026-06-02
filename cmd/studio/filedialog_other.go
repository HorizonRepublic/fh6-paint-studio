//go:build !windows

package main

// File dialogs are Windows-only (native common dialogs); off Windows these are no-ops so the package
// still builds. The studio is a Windows app (FH6 injection), so these paths are never exercised.

func pickFile(uintptr) string             { return "" }
func pickSaveFile(uintptr, string) string { return "" }
func prewarmDialogs()                     {}
