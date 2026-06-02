//go:build cuda

package cuda

import (
	"math"
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/backend/cpu"
	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/model"
)

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
			a = 0 // overhang region
		}
		target[i*4+3] = a
		weight[i] = 0.2 + 0.8*rng.Float32()
	}
	return
}

func randCands(rng *rand.Rand, w, h, n int) []model.Candidate {
	kinds := []model.ShapeKind{model.KindEllipse, model.KindRectangle, model.KindTriangle, model.KindLine}
	out := make([]model.Candidate, n)
	for i := range out {
		k := kinds[rng.Intn(len(kinds))]
		var p [6]float32
		switch k {
		case model.KindTriangle:
			for j := 0; j < 6; j++ {
				if j%2 == 0 {
					p[j] = rng.Float32() * float32(w)
				} else {
					p[j] = rng.Float32() * float32(h)
				}
			}
		case model.KindLine:
			p = [6]float32{rng.Float32() * float32(w), rng.Float32() * float32(h),
				rng.Float32() * float32(w), rng.Float32() * float32(h), 1 + rng.Float32()*8, 0}
		default: // ellipse / rectangle
			p = [6]float32{rng.Float32() * float32(w), rng.Float32() * float32(h),
				2 + rng.Float32()*float32(w)/3, 2 + rng.Float32()*float32(h)/3, rng.Float32() * 180, 0}
		}
		out[i] = model.Candidate{Kind: k, P: p, Color: model.RGBA{
			R: rng.Float32(), G: rng.Float32(), B: rng.Float32(), A: 0.3 + 0.7*rng.Float32()}}
	}
	return out
}

// Compile-time guard: *CUDA MUST satisfy engine.PolishAccel, else the engine silently
// falls back to CPU polish (a method-name mismatch did exactly that). This catches it.
var _ engine.PolishAccel = (*CUDA)(nil)

const rejected = float32(math.MaxFloat32)

// TestGoldenDiffEvaluate asserts the CUDA Evaluate matches the CPU reference
// (scores + optimal colors) within float32 tolerance over a batch of random
// candidates, on both opaque and transparent (cutout) targets.
func TestGoldenDiffEvaluate(t *testing.T) {
	for _, transparent := range []bool{false, true} {
		rng := rand.New(rand.NewSource(42))
		w, h := 37, 29 // non-square, non-power-of-two to exercise edge cases
		target, weight := makeTarget(rng, w, h, transparent)

		ref := cpu.New(target, w, h, 8)
		ref.SetWeight(weight)
		gpu, err := New(target, weight, w, h, 8)
		if err != nil {
			t.Fatalf("cuda.New: %v", err)
		}
		defer gpu.Close()

		// Random non-trivial canvas, identical on both backends.
		canvas := make([]float32, w*h*4)
		for i := range canvas {
			canvas[i] = rng.Float32()
		}
		_ = ref.Reset(canvas)
		_ = gpu.Reset(canvas)

		cands := randCands(rng, w, h, 2000)
		rc, _ := ref.Evaluate(cands)
		gc, _ := gpu.Evaluate(cands)

		var mismatches int
		for i := range cands {
			rRej := rc[i].Score == rejected
			gRej := gc[i].Score == rejected
			if rRej || gRej {
				if rRej != gRej {
					t.Errorf("[transp=%v] cand %d reject mismatch: cpu=%v cuda=%v", transparent, i, rRej, gRej)
				}
				continue
			}
			if !closeRel(rc[i].Score, gc[i].Score, 2e-3, 1e-2) {
				if mismatches++; mismatches <= 10 {
					t.Errorf("[transp=%v] cand %d score: cpu=%.5f cuda=%.5f", transparent, i, rc[i].Score, gc[i].Score)
				}
			}
			for _, cc := range [][2]float32{
				{rc[i].Color.R, gc[i].Color.R}, {rc[i].Color.G, gc[i].Color.G},
				{rc[i].Color.B, gc[i].Color.B}, {rc[i].Color.A, gc[i].Color.A},
			} {
				if math.Abs(float64(cc[0]-cc[1])) > 1e-3 {
					t.Errorf("[transp=%v] cand %d color: cpu=%.5f cuda=%.5f", transparent, i, cc[0], cc[1])
					break
				}
			}
		}
	}
}

// TestGoldenDiffApplyAndGrid checks that Apply composites identically and the
// error grid matches after a sequence of applied shapes.
func TestGoldenDiffApplyAndGrid(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	w, h := 40, 40
	target, weight := makeTarget(rng, w, h, false)
	ref := cpu.New(target, w, h, 8)
	ref.SetWeight(weight)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Fatalf("cuda.New: %v", err)
	}
	defer gpu.Close()

	canvas := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		canvas[i*4+3] = 1 // opaque black
	}
	_ = ref.Reset(canvas)
	_ = gpu.Reset(canvas)

	for _, c := range randCands(rng, w, h, 25) {
		_ = ref.Apply(c)
		_ = gpu.Apply(c)
	}

	rcv := make([]float32, w*h*4)
	gcv := make([]float32, w*h*4)
	_ = ref.ReadCanvas(rcv)
	_ = gpu.ReadCanvas(gcv)
	for i := range rcv {
		if math.Abs(float64(rcv[i]-gcv[i])) > 1e-4 {
			t.Fatalf("canvas[%d]: cpu=%.6f cuda=%.6f", i, rcv[i], gcv[i])
		}
	}

	rg, _, _, _ := ref.ErrorGrid()
	gg, _, _, _ := gpu.ErrorGrid()
	for i := range rg {
		if !closeRel(rg[i], gg[i], 1e-3, 1e-3) {
			t.Fatalf("grid[%d]: cpu=%.5f cuda=%.5f", i, rg[i], gg[i])
		}
	}
}

