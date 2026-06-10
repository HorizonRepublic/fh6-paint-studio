//go:build vulkan

package vulkan

import (
	"math"
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/backend/cpu"
	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/model"
)

const rejected = float32(math.MaxFloat32)

// Compile-time guard: *Vulkan MUST satisfy engine.PolishAccel, else the engine silently
// falls back to CPU polish (a method-name mismatch did exactly that on the CUDA backend).
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
		default:
			p = [6]float32{rng.Float32() * float32(w), rng.Float32() * float32(h),
				2 + rng.Float32()*float32(w)/3, 2 + rng.Float32()*float32(h)/3, rng.Float32() * 180, 0}
		}
		out[i] = model.Candidate{Kind: k, P: p, Color: model.RGBA{
			R: rng.Float32(), G: rng.Float32(), B: rng.Float32(), A: 0.3 + 0.7*rng.Float32()}}
	}
	return out
}

// TestGoldenDiffEvaluate asserts the Vulkan Evaluate matches the CPU reference (scores +
// optimal colors) within float32 tolerance over a batch of random candidates, on both
// opaque and transparent (cutout) targets. This is the Phase 1 GO/NO-GO gate.
func TestGoldenDiffEvaluate(t *testing.T) {
	for _, transparent := range []bool{false, true} {
		rng := rand.New(rand.NewSource(42))
		w, h := 37, 29
		target, weight := makeTarget(rng, w, h, transparent)

		ref := cpu.New(target, w, h, 8)
		ref.SetWeight(weight)
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
					t.Errorf("[transp=%v] cand %d reject mismatch: cpu=%v vk=%v", transparent, i, rRej, gRej)
				}
				continue
			}
			if !closeRel(rc[i].Score, gc[i].Score, 2e-3, 1e-2) {
				if mismatches++; mismatches <= 10 {
					t.Errorf("[transp=%v] cand %d score: cpu=%.5f vk=%.5f", transparent, i, rc[i].Score, gc[i].Score)
				}
			}
			for _, cc := range [][2]float32{
				{rc[i].Color.R, gc[i].Color.R}, {rc[i].Color.G, gc[i].Color.G},
				{rc[i].Color.B, gc[i].Color.B}, {rc[i].Color.A, gc[i].Color.A},
			} {
				if math.Abs(float64(cc[0]-cc[1])) > 1e-3 {
					t.Errorf("[transp=%v] cand %d color: cpu=%.5f vk=%.5f", transparent, i, cc[0], cc[1])
					break
				}
			}
		}
	}
}

// TestGoldenDiffApply asserts the Vulkan Apply composites identically to the CPU
// reference: apply a sequence of shapes on both, then compare the full canvas.
func TestGoldenDiffApply(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	w, h := 41, 33
	target, weight := makeTarget(rng, w, h, false)

	ref := cpu.New(target, w, h, 8)
	ref.SetWeight(weight)
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
				t.Errorf("canvas[%d] (px %d ch %d): cpu=%.5f vk=%.5f", i, i/4, i%4, rcanv[i], gcanv[i])
			}
		}
	}
	if bad > 0 {
		t.Errorf("Apply mismatch: %d/%d channels diverged", bad, len(rcanv))
	}
}

// TestGoldenDiffErrorGrid asserts the on-device error grid matches cpu.ErrorGrid (within
// a float-reduction tolerance) after compositing a few shapes onto a shared canvas.
func TestGoldenDiffErrorGrid(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	w, h, grid := 53, 47, 8
	target, weight := makeTarget(rng, w, h, false)
	ref := cpu.New(target, w, h, grid)
	ref.SetWeight(weight)
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
			t.Errorf("grid[%d]: cpu=%.5f vk=%.5f", i, rg[i], gg[i])
		}
	}
}

