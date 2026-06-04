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
	Group      uint64 `json:"group"`
	Count      int    `json:"count"`
}

func locationCachePath() string {
	return filepath.Join(os.TempDir(), "fh6-paint-studio-vtable.json")
}

// loadCache returns the cached (vtable, group) iff it was saved for this exact running process (same
// pid AND same module base). Any mismatch / missing file -> no hint.
func loadCache(p *proc) (vtable, group uintptr, ok bool) {
	data, err := os.ReadFile(locationCachePath())
	if err != nil {
		return 0, 0, false
	}
	var c locationCache
	if json.Unmarshal(data, &c) != nil || c.Vtable == 0 || c.PID != p.pid {
		return 0, 0, false
	}
	base, ok := p.moduleBase()
	if !ok || uint64(base) != c.ModuleBase {
		return 0, 0, false
	}
	return uintptr(c.Vtable), uintptr(c.Group), true
}

// cacheLocation best-effort persists where the group was found for this process (errors are
// non-fatal — the cache is only an optimization, never a correctness dependency).
func cacheLocation(p *proc, vtable, group uintptr, count int) {
	base, _ := p.moduleBase()
	c := locationCache{PID: p.pid, ModuleBase: uint64(base), Vtable: uint64(vtable), Group: uint64(group), Count: count}
	if data, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(locationCachePath(), data, 0o644)
	}
}
