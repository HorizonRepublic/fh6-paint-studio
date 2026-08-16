//go:build windows

package inject

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"fh6-paint-studio/internal/model"
)

// writeSafeProcess reports whether a matched process is the game the FH6 layer offsets were
// calibrated against. The reverse-engineered field/table offsets must NEVER be applied as WRITES
// to Forza Horizon 5 (a different struct layout); FH5 stays in the process list for read-only
// diagnostics only.
func writeSafeProcess(name string) bool {
	return strings.Contains(strings.ToLower(name), "horizon6")
}

// run performs the live injection: find the process, locate the layer table, write each
// supported shape into a consecutive template slot (compacting over skipped lines), then blank
// the leftover template layers.
func (f *FH6) run(shapes []model.Shape, cm CanvasMap) error {
	pid, name, err := findProcess(f.Profile.ProcessNames)
	if err != nil {
		return err
	}
	f.logf("found %s (pid %d)", name, pid)
	if !writeSafeProcess(name) {
		return fmt.Errorf("refusing to inject into %q — these layer offsets are reverse-engineered for Forza Horizon 6 only; FH5 has a different struct layout (read-only diagnostics are still allowed)", name)
	}

	p, err := openProc(pid, true)
	if err != nil {
		return err
	}
	defer p.close()

	f.logf("locating layer table for %d template layers…", f.Layers)
	table, err := locateTable(p, f.Profile, f.Layers, func(s string) { f.logf("%s", s) })
	if err != nil {
		return err
	}
	f.logf("located layer table @ 0x%x", table)

	written := 0
	for _, s := range shapes {
		lw, ok := ShapeToLayer(s, cm)
		if !ok {
			continue // line / unsupported — compact over it
		}
		if written >= f.Layers {
			f.logf("reached template capacity (%d layers); %d shapes not written", f.Layers, countSupported(shapes)-written)
			break
		}
		ptr, ok := p.readU64(table + uintptr(written)*8)
		// Re-assert private+writable per slot immediately before writing (not just a range check):
		// the editor heap can move/free a layer between locate-time validation and this write (add/
		// delete/undo/close-group), so a stale slot that still passes the range check must NOT be
		// written. Abort the whole inject — a partial write is worse than none.
		if !ok || !p.isPrivateWritable(ptr) {
			return fmt.Errorf("layer slot %d resolved to a null/invalid/non-writable pointer (stale table — re-open the group and auto-locate)", written+1)
		}
		for _, fw := range lw.Writes(f.Profile) {
			if err := p.write(ptr+uintptr(fw.Offset), fw.Data); err != nil {
				return fmt.Errorf("write layer %d field +0x%x: %w", written+1, fw.Offset, err)
			}
		}
		if f.WritePaths {
			f.writeLayerPath(p, ptr, lw.Word) // best-effort; a stale path just falls back to reload
		}
		written++
		if written == 1 || written%100 == 0 {
			f.logf("wrote layer %d/%d", written, f.Layers)
		}
	}
	f.logf("wrote %d shapes into the template", written)

	if f.ClearUnused {
		cleared := 0
		for idx := written; idx < f.Layers; idx++ {
			ptr, ok := p.readU64(table + uintptr(idx)*8)
			// isPrivateWritable, not the weaker range-only isUserPointer: the write loop above
			// re-asserts it per slot for exactly this reason — a slot the editor freed since
			// locate could be a recycled writable heap block we must not scribble on.
			if !ok || !p.isPrivateWritable(ptr) {
				continue
			}
			slotOK := true
			for _, fw := range ClearWrites(f.Profile) {
				if err := p.write(ptr+uintptr(fw.Offset), fw.Data); err != nil {
					slotOK = false
					break // leave partially-cleared leftover; not fatal
				}
			}
			if slotOK {
				cleared++ // only count slots fully cleared, so the log doesn't over-report
			}
		}
		f.logf("cleared %d unused template layers", cleared)
	}

	f.logf("DONE — now SAVE the vinyl in FH6 and load it again to apply. FH6 re-derives each layer's")
	f.logf("mesh from its shape-word only on (re)load, so a fresh inject shows STALE (circle) meshes —")
	f.logf("triangles/squares look like circles until you save+reload, then they render correctly.")
	return nil
}

// locate finds the process + layer table read-only (no writes).
func (f *FH6) locate() (string, error) {
	pid, name, err := findProcess(f.Profile.ProcessNames)
	if err != nil {
		return "", err
	}
	p, err := openProc(pid, false) // read-only handle
	if err != nil {
		return "", err
	}
	defer p.close()
	table, err := locateTable(p, f.Profile, f.Layers, func(s string) { f.logf("%s", s) })
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s (pid %d): layer table @ 0x%x validated for %d layers", name, pid, table, f.Layers), nil
}

