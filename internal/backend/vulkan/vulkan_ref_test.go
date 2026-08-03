//go:build vulkan

package vulkan

// Vulkan against the pure-Go reference (refeval_test.go) — the correctness gate that needs no
// second GPU. Vulkan is the one supported backend, so its contract cannot be "matches CUDA":
// two ports agreeing proves only that they agree, and on 2026-08-03 they agreed on a WRONG
// gradient score for months. These tests compare against an independent implementation of the
// same maths, so a pass means the shader computes what the model says it should.
//
// Coverage is deliberately every shape family the presets actually emit: hard kinds, the radial
// gradients (glow/disk), and the dictionary words. A family missing here is a family that can
// silently rot — that is exactly how the gradient and mask gaps survived.

import (
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/model"
)

// evalAgainstRef runs the same candidates through Vulkan and the reference and compares scores,
// rejections and solved colours. tolerances are loose enough for fp32-vs-float64 accumulation
// order but tight enough that a wrong branch (a glow scored as a solid ellipse) cannot pass.
func evalAgainstRef(t *testing.T, w, h int, cands []model.Candidate, seed int64, transparent bool) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	target, weight := makeTarget(rng, w, h, transparent)

	ref := newRef(target, weight, w, h)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Skipf("vulkan unavailable: %v", err)
	}
	defer gpu.Close()

	canvas := make([]float32, w*h*4)
	for i := range canvas {
		canvas[i] = rng.Float32()
	}
	ref.Reset(canvas)
	if err := gpu.Reset(canvas); err != nil {
		t.Fatalf("vulkan Reset: %v", err)
	}

	rc := ref.Evaluate(cands)
	gc, err := gpu.Evaluate(cands)
	if err != nil {
		t.Fatalf("vulkan Evaluate: %v", err)
	}
	var mismatches int
	for i := range cands {
		rRej, gRej := rc[i].Score == rejected, gc[i].Score == rejected
		if rRej != gRej {
			t.Fatalf("cand %d (kind %v): reject mismatch ref=%v vk=%v", i, cands[i].Kind, rRej, gRej)
		}
		if rRej {
			continue
		}
		if !closeRel(rc[i].Score, gc[i].Score, 2e-3, 1e-2) {
			if mismatches++; mismatches <= 10 {
				t.Errorf("cand %d (kind %v): score ref=%.5f vk=%.5f", i, cands[i].Kind, rc[i].Score, gc[i].Score)
			}
		}
		if !closeRel(rc[i].Color.R, gc[i].Color.R, 3e-3, 3e-3) ||
			!closeRel(rc[i].Color.G, gc[i].Color.G, 3e-3, 3e-3) ||
			!closeRel(rc[i].Color.B, gc[i].Color.B, 3e-3, 3e-3) {
			if mismatches++; mismatches <= 10 {
				t.Errorf("cand %d (kind %v): colour ref=%v vk=%v", i, cands[i].Kind, rc[i].Color, gc[i].Color)
			}
		}
	}
}

func TestRefEvaluateHardKinds(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	evalAgainstRef(t, 41, 33, randCands(rng, 41, 33, 400), 7, false)
}

func TestRefEvaluateHardKindsTransparent(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	evalAgainstRef(t, 37, 29, randCands(rng, 37, 29, 300), 11, true)
}

// TestRefEvaluateGradientKinds is the one that would have caught the 2026-08-03 gate: a glow
// scored as a solid ellipse is off by 1.6-2x, far outside these tolerances.
func TestRefEvaluateGradientKinds(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	evalAgainstRef(t, 37, 29, randGradCands(rng, 37, 29, 400), 13, false)
}

func TestRefEvaluateMaskWords(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
	evalAgainstRef(t, 45, 39, randMaskCands(t, rng, 45, 39, 250), 17, false)
}

// TestRefApply covers the composite rather than the score: a per-pixel-alpha kind must lay down
// its falloff, not a flat disc. Evaluate and Apply share no code, so both need their own gate.
func TestRefApply(t *testing.T) {
	rng := rand.New(rand.NewSource(23))
	w, h := 39, 31
	target, weight := makeTarget(rng, w, h, false)

	ref := newRef(target, weight, w, h)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Skipf("vulkan unavailable: %v", err)
	}
	defer gpu.Close()

	canvas := make([]float32, w*h*4)
	for i := range canvas {
		canvas[i] = rng.Float32()
	}
	ref.Reset(canvas)
	if err := gpu.Reset(canvas); err != nil {
		t.Fatalf("vulkan Reset: %v", err)
	}

	var cands []model.Candidate
	cands = append(cands, randCands(rng, w, h, 12)...)
	cands = append(cands, randGradCands(rng, w, h, 12)...)
	cands = append(cands, randMaskCands(t, rng, w, h, 8)...)
	for i, c := range cands {
		ref.Apply(c)
		if err := gpu.Apply(c); err != nil {
			t.Fatalf("vulkan Apply %d: %v", i, err)
		}
	}

	rcv := make([]float32, w*h*4)
	gcv := make([]float32, w*h*4)
	ref.ReadCanvas(rcv)
	if err := gpu.ReadCanvas(gcv); err != nil {
		t.Fatalf("vulkan ReadCanvas: %v", err)
	}
	for i := range rcv {
		if !closeRel(rcv[i], gcv[i], 3e-3, 3e-3) {
			t.Fatalf("canvas[%d]: ref=%.5f vk=%.5f (px %d, chan %d)", i, rcv[i], gcv[i], i/4, i%4)
		}
	}
}

func TestRefErrorGrid(t *testing.T) {
	rng := rand.New(rand.NewSource(29))
	w, h := 40, 32
	target, weight := makeTarget(rng, w, h, false)

	ref := newRef(target, weight, w, h)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Skipf("vulkan unavailable: %v", err)
	}
	defer gpu.Close()

	canvas := make([]float32, w*h*4)
	for i := range canvas {
		canvas[i] = rng.Float32()
	}
	ref.Reset(canvas)
	if err := gpu.Reset(canvas); err != nil {
		t.Fatalf("vulkan Reset: %v", err)
	}

	const grid = 8
	rg := ref.ErrorGrid(grid)
	gg, gw, gh, err := gpu.ErrorGrid()
	if err != nil {
		t.Fatalf("vulkan ErrorGrid: %v", err)
	}
	if gw != grid || gh != grid {
		t.Fatalf("grid dims %dx%d, want %dx%d", gw, gh, grid, grid)
	}
	for i := range rg {
		if !closeRel(rg[i], gg[i], 2e-3, 1e-3) {
			t.Fatalf("errorgrid[%d]: ref=%.5f vk=%.5f", i, rg[i], gg[i])
		}
	}
}
