//go:build windows

package main

import (
	_ "embed"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"gioui.org/app"
	"golang.org/x/sys/windows"

	"fh6-paint-studio/internal/applog"
)

//go:embed icon.ico
var iconICO []byte

var (
	modUser32Icon    = windows.NewLazySystemDLL("user32.dll")
	procLoadImageW   = modUser32Icon.NewProc("LoadImageW")
	procPostMessageW = modUser32Icon.NewProc("PostMessageW")
	iconOnce         sync.Once
)

const (
	imageIcon      = 1
	lrLoadFromFile = 0x0010
	lrDefaultSize  = 0x0040
	wmSetIcon      = 0x0080
	iconSmall      = 0
	iconBig        = 1
)

// applyWindowIcon sets the studio window's title-bar + taskbar icon from the embedded .ico when the
// Win32 view first appears. The exe's resource icon already covers Explorer; this guarantees the live
// window shows our icon regardless of which resource ID the windowing toolkit looks up. Best-effort.
func applyWindowIcon(ev any) {
	ve, ok := ev.(app.Win32ViewEvent)
	if !ok || ve.HWND == 0 {
		return
	}
	iconOnce.Do(func() { setWindowIcon(ve.HWND) })
}

func setWindowIcon(hwnd uintptr) {
	if len(iconICO) == 0 {
		return
	}
	// LoadImage needs a file; write the embedded multi-size .ico to a stable temp path.
	path := filepath.Join(os.TempDir(), "fh6-paint-studio-icon.ico")
	if err := os.WriteFile(path, iconICO, 0o644); err != nil {
		return
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	load := func(cx, cy int, flags uintptr) uintptr {
		h, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(p)), imageIcon,
			uintptr(cx), uintptr(cy), flags|lrLoadFromFile)
		return h
	}
	big := load(0, 0, lrDefaultSize) // default large size (LoadImage picks the best matching entry)
	small := load(16, 16, 0)
	// PostMessage (async) — NOT SendMessage: this runs on the event goroutine, and a synchronous
	// SendMessage to the window (owned by the UI thread) would deadlock the frame handshake.
	if big != 0 {
		procPostMessageW.Call(hwnd, wmSetIcon, iconBig, big)
	}
	if small != 0 {
		procPostMessageW.Call(hwnd, wmSetIcon, iconSmall, small)
	}
	applog.Printf("window icon set (big=%v small=%v)", big != 0, small != 0)
}
