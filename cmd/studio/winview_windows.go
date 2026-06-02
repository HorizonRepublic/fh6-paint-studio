//go:build windows

package main

import (
	"unsafe"

	"gioui.org/app"
	"golang.org/x/sys/windows"
)

// Windows-only window helpers: extract the native HWND from Gio's view event (to drive the taskbar
// progress + a completion flash), and flash the taskbar button when a generation finishes while the
// user is in another window (e.g. FH6). Pure x/sys/windows syscalls, CGO-free.

var (
	modUser32View     = windows.NewLazySystemDLL("user32.dll")
	procFlashWindowEx = modUser32View.NewProc("FlashWindowEx")
)

const (
	flashwTray      = 0x00000002 // flash the taskbar button
	flashwTimerNoFG = 0x0000000C // keep flashing until the window comes to the foreground
)

// flashwinfo mirrors the Win32 FLASHWINFO struct.
type flashwinfo struct {
	cbSize    uint32
	hwnd      uintptr
	dwFlags   uint32
	uCount    uint32
	dwTimeout uint32
}

// viewHWND returns the native window handle from a Gio Win32 view event (0 for any other event).
func viewHWND(ev any) uintptr {
	if ve, ok := ev.(app.Win32ViewEvent); ok {
		return ve.HWND
	}
	return 0
}

// flashWindow flashes the taskbar button until the window is brought to the foreground. A no-op if the
// window is already in the foreground (FLASHW_TIMERNOFG), so it only nags when the user is away.
func flashWindow(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	fi := flashwinfo{dwFlags: flashwTray | flashwTimerNoFG}
	fi.cbSize = uint32(unsafe.Sizeof(fi))
	fi.hwnd = hwnd
	procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
}
