//go:build windows

package inject

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// memImage is MEM_IMAGE — committed memory backed by a mapped module (the .exe/.dll sections,
// where MSVC keeps RTTI type descriptors, complete-object-locators and vtables).
const memImage = 0x1000000

// memMapped is MEM_MAPPED — committed memory backed by a section/mapped file (some loaders map the
// executable here instead of MEM_IMAGE).
const memMapped = 0x40000

// probeMemory is a read-only diagnostic: it reports committed-region stats per memory type and
// returns up to maxHits matches of needle within image/mapped regions (where RTTI names live),
// each with surrounding printable context. If filter is non-empty, only hits whose printable
// context contains it are kept (e.g. needle ".?AV", filter "Livery" -> Livery RTTI descriptors).
// Reads in chunks so regions larger than maxRegionRead are not skipped.
func probeMemory(p *proc, needle, filter []byte, maxHits int, log func(string)) []ProbeHit {
	const chunk = 64 * 1024 * 1024
	var hits []ProbeHit
	type stat struct {
		count, oversized int
		total, largest   uint64
	}
	stats := map[uint32]*stat{}
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
		if mbi.State == memCommit && isReadable(mbi.Protect) {
			s := stats[mbi.Type]
			if s == nil {
				s = &stat{}
				stats[mbi.Type] = s
			}
			s.count++
			s.total += uint64(size)
			if uint64(size) > s.largest {
				s.largest = uint64(size)
			}
			if size > maxRegionRead {
				s.oversized++
			}
			// Search image/mapped only (RTTI strings live there; skips the huge private heap).
			if (mbi.Type == memImage || mbi.Type == memMapped) && len(hits) < maxHits {
				for off := uintptr(0); off < size && len(hits) < maxHits; off += chunk {
					n := size - off
					if n > chunk {
						n = chunk
					}
					mem, err := p.read(base+off, int(n))
					if err != nil || len(mem) < len(needle) {
						continue
					}
					for start := 0; len(hits) < maxHits; {
						i := bytes.Index(mem[start:], needle)
						if i < 0 {
							break
						}
						pos := start + i
						ctx := printableContext(mem, pos, len(needle))
						if len(filter) == 0 || bytes.Contains([]byte(ctx), filter) {
							hits = append(hits, ProbeHit{
								Addr:    base + off + uintptr(pos),
								Type:    mbi.Type,
								Context: ctx,
							})
						}
						start = pos + 1
					}
				}
			}
		}
		next := base + size
		if next <= addr {
			break
		}
		addr = next
	}
	for t, s := range stats {
		log(fmt.Sprintf("region type 0x%08x: %d regions, %d MB total, largest %d MB, >256MB(skipped by scan): %d",
			t, s.count, s.total/(1024*1024), s.largest/(1024*1024), s.oversized))
	}
	return hits
}

// printableContext renders bytes around a hit as ASCII (non-printable -> '.').
func printableContext(mem []byte, pos, n int) string {
	lo := pos - 8
	if lo < 0 {
		lo = 0
	}
	hi := pos + n + 48
	if hi > len(mem) {
		hi = len(mem)
	}
	out := make([]byte, 0, hi-lo)
	for _, c := range mem[lo:hi] {
		if c >= 0x20 && c < 0x7f {
			out = append(out, c)
		} else {
			out = append(out, '.')
		}
	}
	return string(out)
}

// --- Build-specific locator "codes" (update here if a future FH6 build moves things) ------------
//
// The live layer table is found by THREE locators, tried in order and logged so you can see which
// one won:
//
//  1. RTTI  — find the CLiveryGroup C++ object by its MSVC type-descriptor name, then its vtable.
//  2. vtable — a CLiveryGroup vtable cached from an earlier count scan this session (count-free, fast).
//  3. count  — scan the heap for the exact layer count (always-works fallback; re-learns the vtable).
//
// Every locator validates the table (scoreTable + validateCoverage) before returning, so a write
// target is never trusted blindly. If a new build RENAMES the class, add the new mangled descriptor
// name below — the count scan keeps working until then, and once it succeeds it caches the new
// vtable so locating stays fast. The other build-specific values (field offsets + shape words) live
// in profile.go (GameProfile / Word* consts).
var cliveryGroupRTTINames = [][]byte{
	[]byte(".?AVCLiveryGroup@@"), // FH5/FH6 (note: stripped in current FH6 shipping builds -> count-scan path)
}

