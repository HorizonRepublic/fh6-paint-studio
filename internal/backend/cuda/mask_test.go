//go:build cuda

package cuda

import (
	"math"
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"
)

// maskCands builds random dictionary-mask candidates: any bank word, full placement affine
// [cx, cy, Hx, Hy, rotDeg, skew] including mirrored (negative Hx) and skewed stamps.
func maskCands(rng *rand.Rand, w, h, n int) []model.Candidate {
	bank := maskbank.All()
	out := make([]model.Candidate, n)
	for i := range out {
		e := bank[rng.Intn(len(bank))]
		hx := 4 + rng.Float32()*float32(w)
		if rng.Float32() < 0.3 {
			hx = -hx
		}
		out[i] = model.Candidate{
			Kind: e.Kind,
			P: [6]float32{
				rng.Float32() * float32(w), rng.Float32() * float32(h),
				hx, 4 + rng.Float32()*float32(h),
				rng.Float32() * 360, (rng.Float32() - 0.5) * 0.6,
			},
			Color: model.RGBA{R: rng.Float32(), G: rng.Float32(), B: rng.Float32(), A: 0.3 + 0.7*rng.Float32()},
		}
	}
	return out
}

// TestMaskEvalMatchesCPU asserts the device mask-atlas eval matches the CPU coverage reference
// (scores + optimal colors) within the float32 tolerance used by the gradient kinds — the mask
// path is per-pixel-alpha like KindGlow/KindDisk, with a bilinear texture sample as the coverage.
func TestMaskEvalMatchesCPU(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	w, h := 37, 29
	target, weight := makeTarget(rng, w, h, false)
	ref := newRef(target, weight, w, h)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Fatalf("cuda.New: %v", err)
	}
	defer gpu.Close()
	if !gpu.masksOn {
		t.Skip("DLL predates fp_set_masks — rebuild fh6cuda.dll")
	}

	canvas := make([]float32, w*h*4)
	for i := range canvas {
		canvas[i] = rng.Float32()
	}
	ref.Reset(canvas)
	_ = gpu.Reset(canvas)

	cands := maskCands(rng, w, h, 1200)
	rc := ref.Evaluate(cands)
	gc, _ := gpu.Evaluate(cands)

	var mism int
	for i := range cands {
		rRej, gRej := rc[i].Score == rejected, gc[i].Score == rejected || gc[i].Score == maskRejected
		if rRej || gRej {
			if rRej != gRej {
				t.Errorf("cand %d (kind %d) reject mismatch: cpu=%v cuda=%v", i, cands[i].Kind, rRej, gRej)
			}
			continue
		}
		if !closeRel(rc[i].Score, gc[i].Score, 6e-3, 3e-2) {
			if mism++; mism <= 8 {
				t.Errorf("cand %d (kind %d) score: cpu=%.5f cuda=%.5f", i, cands[i].Kind, rc[i].Score, gc[i].Score)
			}
		}
		if math.Abs(float64(rc[i].Color.R-gc[i].Color.R)) > 5e-3 {
			t.Errorf("cand %d (kind %d) colorR: cpu=%.4f cuda=%.4f", i, cands[i].Kind, rc[i].Color.R, gc[i].Color.R)
		}
	}
}

// TestMaskApplyMatchesCPU checks mask stamps composite onto the device canvas like the CPU.
func TestMaskApplyMatchesCPU(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	w, h := 40, 40
	target, weight := makeTarget(rng, w, h, false)
	ref := newRef(target, weight, w, h)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Fatalf("cuda.New: %v", err)
	}
	defer gpu.Close()
	if !gpu.masksOn {
		t.Skip("DLL predates fp_set_masks — rebuild fh6cuda.dll")
	}

	canvas := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		canvas[i*4+3] = 1
	}
	ref.Reset(canvas)
	_ = gpu.Reset(canvas)
	for _, c := range maskCands(rng, w, h, 20) {
		ref.Apply(c)
		_ = gpu.Apply(c)
	}
	rcv := make([]float32, w*h*4)
	gcv := make([]float32, w*h*4)
	ref.ReadCanvas(rcv)
	_ = gpu.ReadCanvas(gcv)
	for i := range rcv {
		if math.Abs(float64(rcv[i]-gcv[i])) > 5e-3 {
			t.Fatalf("canvas[%d]: cpu=%.6f cuda=%.6f", i, rcv[i], gcv[i])
		}
	}
}

