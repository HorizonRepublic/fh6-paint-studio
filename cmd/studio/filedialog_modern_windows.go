//go:build windows

package main

import (
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Modern Common Item Dialog (IFileOpenDialog / IFileSaveDialog) — the Explorer-style file dialog
// Windows 10/11 users expect (breadcrumb address bar, navigation pane, search). Pure COM through
// golang.org/x/sys/windows syscalls (CGO-free), mirroring taskbar_windows.go. Runs on a dedicated
// STA-initialised, OS-locked thread (the dialog pumps its own modal message loop and the shell
// namespace wants a single-threaded apartment). owner=0 (UNOWNED): an owner HWND on Gio's main
// thread would make the modal SendMessage to that thread and deadlock. If COM is unavailable the
// caller falls back to the legacy comdlg32 dialog.

var procCoTaskMemFree = modOle32.NewProc("CoTaskMemFree")

// CLSID_FileOpenDialog {DC1C5A9C-E88A-4DDE-A5A1-60F82A20AEF7}
var clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE,
	Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}

// CLSID_FileSaveDialog {C0B4E2F3-BA21-4773-8DBA-335EC946EB8B}
var clsidFileSaveDialog = windows.GUID{Data1: 0xC0B4E2F3, Data2: 0xBA21, Data3: 0x4773,
	Data4: [8]byte{0x8D, 0xBA, 0x33, 0x5E, 0xC9, 0x46, 0xEB, 0x8B}}

// IID_IFileOpenDialog {D57C7288-D4AD-4768-BE02-9D969532D960}
var iidIFileOpenDialog = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768,
	Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}

// IID_IFileSaveDialog {84BCCD23-5FDE-4CDB-AEA4-AF64B83D78AB}
var iidIFileSaveDialog = windows.GUID{Data1: 0x84BCCD23, Data2: 0x5FDE, Data3: 0x4CDB,
	Data4: [8]byte{0xAE, 0xA4, 0xAF, 0x64, 0xB8, 0x3D, 0x78, 0xAB}}

const (
	// FILEOPENDIALOGOPTIONS bits.
	fosOverwritePrompt = 0x00000002
	fosNoChangeDir     = 0x00000008
	fosForceFilesystem = 0x00000040
	fosPathMustExist   = 0x00000800
	fosFileMustExist   = 0x00001000

	sigdnFilesysPath = 0x80058000 // SIGDN: full filesystem path
)

// iFileDialogVtbl mirrors the IFileDialog vtable (IUnknown, IModalWindow, then IFileDialog) up to the
// methods we call. IFileOpenDialog and IFileSaveDialog both begin with this layout, so one struct
// drives both. Naming the slots avoids pointer arithmetic that go vet would flag.
type iFileDialogVtbl struct {
	QueryInterface      uintptr
	AddRef              uintptr
	Release             uintptr
	Show                uintptr // IModalWindow
	SetFileTypes        uintptr // IFileDialog ...
	SetFileTypeIndex    uintptr
	GetFileTypeIndex    uintptr
	Advise              uintptr
	Unadvise            uintptr
	SetOptions          uintptr
	GetOptions          uintptr
	SetDefaultFolder    uintptr
	SetFolder           uintptr
	GetFolder           uintptr
	GetCurrentSelection uintptr
	SetFileName         uintptr
	GetFileName         uintptr
	SetTitle            uintptr
	SetOkButtonLabel    uintptr
	SetFileNameLabel    uintptr
	GetResult           uintptr
	AddPlace            uintptr
	SetDefaultExtension uintptr
	Close               uintptr
	SetClientGuid       uintptr
	// (ClearClientData, SetFilter, and the IFileOpenDialog/IFileSaveDialog extension slots
	// follow — all unused.)
}

// dialogClientGuid is a stable per-app identity for the file dialogs, so Windows remembers this
// app's last-used folder across sessions (and across build locations).
var dialogClientGuid = windows.GUID{Data1: 0x7C0A3D8E, Data2: 0x2F4B, Data3: 0x4E6A,
	Data4: [8]byte{0x9C, 0x1D, 0xA1, 0xB2, 0xC3, 0xD4, 0xE5, 0xF6}}

type iFileDialog struct{ vtbl *iFileDialogVtbl }

// iShellItemVtbl mirrors IShellItem up to GetDisplayName (the only method we call).
type iShellItemVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	BindToHandler  uintptr
	GetParent      uintptr
	GetDisplayName uintptr
	GetAttributes  uintptr
	Compare        uintptr
}

type iShellItem struct{ vtbl *iShellItemVtbl }

// comdlgFilterSpec is COMDLG_FILTERSPEC { LPCWSTR pszName; LPCWSTR pszSpec }.
type comdlgFilterSpec struct {
	pszName *uint16
	pszSpec *uint16
}

func (d *iFileDialog) release() {
	syscall.SyscallN(d.vtbl.Release, uintptr(unsafe.Pointer(d)))
}
func (d *iFileDialog) getOptions() uint32 {
	var opts uint32
	syscall.SyscallN(d.vtbl.GetOptions, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(&opts)))
	return opts
}
func (d *iFileDialog) setOptions(opts uint32) {
	syscall.SyscallN(d.vtbl.SetOptions, uintptr(unsafe.Pointer(d)), uintptr(opts))
}
func (d *iFileDialog) setClientGuid(g *windows.GUID) {
	syscall.SyscallN(d.vtbl.SetClientGuid, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(g)))
}
func (d *iFileDialog) setTitle(s string) {
	if p, err := windows.UTF16PtrFromString(s); err == nil {
		syscall.SyscallN(d.vtbl.SetTitle, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(p)))
		runtime.KeepAlive(p)
	}
}
func (d *iFileDialog) setFileName(s string) {
	if p, err := windows.UTF16PtrFromString(s); err == nil {
		syscall.SyscallN(d.vtbl.SetFileName, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(p)))
		runtime.KeepAlive(p)
	}
}
func (d *iFileDialog) setDefaultExtension(s string) {
	if p, err := windows.UTF16PtrFromString(s); err == nil {
		syscall.SyscallN(d.vtbl.SetDefaultExtension, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(p)))
		runtime.KeepAlive(p)
	}
}

