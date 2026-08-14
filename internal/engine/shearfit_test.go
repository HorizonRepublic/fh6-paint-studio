package engine

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// The claim shearedWordFits rests on: a mask's ramp runs along deg − atan(skew), so a word placed on
// one frame can shade along a different direction. If that identity is off, the sheared placements
// are worse than useless — they aim the light the wrong way while looking plausible in the menu.

const rampWord = 2204 // the bank's linear Gouraud ramp

// rampAngleAt recovers the direction the coverage actually increases in, by sampling a small
// cross around a point and reading off the numerical gradient.
func rampAngleAt(kind model.ShapeKind, p [6]float32, x, y int) (float64, float64) {
	const d = 6
	cov := func(xx, yy int) float64 { return raster.Coverage(kind, p, xx, yy) }
	gx := (cov(x+d, y) - cov(x-d, y)) / (2 * d)
	gy := (cov(x, y+d) - cov(x, y-d)) / (2 * d)
	return math.Atan2(gy, gx) * 180 / math.Pi, math.Hypot(gx, gy)
}

func TestShearAimsTheRampOffTheFrame(t *testing.T) {
	kind, ok := model.MaskKind(rampWord)
	if !ok {
		t.Skipf("word %d not in the bank", rampWord)
	}
	// deg is where the footprint faces; want is where the shading should run. They are deliberately
	// far apart — that separation is the whole point of the shear.
	for _, tc := range []struct{ deg, want float64 }{
		{0, 30}, {0, -35}, {25, 70}, {70, 25}, {-40, 10}, {110, 60},
	} {
		k := math.Tan((tc.deg - tc.want) * math.Pi / 180)
		c, ok := maskShearFit(kind, 300, 300, 90+math.Abs(k)*60, 60, tc.deg, k)
		if !ok {
			t.Fatalf("deg=%g want=%g: no fit", tc.deg, tc.want)
		}
		got, mag := rampAngleAt(kind, c.P, 300, 300)
		if mag <= 1e-6 {
			t.Errorf("deg=%g want=%g: the ramp is flat at the centre (|grad|=%g)", tc.deg, tc.want, mag)
			continue
		}
		// The ramp is a signed direction; the fit may point it either way along the axis, and
		// smoothMenu offers both by flipping deg. Only the axis is asserted here.
		if diff := axisDiff(got, tc.want); diff > 3 {
			t.Errorf("deg=%g skew=%.3f: ramp runs at %.1f°, wanted the %.1f° axis (off by %.1f°)",
				tc.deg, k, got, tc.want, diff)
		}
	}
}

// TestShearFitCoversTheRegion checks the other half of the placement: the widened parallelogram has
// to still contain the region's moment box. A shear that aims the ramp correctly but slides the
// footprint off the region would show up as a win in no measurement at all.
func TestShearFitCoversTheRegion(t *testing.T) {
	kind, ok := model.MaskKind(rampWord)
	if !ok {
		t.Skipf("word %d not in the bank", rampWord)
	}
	const cx, cy, hu, hv = 300.0, 300.0, 90.0, 60.0
	for _, deg := range []float64{0, 25, -40, 70} {
		for _, k := range []float64{0.3, 0.8, 1.5} {
			c, ok := maskShearFit(kind, cx, cy, hu+math.Abs(k)*hv, hv, deg, k)
			if !ok {
				t.Fatalf("deg=%g k=%g: no fit", deg, k)
			}
			th := deg * math.Pi / 180
			cs, sn := math.Cos(th), math.Sin(th)
			for _, u := range []float64{-0.95, 0, 0.95} {
				for _, v := range []float64{-0.95, 0, 0.95} {
					// A corner of the region's moment box, in screen pixels.
					lx, ly := u*hu, v*hv
					x := int(cx + lx*cs - ly*sn)
					y := int(cy + lx*sn + ly*cs)
					if cov := raster.Coverage(kind, c.P, x, y); cov <= 0 {
						t.Errorf("deg=%g k=%g: moment-box point (%.2f,%.2f) is outside the footprint", deg, k, u, v)
					}
				}
			}
		}
	}
}

// axisDiff is the angle between two directions treated as unsigned axes, in degrees.
func axisDiff(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), 180)
	if d > 90 {
		d = 180 - d
	}
	return d
}
