//go:build !windows

package main

// openInExplorer is a no-op off Windows (the studio is a Windows app; the library folder-reveal is
// Explorer-specific). Keeps the package building on other platforms.
func openInExplorer(string) {}