// setFileTypes installs the filter (label, pattern) pairs. The dialog copies the strings, so the
// backing slices only need to survive this call.
func (d *iFileDialog) setFileTypes(pairs [][2]string) {
	specs := make([]comdlgFilterSpec, 0, len(pairs))
	keep := make([]*uint16, 0, len(pairs)*2)
	for _, p := range pairs {
		name, err1 := windows.UTF16PtrFromString(p[0])
		spec, err2 := windows.UTF16PtrFromString(p[1])
		if err1 != nil || err2 != nil {
			continue
		}
		keep = append(keep, name, spec)
		specs = append(specs, comdlgFilterSpec{pszName: name, pszSpec: spec})
	}
	if len(specs) == 0 {
		return
	}
	syscall.SyscallN(d.vtbl.SetFileTypes, uintptr(unsafe.Pointer(d)),
		uintptr(len(specs)), uintptr(unsafe.Pointer(&specs[0])))
	runtime.KeepAlive(specs)
	runtime.KeepAlive(keep)
}

// show runs the modal dialog; returns the HRESULT (0 = a selection was confirmed).
func (d *iFileDialog) show(owner uintptr) uintptr {
	hr, _, _ := syscall.SyscallN(d.vtbl.Show, uintptr(unsafe.Pointer(d)), owner)
	return hr
}

// result returns the chosen path after a confirmed show, or "".
func (d *iFileDialog) result() string {
	var item *iShellItem
	hr, _, _ := syscall.SyscallN(d.vtbl.GetResult, uintptr(unsafe.Pointer(d)), uintptr(unsafe.Pointer(&item)))
	if hr != 0 || item == nil {
		return ""
	}
	defer syscall.SyscallN(item.vtbl.Release, uintptr(unsafe.Pointer(item)))
	var p *uint16
	hr, _, _ = syscall.SyscallN(item.vtbl.GetDisplayName, uintptr(unsafe.Pointer(item)),
		uintptr(sigdnFilesysPath), uintptr(unsafe.Pointer(&p)))
	if hr != 0 || p == nil {
		return ""
	}
	path := windows.UTF16PtrToString(p)
	procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	return path
}

// runItemDialog creates the dialog, lets configure() set it up, shows it, and returns
// (path, created). created=false means the COM object could not be created — the caller should fall
// back to the legacy dialog. created=true with an empty path means the user cancelled.
func runItemDialog(clsid, iid *windows.GUID, owner uintptr, configure func(*iFileDialog)) (string, bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if hr != hrChangedMode {
		defer procCoUninitialize.Call()
	}
	var obj *iFileDialog
	r, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(clsid)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&obj)))
	if r != 0 || obj == nil {
		return "", false // COM unavailable — fall back to legacy
	}
	defer obj.release()
	configure(obj)
	if obj.show(owner) != 0 {
		return "", true // cancelled / closed — dialog handled it, no fallback
	}
	return obj.result(), true
}

func pickFileModern(owner uintptr) (string, bool) {
	return runItemDialog(&clsidFileOpenDialog, &iidIFileOpenDialog, owner, func(d *iFileDialog) {
		d.setClientGuid(&dialogClientGuid)
		d.setOptions(d.getOptions() | fosForceFilesystem | fosFileMustExist | fosPathMustExist | fosNoChangeDir)
		d.setFileTypes([][2]string{
			{"Images", "*.png;*.jpg;*.jpeg;*.webp;*.bmp;*.tif;*.tiff"},
			{"All files", "*.*"},
		})
		d.setTitle("Open image")
	})
}

func pickSaveFileModern(owner uintptr, suggested string) (string, bool) {
	return runItemDialog(&clsidFileSaveDialog, &iidIFileSaveDialog, owner, func(d *iFileDialog) {
		d.setClientGuid(&dialogClientGuid)
		d.setOptions(d.getOptions() | fosForceFilesystem | fosOverwritePrompt | fosPathMustExist | fosNoChangeDir)
		d.setFileTypes([][2]string{
			{"Forza geometry (*.forza.json)", "*.forza.json"},
			{"All files", "*.*"},
		})
		d.setTitle("Export geometry")
		d.setDefaultExtension("json")
		if name := filepath.Base(suggested); name != "" && name != "." {
			d.setFileName(name)
		}
	})
}

// prewarmDialogs creates and immediately releases a FileOpenDialog on a background STA thread at
// startup. This loads the shell dialog infrastructure ahead of the first real Open, so clicking
// Open shows the dialog without the cold-start delay. Best-effort: any failure is ignored.
func prewarmDialogs() {
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
		if hr != hrChangedMode {
			defer procCoUninitialize.Call()
		}
		var obj *iFileDialog
		r, _, _ := procCoCreateInstance.Call(
			uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
			uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&obj)))
		if r == 0 && obj != nil {
			obj.release()
		}
	}()
}
