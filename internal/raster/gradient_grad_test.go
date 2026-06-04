package raster

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

// TestGaussianCovGradFD verifies the analytic glow coverage gradient against central finite differences
// of FalloffGlow(ellipseNormRadius(·)) — the function the forward render actually composites. If the math
// is wrong, gaussian geometry training (#7) would optimise toward noise.
func TestGaussianCovGradFD(t *testing.T) {
	// A few off-centre pixels and non-trivial geometry (rotated, anisotropic) inside the footprint.
	cases := []struct {
		p    [6]float32
		x, y int
	}{
		{[6]float32{20, 15, 12, 8, 25, 0}, 24, 18},
		{[6]float32{30, 30, 18, 14, -40, 0}, 22, 37},
		{[6]float32{50, 40, 25, 25, 0, 0}, 58, 47},
	}
	for ci, tc := range cases {
		cov, g := GaussianCovGrad(model.KindGlow, tc.p, tc.x, tc.y)
		if cov <= 0 {
			t.Fatalf("case %d: pixel outside footprint (cov=%.4f) — pick an interior pixel", ci, cov)
		}
		const h = 1e-2
		for i := 0; i < 5; i++ {
			pp, pm := tc.p, tc.p
			pp[i] += h
			pm[i] -= h
			cp := FalloffGlow(ellipseNormRadius(pp, tc.x, tc.y))
			cm := FalloffGlow(ellipseNormRadius(pm, tc.x, tc.y))
			fd := (cp - cm) / (2 * h)
			if math.Abs(fd-g[i]) > 1e-2*(1+math.Abs(fd)) {
				t.Errorf("case %d param %d: analytic %.6f vs FD %.6f", ci, i, g[i], fd)
			}
		}
	}
}
