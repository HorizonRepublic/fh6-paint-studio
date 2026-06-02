//go:build windows

package main

import "os/exec"

// openInExplorer reveals a folder in Windows Explorer (used for the library root + a panel-set's
// panels/ directory). Best-effort: a failure is silent (the path may not exist yet).
func openInExplorer(path string) { _ = exec.Command("explorer", path).Start() }
