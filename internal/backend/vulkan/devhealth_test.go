package vulkan

import (
	"math/rand"
	"testing"
)

// The device-health surface backs the engine's honest failure paths: MemInfo feeds the polish
// VRAM ladder, PolishMemNeed is the estimate it compares, DeviceLost is the abort signal, and
// PolishSetup's error is the "setup silently died" fix. A healthy device must report sane
// numbers and no faults — the failure sides are exercised by the engine's ladder logic.
func TestDeviceHealthSurface(t *testing.T) {
	const w, h = 64, 48
	rng := rand.New(rand.NewSource(3))
	target, weight := smoothTarget(rng, w, h)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Skipf("vulkan unavailable: %v", err)
	}
	defer gpu.Close()

	if gpu.DeviceLost() {
		t.Fatal("fresh device reports lost")
	}
	budget, usage, heap, ok := gpu.MemInfo()
	if !ok {
		t.Fatal("fp_mem_info missing from a freshly built DLL")
	}
	if heap <= 0 {
		t.Fatalf("device-local heap size %d", heap)
	}
	// budget/usage are 0 without VK_EXT_memory_budget; with it they must be sane.
	if budget != 0 && (budget > heap*2 || usage < 0 || usage > heap*2) {
		t.Fatalf("implausible budget/usage: budget=%d usage=%d heap=%d", budget, usage, heap)
	}

	needBare := gpu.PolishMemNeed(100, 50_000, 0)
	needTerms := gpu.PolishMemNeed(100, 50_000, 7)
	if needBare <= 0 || needTerms <= needBare {
		t.Fatalf("estimate not monotone in terms: bare=%d terms=%d", needBare, needTerms)
	}
	if grown := gpu.PolishMemNeed(100, 5_000_000, 0); grown <= needBare {
		t.Fatalf("estimate not monotone in belowTotal: %d vs %d", grown, needBare)
	}

	base := make([]float32, w*h*4)
	if err := gpu.PolishSetup(base, 3); err != nil {
		t.Fatalf("PolishSetup on a healthy device: %v", err)
	}
	gpu.PolishFree()
	if gpu.DeviceLost() {
		t.Fatal("device reports lost after a normal setup/free cycle")
	}
}
