//go:build vulkan

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
		// The native gradient primitives: a glow trains its geometry through the analytic
		// gaussian gradient, a disk composites softly with its geometry frozen. Without them in
		// the stack the polish golden-diff cannot see a backend that ignores the falloff.
		mk(model.KindGlow, [6]float32{16, 18, 9, 6, 20, 0}, 0.3, 0.6, 0.8, 0.8),
		mk(model.KindDisk, [6]float32{24, 10, 6, 8, 0, 0}, 0.7, 0.4, 0.2, 0.6),
	}
	bg := model.RGBA{R: 0.4, G: 0.4, B: 0.4}
	tau := 1.5

	// Deterministic per-pixel term-weight map for the weighted modes (a horizontal ramp exercises
	// weighting without depending on any detector).
	twMap := make([]float32, w*h)
	for i := range twMap {
		twMap[i] = float32(i%w) / float32(w-1)
	}
	for _, mode := range []struct {
		ste, oklab bool
		fe         float64
		ssim       float64
		eagle      float64
		lost       float64
		tw         bool
	}{{false, false, 0, 0, 0, 0, false}, {true, false, 0, 0, 0, 0, false}, {false, true, 0, 0, 0, 0, false}, {true, true, 0, 0, 0, 0, false},
		{false, false, 0.01, 0, 0, 0, false}, {true, false, 0.01, 0, 0, 0, false},
		{false, false, 0, 0.01, 0, 0, false}, {true, false, 0, 0.01, 0, 0, false}, {false, false, 0.01, 0.01, 0, 0, false},
		{false, false, 0, 0, 0.02, 0, false}, {true, false, 0, 0, 0.02, 0, false}, {false, false, 0.01, 0.01, 0.02, 0, false},
		{false, false, 0.01, 0, 0.02, 0, true}, {true, false, 0.01, 0.01, 0.02, 0, true},
		// Lost-detail (the FE mirror) alone, weighted, and COMBINED with FE. The combined case is
		// the one that matters: both terms scatter through the same Sobel stencil with opposite
		// signs into one dir plane, so a sign or activation slip cancels in isolation.
		{false, false, 0, 0, 0, 0.01, false}, {true, false, 0, 0, 0, 0.01, false},
		{false, false, 0, 0, 0, 0.01, true}, {false, false, 0.01, 0, 0, 0.01, false},
		{false, false, 0.01, 0.01, 0.02, 0.01, true}} {
		ste, oklab, feLam, ssLam, egLam, ldLam := mode.ste, mode.oklab, mode.fe, mode.ssim, mode.eagle, mode.lost
		var tw []float32
		if mode.tw {
			tw = twMap
		}
		if oklab && !gpu.PolishSetOKLab(true) {
			t.Log("DLL lacks fp_set_polish_oklab — skipping the OKLab golden-diff (rebuild the DLL)")
			continue
		}
		ref := engine.PolishStepProbe(shapes, target, weight, w, h, bg, false, tau, ste, oklab, feLam, ssLam, egLam, ldLam, tw)

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
		if egLam > 0 && !gpu.PolishSetEagle(egLam) {
			t.Log("DLL lacks fp_set_polish_eagle — skipping the EAGLE golden-diff (rebuild the DLL)")
			gpu.PolishFree()
			continue
		}
		if ldLam > 0 && !gpu.PolishSetLostDetail(ldLam) {
			t.Log("DLL lacks fp_set_polish_lostdetail — skipping the lost-detail golden-diff (rebuild the DLL)")
			gpu.PolishFree()
			continue
		}
		if !gpu.PolishSetTermWeight(tw) && tw != nil {
			t.Log("DLL lacks fp_set_term_weight — skipping the weighted golden-diff (rebuild the DLL)")
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
