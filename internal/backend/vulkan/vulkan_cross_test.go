//go:build vulkan && cuda

package vulkan

// Cross-GPU golden-diff: with the pure-Go CPU backend dropped (owner decision 2026-07-19),
// CUDA is the golden reference and Vulkan must match it — two independent implementations
// (nvcc kernels vs GLSL compute) agreeing is the correctness contract. Needs BOTH DLLs;
// run with -tags "cuda vulkan". On machines without CUDA the tests skip.

import (
	"math"
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/backend/cuda"
)

// TestGoldenDiffEvaluate asserts the Vulkan Evaluate matches the CUDA golden reference (scores +
// optimal colors) within float32 tolerance over a batch of random candidates, on both
// opaque and transparent (cutout) targets. This is the Phase 1 GO/NO-GO gate.
func TestGoldenDiffEvaluate(t *testing.T) {
	for _, transparent := range []bool{false, true} {
		rng := rand.New(rand.NewSource(42))
		w, h := 37, 29
		target, weight := makeTarget(rng, w, h, transparent)

		ref, refErr := cuda.New(target, weight, w, h, 8)
		if refErr != nil {
			t.Skipf("cross golden-diff needs the CUDA golden backend on this machine: %v", refErr)
		}
		defer ref.Close()
		gpu, err := New(target, weight, w, h, 8)
		if err != nil {
			t.Fatalf("vulkan.New: %v", err)
		}
		defer gpu.Close()

		canvas := make([]float32, w*h*4)
		for i := range canvas {
			canvas[i] = rng.Float32()
		}
		_ = ref.Reset(canvas)
		_ = gpu.Reset(canvas)

		cands := randCands(rng, w, h, 2000)
		rc, _ := ref.Evaluate(cands)
		gc, err := gpu.Evaluate(cands)
		if err != nil {
			t.Fatalf("vulkan Evaluate: %v", err)
		}

		var mismatches int
		for i := range cands {
			rRej := rc[i].Score == rejected
			gRej := gc[i].Score == rejected
			if rRej || gRej {
				if rRej != gRej {
					t.Errorf("[transp=%v] cand %d reject mismatch: cuda=%v vk=%v", transparent, i, rRej, gRej)
				}
				continue
			}
			if !closeRel(rc[i].Score, gc[i].Score, 2e-3, 1e-2) {
				if mismatches++; mismatches <= 10 {
					t.Errorf("[transp=%v] cand %d score: cuda=%.5f vk=%.5f", transparent, i, rc[i].Score, gc[i].Score)
				}
			}
			for _, cc := range [][2]float32{
				{rc[i].Color.R, gc[i].Color.R}, {rc[i].Color.G, gc[i].Color.G},
				{rc[i].Color.B, gc[i].Color.B}, {rc[i].Color.A, gc[i].Color.A},
			} {
				if math.Abs(float64(cc[0]-cc[1])) > 1e-3 {
					t.Errorf("[transp=%v] cand %d color: cuda=%.5f vk=%.5f", transparent, i, cc[0], cc[1])
					break
				}
			}
		}
	}
}

// TestGoldenDiffEvaluateAlphaGrid repeats the Evaluate golden-diff with the analytic-alpha grid
// installed on BOTH backends (fp_set_alpha_grid vs the CUDA golden): scores, colors AND the
// chosen alphas must agree, and the grid must actually engage (some alphas move).
func TestGoldenDiffEvaluateAlphaGrid(t *testing.T) {
	rng := rand.New(rand.NewSource(43))
	w, h := 37, 29
	target, weight := makeTarget(rng, w, h, false)

	ref, refErr := cuda.New(target, weight, w, h, 8)
	if refErr != nil {
		t.Skipf("cross golden-diff needs the CUDA golden backend on this machine: %v", refErr)
	}
	defer ref.Close()
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Fatalf("vulkan.New: %v", err)
	}
	defer gpu.Close()

	grid := []float32{0.3, 0.44, 0.58, 0.72, 0.86, 1.0}
	if err := gpu.SetAlphaGrid(grid); err != nil {
		t.Fatalf("SetAlphaGrid: %v (rebuild the DLL)", err)
	}
	if err := ref.SetAlphaGrid(grid); err != nil {
		t.Fatalf("cuda SetAlphaGrid: %v (rebuild fh6cuda.dll)", err)
	}
	defer func() { _ = ref.SetAlphaGrid(nil) }() // the CUDA grid is DLL-global state too
	defer func() { _ = gpu.SetAlphaGrid(nil) }() // never leak the grid into other tests (pooled state)

	canvas := make([]float32, w*h*4)
	for i := range canvas {
		canvas[i] = rng.Float32()
	}
	_ = ref.Reset(canvas)
	_ = gpu.Reset(canvas)

	cands := randCands(rng, w, h, 2000)
	rc, _ := ref.Evaluate(cands)
	gc, err := gpu.Evaluate(cands)
	if err != nil {
		t.Fatalf("vulkan Evaluate: %v", err)
	}

	var mismatches, moved int
	for i := range cands {
		rRej := rc[i].Score == rejected
		gRej := gc[i].Score == rejected
		if rRej || gRej {
			if rRej != gRej {
				t.Errorf("cand %d reject mismatch: cuda=%v vk=%v", i, rRej, gRej)
			}
			continue
		}
		if rc[i].Color.A != cands[i].Color.A {
			moved++
		}
		if !closeRel(rc[i].Score, gc[i].Score, 2e-3, 1e-2) {
			if mismatches++; mismatches <= 10 {
				t.Errorf("cand %d score: cuda=%.5f vk=%.5f", i, rc[i].Score, gc[i].Score)
			}
		}
		for _, cc := range [][2]float32{
			{rc[i].Color.R, gc[i].Color.R}, {rc[i].Color.G, gc[i].Color.G},
			{rc[i].Color.B, gc[i].Color.B}, {rc[i].Color.A, gc[i].Color.A},
		} {
			if math.Abs(float64(cc[0]-cc[1])) > 1e-3 {
				t.Errorf("cand %d color/alpha: cuda=%.5f vk=%.5f", i, cc[0], cc[1])
				break
			}
		}
	}
	if moved == 0 {
		t.Errorf("alpha grid never moved a candidate's alpha — grid not engaged")
	}
}