// locateTable finds the live FH6 layer pointer-table for a group whose layer count == count, trying
// (and logging) four locators in order, fastest first:
//
//  1. cached group  — go straight to the previously located object (instant) if it is still a valid
//     CLiveryGroup at the same address; the common case in a tight inject/calibrate loop.
//  2. cached vtable  — the group moved (reopen) but its class vtable is stable: targeted vtable scan.
//  3. RTTI          — derive the vtable fresh from the type descriptor (builds that keep RTTI).
//  4. count scan    — exact layer-count heap scan (always works); learns the group+vtable for 1/2.
//
// Every path re-validates the table (scoreTable + validateCoverage) before returning, so a write
// target is never trusted blindly.
func locateTable(p *proc, prof GameProfile, count int, log func(string)) (uintptr, error) {
	if log == nil {
		log = func(string) {}
	}
	if count <= 0 || count > 0xFFFF {
		return 0, fmt.Errorf("invalid layer count %d (expected the exact FH6 template count, 1..65535)", count)
	}

	cachedVt, cachedGroup, haveCache := loadCache(p)

	// 1. Cached exact group — instant direct hit (no scan), re-validated.
	if haveCache {
		if table, ok := tableFromGroup(p, prof, cachedGroup, cachedVt, count); ok {
			log(fmt.Sprintf("locator: ✓ via cached group 0x%x (vtable 0x%x) → table 0x%x", cachedGroup, cachedVt, table))
			return table, nil
		}
	}

	// 2. Cached vtable — group moved but its class vtable is stable: targeted scan.
	if haveCache {
		if table, vt, group, ok := tableFromVtables(p, prof, count, []uintptr{cachedVt}); ok {
			cacheLocation(p, vt, group, count)
			log(fmt.Sprintf("locator: ✓ via cached vtable 0x%x → table 0x%x", vt, table))
			return table, nil
		}
		log(fmt.Sprintf("locator: cached vtable 0x%x no longer matches — trying RTTI / count scan", cachedVt))
	}

	// 3. RTTI: derive the CLiveryGroup vtable(s) fresh from the type descriptor.
	if vtables, err := cliveryGroupVtables(p, log); err != nil {
		log(fmt.Sprintf("locator: RTTI unavailable (%v)", err))
	} else if table, vt, group, ok := tableFromVtables(p, prof, count, vtables); ok {
		cacheLocation(p, vt, group, count)
		log(fmt.Sprintf("locator: ✓ via RTTI (vtable 0x%x) → table 0x%x", vt, table))
		return table, nil
	} else {
		log("locator: RTTI vtable(s) found but no matching CLiveryGroup instance — trying count scan")
	}

	// 4. Count scan (always works). Learn the group + vtable so paths 1/2 work next time.
	table, group, err := locateTableByCount(p, prof, count, log)
	if err != nil {
		return 0, err
	}
	if vt, ok := p.readU64(group); ok && isUserPointer(vt) {
		cacheLocation(p, vt, group, count)
		log(fmt.Sprintf("locator: ✓ via count scan → table 0x%x (learned group 0x%x / vtable 0x%x, cached)", table, group, vt))
	} else {
		log(fmt.Sprintf("locator: ✓ via count scan → table 0x%x (vtable unreadable, not cached)", table))
	}
	return table, nil
}

