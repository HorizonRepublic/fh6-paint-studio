package engine

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/backend/cpu"
	"fh6-paint-studio/internal/model"
)

// bgFromTarget computes the mean color of a target buffer.
func bgFromTarget(target []float32, w, h int) model.RGBA {
	var r, g, b, a float64
	n := float64(w * h)
	for i := 0; i < w*h; i++ {
		r, g, b, a = r+float64(target[i*4]), g+float64(target[i*4+1]), b+float64(target[i*4+2]), a+float64(target[i*4+3])
	}
	return model.RGBA{R: float32(r / n), G: float32(g / n), B: float32(b / n), A: float32(a / n)}
}

// stopIndex feeds an error sequence to a fresh detector and returns the 1-based shape
// count at which it fires (or -1 if it never fires).
func stopIndex(tol float64, errs []float64) int {
	k := newKneeDetector(tol, 0, 0) // floor off → legacy pure-relative behaviour
	for i, e := range errs {
		if k.push(e) {
			return i + 1
		}
	}
	return -1
}

// TestKneeDetector verifies the auto-shape-count knee logic on synthetic error curves:
// a plateauing (exp-decay) curve must fire after window+sustain; a steadily-improving
// curve must NOT fire; tol<=0 never fires; and the RELATIVE rate makes the stop point
// invariant to the absolute error scale (the property that lets one tol fit all content).
func TestKneeDetector(t *testing.T) {
	// Exponential decay toward a floor: per-shape relative improvement decays -> must fire.
	expDecay := func(floor, amp, scale float64, n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = floor + amp*math.Exp(-float64(i)/scale)
		}
		return out
	}
	curve := expDecay(100, 1000, 300, 2200)
	got := stopIndex(2e-3, curve)
	if got < 300 { // cannot fire before window(100)+sustain(200)
		t.Fatalf("plateau curve fired too early: %d (need >= 300)", got)
	}
	if got < 0 || got >= len(curve) {
		t.Fatalf("plateau curve never fired (or at end): %d", got)
	}

	// Scale invariance: multiplying every error by 1000 must give the SAME stop index,
	// because the rate is relative to current error. This is the cross-content property.
	scaled := make([]float64, len(curve))
	for i, e := range curve {
		scaled[i] = e * 1000
	}
	if g2 := stopIndex(2e-3, scaled); g2 != got {
		t.Fatalf("rate not scale-invariant: base=%d scaled=%d", got, g2)
	}

	// A steadily-improving curve (constant relative improvement) must NEVER fire — each
	// shape keeps reducing error by a fixed fraction, so the rate stays above tol.
	geo := make([]float64, 2200)
	geo[0] = 1e6
	for i := 1; i < len(geo); i++ {
		geo[i] = geo[i-1] * 0.997 // 0.3%/shape -> rate ~3e-3/shape, above 2e-3 tol
	}
	if g := stopIndex(2e-3, geo); g != -1 {
		t.Fatalf("steadily-improving curve should not fire, fired at %d", g)
	}

	// tol<=0 disables the detector.
	if g := stopIndex(0, curve); g != -1 {
		t.Fatalf("tol=0 should never fire, fired at %d", g)
	}

	// Lower tol => stops later (more shapes) on the same curve.
	loose := stopIndex(5e-3, curve)
	tight := stopIndex(5e-4, curve)
	if !(loose > 0 && tight > loose) {
		t.Fatalf("expected tighter tol to stop later: loose(5e-3)=%d tight(5e-4)=%d", loose, tight)
	}
}

func TestRunReducesErrorAndProducesShapes(t *testing.T) {
	w, h := 32, 32
	target := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := (y*w + x) * 4
			if x < w/2 {
				target[p+0] = 1 // left half red
			} else {
				target[p+2] = 1 // right half blue
			}
			target[p+3] = 1
		}
	}
	be := cpu.New(target, w, h, 8)
	res := Run(be, Options{Width: w, Height: h, Background: bgFromTarget(target, w, h), StopAt: 40, RandomSamples: 200, MutatedSamples: 100, Seed: 1})
	if len(res.Shapes) < 2 {
		t.Fatalf("got %d shapes, want >=2 (background + ellipses)", len(res.Shapes))
	}
	if res.FinalError >= res.InitialError {
		t.Fatalf("final error %v not below initial %v", res.FinalError, res.InitialError)
	}
}
