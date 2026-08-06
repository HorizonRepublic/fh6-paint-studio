//go:build windows

package inject

import (
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// Native process-memory access for the FH6 injector, on the standard library's syscall package plus
// the four declarations at the bottom of this file. No cgo, and deliberately no
// golang.org/x/sys/windows -- see the comment there for why.

const (
	memCommit    = 0x1000
	memPrivate   = 0x20000
	pageGuard    = 0x100
	pageNoAccess = 0x01
	// PAGE_READWRITE|WRITECOPY|EXECUTE_READWRITE|EXECUTE_WRITECOPY — the readable+writable page mask.
	rwMask        = 0xCC
	maxRegionRead = 256 * 1024 * 1024
	scanChunk     = 64 * 1024 * 1024 // count-scan reads big regions in chunks of this size (no skip)
	userPtrMin    = 0x10000
	userPtrMax    = 0x7FFFFFFFFFFF
)

var (
	modKernel32            = syscall.NewLazyDLL("kernel32.dll")
	procVirtualQueryEx     = modKernel32.NewProc("VirtualQueryEx")
	procReadProcessMemory  = modKernel32.NewProc("ReadProcessMemory")
	procWriteProcessMemory = modKernel32.NewProc("WriteProcessMemory")
	procModule32FirstW     = modKernel32.NewProc("Module32FirstW")
)

// memBasicInfo mirrors MEMORY_BASIC_INFORMATION (x64 layout, 48 bytes).
type memBasicInfo struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	_                 uint32
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
	_                 uint32
}

// proc is an open handle to the target process.
type proc struct {
	h   syscall.Handle
	pid uint32
}

// findProcess returns the pid + name of the first running process matching any of names.
func findProcess(names []string) (uint32, string, error) {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, "", err
	}
	defer syscall.CloseHandle(snap)

	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	var e syscall.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = syscall.Process32First(snap, &e); err == nil; err = syscall.Process32Next(snap, &e) {
		name := syscall.UTF16ToString(e.ExeFile[:])
		if want[strings.ToLower(name)] {
			return e.ProcessID, name, nil
		}
	}
	return 0, "", fmt.Errorf("game process not found (looked for %s) — start FH6 first", strings.Join(names, ", "))
}

func openProc(pid uint32, write bool) (*proc, error) {
	access := uint32(syscall.PROCESS_QUERY_INFORMATION | processVMRead)
	if write {
		access |= processVMOperation | processVMWrite
	}

	// No privilege is requested, and none is needed. The game is an ordinary process owned by the
	// same user at the same integrity level, and Windows grants a full-access handle to those
	// freely — which is why this has always worked without the app ever being elevated. When it does
	// fail, the reason is the Microsoft Store build, which runs sandboxed and refuses a handle to an
	// administrator just as readily; asking for privileges would be a detour to the same dead end.
	h, err := syscall.OpenProcess(access, false, pid)
	if err != nil {
		return nil, fmt.Errorf("OpenProcess(pid %d): %w — if this is the Microsoft Store / Game Pass build of the game it runs sandboxed and cannot be written to; the Steam build injects normally", pid, err)
	}
	return &proc{h: h, pid: pid}, nil
}

func (p *proc) close() {
	if p.h != 0 {
		syscall.CloseHandle(p.h)
		p.h = 0
	}
}

func (p *proc) read(addr uintptr, n int) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	var nread uintptr
	if err := readProcessMemory(p.h, addr, &buf[0], uintptr(n), &nread); err != nil {
		return nil, err
	}
	if int(nread) != n {
		// Return nil (not the partial buffer) so a caller that ignores the error can't index a
		// short read; every reader here checks err, but nil is the safe contract.
		return nil, fmt.Errorf("short read at 0x%x: %d/%d", addr, nread, n)
	}
	return buf, nil
}

func (p *proc) write(addr uintptr, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	var nw uintptr
	if err := writeProcessMemory(p.h, addr, &data[0], uintptr(len(data)), &nw); err != nil {
		return err
	}
	if int(nw) != len(data) {
		return fmt.Errorf("short write at 0x%x: %d/%d", addr, nw, len(data))
	}
	return nil
}

func (p *proc) readU16(addr uintptr) (uint16, bool) {
	b, err := p.read(addr, 2)
	if err != nil {
		return 0, false
	}
	return binary.LittleEndian.Uint16(b), true
}

func (p *proc) readU64(addr uintptr) (uintptr, bool) {
	b, err := p.read(addr, 8)
	if err != nil {
		return 0, false
	}
	return uintptr(binary.LittleEndian.Uint64(b)), true
}

func (p *proc) readFloatPair(addr uintptr) ([2]float32, bool) {
	b, err := p.read(addr, 8)
	if err != nil {
		return [2]float32{}, false
	}
	return [2]float32{
		math.Float32frombits(binary.LittleEndian.Uint32(b[0:])),
		math.Float32frombits(binary.LittleEndian.Uint32(b[4:])),
	}, true
}

