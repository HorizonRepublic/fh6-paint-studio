//go:build windows

package main

import "os/exec"

// openURL opens url in the default browser (best-effort).
func openURL(url string) { _ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start() }
