//go:build windows

package inject

import (
	"encoding/binary"
	"fmt"
)

// locateTableByCount finds the live FH6 layer pointer-table for a group whose layer count == count.
// It scans private writable memory for the uint16 count, derives the group (count - countOffset),
// reads the table pointer (group + tableOffset), and validates that the table's entries look like
// real layers. This is the fallback used when neither RTTI nor a cached vtable can anchor on
// CLiveryGroup. It
// also returns the group object's address (countAddr-countOffset) so the caller can learn the
// CLiveryGroup vtable from it (object offset 0) and cache it for faster subsequent locates.
func locateTableByCount(p *proc, prof GameProfile, count int, log func(string)) (table, group uintptr, err error) {
	if log == nil {
		log = func(string) {}
	}
	if count <= 0 || count > 0xFFFF {
		return 0, 0, fmt.Errorf("invalid layer count %d (expected the exact FH6 template count, 1..65535)", count)
	}
	needle := make([]byte, 2)
	binary.LittleEndian.PutUint16(needle, uint16(count))
	countOff := uintptr(prof.LiveryCountOffset)
	tableOff := uintptr(prof.LayerTableOffset)

	var resultTable, resultGroup uintptr
	p.iterPrivateWritable(func(r region) bool {
		// Scan the region in chunks so regions LARGER than scanChunk are NOT skipped — after a long
		// editing session the live editor heap can grow past the old 256 MB whole-region cap, which
		// would hide the layer table from the scan. Chunking keeps the per-read allocation bounded
		// while still covering every byte (a 1-byte overlap catches a count straddling a boundary).
		for base := r.base; base < r.base+r.size; {
			n := r.size - (base - r.base)
			if n > scanChunk {
				n = scanChunk
			}
			readLen := int(n)
			if base+n < r.base+r.size {
				readLen++ // overlap so a uint16 on the chunk boundary is still seen
			}
			mem, err := p.read(base, readLen)
			if err != nil || len(mem) < 2 {
				base += n
				continue
			}
			for off := 0; off+2 <= len(mem); off++ {
				if mem[off] != needle[0] || mem[off+1] != needle[1] {
					continue
				}
				countAddr := base + uintptr(off)
				if countAddr < countOff {
					continue
				}
				groupAddr := countAddr - countOff
				tbl, ok := p.readU64(groupAddr + tableOff)
				if !ok || !isUserPointer(tbl) || !p.isPrivateWritable(tbl) {
					continue
				}
				if scoreTable(p, prof, tbl, min(count, 64)) <= 0 {
					continue
				}
				if !validateCoverage(p, prof, tbl, count) {
					continue
				}
				resultTable, resultGroup = tbl, groupAddr
				return false // first validated table wins
			}
			base += n
		}
		return true
	})

	if resultTable == 0 {
		return 0, 0, fmt.Errorf("no valid FH6 layer table found for %d layers — confirm FH6 is in the Vinyl Group Editor, the template is ungrouped, and the layer count is exact", count)
	}
	log(fmt.Sprintf("located layer table via layout-count scan @ 0x%x", resultTable))
	return resultTable, resultGroup, nil
}

// scoreLayerPointer scores how layer-like a pointer's target is (0..5).
func scoreLayerPointer(p *proc, prof GameProfile, ptr uintptr) int {
	if !isUserPointer(ptr) {
		return 0
	}
	score := 0
	if pos, ok := p.readFloatPair(ptr + uintptr(prof.PosOffset)); ok && pairInRange(pos, -10000, 10000) {
		score++
	}
	if sc, ok := p.readFloatPair(ptr + uintptr(prof.ScaleOffset)); ok && pairAbsInRange(sc, 0, 10000) {
		score++
	}
	if col, err := p.read(ptr+uintptr(prof.ColorOffset), 4); err == nil && len(col) == 4 {
		score++
	}
	if plausibleShapeWord(p, prof, ptr) {
		score++
	}
	if mk, err := p.read(ptr+uintptr(prof.MaskOffset), 1); err == nil && len(mk) == 1 && (mk[0] == 0 || mk[0] == 1) {
		score++
	}
	return score
}

