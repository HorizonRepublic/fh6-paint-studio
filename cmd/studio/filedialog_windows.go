//go:build windows

package main

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native Win32 common dialogs via comdlg32 (GetOpenFileNameW / GetSaveFileNameW) — no powershell
// child process (so no flashing console), instant, and CGO-free (pure golang.org/x/sys/windows
// syscalls, the same mechanism the FH6 injector uses). Owned by the app window so they centre on it
// and disable it while open (proper modality).

var (
	modComdlg32         = windows.NewLazySystemDLL("comdlg32.dll")
	procGetOpenFileName = modComdlg32.NewProc("GetOpenFileNameW")
	procGetSaveFileName = modComdlg32.NewProc("GetSaveFileNameW")
	modOle32            = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx  = modOle32.NewProc("CoInitializeEx")
	procCoUninitialize  = modOle32.NewProc("CoUninitialize")
)

// openFileNameW mirrors the Win32 OPENFILENAMEW struct (x64 layout). Pointer fields use *uint16
// (wide strings); handle/opaque fields use uintptr. lStructSize is set from unsafe.Sizeof so the
// layout never has to be hand-counted.
type openFileNameW struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	flagsEx           uint32
}

const (
	ofnOverwritePrompt = 0x00000002
	ofnHideReadOnly    = 0x00000004
	ofnNoChangeDir     = 0x00000008
	ofnPathMustExist   = 0x00000800
	ofnFileMustExist   = 0x00001000
	ofnExplorer        = 0x00080000

	coinitApartmentThreaded = 0x2
	hrChangedMode           = 0x80010106 // RPC_E_CHANGED_MODE: thread already COM-init'd in another mode
)

// pickFile opens the image open-file dialog and returns the chosen path, or "" on cancel. It uses
// the modern Common Item Dialog (IFileOpenDialog) — the Explorer-style dialog Windows users expect —
// and falls back to the legacy comdlg32 dialog only if COM is unavailable.
func pickFile(owner uintptr) string {
	if path, ok := pickFileModern(owner); ok {
		return path
	}
	return pickFileLegacy(owner)
}

// pickSaveFile opens the save-as dialog pre-filled with `suggested`, returning the chosen path
// (with a .json default extension), or "" on cancel. Prompts before overwriting. Modern dialog with
// a legacy fallback, mirroring pickFile.
func pickSaveFile(owner uintptr, suggested string) string {
	if path, ok := pickSaveFileModern(owner, suggested); ok {
		return path
	}
	return pickSaveFileLegacy(owner, suggested)
}

// pickFileLegacy is the comdlg32 GetOpenFileNameW fallback (older dialog style).
func pickFileLegacy(owner uintptr) string {
	buf := make([]uint16, 4096) // receives the selected path (must be writable)
	filter := ofnFilter("Images", "*.png;*.jpg;*.jpeg;*.webp;*.bmp;*.tif;*.tiff", "All files", "*.*")
	ofn := newOFN(owner, buf, filter, "Open image", nil)
	ofn.flags = ofnExplorer | ofnFileMustExist | ofnPathMustExist | ofnHideReadOnly | ofnNoChangeDir
	if !runDialog(procGetOpenFileName, &ofn) {
		return ""
	}
	return windows.UTF16ToString(buf)
}

// pickSaveFileLegacy is the comdlg32 GetSaveFileNameW fallback (older dialog style).
func pickSaveFileLegacy(owner uintptr, suggested string) string {
	buf := make([]uint16, 4096)
	if init, err := windows.UTF16FromString(suggested); err == nil && len(init) <= len(buf) {
		copy(buf, init) // the dialog shows this path/name as the default
	}
	filter := ofnFilter("Forza geometry (*.forza.json)", "*.forza.json", "All files", "*.*")
	defExt, _ := windows.UTF16PtrFromString("json")
	ofn := newOFN(owner, buf, filter, "Export geometry", defExt)
	ofn.flags = ofnExplorer | ofnOverwritePrompt | ofnPathMustExist | ofnHideReadOnly | ofnNoChangeDir
	if !runDialog(procGetSaveFileName, &ofn) {
		return ""
	}
	return windows.UTF16ToString(buf)
}

// newOFN fills a common OPENFILENAMEW. fileBuf is both the initial filename and the result buffer.
// The caller keeps fileBuf, filter (and is the owner of title/defExt via the returned struct's
// pointer fields) alive across runDialog, so nothing is GC'd mid-syscall.
func newOFN(owner uintptr, fileBuf []uint16, filter []uint16, title string, defExt *uint16) openFileNameW {
	t, _ := windows.UTF16PtrFromString(title)
	ofn := openFileNameW{
		hwndOwner:    owner,
		lpstrFilter:  &filter[0],
		nFilterIndex: 1,
		lpstrFile:    &fileBuf[0],
		nMaxFile:     uint32(len(fileBuf)),
		lpstrTitle:   t,
		lpstrDefExt:  defExt,
	}
	ofn.lStructSize = uint32(unsafe.Sizeof(ofn))
	return ofn
}

// runDialog runs a comdlg32 dialog on a locked OS thread with STA COM init (the shell common dialog
// runs its own modal message loop and wants a single-threaded apartment for namespace extensions).
// Returns true when the user confirmed a selection.
func runDialog(proc *windows.LazyProc, ofn *openFileNameW) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if hr != hrChangedMode { // we initialized COM on this thread (S_OK/S_FALSE) -> balance it
		defer procCoUninitialize.Call()
	}
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(ofn)))
	return r != 0
}

// ofnFilter builds the OPENFILENAME filter: NUL-separated "label\0pattern\0..." with a final
// double-NUL. Each UTF16FromString already appends one trailing NUL.
func ofnFilter(parts ...string) []uint16 {
	var buf []uint16
	for _, p := range parts {
		u, err := windows.UTF16FromString(p)
		if err != nil {
			continue
		}
		buf = append(buf, u...) // includes the trailing NUL
	}
	return append(buf, 0) // final terminating NUL
}