// dump locates the live table and decodes the requested layer slots (read-only).
func (f *FH6) dump(indices []int) ([]LayerInfo, error) {
	pid, name, err := findProcess(f.Profile.ProcessNames)
	if err != nil {
		return nil, err
	}
	f.logf("found %s (pid %d)", name, pid)
	p, err := openProc(pid, false) // read-only
	if err != nil {
		return nil, err
	}
	defer p.close()
	table, err := locateTable(p, f.Profile, f.Layers, func(s string) { f.logf("%s", s) })
	if err != nil {
		return nil, err
	}
	f.logf("located layer table @ 0x%x", table)

	out := make([]LayerInfo, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= f.Layers {
			continue
		}
		ptr, ok := p.readU64(table + uintptr(idx)*8)
		if !ok || !isUserPointer(ptr) {
			continue
		}
		raw, err := p.read(ptr, 0xB0)
		if err != nil || len(raw) < 0xB0 {
			raw, err = p.read(ptr, 0x80) // fall back to the short layout if the tail is unreadable
			if err != nil || len(raw) < 0x7C {
				continue
			}
		}
		out = append(out, decodeLayer(idx, ptr, raw, f.Profile))
	}
	return out, nil
}

// RawLayer is one slot's raw struct bytes, for offset-level diagnostics (read-only).
type RawLayer struct {
	Index int
	Ptr   uintptr
	Bytes []byte
}

// DumpRaw reads `size` bytes of each requested layer slot's struct from the live group (read-only).
// Used by the mesh-path diagnostics to inspect fields beyond the decoded set (e.g. the resource-path
// pointer at +0x80 and the geometry resource at +0xA8).
func (f *FH6) DumpRaw(indices []int, size int) ([]RawLayer, error) {
	pid, name, err := findProcess(f.Profile.ProcessNames)
	if err != nil {
		return nil, err
	}
	f.logf("found %s (pid %d)", name, pid)
	p, err := openProc(pid, false) // read-only
	if err != nil {
		return nil, err
	}
	defer p.close()
	table, err := locateTable(p, f.Profile, f.Layers, func(s string) { f.logf("%s", s) })
	if err != nil {
		return nil, err
	}
	out := make([]RawLayer, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= f.Layers {
			continue
		}
		ptr, ok := p.readU64(table + uintptr(idx)*8)
		if !ok || !isUserPointer(ptr) {
			continue
		}
		raw, err := p.read(ptr, size)
		if err != nil || len(raw) < size {
			continue
		}
		out = append(out, RawLayer{Index: idx, Ptr: ptr, Bytes: raw})
	}
	return out, nil
}

// ReadAt reads n bytes at an arbitrary address in the live game (read-only diagnostic) — used to
// follow a layer's pointers (the resource-path string at +0x80, the resource at +0xA8) to their
// targets. It opens its own read-only handle, so it is fine to call standalone.
func (f *FH6) ReadAt(addr uintptr, n int) ([]byte, error) {
	pid, _, err := findProcess(f.Profile.ProcessNames)
	if err != nil {
		return nil, err
	}
	p, err := openProc(pid, false) // read-only
	if err != nil {
		return nil, err
	}
	defer p.close()
	return p.read(addr, n)
}

// SearchHit is one occurrence of a needle in the live game's memory (read-only).
type SearchHit struct {
	Addr    uintptr
	Context []byte // bytes surrounding the hit (ctxBefore before, then the needle + tail)
}

// SearchAll scans ALL committed readable memory (image + mapped + private heap) for needle and returns
// up to max hits, each with `ctxBefore` bytes before and `ctxAfter` bytes after for structure spotting.
// Read-only diagnostic — used to locate the shape catalog by an anchor path string.
func (f *FH6) SearchAll(needle []byte, max, ctxBefore, ctxAfter int) ([]SearchHit, error) {
	pid, _, err := findProcess(f.Profile.ProcessNames)
	if err != nil {
		return nil, err
	}
	p, err := openProc(pid, false)
	if err != nil {
		return nil, err
	}
	defer p.close()
	var hits []SearchHit
	const chunk = 32 * 1024 * 1024
	addr := uintptr(userPtrMin)
	for addr < userPtrMax && len(hits) < max {
		mbi, ok := p.query(addr)
		if !ok {
			addr += 0x10000
			continue
		}
		base, size := mbi.BaseAddress, mbi.RegionSize
		if size == 0 {
			break
		}
		if mbi.State == memCommit && isReadable(mbi.Protect) {
			for off := uintptr(0); off < size && len(hits) < max; off += chunk {
				n := size - off
				if n > chunk {
					n = chunk
				}
				mem, err := p.read(base+off, int(n))
				if err != nil || len(mem) < len(needle) {
					continue
				}
				start := 0
				for len(hits) < max {
					i := bytes.Index(mem[start:], needle)
					if i < 0 {
						break
					}
					pos := start + i
					start = pos + 1
					lo := pos - ctxBefore
					if lo < 0 {
						lo = 0
					}
					hi := pos + len(needle) + ctxAfter
					if hi > len(mem) {
						hi = len(mem)
					}
					ctx := make([]byte, hi-lo)
					copy(ctx, mem[lo:hi])
					hits = append(hits, SearchHit{Addr: base + off + uintptr(pos), Context: ctx})
				}
			}
		}
		next := base + size
		if next <= addr {
			break
		}
		addr = next
	}
	return hits, nil
}