// TestGoldenDiffApply asserts the Vulkan Apply composites identically to the CUDA golden
// reference: apply a sequence of shapes on both, then compare the full canvas.
func TestGoldenDiffApply(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	w, h := 41, 33
	target, weight := makeTarget(rng, w, h, false)

	ref, refErr := cuda.New(target, weight, w, h, 8)
	if refErr != nil {
		t.Skipf("cross golden-diff needs the CUDA golden backend on this machine: %v", refErr)
	}
	defer ref.Close()
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Fatalf("vulkan.New: %v", err)
	}
	defer gpu.Close()

	canvas := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		canvas[i*4+3] = 1
	}
	_ = ref.Reset(canvas)
	_ = gpu.Reset(canvas)

	for _, c := range randCands(rng, w, h, 60) {
		_ = ref.Apply(c)
		_ = gpu.Apply(c)
	}

	rcanv := make([]float32, w*h*4)
	gcanv := make([]float32, w*h*4)
	_ = ref.ReadCanvas(rcanv)
	if err := gpu.ReadCanvas(gcanv); err != nil {
		t.Fatalf("vulkan ReadCanvas: %v", err)
	}
	var bad int
	for i := range rcanv {
		if math.Abs(float64(rcanv[i]-gcanv[i])) > 2e-3 {
			if bad++; bad <= 10 {
				t.Errorf("canvas[%d] (px %d ch %d): cuda=%.5f vk=%.5f", i, i/4, i%4, rcanv[i], gcanv[i])
			}
		}
	}
	if bad > 0 {
		t.Errorf("Apply mismatch: %d/%d channels diverged", bad, len(rcanv))
	}
}

// TestGoldenDiffErrorGrid asserts the on-device error grid matches the CUDA golden ErrorGrid (within
// a float-reduction tolerance) after compositing a few shapes onto a shared canvas.
func TestGoldenDiffErrorGrid(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	w, h, grid := 53, 47, 8
	target, weight := makeTarget(rng, w, h, false)
	ref, refErr := cuda.New(target, weight, w, h, grid)
	if refErr != nil {
		t.Skipf("cross golden-diff needs the CUDA golden backend on this machine: %v", refErr)
	}
	defer ref.Close()
	gpu, err := New(target, weight, w, h, grid)
	if err != nil {
		t.Fatalf("vulkan.New: %v", err)
	}
	defer gpu.Close()

	canvas := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		canvas[i*4+3] = 1
	}
	_ = ref.Reset(canvas)
	_ = gpu.Reset(canvas)
	for _, c := range randCands(rng, w, h, 40) {
		_ = ref.Apply(c)
		_ = gpu.Apply(c)
	}
	rg, _, _, _ := ref.ErrorGrid()
	gg, gw, gh, err := gpu.ErrorGrid()
	if err != nil {
		t.Fatalf("vulkan ErrorGrid: %v", err)
	}
	if gw != grid || gh != grid {
		t.Fatalf("grid dims %dx%d, want %dx%d", gw, gh, grid, grid)
	}
	for i := range rg {
		if !closeRel(rg[i], gg[i], 2e-3, 1e-3) {
			t.Errorf("grid[%d]: cuda=%.5f vk=%.5f", i, rg[i], gg[i])
		}
	}
}