// strictLayerPointer is the conservative layer check used for safety validation.
func strictLayerPointer(p *proc, prof GameProfile, ptr uintptr) bool {
	if !p.isPrivateWritable(ptr) {
		return false
	}
	pos, ok1 := p.readFloatPair(ptr + uintptr(prof.PosOffset))
	sc, ok2 := p.readFloatPair(ptr + uintptr(prof.ScaleOffset))
	if !ok1 || !pairInRange(pos, -10000, 10000) {
		return false
	}
	if !ok2 || !pairAbsInRange(sc, 0, 10000) {
		return false
	}
	if abs32(sc[0]) < 0.0001 && abs32(sc[1]) < 0.0001 {
		return false
	}
	// Colour must be readable, but alpha may be ANYTHING: reconstructions are mostly semi-transparent,
	// so an `alpha ∈ {0,255}` gate would silently reject the real table for a transparent vinyl —
	// strict-coverage would collapse below threshold and the locator would report "no valid table"
	// even with the art live in the editor.
	if col, err := p.read(ptr+uintptr(prof.ColorOffset), 4); err != nil || len(col) != 4 {
		return false
	}
	if !plausibleShapeWord(p, prof, ptr) {
		return false
	}
	mk, err := p.read(ptr+uintptr(prof.MaskOffset), 1)
	if err != nil || len(mk) != 1 || (mk[0] != 0 && mk[0] != 1) {
		return false
	}
	return true
}

// scoreTable scores a candidate table by sampling its first entries.
func scoreTable(p *proc, prof GameProfile, table uintptr, sampleCount int) int {
	if !p.isPrivateWritable(table) {
		return 0
	}
	if sampleCount > 64 {
		sampleCount = 64
	}
	total, layerLike, distinct := 0, 0, map[uintptr]bool{}
	for i := 0; i < sampleCount; i++ {
		ptr, ok := p.readU64(table + uintptr(i)*8)
		if !ok || !p.isPrivateWritable(ptr) {
			return 0
		}
		distinct[ptr] = true
		s := scoreLayerPointer(p, prof, ptr)
		total += s
		if s >= 3 {
			layerLike++
		}
	}
	if sampleCount >= 16 {
		if len(distinct) < maxInt(8, sampleCount*3/4) {
			return 0
		}
		if layerLike < maxInt(8, sampleCount/2) {
			return 0
		}
	}
	return total + layerLike
}

// validateCoverage confirms the table covers `count` layer-like entries before any write.
func validateCoverage(p *proc, prof GameProfile, table uintptr, count int) bool {
	if !p.isPrivateWritable(table) {
		return false
	}
	required := count
	if required > 3000 {
		required = 3000
	}
	scanLimit := required * 2
	if scanLimit < required+512 {
		scanLimit = required + 512
	}
	if scanLimit > 3000 {
		scanLimit = 3000
	}
	valid, strict := 0, 0
	seen := map[uintptr]bool{}
	for i := 0; i < scanLimit; i++ {
		ptr, ok := p.readU64(table + uintptr(i)*8)
		if !ok || seen[ptr] {
			continue
		}
		if p.isPrivateWritable(ptr) && scoreLayerPointer(p, prof, ptr) >= 3 {
			seen[ptr] = true
			valid++
			if strictLayerPointer(p, prof, ptr) {
				strict++
			}
			if valid >= required {
				strictRequired := required / 4
				if strictRequired < 32 {
					strictRequired = 32
				}
				if strictRequired > required {
					strictRequired = required
				}
				return strict >= strictRequired
			}
		}
	}
	return false
}

// --- small numeric helpers ---

func pairInRange(v [2]float32, lo, hi float32) bool {
	return v[0] > lo && v[0] < hi && v[1] > lo && v[1] < hi
}

func pairAbsInRange(v [2]float32, lo, hi float32) bool {
	return abs32(v[0]) >= lo && abs32(v[0]) < hi && abs32(v[1]) >= lo && abs32(v[1]) < hi
}

func abs32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

// plausibleShapeWord reports whether the layer at ptr carries a sensible 16-bit shape word at
// ShapeIDOffset. Rather than enumerate specific primitives (which rejects valid templates built from
// arrows, stars, fonts, etc.), it just requires the word to be nonzero and small: page-1 primitives
// are 0x0065..0x0088 and font glyphs/decorative shapes stay well under 0x2000, while zeroed or
// garbage memory reads as 0x0000 or large values. Combined with the pos/scale/color/mask checks this
// keeps the locator safe while tolerating any real template content.
func plausibleShapeWord(p *proc, prof GameProfile, ptr uintptr) bool {
	w, ok := p.readU16(ptr + uintptr(prof.ShapeIDOffset))
	return ok && w != 0 && w < 0x2000
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
