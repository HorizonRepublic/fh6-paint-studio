//go:build windows

package main

import (
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows taskbar progress via ITaskbarList3 (the green fill on the app's taskbar button during a
// generation). Pure COM through golang.org/x/sys/windows syscalls (CGO-free). The COM object is
// apartment-threaded, so it is created and used on ONE dedicated, STA-initialised, OS-locked thread;
// the UI feeds it (completed,total) over a channel. Any COM failure silently disables it — the
// taskbar is purely cosmetic and must never block or crash the UI.

var procCoCreateInstance = modOle32.NewProc("CoCreateInstance")

// CLSID_TaskbarList {56FDF344-FD6D-11D0-958A-006097C9A090}
var clsidTaskbarList = windows.GUID{Data1: 0x56FDF344, Data2: 0xFD6D, Data3: 0x11D0,
	Data4: [8]byte{0x95, 0x8A, 0x00, 0x60, 0x97, 0xC9, 0xA0, 0x90}}

// IID_ITaskbarList3 {EA1AFB91-9E28-4B86-90E9-9E9F8A5EEFAF}
var iidITaskbarList3 = windows.GUID{Data1: 0xEA1AFB91, Data2: 0x9E28, Data3: 0x4B86,
	Data4: [8]byte{0x90, 0xE9, 0x9E, 0x9F, 0x8A, 0x5E, 0xEF, 0xAF}}

const (
	clsctxInprocServer = 0x1

	// TBPFLAG progress states.
	tbpfNoProgress    = 0x0
	tbpfIndeterminate = 0x1
	tbpfNormal        = 0x2
)

// iTaskbarList3Vtbl mirrors the COM vtable in declaration order (IUnknown, ITaskbarList,
// ITaskbarList2, then ITaskbarList3) up to the methods we call. Naming the slots lets us invoke them
// without uintptr pointer arithmetic (which `go vet` rightly flags) — we only ever do the vet-allowed
// uintptr(unsafe.Pointer(x)) directly in the syscall argument list.
type iTaskbarList3Vtbl struct {
	QueryInterface       uintptr
	AddRef               uintptr
	Release              uintptr
	HrInit               uintptr
	AddTab               uintptr
	DeleteTab            uintptr
	ActivateTab          uintptr
	SetActiveAlt         uintptr
	MarkFullscreenWindow uintptr
	SetProgressValue     uintptr
	SetProgressState     uintptr
	// (remaining ITaskbarList3 slots — RegisterTab, ThumbBar*, etc. — are unused)
}

type iTaskbarList3 struct {
	vtbl *iTaskbarList3Vtbl
}

func (o *iTaskbarList3) hrInit() {
	syscall.SyscallN(o.vtbl.HrInit, uintptr(unsafe.Pointer(o)))
}
func (o *iTaskbarList3) release() {
	syscall.SyscallN(o.vtbl.Release, uintptr(unsafe.Pointer(o)))
}
func (o *iTaskbarList3) setState(hwnd uintptr, flags uint32) {
	syscall.SyscallN(o.vtbl.SetProgressState, uintptr(unsafe.Pointer(o)), hwnd, uintptr(flags))
}
func (o *iTaskbarList3) setValue(hwnd uintptr, completed, total uint64) {
	syscall.SyscallN(o.vtbl.SetProgressValue, uintptr(unsafe.Pointer(o)), hwnd, uintptr(completed), uintptr(total))
}

type tbCmd struct {
	state            uint32 // TBPF_*
	hasVal           bool
	completed, total uint64
}

type taskbar struct {
	cmds chan tbCmd
}

// newTaskbar starts the COM-owning worker for the given window handle. Returns nil (a valid no-op
// receiver) if the handle is zero.
func newTaskbar(hwnd uintptr) *taskbar {
	if hwnd == 0 {
		return nil
	}
	t := &taskbar{cmds: make(chan tbCmd, 16)}
	go t.run(hwnd)
	return t
}

func (t *taskbar) run(hwnd uintptr) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if hr != hrChangedMode {
		defer procCoUninitialize.Call()
	}
	var obj *iTaskbarList3
	r, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidTaskbarList)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidITaskbarList3)),
		uintptr(unsafe.Pointer(&obj)),
	)
	if r != 0 || obj == nil { // COM/taskbar unavailable — drain so senders never block, then stop.
		for range t.cmds {
		}
		return
	}
	obj.hrInit()
	defer obj.release()
	for c := range t.cmds {
		obj.setState(hwnd, c.state)
		if c.hasVal {
			obj.setValue(hwnd, c.completed, c.total)
		}
	}
}

// set shows a determinate progress fill (completed/total). Non-blocking — a dropped frequent update
// is corrected by the next one.
func (t *taskbar) set(completed, total uint64) {
	if t == nil || total == 0 {
		return
	}
	select {
	case t.cmds <- tbCmd{state: tbpfNormal, hasVal: true, completed: completed, total: total}:
	default:
	}
}

// indeterminate / clear are rare state changes (the worker drains fast). Sent non-blocking like
// set() so a stalled COM call under a Status burst can never block the Gio event loop — a dropped
// marquee toggle is harmless and the next state change self-corrects.
func (t *taskbar) indeterminate() { t.send(tbCmd{state: tbpfIndeterminate}) }
func (t *taskbar) clear()         { t.send(tbCmd{state: tbpfNoProgress}) }

func (t *taskbar) send(c tbCmd) {
	if t == nil {
		return
	}
	select {
	case t.cmds <- c:
	default:
	}
}

func (t *taskbar) close() {
	if t != nil {
		close(t.cmds)
	}
}
