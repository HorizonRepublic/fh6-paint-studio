//go:build windows

package inject

import (
	"encoding/binary"
	"fmt"
	"math"

	"fh6-paint-studio/internal/model"
)

// run performs the live injection: find the process, locate the layer table, write each
// supported shape into a consecutive template slot (compacting over skipped lines), then blank
// the leftover template layers.
func (f *FH6) run(shapes []model.Shape, cm CanvasMap) error {
	pid, name, err := findProcess(f.Profile.ProcessNames)
	if err != nil {
		return err
	}
	f.logf("found %s (pid %d)", name, pid)

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
		if !ok || !isUserPointer(ptr) {
			return fmt.Errorf("layer slot %d resolved to a null/invalid pointer (stale table — re-open the group and auto-locate)", written+1)
		}
		for _, fw := range lw.Writes(f.Profile) {
			if err := p.write(ptr+uintptr(fw.Offset), fw.Data); err != nil {
				return fmt.Errorf("write layer %d field +0x%x: %w", written+1, fw.Offset, err)
			}
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
			if !ok || !isUserPointer(ptr) {
				continue
			}
			for _, fw := range ClearWrites(f.Profile) {
				if err := p.write(ptr+uintptr(fw.Offset), fw.Data); err != nil {
					break // leave partially-cleared leftover; not fatal
				}
			}
			cleared++
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