// TestPolishMaskShape runs one polish probe step on a scene containing a dictionary mask: the
// forward render/loss must match the CPU, the mask's geometry gradient must stay zero on BOTH
// sides (frozen — masks are colour-polished only), and its colour gradient must agree.
func TestPolishMaskShape(t *testing.T) {
	rng := rand.New(rand.NewSource(77))
	w, h := 41, 31
	target, weight := makeTarget(rng, w, h, false)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Fatalf("cuda.New: %v", err)
	}
	defer gpu.Close()
	if !gpu.masksOn {
		t.Skip("DLL predates fp_set_masks — rebuild fh6cuda.dll")
	}
	if !gpu.PolishSupported() {
		t.Fatal("DLL lacks polish API (rebuild fh6cuda.dll)")
	}

	bank := maskbank.All()
	mask := bank[3]
	mk := func(k model.ShapeKind, p [6]float32, r, g, b, a float32) model.Shape {
		return model.Candidate{Kind: k, P: p, Color: model.RGBA{R: r, G: g, B: b, A: a}}.ToShape(0)
	}
	shapes := []model.Shape{
		mk(model.KindRectangle, [6]float32{0, 0, float32(w), float32(h), 0, 0}, 0.4, 0.4, 0.4, 1),
		mk(model.KindEllipse, [6]float32{20, 15, 11, 4, 30, 0}, 0.8, 0.2, 0.1, 0.7),
		mk(mask.Kind, [6]float32{18, 14, 22, 17, 25, 0.1}, 0.2, 0.6, 0.9, 0.8),
		mk(model.KindTriangle, [6]float32{5, 5, 26, 8, 12, 26}, 0.2, 0.3, 0.9, 0.9),
	}
	bg := model.RGBA{R: 0.4, G: 0.4, B: 0.4}
	tau := 1.5

	for _, ste := range []bool{false, true} {
		ref := engine.PolishStepProbe(shapes, target, weight, w, h, bg, false, tau, ste, false, 0, 0, 0, 0, nil)
		gpu.PolishSetSTE(ste)
		gpu.PolishSetup(ref.Base, ref.N)
		gpu.PolishUpload(ref.P, ref.Col, ref.Kinds, ref.BBX, ref.Boff, ref.BelowTotal)
		gpu.PolishForward(tau, ref.BBX)
		lossGPU := gpu.PolishLoss()
		renderGPU := make([]float32, w*h*4)
		gpu.PolishReadRender(renderGPU)
		gpu.PolishBackward(tau, ref.BBX)
		gradGPU := make([]float64, ref.N*10)
		gpu.PolishReadGrad(gradGPU)
		gpu.PolishFree()

		var maxRenderDiff float64
		for i := range ref.Render {
			if d := math.Abs(float64(ref.Render[i] - renderGPU[i])); d > maxRenderDiff {
				maxRenderDiff = d
			}
		}
		if maxRenderDiff > 5e-3 {
			t.Errorf("[ste=%v] forward render max diff %.5f", ste, maxRenderDiff)
		}
		if !closeRel(float32(ref.Loss), float32(lossGPU), 5e-3, 1e-2) {
			t.Errorf("[ste=%v] loss: cpu=%.5f cuda=%.5f", ste, ref.Loss, lossGPU)
		}
		// the mask is probe shape index 1 (the probe drops the background)
		mi := 1
		for j := 0; j < 6; j++ {
			if g := gradGPU[mi*10+j]; g != 0 {
				t.Errorf("[ste=%v] mask geometry grad[%d] = %g, want 0 (frozen)", ste, j, g)
			}
			if g := ref.Grad[mi*10+j]; g != 0 {
				t.Errorf("[ste=%v] CPU mask geometry grad[%d] = %g, want 0 (frozen)", ste, j, g)
			}
		}
		for j := 6; j < 10; j++ {
			rg, gg := ref.Grad[mi*10+j], gradGPU[mi*10+j]
			if math.Abs(rg-gg) > 5e-3*math.Max(1, math.Abs(rg)) {
				t.Errorf("[ste=%v] mask color grad[%d]: cpu=%.5f cuda=%.5f", ste, j, rg, gg)
			}
		}
	}
}