// TestGoldenDiffPolish asserts the CUDA polish forward render, loss, and per-shape
// gradients match the pure-Go reference (engine.PolishStepProbe) for one step at a fixed
// tau, over a mixed scene (ellipse+rect = soft/optGeo, triangle = hard coverage). This is
// the spec the GPU polish primitives must satisfy; the orchestration (Adam/anneal/snap)
// is shared host code so validating one step validates the loop.
func TestGoldenDiffPolish(t *testing.T) {
	rng := rand.New(rand.NewSource(123))
	w, h := 41, 31
	target, weight := makeTarget(rng, w, h, false)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Fatalf("cuda.New: %v", err)
	}
	defer gpu.Close()
	if !gpu.PolishSupported() {
		t.Fatal("DLL lacks polish API (rebuild fh6cuda.dll)")
	}

	mk := func(k model.ShapeKind, p [6]float32, r, g, b, a float32) model.Shape {
		return model.Candidate{Kind: k, P: p, Color: model.RGBA{R: r, G: g, B: b, A: a}}.ToShape(0)
	}
	shapes := []model.Shape{
		mk(model.KindRectangle, [6]float32{0, 0, float32(w), float32(h), 0, 0}, 0.4, 0.4, 0.4, 1), // bg (ignored by probe; base uses bg arg)
		mk(model.KindEllipse, [6]float32{20, 15, 11, 4, 30, 0}, 0.8, 0.2, 0.1, 0.7),
		mk(model.KindRectangle, [6]float32{10, 12, 6, 8, 10, 0}, 0.1, 0.7, 0.2, 0.6),
		mk(model.KindTriangle, [6]float32{5, 5, 26, 8, 12, 26}, 0.2, 0.3, 0.9, 0.9),
		mk(model.KindEllipse, [6]float32{30, 20, 7, 7, 0, 0}, 0.9, 0.9, 0.1, 0.5),
	}
	bg := model.RGBA{R: 0.4, G: 0.4, B: 0.4}
	tau := 1.5

	// Run both coverage modes: soft (the original) and STE (hard forward + soft surrogate
	// gradient). The mixed scene's optGeo shapes have edges inside their expanded bbox, so
	// the STE split-guard outer-band geometry gradient is exercised.
	for _, ste := range []bool{false, true} {
		ref := engine.PolishStepProbe(shapes, target, weight, w, h, bg, false, tau, ste)

		gpu.PolishSetSTE(ste)
		gpu.PolishSetup(ref.Base, ref.N)
		gpu.PolishUpload(ref.P, ref.Col, ref.Kinds, ref.BBX, ref.Boff, ref.BelowTotal)
		gpu.PolishForward(tau, ref.BBX)
		lossGPU := gpu.PolishLoss()
		renderGPU := make([]float32, w*h*4)
		gpu.PolishReadRender(renderGPU)
		gpu.PolishBackward(tau, ref.BBX)
		gradGPU := make([]float64, ref.N*10) // 6 geo + 4 color per shape (was *9 -> heap overflow vs the n*10 device copy)
		gpu.PolishReadGrad(gradGPU)
		// Hard-coverage best-hard render (the shipped deliverable): GPU vs CPU. The GPU reuses
		// the EXPANDED bbox while the CPU loops the native bbox — hard inside() is 0 outside the
		// native box, so they must agree. (STE-independent: hardloss is always full hard.)
		hardGPU, ok := gpu.PolishHardLoss(ref.BBX)
		if !ok {
			t.Fatal("DLL lacks fp_polish_hard_loss (rebuild fh6cuda.dll)")
		}
		gpu.PolishFree()

		// Forward render: float32 composite on both -> tight.
		var maxRenderDiff float64
		for i := range ref.Render {
			d := math.Abs(float64(ref.Render[i] - renderGPU[i]))
			if d > maxRenderDiff {
				maxRenderDiff = d
			}
		}
		if maxRenderDiff > 2e-3 {
			t.Errorf("[ste=%v] polish forward render max diff %.5f (cpu vs cuda)", ste, maxRenderDiff)
		}
		// Loss: weighted SSE, double both.
		if !closeRel(float32(ref.Loss), float32(lossGPU), 2e-3, 1e-3) {
			t.Errorf("[ste=%v] polish loss: cpu=%.5f cuda=%.5f", ste, ref.Loss, lossGPU)
		}
		// Hard loss: hard render is float32 composite both sides -> tight rel tol.
		if !closeRel(float32(ref.HardLoss), float32(hardGPU), 2e-3, 1e-3) {
			t.Errorf("[ste=%v] polish HARD loss: cpu=%.5f cuda=%.5f", ste, ref.HardLoss, hardGPU)
		}
		// Gradients: device dC is float32 (CPU float64) so allow a looser rel tol.
		var mism int
		for i := 0; i < ref.N*10; i++ {
			a, b := ref.Grad[i], gradGPU[i]
			d := math.Abs(a - b)
			m := math.Max(math.Abs(a), math.Abs(b))
			if d > 1e-4 && d > 1.5e-2*m {
				if mism++; mism <= 12 {
					t.Errorf("[ste=%v] polish grad[%d] (shape %d slot %d): cpu=%.6f cuda=%.6f", ste, i, i/10, i%10, a, b)
				}
			}
		}
	}
	gpu.PolishSetSTE(false) // reset device state for any later test
}

// closeRel reports whether a and b agree within relative rel or absolute abs.
func closeRel(a, b, rel, abs float32) bool {
	d := math.Abs(float64(a - b))
	if d <= float64(abs) {
		return true
	}
	m := math.Max(math.Abs(float64(a)), math.Abs(float64(b)))
	return d <= float64(rel)*m
}