// tableFromGroup validates that the object at group is still a CLiveryGroup with the expected vtable
// and layer count, and returns its validated layer table — the instant cache direct-hit (no scan).
func tableFromGroup(p *proc, prof GameProfile, group, wantVtable uintptr, count int) (uintptr, bool) {
	if vt, ok := p.readU64(group); !ok || vt != wantVtable {
		return 0, false
	}
	cnt, ok := p.readU16(group + uintptr(prof.LiveryCountOffset))
	if !ok || int(cnt) != count {
		return 0, false
	}
	table, ok := p.readU64(group + uintptr(prof.LayerTableOffset))
	if !ok || !isUserPointer(table) || !p.isPrivateWritable(table) {
		return 0, false
	}
	if scoreTable(p, prof, table, min(count, 64)) <= 0 || !validateCoverage(p, prof, table, count) {
		return 0, false
	}
	return table, true
}

// cliveryGroupVtables walks the standard MSVC x64 RTTI chain to recover the vtable address(es) of
// class CLiveryGroup, for each configured type-descriptor name:
//
//	type name ".?AVCLiveryGroup@@"  -> TypeDescriptor (name lives at +0x10 in the descriptor)
//	descriptor's image-relative off -> CompleteObjectLocator (pTypeDescriptor field at +0xC, sig==1)
//	pointer to that locator         -> vtable (the locator pointer sits one slot before the vtable)
//
// A live object whose first qword equals one of these vtables is a CLiveryGroup instance.
func cliveryGroupVtables(p *proc, log func(string)) ([]uintptr, error) {
	base, ok := p.moduleBase()
	if !ok || base == 0 {
		return nil, fmt.Errorf("main module base not found")
	}
	var vtables []uintptr
	seenV := map[uintptr]bool{}
	for _, name := range cliveryGroupRTTINames {
		nameAddr, ok := findFirstPattern(p, name, memImage, false)
		if !ok {
			continue
		}
		descAddr := nameAddr - 0x10 // TypeDescriptor: vftable(+0) spare(+8) name(+0x10)
		if descAddr < base {
			continue
		}
		descOff := descAddr - base
		if descOff > 0xFFFFFFFF {
			continue
		}
		// CompleteObjectLocator: signature(+0)==1 for x64, pTypeDescriptor(+0xC) == descOff.
		var cols []uintptr
		for _, a := range scanPattern(p, u32b(uint32(descOff)), memImage, false, 4) {
			col := a - 0xC
			if sig, err := p.read(col, 1); err == nil && len(sig) == 1 && sig[0] == 1 {
				cols = append(cols, col)
			}
		}
		// vtable: the pointer to the locator is stored at vtable-8, so vtable = match + 8.
		found := 0
		for _, col := range cols {
			for _, a := range scanPattern(p, u64b(uint64(col)), memImage, false, 8) {
				if v := a + 8; !seenV[v] {
					seenV[v] = true
					vtables = append(vtables, v)
					found++
				}
			}
		}
		log(fmt.Sprintf("RTTI: %q → descriptor off 0x%x, %d locator(s), %d vtable(s)", name, descOff, len(cols), found))
	}
	if len(vtables) == 0 {
		return nil, fmt.Errorf("no CLiveryGroup vtable found (type descriptor absent/stripped in this build)")
	}
	return vtables, nil
}

// tableFromVtables scans the private heap for CLiveryGroup instances carrying one of vtables and
// returns the validated layer table (plus the matching vtable and group address) of the instance
// whose layer count equals count. Used by both the RTTI path and the cached-vtable path.
func tableFromVtables(p *proc, prof GameProfile, count int, vtables []uintptr) (table, vtable, group uintptr, ok bool) {
	if len(vtables) == 0 {
		return 0, 0, 0, false
	}
	countOff := uintptr(prof.LiveryCountOffset)
	tableOff := uintptr(prof.LayerTableOffset)
	p.iterPrivateWritable(func(r region) bool {
		if r.size > maxRegionRead {
			return true
		}
		mem, err := p.read(r.base, int(r.size))
		if err != nil || len(mem) < 8 {
			return true
		}
		for _, v := range vtables {
			needle := u64b(uint64(v))
			start := 0
			for {
				i := bytes.Index(mem[start:], needle)
				if i < 0 {
					break
				}
				pos := start + i
				start = pos + 8
				grp := r.base + uintptr(pos)
				cnt, okc := p.readU16(grp + countOff)
				if !okc || int(cnt) != count {
					continue
				}
				tbl, okt := p.readU64(grp + tableOff)
				if !okt || !isUserPointer(tbl) || !p.isPrivateWritable(tbl) {
					continue
				}
				if scoreTable(p, prof, tbl, min(count, 64)) <= 0 {
					continue
				}
				if !validateCoverage(p, prof, tbl, count) {
					continue
				}
				table, vtable, group, ok = tbl, v, grp, true
				return false
			}
		}
		return true
	})
	return table, vtable, group, ok
}