// TestGoldenDiffPolish asserts the Vulkan polish forward render, loss, hard loss, and
// per-shape gradients match the pure-Go reference (engine.PolishStepProbe) for one step at
// a fixed tau, over a mixed scene (ellipse+rect = soft/optGeo, triangle = hard coverage),
// in both soft and STE modes. Mirrors the CUDA polish gate; glow is out of Phase 3 scope.
func TestGoldenDiffPolish(t *testing.T) {
	rng := rand.New(rand.NewSource(123))
	w, h := 41, 31
	target, weight := makeTarget(rng, w, h, false)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Fatalf("vulkan.New: %v", err)
	}
	defer gpu.Close()
	if !gpu.PolishSupported() {
		t.Fatal("DLL lacks polish API (rebuild fh6vk.dll)")
	}

	mk := func(k model.ShapeKind, p [6]float32, r, g, b, a float32) model.Shape {
		return model.Candidate{Kind: k, P: p, Color: model.RGBA{R: r, G: g, B: b, A: a}}.ToShape(0)
	}
	shapes := []model.Shape{
		mk(model.KindRectangle, [6]float32{0, 0, float32(w), float32(h), 0, 0}, 0.4, 0.4, 0.4, 1), // bg (ignored by probe)
		mk(model.KindEllipse, [6]float32{20, 15, 11, 4, 30, 0}, 0.8, 0.2, 0.1, 0.7),
		mk(model.KindRectangle, [6]float32{10, 12, 6, 8, 10, 0}, 0.1, 0.7, 0.2, 0.6),
		mk(model.KindTriangle, [6]float32{5, 5, 26, 8, 12, 26}, 0.2, 0.3, 0.9, 0.9),
		mk(model.KindEllipse, [6]float32{30, 20, 7, 7, 0, 0}, 0.9, 0.9, 0.1, 0.5),
	}
	bg := model.RGBA{R: 0.4, G: 0.4, B: 0.4}
	tau := 1.5

	for _, mode := range []struct {
		ste, oklab bool
		fe         float64
		ssim       float64
	}{{false, false, 0, 0}, {true, false, 0, 0}, {false, true, 0, 0}, {true, true, 0, 0},
		{false, false, 0.01, 0}, {true, false, 0.01, 0},
		{false, false, 0, 0.01}, {true, false, 0, 0.01}, {false, false, 0.01, 0.01}} {
		ste, oklab, feLam, ssLam := mode.ste, mode.oklab, mode.fe, mode.ssim
		if oklab && !gpu.PolishSetOKLab(true) {
			t.Log("DLL lacks fp_set_polish_oklab — skipping the OKLab golden-diff (rebuild the DLL)")
			continue
		}
		ref := engine.PolishStepProbe(shapes, target, weight, w, h, bg, false, tau, ste, oklab, feLam, ssLam)

		gpu.PolishSetSTE(ste)
		gpu.PolishSetup(ref.Base, ref.N)
		if feLam > 0 && !gpu.PolishSetFalseEdge(feLam) {
			t.Log("DLL lacks fp_set_polish_false_edge — skipping the false-edge golden-diff (rebuild the DLL)")
			gpu.PolishFree()
			continue
		}
		if ssLam > 0 && !gpu.PolishSetSSIM(ssLam) {
			t.Log("DLL lacks fp_set_polish_ssim — skipping the SSIM golden-diff (rebuild the DLL)")
			gpu.PolishFree()
			continue
		}
		gpu.PolishUpload(ref.P, ref.Col, ref.Kinds, ref.BBX, ref.Boff, ref.BelowTotal)
		gpu.PolishForward(tau, ref.BBX)
		lossGPU := gpu.PolishLoss()
		renderGPU := make([]float32, w*h*4)
		gpu.PolishReadRender(renderGPU)
		gpu.PolishBackward(tau, ref.BBX)
		gradGPU := make([]float64, ref.N*10)
		gpu.PolishReadGrad(gradGPU)
		hardGPU, ok := gpu.PolishHardLoss(ref.BBX)
		if !ok {
			t.Fatal("DLL lacks fp_polish_hard_loss")
		}
		gpu.PolishFree()
		gpu.PolishSetOKLab(false)

		var maxRenderDiff float64
		for i := range ref.Render {
			d := math.Abs(float64(ref.Render[i] - renderGPU[i]))
			if d > maxRenderDiff {
				maxRenderDiff = d
			}
		}
		if maxRenderDiff > 2e-3 {
			t.Errorf("[ste=%v oklab=%v fe=%g ssim=%g] polish forward render max diff %.5f (cpu vs vk)", ste, oklab, feLam, ssLam, maxRenderDiff)
		}
		if !closeRel(float32(ref.Loss), float32(lossGPU), 2e-3, 1e-3) {
			t.Errorf("[ste=%v oklab=%v fe=%g ssim=%g] polish loss: cpu=%.5f vk=%.5f", ste, oklab, feLam, ssLam, ref.Loss, lossGPU)
		}
		if !closeRel(float32(ref.HardLoss), float32(hardGPU), 2e-3, 1e-3) {
			t.Errorf("[ste=%v oklab=%v fe=%g ssim=%g] polish HARD loss: cpu=%.5f vk=%.5f", ste, oklab, feLam, ssLam, ref.HardLoss, hardGPU)
		}
		var mism int
		for i := 0; i < ref.N*10; i++ {
			a, b := ref.Grad[i], gradGPU[i]
			d := math.Abs(a - b)
			m := math.Max(math.Abs(a), math.Abs(b))
			if d > 1e-4 && d > 1.5e-2*m {
				if mism++; mism <= 12 {
					t.Errorf("[ste=%v oklab=%v] polish grad[%d] (shape %d slot %d): cpu=%.6f vk=%.6f", ste, oklab, i, i/10, i%10, a, b)
				}
			}
		}
	}
	gpu.PolishSetSTE(false)
}
