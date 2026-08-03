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
	"fh6-paint-studio/internal/model"
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

// randGradCands generates the NATIVE FH6 gradient primitives — a glow and a disk have a per-pixel
// alpha (a radial falloff), not the binary coverage the hard kinds have.
func randGradCands(rng *rand.Rand, w, h, n int) []model.Candidate {
	kinds := []model.ShapeKind{model.KindGlow, model.KindDisk}
	out := make([]model.Candidate, n)
	for i := range out {
		out[i] = model.Candidate{
			Kind: kinds[rng.Intn(len(kinds))],
			P: [6]float32{rng.Float32() * float32(w), rng.Float32() * float32(h),
				2 + rng.Float32()*float32(w)/3, 2 + rng.Float32()*float32(h)/3, rng.Float32() * 180, 0},
			Color: model.RGBA{R: rng.Float32(), G: rng.Float32(), B: rng.Float32(), A: 0.2 + rng.Float32()*0.8},
		}
	}
	return out
}

// TestGoldenDiffEvaluateGradientKinds is the gate the earlier cross-diff was missing: it only ever
// fed ellipse/rect/triangle/line, so a backend that cannot score a glow or a disk passed it while
// silently dropping the primitives several shipped presets depend on.
func TestGoldenDiffEvaluateGradientKinds(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
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

	canvas := make([]float32, w*h*4)
	for i := range canvas {
		canvas[i] = rng.Float32()
	}
	_ = ref.Reset(canvas)
	_ = gpu.Reset(canvas)

	// Both backends must be on their gradient-aware eval path: CUDA's fast warp kernel has no
	// per-pixel-alpha branch and would score a glow as a flat ellipse (the engine flips this on
	// for its gradient evals the same way).
	if !ref.SetGradients(true) {
		t.Skip("golden backend has no gradient eval path")
	}
	defer ref.SetGradients(false)
	gpu.SetGradients(true)

	cands := randGradCands(rng, w, h, 400)
	rc, _ := ref.Evaluate(cands)
	gc, err := gpu.Evaluate(cands)
	if err != nil {
		t.Fatalf("vulkan Evaluate: %v", err)
	}
	var mismatches int
	for i := range cands {
		if rc[i].Score == rejected {
			continue // the reference itself declined this placement
		}
		if gc[i].Score == rejected {
			t.Fatalf("cand %d (kind %v): vulkan rejects a gradient primitive the golden backend scores (%.5f)",
				i, cands[i].Kind, rc[i].Score)
		}
		if !closeRel(rc[i].Score, gc[i].Score, 2e-3, 1e-2) {
			if mismatches++; mismatches <= 10 {
				t.Errorf("cand %d (kind %v) score: cuda=%.5f vk=%.5f", i, cands[i].Kind, rc[i].Score, gc[i].Score)
			}
		}
	}
}

// TestGoldenDiffApplyGradientKind composites a glow through both backends: the canvas must match,
// which the binary-coverage path cannot fake.
func TestGoldenDiffApplyGradientKind(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
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
	for i := range canvas {
		canvas[i] = rng.Float32()
	}
	_ = ref.Reset(canvas)
	_ = gpu.Reset(canvas)
	for _, c := range randGradCands(rng, w, h, 8) {
		if err := ref.Apply(c); err != nil {
			t.Fatalf("cuda Apply: %v", err)
		}
		if err := gpu.Apply(c); err != nil {
			t.Fatalf("vulkan Apply: %v", err)
		}
	}
	rcv := make([]float32, w*h*4)
	gcv := make([]float32, w*h*4)
	if err := ref.ReadCanvas(rcv); err != nil {
		t.Fatalf("cuda ReadCanvas: %v", err)
	}
	if err := gpu.ReadCanvas(gcv); err != nil {
		t.Fatalf("vulkan ReadCanvas: %v", err)
	}
	for i := range rcv {
		if math.Abs(float64(rcv[i]-gcv[i])) > 2e-3 {
			t.Fatalf("canvas[%d]: cuda=%.5f vk=%.5f (a glow composites with a per-pixel alpha)", i, rcv[i], gcv[i])
		}
	}
}

