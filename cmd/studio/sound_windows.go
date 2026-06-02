//go:build windows

package main

import "golang.org/x/sys/windows"

var (
	modUser32       = windows.NewLazySystemDLL("user32.dll")
	procMessageBeep = modUser32.NewProc("MessageBeep")
)

// playDoneSound plays the Windows "Asterisk" notification chime — the standard done/success sound.
// Best-effort (the result is ignored); CGO-free via user32!MessageBeep.
func playDoneSound() { _, _, _ = procMessageBeep.Call(0x40) } // MB_ICONASTERISK