// listGroupsRTTI returns every CLiveryGroup instance found via RTTI with its layer count and
// whether its layer table validates. Read-only and needs no preset count — it answers "what
// groups are live right now and how many layers each has".
func listGroupsRTTI(p *proc, prof GameProfile, log func(string)) ([]GroupInfo, error) {
	vtables, err := cliveryGroupVtables(p, log)
	if err != nil {
		return nil, err
	}
	countOff := uintptr(prof.LiveryCountOffset)
	tableOff := uintptr(prof.LayerTableOffset)
	var groups []GroupInfo
	seen := map[uintptr]bool{}
	p.iterPrivateWritable(func(r region) bool {
		if r.size > maxRegionRead {
			return true
		}
		mem, err := p.read(r.base, int(r.size))
		if err != nil || len(mem) < 8 {
			return true
		}
		for _, v := range vtables {
			needle := u64b(uint64(v))
			start := 0
			for {
				i := bytes.Index(mem[start:], needle)
				if i < 0 {
					break
				}
				pos := start + i
				start = pos + 8
				group := r.base + uintptr(pos)
				if seen[group] {
					continue
				}
				seen[group] = true
				cnt, ok := p.readU16(group + countOff)
				if !ok {
					continue
				}
				gi := GroupInfo{Addr: group, Count: int(cnt)}
				if table, ok := p.readU64(group + tableOff); ok && isUserPointer(table) && p.isPrivateWritable(table) {
					gi.Table = table
					if cnt > 0 &&
						scoreTable(p, prof, table, min(int(cnt), 64)) > 0 &&
						validateCoverage(p, prof, table, int(cnt)) {
						gi.Valid = true
					}
				}
				groups = append(groups, gi)
			}
		}
		return true
	})
	return groups, nil
}

// findFirstPattern returns the address of the first occurrence of pattern across regions matching
// typeFilter / writableOnly, or ok=false.
func findFirstPattern(p *proc, pattern []byte, typeFilter uint32, writableOnly bool) (uintptr, bool) {
	var addr uintptr
	var found bool
	p.iterRegions(typeFilter, writableOnly, func(r region) bool {
		if r.size > maxRegionRead {
			return true
		}
		mem, err := p.read(r.base, int(r.size))
		if err != nil || len(mem) < len(pattern) {
			return true
		}
		if i := bytes.Index(mem, pattern); i >= 0 {
			addr = r.base + uintptr(i)
			found = true
			return false
		}
		return true
	})
	return addr, found
}

// scanPattern returns every address of pattern across regions matching typeFilter / writableOnly,
// keeping only matches whose address satisfies the given byte alignment (alignment<=1 = any).
func scanPattern(p *proc, pattern []byte, typeFilter uint32, writableOnly bool, alignment int) []uintptr {
	var out []uintptr
	step := alignment
	if step < 1 {
		step = 1
	}
	p.iterRegions(typeFilter, writableOnly, func(r region) bool {
		if r.size > maxRegionRead {
			return true
		}
		mem, err := p.read(r.base, int(r.size))
		if err != nil || len(mem) < len(pattern) {
			return true
		}
		start := 0
		for {
			i := bytes.Index(mem[start:], pattern)
			if i < 0 {
				break
			}
			pos := start + i
			addr := r.base + uintptr(pos)
			if alignment <= 1 || addr%uintptr(alignment) == 0 {
				out = append(out, addr)
			}
			start = pos + step
		}
		return true
	})
	return out
}

func u32b(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}
