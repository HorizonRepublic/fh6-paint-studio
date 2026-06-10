package stroke

import "testing"

// A drawn line is a luma RIDGE (darker than both sides); a glow rim is a one-sided STEP edge.
// The gate must separate the two — that is its whole job.
func TestRidgeFraction(t *testing.T) {
	w, h := 64, 64
	luma := make([]float64, w*h)
	for i := range luma {
		luma[i] = 0.9
	}
	// Horizontal dark line at y=20 (1px ridge).
	for x := 0; x < w; x++ {
		luma[20*w+x] = 0.05
	}
	// Step edge at y=40: bright above, dark below (a glow rim profile).
	for y := 40; y < h; y++ {
		for x := 0; x < w; x++ {
			luma[y*w+x] = 0.1
		}
	}
	line := [][2]float64{{4, 20}, {60, 20}}
	if f := ridgeFraction(luma, w, h, line, 0.5); f < 0.9 {
		t.Errorf("ridge polyline fraction = %.2f, want ≥0.9", f)
	}
	// FDoG places the step-edge response just inside the dark side.
	step := [][2]float64{{4, 41}, {60, 41}}
	if f := ridgeFraction(luma, w, h, step, 0.5); f > 0.1 {
		t.Errorf("step-edge polyline fraction = %.2f, want ≤0.1", f)
	}
}
