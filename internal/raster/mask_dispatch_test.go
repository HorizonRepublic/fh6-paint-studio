package raster

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

// These exercise the full dispatch: KindMask routed through IsGradient/Coverage/BBox using the embedded
// bank (maskbank registers the words at init when raster imports it).

func TestMaskDispatchCircleGolden(t *testing.T) {
	k, ok := model.MaskKind(0x0066) // circle word
	if !ok {
		t.Fatal("circle word 0x0066 not registered — is the maskbank wired into raster?")
	}
	if !IsGradient(k) {
		t.Error("IsGradient(maskKind) should be true (soft coverage)")
	}
	p := [6]float32{100, 100, 100, 100, 0, 0} // footprint 100px, centred
	if c := Coverage(k, p, 100, 100); c < 0.9 {
		t.Errorf("centre coverage %.3f want >0.9", c)
	}
	if c := Coverage(k, p, 185, 100); c > 0.05 {
		t.Errorf("far-outside coverage %.3f want ~0", c)
	}
	// radial symmetry near the silhouette edge (r≈48): four cardinal samples within AA tolerance.
	e := []float64{
		Coverage(k, p, 148, 100), Coverage(k, p, 52, 100),
		Coverage(k, p, 100, 148), Coverage(k, p, 100, 52),
	}
	mn, mx := e[0], e[0]
	for _, v := range e {
		mn, mx = math.Min(mn, v), math.Max(mx, v)
	}
	if mx-mn > 0.2 {
		t.Errorf("circle not radially symmetric: %v (spread %.3f)", e, mx-mn)
	}
	// BBox ≈ the footprint box [50,50]..[150,150].
	x0, y0, x1, y1 := BBox(k, p, 400, 400)
	if x0 < 46 || x0 > 54 || y0 < 46 || y0 > 54 || x1 < 146 || x1 > 154 || y1 < 146 || y1 > 154 {
		t.Errorf("bbox [%d,%d]..[%d,%d] want ~[50,50]..[150,150]", x0, y0, x1, y1)
	}
}

// arc-90 is a quarter shape — its coverage must be strongly asymmetric across the footprint quadrants
// (a guard that masks aren't accidentally symmetrised or sampled with a flipped/centred UV).
func TestMaskDispatchArc90Asymmetric(t *testing.T) {
	k, ok := model.MaskKind(0x089b)
	if !ok {
		t.Fatal("arc-90 word 0x089b not registered")
	}
	p := [6]float32{100, 100, 54, 54, 0, 0}
	q := func(x0, x1, y0, y1 int) float64 {
		var s float64
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				s += Coverage(k, p, x, y)
			}
		}
		return s
	}
	qs := []float64{q(74, 100, 74, 100), q(100, 127, 74, 100), q(74, 100, 100, 127), q(100, 127, 100, 127)}
	mn, mx := qs[0], qs[0]
	for _, v := range qs {
		mn, mx = math.Min(mn, v), math.Max(mx, v)
	}
	if mx < 3*mn+1 {
		t.Errorf("arc-90 quadrants too uniform (expected asymmetric): %v", qs)
	}
}
