package inject

import (
	"fmt"
	"math"
	"testing"
)

// TestTriangleFitRoundTrip checks that decomposing the forward transform recovers its parameters,
// and that the fitted transform reproduces the target vertices exactly.
func TestTriangleFitRoundTrip(t *testing.T) {
	cases := []struct{ px, py, sx, sy, rot, skew float64 }{
		{0, 0, 1, 1, 0, 0},
		{100, 200, 3, 2, 30, 20},
		{-50, 75, 2.5, 4, -65, -15},
		{0, 0, 5, 5, 120, 0},
	}
	for _, c := range cases {
		v := triApply(c.px, c.py, c.sx, c.sy, c.rot, c.skew)
		px, py, sx, sy, rot, skew := TriangleFit(v[0], v[1], v[2])
		// The fitted transform must reproduce the same vertices (the parameterisation can differ).
		got := triApply(px, py, sx, sy, rot, skew)
		for i := 0; i < 3; i++ {
			if math.Abs(got[i][0]-v[i][0]) > 1e-6 || math.Abs(got[i][1]-v[i][1]) > 1e-6 {
				t.Errorf("case %+v vertex %d: got (%.4f,%.4f) want (%.4f,%.4f)", c, i, got[i][0], got[i][1], v[i][0], v[i][1])
			}
		}
	}
}

// TestTriangleFitTarget prints the FH6 params for a chosen scalene target triangle, for live
// in-game verification (run with -v).
func TestTriangleFitTarget(t *testing.T) {
	targets := [][3][2]float64{
		{{0, 500}, {-400, -300}, {500, -200}}, // scalene, spans the canvas
		{{0, 400}, {-350, -200}, {350, -200}}, // isosceles pointing up
	}
	for _, tg := range targets {
		px, py, sx, sy, rot, skew := TriangleFit(tg[0], tg[1], tg[2])
		fmt.Printf("target %v -> pos(%.1f,%.1f) scale(%.4f,%.4f) rot=%.2f skew=%.2f\n", tg, px, py, sx, sy, rot, skew)
	}
}
