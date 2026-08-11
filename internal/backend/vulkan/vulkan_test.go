package vulkan

import (
	"math"
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/model"
)

const rejected = float32(math.MaxFloat32)

// Compile-time guard: *Vulkan MUST satisfy engine.PolishAccel, else the engine silently
// skips polish (a method-name mismatch did exactly that on the CUDA backend).
var _ engine.PolishAccel = (*Vulkan)(nil)

// closeRel reports whether a and b agree within an absolute OR relative tolerance.
func closeRel(a, b, rel, abs float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	if d <= abs {
		return true
	}
	m, n := a, b
	if m < 0 {
		m = -m
	}
	if n < 0 {
		n = -n
	}
	if n > m {
		m = n
	}
	return d <= rel*m
}

// makeTarget builds a small synthetic RGBA target and a random weight map.
func makeTarget(rng *rand.Rand, w, h int, transparent bool) (target, weight []float32) {
	target = make([]float32, w*h*4)
	weight = make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		target[i*4+0] = rng.Float32()
		target[i*4+1] = rng.Float32()
		target[i*4+2] = rng.Float32()
		a := float32(1)
		if transparent && rng.Float32() < 0.3 {
			a = 0
		}
		target[i*4+3] = a
		weight[i] = 0.2 + 0.8*rng.Float32()
	}
	return
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

// TestGradientGateIsLive pins the tuning decision the shipped search depends on: with SetGradients
// OFF a glow is scored as a SOLID shape, not with its radial alpha. That over-credits its coverage
// on purpose — honest gradient scoring is locally correct and measurably worse end to end
// (89876/16 against 90916/7 on img_9@1000), which is why removing the gate on 2026-08-03 had to be
// found by bisect and put back.
//
// The failure this guards against is silent in every other test: an honest branch left ungated still
// agrees with itself across seeds and still produces a plausible picture. Only the end-to-end error
// moves, a day later, on a bench nobody re-runs. So the gate needs a test that fails the moment it
// stops gating.
func TestGradientGateIsLive(t *testing.T) {
	rng := rand.New(rand.NewSource(29))
	w, h := 37, 29
	target, weight := makeTarget(rng, w, h, false)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Skipf("vulkan unavailable: %v", err)
	}
	defer gpu.Close()

	canvas := make([]float32, w*h*4)
	for i := range canvas {
		canvas[i] = rng.Float32()
	}
	cands := randGradCands(rng, w, h, 200)

	if err := gpu.Reset(canvas); err != nil {
		t.Fatalf("vulkan Reset: %v", err)
	}
	gpu.SetGradients(false)
	off, err := gpu.Evaluate(cands)
	if err != nil {
		t.Fatalf("vulkan Evaluate (gate on): %v", err)
	}
	if err := gpu.Reset(canvas); err != nil {
		t.Fatalf("vulkan Reset: %v", err)
	}
	gpu.SetGradients(true)
	on, err := gpu.Evaluate(cands)
	if err != nil {
		t.Fatalf("vulkan Evaluate (honest): %v", err)
	}

	moved := 0
	for i := range cands {
		if off[i].Score == rejected || on[i].Score == rejected {
			continue
		}
		if !closeRel(off[i].Score, on[i].Score, 2e-3, 1e-2) {
			moved++
		}
	}
	// A glow scored as a solid ellipse is off by 1.6-2x, so nearly every candidate should move. A
	// handful may not (a tiny glow whose falloff barely differs from full coverage), hence the
	// margin rather than an exact count.
	if moved < len(cands)/2 {
		t.Errorf("only %d of %d gradient scores changed when the gate was lifted — the gate is not gating",
			moved, len(cands))
	}
}
