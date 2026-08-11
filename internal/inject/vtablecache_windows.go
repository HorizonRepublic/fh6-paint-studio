//go:build windows

package inject

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// locationCache persists where the CLiveryGroup was last found for the running FH6 process so repeat
// locates (across separate fh6dbg invocations / studio runs) can skip the slow full-heap scan:
//
//   - Group  lets the next locate go DIRECTLY to the object (instant) if it is still a valid
//     CLiveryGroup at the same address — the common case in a tight inject/calibrate loop.
//   - Vtable lets it re-find the instance by a targeted vtable scan if the group moved (reopen).
//
// It is only a HINT: every path re-validates (scoreTable + validateCoverage) before any write, and it
// is keyed by pid + module base so a game restart (new pid / new ASLR base) misses cleanly and the
// count scan re-learns it.
type locationCache struct {
	PID        uint32 `json:"pid"`
	ModuleBase uint64 `json:"module_base"`
	Vtable     uint64 `json:"vtable"`
	VtableRVA  uint64 `json:"vtable_rva"` // vtable − module_base: stable across restarts (same game build)
	Group      uint64 `json:"group"`
	Count      int    `json:"count"`
}

// locationCachePath keeps the hint in the app's own directory, never %TEMP%.
//
// What lands in this file is another process's pid, its module base and two live object addresses.
// Dropping that into the most-watched directory on the system, moments after opening a handle to a
// running game, is the exact sequence a sandbox scores as staging — and it is a pure speed hint that
// every path re-validates before use, so it has no business being anywhere so conspicuous.
func locationCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, "FH6PaintStudio")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return filepath.Join(dir, "vtable-cache.json")
}

// loadCache returns the cached locator hint for the running FH6 process, in two tiers:
//
//   - haveGroup: the SAME live process (pid + module base match) — the heap group address is still
//     valid, so the caller can go straight to the object (instant, no scan).
//   - haveVtable: a DIFFERENT session (game restarted → new pid / ASLR base) — the heap group is
//     gone, but the vtable lives in the module at a fixed RVA, so it is reconstructed as base+RVA and
//     the caller re-finds the group with a fast targeted vtable scan instead of the slow full-heap
//     count scan. This is what makes the first inject after a game restart quick.
//
// It is only a HINT: every locate path re-validates (scoreTable + validateCoverage) before any write,
// so a stale RVA (e.g. after a game update that moved the vtable) just wastes one scan and falls back.
func loadCache(p *proc) (vtable, group uintptr, haveGroup, haveVtable bool) {
	data, err := os.ReadFile(locationCachePath())
	if err != nil {
		return 0, 0, false, false
	}
	var c locationCache
	if json.Unmarshal(data, &c) != nil {
		return 0, 0, false, false
	}
	base, ok := p.moduleBase()
	if !ok || base == 0 {
		return 0, 0, false, false
	}
	if c.Vtable != 0 && c.PID == p.pid && c.ModuleBase == uint64(base) {
		return uintptr(c.Vtable), uintptr(c.Group), true, true
	}
	if c.VtableRVA != 0 {
		return base + uintptr(c.VtableRVA), 0, false, true // vtable-only: reconstruct from the stable RVA
	}
	return 0, 0, false, false
}

// cacheLocation best-effort persists where the group was found for this process (errors are
// non-fatal — the cache is only an optimization, never a correctness dependency). The vtable is also
// stored as an RVA so the next session (new pid/base) can still skip the count scan.
func cacheLocation(p *proc, vtable, group uintptr, count int) {
	base, _ := p.moduleBase()
	var rva uint64
	if base != 0 && uint64(vtable) > uint64(base) {
		rva = uint64(vtable) - uint64(base)
	}
	c := locationCache{PID: p.pid, ModuleBase: uint64(base), Vtable: uint64(vtable), VtableRVA: rva, Group: uint64(group), Count: count}
	if data, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(locationCachePath(), data, 0o644)
	}
}
