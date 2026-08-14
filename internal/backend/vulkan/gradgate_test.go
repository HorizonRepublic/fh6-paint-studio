package vulkan

import (
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/model"
)

// The honest-gradient switch has to reach the ON-DEVICE search, not just the host Evaluate path.
// It did not: the push constant was left out of the two search entry points and aggregate-initialised
// to zero, so one run scored gradients two different ways depending on which half of the engine was
// asking, and every A/B ever run with the flag moved only half the system.
//
// The probe relies on the search being seeded: with the same seed the generated candidate stream is
// identical, so the ONLY thing that can change the winner or its score is how the candidates were
// scored. A shader ignoring the flag returns bit-identical results.
func TestGradientFlagReachesTheDeviceSearch(t *testing.T) {
	const w, h = 64, 48
	rng := rand.New(rand.NewSource(11))
	target, weight := smoothTarget(rng, w, h)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Skipf("vulkan unavailable: %v", err)
	}
	defer gpu.Close()
	canvas := flatCanvas(w, h, 0.5)

	grid := make([]float32, 8*6)
	for i := range grid {
		grid[i] = 1
	}
	kinds := []model.ShapeKind{model.KindGlow}
	kindCDF := []float32{1}

	run := func(on bool) (model.Candidate, float32, bool) {
		gpu.SetGradients(on)
		if err := gpu.Reset(canvas); err != nil {
			t.Fatalf("Reset: %v", err)
		}
		return gpu.SearchRandom(4242, 512, kinds, kindCDF, 24, true, 0.3, 4, false, 1, grid, 8, 6, 0, 0, 0)
	}
	cOff, sOff, ok := run(false)
	if !ok {
		t.Skip("on-device search unavailable")
	}
	cOn, sOn, _ := run(true)

	// A glow scored as a solid ellipse and a glow scored through its own falloff put very different
	// amounts of paint on the canvas, so the winning candidate, its score, or both must move.
	if sOff == sOn && cOff.P == cOn.P && cOff.Kind == cOn.Kind {
		t.Errorf("the search returned an identical result with gradients off and on "+
			"(score %.4f, P %v) — fp_set_gradients is not reaching the device search", sOff, cOff.P)
	}
}
