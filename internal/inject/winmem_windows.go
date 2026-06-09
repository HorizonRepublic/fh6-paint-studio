//go:build windows

package inject

import (
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native process-memory access for the FH6 injector, built on golang.org/x/sys/windows (no cgo).

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
	modKernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procVirtualQueryEx = modKernel32.NewProc("VirtualQueryEx")
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
	h   windows.Handle
	pid uint32
}

// findProcess returns the pid + name of the first running process matching any of names.
func findProcess(names []string) (uint32, string, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, "", err
	}
	defer windows.CloseHandle(snap)

	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		name := windows.UTF16ToString(e.ExeFile[:])
		if want[strings.ToLower(name)] {
			return e.ProcessID, name, nil
		}
	}
	return 0, "", fmt.Errorf("game process not found (looked for %s) — start FH6 first", strings.Join(names, ", "))
}

func openProc(pid uint32, write bool) (*proc, error) {
	access := uint32(windows.PROCESS_QUERY_INFORMATION | windows.PROCESS_VM_READ)
	if write {
		access |= windows.PROCESS_VM_OPERATION | windows.PROCESS_VM_WRITE
	}
	h, err := windows.OpenProcess(access, false, pid)
	if err != nil {
		return nil, fmt.Errorf("OpenProcess(pid %d): %w (try running as administrator)", pid, err)
	}
	return &proc{h: h, pid: pid}, nil
}

func (p *proc) close() {
	if p.h != 0 {
		windows.CloseHandle(p.h)
		p.h = 0
	}
}

func (p *proc) read(addr uintptr, n int) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	var nread uintptr
	if err := windows.ReadProcessMemory(p.h, addr, &buf[0], uintptr(n), &nread); err != nil {
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
	if err := windows.WriteProcessMemory(p.h, addr, &data[0], uintptr(len(data)), &nw); err != nil {
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
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, p.pid)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(snap)
	var me windows.ModuleEntry32
	me.Size = uint32(unsafe.Sizeof(me))
	if err := windows.Module32First(snap, &me); err != nil {
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
