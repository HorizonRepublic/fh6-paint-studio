package vulkan

import (
	"testing"

	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/model"
)

// TestPolishHonoursAlphaFloor drives the real polish through the Vulkan backend and asserts the
// descent never pushes a shape below PolishOptions.AlphaMin. The floor is a shipped quality default
// (organic alphaMin 0.30) that the polish once quietly undid by clamping to its own hard-coded 0.05,
// so the guarantee has to be checked on the path that actually runs — this test lives in the vulkan
// package because the engine cannot import a backend.
func TestPolishHonoursAlphaFloor(t *testing.T) {
	const w, h, floor = 32, 28, 0.6
	// A fully transparent target. Colour cannot absorb the error here — the loss charges the alpha
	// channel too, and no RGB choice makes an opaque shape match a hole — so alpha is the only
	// parameter with anywhere to go, and it goes down until something stops it.
	target := make([]float32, w*h*4)
	weight := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		weight[i] = 1
	}
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Skipf("vulkan unavailable: %v", err)
	}
	defer gpu.Close()
	if !gpu.PolishSupported() {
		t.Skip("device cannot polish")
	}

	bg := model.RGBA{A: 1}
	shapes := []model.Shape{model.Candidate{Kind: model.KindRectangle,
		P: [6]float32{16, 14, 16, 14, 0, 0}, Color: bg}.ToShape(0)}
	for i := 0; i < 5; i++ {
		shapes = append(shapes, model.Candidate{Kind: model.KindEllipse,
			P:     [6]float32{6 + float32(i)*5, 8 + float32(i)*3, 5, 4, float32(i) * 17, 0},
			Color: model.RGBA{R: 0.95, G: 0.9, B: 0.85, A: 0.95}}.ToShape(0))
	}

	run := func(alphaMin float64) []int {
		opt := engine.DefaultPolishOptions()
		opt.Iters = 80
		opt.LRAlpha = 0.03 // the default 0.01 needs hundreds of iterations to cross the floor
		opt.AlphaMin = alphaMin
		res := engine.PolishWithBackend(shapes, target, weight, w, h, bg, true, opt, gpu)
		out := make([]int, 0, len(res.Shapes)-1)
		for _, s := range res.Shapes[1:] {
			out = append(out, s.Color[3])
		}
		return out
	}

	// F2B rounds the exported alpha to a byte, so the floor lands one count either side of 255*floor.
	minByte := model.F2B(floor) - 1
	floored := run(floor)
	rested := false // did anything actually come to rest ON the floor?
	for i, a := range floored {
		if a < minByte {
			t.Errorf("shape %d ended at alpha byte %d, below the %v floor (%d)", i, a, floor, minByte)
		}
		if a <= minByte+2 {
			rested = true
		}
	}
	if !rested {
		t.Errorf("no shape reached the floor (%v) — the descent never got there, so the clamp went untested", floored)
	}
	// Without the floor the same descent must actually go lower, otherwise the check above passes on
	// a scene that never tested anything.
	free := run(0)
	sank := false
	for _, a := range free {
		if a < minByte {
			sank = true
		}
	}
	if !sank {
		t.Errorf("with no floor the alphas stayed at %v — the scene does not exercise the clamp", free)
	}
}
