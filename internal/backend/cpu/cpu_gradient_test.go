package cpu

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

func sseVsTarget(c *CPU) float64 {
	canvas := make([]float32, c.w*c.h*4)
	_ = c.ReadCanvas(canvas)
	var s float64
	for i := 0; i < c.w*c.h*4; i++ {
		d := float64(c.target[i] - canvas[i])
		s += d * d
	}
	return s
}

// rampTarget makes an opaque horizontal RGB ramp — a non-trivial target the analytic ΔSSE must match.
func rampTarget(w, h int) []float32 {
	t := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := (y*w + x) * 4
			f := float32(x) / float32(w-1)
			t[p+0] = f
			t[p+1] = 1 - f
			t[p+2] = 0.5
			t[p+3] = 1
		}
	}
	return t
}

// The analytic gradient Score must equal the REAL SSE change from applying the chosen optimal colour
// (full-res, opaque -> no sampling scale and no spill penalty). Verifies the per-pixel-alpha solve.
func TestGradientScoreMatchesAppliedDelta(t *testing.T) {
	for _, kind := range []model.ShapeKind{model.KindGlow, model.KindDisk} {
		w, h := 48, 48
		c := New(rampTarget(w, h), w, h, 8)
		c.SetSampleBudget(1 << 20) // force full-res (step=1)

		cand := model.Candidate{Kind: kind, P: [6]float32{24, 24, 16, 12, 30, 0}, Color: model.RGBA{A: 0.85}}
		res, _ := c.Evaluate([]model.Candidate{cand})
		score := float64(res[0].Score)
		if res[0].Score == rejected {
			t.Fatalf("kind %v: candidate rejected unexpectedly", kind)
		}

		before := sseVsTarget(c)
		applied := cand
		applied.Color = res[0].Color // analytic optimal RGB (+ original A)
		_ = c.Apply(applied)
		after := sseVsTarget(c)
		actual := after - before

		if math.Abs(score-actual) > 1e-3*(1+math.Abs(actual)) {
			t.Errorf("kind %v: Score %.6f != applied ΔSSE %.6f", kind, score, actual)
		}
		if actual >= 0 {
			t.Errorf("kind %v: a fitted gradient should reduce SSE, got ΔSSE %.6f", kind, actual)
		}
	}
}

// Per-pixel alpha: applying a glow leaves the centre much closer to the shape colour than the rim.
func TestGradientApplyFalloff(t *testing.T) {
	w, h := 41, 41
	target := make([]float32, w*h*4) // black opaque target (irrelevant; we inspect canvas directly)
	for i := 0; i < w*h; i++ {
		target[i*4+3] = 1
	}
	c := New(target, w, h, 8)
	// White glow centred, A=1 -> centre coverage ≈ peak (0.89), rim ≈ 0.
	cand := model.Candidate{Kind: model.KindGlow, P: [6]float32{20, 20, 18, 18, 0, 0}, Color: model.RGBA{R: 1, G: 1, B: 1, A: 1}}
	_ = c.Apply(cand)
	canvas := make([]float32, w*h*4)
	_ = c.ReadCanvas(canvas)

	center := canvas[(20*w+20)*4] // R at centre
	rim := canvas[(20*w+37)*4]    // R near the footprint edge (t≈0.94)
	if center < 0.8 {
		t.Errorf("glow centre R = %.3f, want ≳ 0.85 (peak ~0.89)", center)
	}
	if rim > 0.25 {
		t.Errorf("glow rim R = %.3f, want small (falloff)", rim)
	}
	if center <= rim {
		t.Errorf("glow should be brighter at centre (%.3f) than rim (%.3f)", center, rim)
	}
}
