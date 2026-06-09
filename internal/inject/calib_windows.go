//go:build windows

package inject

import "fmt"

// calibWrite sets ONLY the transform + colour of existing template slots — it NEVER writes the
// shape-word (0x7A) or the geometry resource (0xA8). Each slot must already hold the expected
// gradient word (placed via the FH6 UI), so the game keeps its mesh and repaints the change LIVE
// (no save+reload). The WantWord check is also a safety gate: if the locator anchored on the wrong
// group (e.g. a stale reconstruction still resident), that slot's live word will not match and the
// write is refused before any memory is touched.
func (f *FH6) calibWrite(layers []CalibLayer) error {
	pid, name, err := findProcess(f.Profile.ProcessNames)
	if err != nil {
		return err
	}
	f.logf("found %s (pid %d)", name, pid)
	p, err := openProc(pid, true) // read-write
	if err != nil {
		return err
	}
	defer p.close()
	table, err := locateTable(p, f.Profile, f.Layers, func(s string) { f.logf("%s", s) })
	if err != nil {
		return err
	}
	prof := f.Profile
	for _, cl := range layers {
		if cl.Slot < 0 || cl.Slot >= f.Layers {
			return fmt.Errorf("calib slot %d out of range [0,%d)", cl.Slot, f.Layers)
		}
		ptr, ok := p.readU64(table + uintptr(cl.Slot)*8)
		if !ok || !isUserPointer(ptr) {
			return fmt.Errorf("calib slot %d: null/invalid layer pointer", cl.Slot)
		}
		if cl.WantWord != 0 {
			w, ok := p.readU16(ptr + uintptr(prof.ShapeIDOffset))
			if !ok {
				return fmt.Errorf("calib slot %d: cannot read shape word", cl.Slot)
			}
			if w != cl.WantWord {
				return fmt.Errorf("calib slot %d: live word 0x%04x != expected 0x%04x — refusing the write "+
					"(wrong group anchored, or the word moved; save+reopen the gradient group and retry)", cl.Slot, w, cl.WantWord)
			}
		}
		// transform + colour ONLY: pos (8B), scale (8B), rot (4B), colour (4B). Word & 0xA8 untouched.
		if err := p.write(ptr+uintptr(prof.PosOffset), f32f32(cl.Pos[0], cl.Pos[1])); err != nil {
			return fmt.Errorf("calib slot %d pos: %w", cl.Slot, err)
		}
		if err := p.write(ptr+uintptr(prof.ScaleOffset), f32f32(cl.Scale[0], cl.Scale[1])); err != nil {
			return fmt.Errorf("calib slot %d scale: %w", cl.Slot, err)
		}
		if err := p.write(ptr+uintptr(prof.RotationOffset), f32b(cl.Rot)); err != nil {
			return fmt.Errorf("calib slot %d rot: %w", cl.Slot, err)
		}
		if err := p.write(ptr+uintptr(prof.SkewOffset), f32b(cl.Skew)); err != nil {
			return fmt.Errorf("calib slot %d skew: %w", cl.Slot, err)
		}
		if err := p.write(ptr+uintptr(prof.ColorOffset), cl.Color[:]); err != nil {
			return fmt.Errorf("calib slot %d color: %w", cl.Slot, err)
		}
		// Optional shape-word write (re-derive test ONLY): FH6 re-derives the mesh from the word on
		// the NEXT save+reload, so the shape shows stale until then. The resource (0xA8) is still never
		// touched. Used to prove word-only gradient injection works from a non-gradient slot.
		wordNote := fmt.Sprintf("word 0x%04x kept", cl.WantWord)
		if cl.SetWord != 0 {
			if err := p.write(ptr+uintptr(prof.ShapeIDOffset), u16b(cl.SetWord)); err != nil {
				return fmt.Errorf("calib slot %d setword: %w", cl.Slot, err)
			}
			wordNote = fmt.Sprintf("word 0x%04x→0x%04x (re-derives on save+reload)", cl.WantWord, cl.SetWord)
		}
		f.logf("calib slot %d ← pos(%.0f,%.0f) scale(%.2f,%.2f) rot%.0f skew%.0f col(%d,%d,%d,%d) [%s]",
			cl.Slot, cl.Pos[0], cl.Pos[1], cl.Scale[0], cl.Scale[1], cl.Rot, cl.Skew,
			cl.Color[0], cl.Color[1], cl.Color[2], cl.Color[3], wordNote)
	}
	f.logf("calib wrote %d slots (transform+colour only; shape-word & resource left untouched)", len(layers))
	return nil
}
