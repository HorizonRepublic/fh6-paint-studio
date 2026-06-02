package engine

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/raster"
)

// TestTriangleSDFGradFD verifies the analytic gradient of triangleSDFGrad against
// central finite differences at points clearly nearest one edge (away from the
// boundary band and the medial axis, where the active edge / sign is ambiguous).
func TestTriangleSDFGradFD(t *testing.T) {
	tri := [6]float64{20, 20, 80, 30, 40, 90}
	const h = 1e-4
	pts := [][2]float64{
		{45, 45}, {35, 35}, {60, 45}, {45, 70}, // inside
		{8, 8}, {95, 95}, {50, 5}, {12, 60}, {88, 60}, {45, 98}, // outside
	}
	sdfAt := func(P [6]float64, x, y float64) float64 { s, _ := triangleSDFGrad(P, x, y); return s }
	for _, pt := range pts {
		px, py := pt[0], pt[1]
		sdf, g := triangleSDFGrad(tri, px, py)
		if math.Abs(sdf) < 1.0 { // skip the boundary band (sign ill-defined)
			continue
		}
		for k := 0; k < 6; k++ {
			pp, pm := tri, tri
			pp[k] += h
			pm[k] -= h
			fd := (sdfAt(pp, px, py) - sdfAt(pm, px, py)) / (2 * h)
			if math.Abs(fd-g[k]) > 2e-2 {
				t.Errorf("pt(%.0f,%.0f) param %d: analytic %.5f vs FD %.5f (sdf=%.3f)", px, py, k, g[k], fd, sdf)
			}
		}
	}
}

// TestTriangleSDFSignMatchesInside checks the soft sdf sign agrees with the hard
// raster.TriangleInside everywhere except a 1px boundary band — so soft coverage
// (sigmoid(-sdf/tau)) converges to the exact hard triangle the game rasterizes.
func TestTriangleSDFSignMatchesInside(t *testing.T) {
	tris := [][6]float64{
		{20, 20, 80, 30, 40, 90}, // generic
		{40, 90, 80, 30, 20, 20}, // reversed winding (same shape)
		{30, 25, 70, 25, 50, 80}, // isosceles
	}
	for ti, tri := range tris {
		var fp [6]float32
		for i := 0; i < 6; i++ {
			fp[i] = float32(tri[i])
		}
		mism := 0
		for y := 0; y < 100; y++ {
			for x := 0; x < 100; x++ {
				sdf, _ := triangleSDFGrad(tri, float64(x)+0.5, float64(y)+0.5)
				if math.Abs(sdf) < 1.2 { // skip boundary band
					continue
				}
				if (sdf < 0) != raster.TriangleInside(fp, x, y) {
					mism++
				}
			}
		}
		if mism > 0 {
			t.Errorf("tri %d: %d sdf-sign vs TriangleInside mismatches outside the boundary band", ti, mism)
		}
	}
}