// TestGoldenDiffEvaluateGradientKindsHardPath covers the path the greedy actually takes: with the
// gradient flag OFF a glow is scored as a flat ellipse (CUDA's warp kernel has no per-pixel-alpha
// branch), and the glow-swap emits exactly such candidates into the on-device search. Both
// backends must agree there too, or the two pick different shapes.
func TestGoldenDiffEvaluateGradientKindsHardPath(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
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

	ref.SetGradients(false)
	gpu.SetGradients(false)
	canvas := make([]float32, w*h*4)
	for i := range canvas {
		canvas[i] = rng.Float32()
	}
	_ = ref.Reset(canvas)
	_ = gpu.Reset(canvas)

	cands := randGradCands(rng, w, h, 400)
	rc, _ := ref.Evaluate(cands)
	gc, err := gpu.Evaluate(cands)
	if err != nil {
		t.Fatalf("vulkan Evaluate: %v", err)
	}
	var mismatches int
	for i := range cands {
		rRej, gRej := rc[i].Score == rejected, gc[i].Score == rejected
		if rRej != gRej {
			t.Fatalf("cand %d (kind %v) reject mismatch: cuda=%v vk=%v", i, cands[i].Kind, rRej, gRej)
		}
		if rRej {
			continue
		}
		if !closeRel(rc[i].Score, gc[i].Score, 2e-3, 1e-2) {
			if mismatches++; mismatches <= 10 {
				t.Errorf("cand %d (kind %v) score: cuda=%.5f vk=%.5f", i, cands[i].Kind, rc[i].Score, gc[i].Score)
			}
		}
	}
}

// randMaskCands places dictionary words — the bank primitives the smooth-base stacks, the shade
// pre-pass and the glyph passes are built from.
func randMaskCands(t *testing.T, rng *rand.Rand, w, h, n int) []model.Candidate {
	t.Helper()
	var kinds []model.ShapeKind
	for _, word := range []uint16{2204, 2202, 2219, 2220} {
		if k, ok := model.MaskKind(word); ok {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		t.Skip("no bank words compiled in")
	}
	out := make([]model.Candidate, n)
	for i := range out {
		out[i] = model.Candidate{
			Kind: kinds[rng.Intn(len(kinds))],
			P: [6]float32{rng.Float32() * float32(w), rng.Float32() * float32(h),
				6 + rng.Float32()*float32(w)/2, 6 + rng.Float32()*float32(h)/2, rng.Float32() * 180, 0},
			Color: model.RGBA{R: rng.Float32(), G: rng.Float32(), B: rng.Float32(), A: 0.3 + rng.Float32()*0.7},
		}
	}
	return out
}

// TestGoldenDiffMaskWords: a bank word carries its coverage in an atlas on the device. Vulkan used
// to have no atlas at all, so every word was rejected and the passes built on them quietly did
// nothing — invisible to a gate that only fed ellipses.
func TestGoldenDiffMaskWords(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
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

	if !ref.MasksOnDevice() {
		t.Skip("golden backend has no atlas")
	}
	if !gpu.MasksOnDevice() {
		t.Fatal("vulkan reports no word atlas on device — the bank never reached the GPU")
	}
	ref.SetGradients(true)
	defer ref.SetGradients(false)
	gpu.SetGradients(true)

	canvas := make([]float32, w*h*4)
	for i := range canvas {
		canvas[i] = rng.Float32()
	}
	_ = ref.Reset(canvas)
	_ = gpu.Reset(canvas)

	cands := randMaskCands(t, rng, w, h, 200)
	rc, _ := ref.Evaluate(cands)
	gc, err := gpu.Evaluate(cands)
	if err != nil {
		t.Fatalf("vulkan Evaluate: %v", err)
	}
	var mismatches int
	for i := range cands {
		if rc[i].Score == rejected || rc[i].Score >= maskRejected {
			continue
		}
		if gc[i].Score >= maskRejected {
			t.Fatalf("cand %d: vulkan rejects a word the golden backend scores (%.5f)", i, rc[i].Score)
		}
		if !closeRel(rc[i].Score, gc[i].Score, 2e-3, 1e-2) {
			if mismatches++; mismatches <= 10 {
				t.Errorf("cand %d word score: cuda=%.5f vk=%.5f", i, rc[i].Score, gc[i].Score)
			}
		}
	}

	// And the composite: a word's per-pixel coverage must land on the canvas identically.
	for _, c := range cands[:6] {
		if err := ref.Apply(c); err != nil {
			t.Fatalf("cuda Apply: %v", err)
		}
		if err := gpu.Apply(c); err != nil {
			t.Fatalf("vulkan Apply: %v", err)
		}
	}
	rcv, gcv := make([]float32, w*h*4), make([]float32, w*h*4)
	if err := ref.ReadCanvas(rcv); err != nil {
		t.Fatalf("cuda ReadCanvas: %v", err)
	}
	if err := gpu.ReadCanvas(gcv); err != nil {
		t.Fatalf("vulkan ReadCanvas: %v", err)
	}
	for i := range rcv {
		if math.Abs(float64(rcv[i]-gcv[i])) > 2e-3 {
			t.Fatalf("canvas[%d] after word composites: cuda=%.5f vk=%.5f", i, rcv[i], gcv[i])
		}
	}
}
