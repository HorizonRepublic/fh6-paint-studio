//go:build windows

package i18n

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// osLanguages returns the user's default UI language as a single BCP-47 tag (e.g. "uk-UA"), or nil.
// GetUserDefaultLocaleName fills a UTF-16 buffer up to LOCALE_NAME_MAX_LENGTH (85).
func osLanguages() []string {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")
	buf := make([]uint16, 85)
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return nil
	}
	if name := windows.UTF16ToString(buf); name != "" {
		return []string{name}
	}
	return nil
}