func (p *proc) query(addr uintptr) (memBasicInfo, bool) {
	var mbi memBasicInfo
	ret, _, _ := procVirtualQueryEx.Call(uintptr(p.h), addr, uintptr(unsafe.Pointer(&mbi)), unsafe.Sizeof(mbi))
	runtime.KeepAlive(&mbi) // &mbi is passed as a uintptr through .Call; keep it live across the syscall
	return mbi, ret != 0
}

func isReadableWritable(protect uint32) bool {
	if protect&pageGuard != 0 || protect&pageNoAccess != 0 {
		return false
	}
	return protect&rwMask != 0
}

// isReadable reports whether a page protection permits reads (anything but NOACCESS/GUARD).
func isReadable(protect uint32) bool {
	if protect&pageGuard != 0 || protect&pageNoAccess != 0 {
		return false
	}
	return protect&0xFE != 0
}

// moduleBase returns the load address (image base) of the target's main module — needed to turn
// the RTTI CompleteObjectLocator's image-relative offsets into absolute addresses.
func (p *proc) moduleBase() (uintptr, bool) {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPMODULE|syscall.TH32CS_SNAPMODULE32, p.pid)
	if err != nil {
		return 0, false
	}
	defer syscall.CloseHandle(snap)
	var me moduleEntry32
	me.Size = uint32(unsafe.Sizeof(me))
	if err := module32First(snap, &me); err != nil {
		return 0, false
	}
	return me.ModBaseAddr, true // first module is always the .exe
}

func isUserPointer(v uintptr) bool { return v >= userPtrMin && v <= userPtrMax }

func (p *proc) isPrivateWritable(addr uintptr) bool {
	if !isUserPointer(addr) {
		return false
	}
	mbi, ok := p.query(addr)
	if !ok {
		return false
	}
	return mbi.State == memCommit && isReadableWritable(mbi.Protect)
}

// region is a committed memory region.
type region struct {
	base, size uintptr
}

// iterPrivateWritable invokes fn for each committed, writable, MEM_PRIVATE region (stop on false).
func (p *proc) iterPrivateWritable(fn func(region) bool) {
	p.iterRegions(memPrivate, true, fn)
}

// iterRegions invokes fn for each committed region whose Type matches typeFilter (0 = any) and
// whose protection permits the requested access (writableOnly ? read+write : readable). Iteration
// stops when fn returns false.
func (p *proc) iterRegions(typeFilter uint32, writableOnly bool, fn func(region) bool) {
	addr := uintptr(userPtrMin)
	for addr < userPtrMax {
		mbi, ok := p.query(addr)
		if !ok {
			addr += 0x10000
			continue
		}
		base, size := mbi.BaseAddress, mbi.RegionSize
		if size == 0 {
			break
		}
		typeOK := typeFilter == 0 || mbi.Type == typeFilter
		accessOK := isReadable(mbi.Protect)
		if writableOnly {
			accessOK = isReadableWritable(mbi.Protect)
		}
		if mbi.State == memCommit && typeOK && accessOK {
			if !fn(region{base: base, size: size}) {
				return
			}
		}
		next := base + size
		if next <= addr {
			break
		}
		addr = next
	}
}

// The access rights, the struct and the three calls the standard library does not surface.
//
// Declared here rather than taken from golang.org/x/sys/windows. Importing that package links its
// ENTIRE Win32 name table into the binary -- CryptProtectData, AdjustTokenPrivileges,
// CreateProcessAsUserW, CreateServiceW, NtCreateFile and hundreds more this program never calls --
// and pulls in net with a DNS resolver besides. All of it lands in .rdata where a string triage
// finds it, and a memory-write API reached through GetProcAddress reads worse than one declared in
// the import table. These are the four we actually use.
const (
	processVMOperation = 0x0008
	processVMRead      = 0x0010
	processVMWrite     = 0x0020
)

// moduleEntry32 mirrors MODULEENTRY32W (x64 layout).
type moduleEntry32 struct {
	Size         uint32
	ModuleID     uint32
	ProcessID    uint32
	GlblcntUsage uint32
	ProccntUsage uint32
	ModBaseAddr  uintptr
	ModBaseSize  uint32
	Module       syscall.Handle
	ModuleName   [256]uint16
	ExePath      [260]uint16
}

func readProcessMemory(h syscall.Handle, addr uintptr, buf *byte, n uintptr, read *uintptr) error {
	r, _, err := procReadProcessMemory.Call(uintptr(h), addr, uintptr(unsafe.Pointer(buf)), n, uintptr(unsafe.Pointer(read)))
	if r == 0 {
		return err
	}
	return nil
}

func writeProcessMemory(h syscall.Handle, addr uintptr, buf *byte, n uintptr, written *uintptr) error {
	r, _, err := procWriteProcessMemory.Call(uintptr(h), addr, uintptr(unsafe.Pointer(buf)), n, uintptr(unsafe.Pointer(written)))
	if r == 0 {
		return err
	}
	return nil
}

func module32First(snap syscall.Handle, me *moduleEntry32) error {
	r, _, err := procModule32FirstW.Call(uintptr(snap), uintptr(unsafe.Pointer(me)))
	if r == 0 {
		return err
	}
	return nil
}