// writeLayerPath rewrites the layer's std::string mesh path (the string object at layer+0x80 is
// {data ptr, size, capacity}) in place to the mesh file for `word`, so FH6 rebuilds the geometry live
// on the next repaint instead of showing the template's mesh until a save+reload. It never grows past
// the existing capacity and never touches the resource pointer at 0xA8 — the game re-derives that from
// the path itself, with correct ownership. Best-effort: on any problem the path is left as-is.
func (f *FH6) writeLayerPath(p *proc, layerPtr uintptr, word uint16) {
	path, ok := wordToMeshPath(word)
	if !ok {
		return // unknown word (e.g. a bank glyph not in the map) — falls back to reload
	}
	const dataOff, sizeOff, capOff = 0x80, 0x90, 0x98
	dataPtr, ok := p.readU64(layerPtr + dataOff)
	if !ok || !isUserPointer(dataPtr) {
		return
	}
	capacity, ok := p.readU64(layerPtr + capOff)
	if !ok || uintptr(len(path))+1 > capacity {
		return // would overflow the layer's own buffer — skip
	}
	if err := p.write(dataPtr, append([]byte(path), 0)); err != nil {
		return
	}
	var sz [8]byte
	binary.LittleEndian.PutUint64(sz[:], uint64(len(path)))
	_ = p.write(layerPtr+sizeOff, sz[:])
}

// WriteAt writes data at an arbitrary address in the live game — a WRITE, gated to the calibrated FH6
// process (never FH5, whose layout differs). Used only by controlled single-field diagnostics.
func (f *FH6) WriteAt(addr uintptr, data []byte) error {
	pid, name, err := findProcess(f.Profile.ProcessNames)
	if err != nil {
		return err
	}
	if !writeSafeProcess(name) {
		return fmt.Errorf("refusing to write into %q — FH6 only", name)
	}
	p, err := openProc(pid, true)
	if err != nil {
		return err
	}
	defer p.close()
	return p.write(addr, data)
}

// dumpGroups lists the live CLiveryGroup instances via RTTI (read-only, no preset count needed).
func (f *FH6) dumpGroups() ([]GroupInfo, error) {
	pid, name, err := findProcess(f.Profile.ProcessNames)
	if err != nil {
		return nil, err
	}
	f.logf("found %s (pid %d)", name, pid)
	p, err := openProc(pid, false) // read-only
	if err != nil {
		return nil, err
	}
	defer p.close()
	return listGroupsRTTI(p, f.Profile, func(s string) { f.logf("%s", s) })
}

// probe searches the live game's image/mapped memory for an ASCII needle (read-only diagnostic).
func (f *FH6) probe(needle, where string, max int) ([]ProbeHit, error) {
	pid, name, err := findProcess(f.Profile.ProcessNames)
	if err != nil {
		return nil, err
	}
	f.logf("found %s (pid %d)", name, pid)
	p, err := openProc(pid, false) // read-only
	if err != nil {
		return nil, err
	}
	defer p.close()
	return probeMemory(p, []byte(needle), []byte(where), max, func(s string) { f.logf("%s", s) }), nil
}

func decodeLayer(idx int, ptr uintptr, raw []byte, prof GameProfile) LayerInfo {
	li := LayerInfo{Index: idx, Ptr: ptr}
	li.Pos = [2]float32{f32at(raw, prof.PosOffset), f32at(raw, prof.PosOffset+4)}
	li.Scale = [2]float32{f32at(raw, prof.ScaleOffset), f32at(raw, prof.ScaleOffset+4)}
	li.Rot = f32at(raw, prof.RotationOffset)
	li.Skew = f32at(raw, prof.SkewOffset)
	copy(li.Color[:], raw[prof.ColorOffset:prof.ColorOffset+4])
	li.Mask = raw[prof.MaskOffset]
	li.Word = binary.LittleEndian.Uint16(raw[prof.ShapeIDOffset:])
	if len(raw) >= 0xB0 {
		li.Res = uintptr(binary.LittleEndian.Uint64(raw[0xA8:]))
	}
	return li
}

func f32at(b []byte, off int) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(b[off:]))
}

func countSupported(shapes []model.Shape) int {
	n := 0
	for _, s := range shapes {
		if _, ok := ShapeToLayer(s, NewCanvasMap(2, 2, 1, ScaleBase)); ok {
			n++
		}
	}
	return n
}
