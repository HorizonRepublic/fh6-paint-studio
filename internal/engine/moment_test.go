package engine

import (
	"math"
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/model"
)

// fillEllipse rasterizes a filled, rotated ellipse (semi-axes a,b, centre cx,cy,
// rotation degrees CCW from +x, y down) into an fw×fh weight field (1 inside, 0
// outside) — the synthetic residual blob the moment fit must recover.
func fillEllipse(fw, fh int, cx, cy, a, b, deg float64) []float32 {
	w := make([]float32, fw*fh)
	th := deg * math.Pi / 180
	cs, sn := math.Cos(th), math.Sin(th)
	for y := 0; y < fh; y++ {
		for x := 0; x < fw; x++ {
			dx := float64(x) - cx
			dy := float64(y) - cy
			u := dx*cs + dy*sn // into ellipse frame
			v := -dx*sn + dy*cs
			if (u*u)/(a*a)+(v*v)/(b*b) <= 1 {
				w[y*fw+x] = 1
			}
		}
	}
	return w
}

// angDiff180 is the acute angle between two orientations (mod 180, since an ellipse
// at θ and θ+180 are identical).
func angDiff180(a, b float64) float64 {
	d := math.Mod(a-b, 180)
	if d < 0 {
		d += 180
	}
	if d > 90 {
		d = 180 - d
	}
	return d
}

func TestMomentEllipseHorizontal(t *testing.T) {
	fw, fh := 61, 41
	w := fillEllipse(fw, fh, 30, 20, 20, 8, 0)
	cx, cy, rx, ry, th, ok := momentEllipse(w, fw, fh)
	if !ok {
		t.Fatal("ok=false on a valid blob")
	}
	if math.Abs(float64(cx)-30) > 1.5 || math.Abs(float64(cy)-20) > 1.5 {
		t.Errorf("centre (%.1f,%.1f) want ~(30,20)", cx, cy)
	}
	if math.Abs(float64(rx)-20) > 2 || math.Abs(float64(ry)-8) > 2 {
		t.Errorf("semi-axes (%.1f,%.1f) want ~(20,8)", rx, ry)
	}
	if angDiff180(float64(th), 0) > 5 {
		t.Errorf("theta %.1f want ~0", th)
	}
}

func TestMomentEllipseVerticalMajorIsRx(t *testing.T) {
	// Taller than wide: the MAJOR semi-axis (rx, along theta) must be the y-extent,
	// theta ~90.
	fw, fh := 41, 61
	w := fillEllipse(fw, fh, 20, 30, 8, 20, 0) // a<b -> tall
	_, _, rx, ry, th, ok := momentEllipse(w, fw, fh)
	if !ok {
		t.Fatal("ok=false")
	}
	if rx < ry {
		t.Errorf("rx (%.1f) should be the MAJOR axis >= ry (%.1f)", rx, ry)
	}
	if math.Abs(float64(rx)-20) > 2 || math.Abs(float64(ry)-8) > 2 {
		t.Errorf("semi-axes (%.1f,%.1f) want major~20 minor~8", rx, ry)
	}
	if angDiff180(float64(th), 90) > 5 {
		t.Errorf("theta %.1f want ~90", th)
	}
}

func TestMomentEllipseRotated45(t *testing.T) {
	fw, fh := 61, 61
	w := fillEllipse(fw, fh, 30, 30, 24, 8, 45)
	_, _, rx, ry, th, ok := momentEllipse(w, fw, fh)
	if !ok {
		t.Fatal("ok=false")
	}
	if math.Abs(float64(rx)-24) > 2.5 || math.Abs(float64(ry)-8) > 2.5 {
		t.Errorf("semi-axes (%.1f,%.1f) want ~(24,8)", rx, ry)
	}
	if angDiff180(float64(th), 45) > 6 {
		t.Errorf("theta %.1f want ~45", th)
	}
}

func TestMomentEllipseCircleIsIsotropic(t *testing.T) {
	fw, fh := 51, 51
	w := fillEllipse(fw, fh, 25, 25, 12, 12, 0)
	_, _, rx, ry, _, ok := momentEllipse(w, fw, fh)
	if !ok {
		t.Fatal("ok=false")
	}
	if math.Abs(float64(rx-ry)) > 1.5 {
		t.Errorf("circle should give rx~ry, got (%.1f,%.1f)", rx, ry)
	}
	if math.Abs(float64(rx)-12) > 2 {
		t.Errorf("radius %.1f want ~12", rx)
	}
}

func TestMomentEllipseEmptyFieldNotOK(t *testing.T) {
	w := make([]float32, 40*40)
	if _, _, _, _, _, ok := momentEllipse(w, 40, 40); ok {
		t.Error("ok=true on an all-zero field; want false")
	}
}

func TestMomentSeedFromGridMapsToPixels(t *testing.T) {
	// 40×40 grid over a 160×160 image (4 px/cell). A horizontal elliptical error blob
	// centred at cell (20,20), semi-axes (10,4) cells -> pixel centre ~82, semi-axes
	// ~40×16 px.
	gw, gh := 40, 40
	grid := fillEllipse(gw, gh, 20, 20, 10, 4, 0)
	cx, cy, rx, ry, _, ok := momentSeedFromGrid(grid, gw, gh, 160, 160, 82, 82, 60)
	if !ok {
		t.Fatal("ok=false on a valid grid blob")
	}
	if math.Abs(float64(cx)-82) > 6 || math.Abs(float64(cy)-82) > 6 {
		t.Errorf("centre (%.1f,%.1f) want ~(82,82)", cx, cy)
	}
	if rx <= ry {
		t.Errorf("horizontal blob: rx (%.1f) should exceed ry (%.1f)", rx, ry)
	}
	if math.Abs(float64(rx)-40) > 8 || math.Abs(float64(ry)-16) > 8 {
		t.Errorf("semi-axes (%.1f,%.1f) px want ~(40,16)", rx, ry)
	}
}

func TestMomentSeedFromGridNonSquareImage(t *testing.T) {
	// The error grid is gridSize×gridSize whatever the target's aspect, so on a 2000×1000
	// image a 64×64 grid has 31.25×15.625 px cells. A ROUND blob in grid space is a 2:1
	// ellipse on the image, and a 45° one is not at 45° on the image.
	const gw, gh, imgW, imgH = 64, 64, 2000, 1000
	defer func(v bool) { gridAnisoOn = v }(gridAnisoOn)

	// What ships (pin off): a round grid blob comes back round, at the average cell scale.
	gridAnisoOn = false
	flat := fillEllipse(gw, gh, 32, 32, 6, 6, 0)
	if _, _, rx, ry, _, ok := momentSeedFromGrid(flat, gw, gh, imgW, imgH, 1000, 500, 400); !ok || rx != ry {
		t.Errorf("pin off: (%.1f,%.1f) ok=%v want a round seed", rx, ry, ok)
	}

	gridAnisoOn = true
	round := fillEllipse(gw, gh, 32, 32, 6, 6, 0)
	_, _, rx, ry, _, ok := momentSeedFromGrid(round, gw, gh, imgW, imgH, 1000, 500, 400)
	if !ok {
		t.Fatal("ok=false on a valid grid blob")
	}
	if a := float64(rx / ry); math.Abs(a-2) > 0.15 {
		t.Errorf("round grid blob: pixel aspect %.2f (%.1f×%.1f) want ~2.00", a, rx, ry)
	}

	// Grid covariance of a 45° blob with semi-axes 10,3 maps through S=diag(31.25,15.625)
	// to θ = ½·atan2(2·5554, 13305−3326) ≈ 24°.
	tilt := fillEllipse(gw, gh, 32, 32, 10, 3, 45)
	_, _, _, _, th, ok := momentSeedFromGrid(tilt, gw, gh, imgW, imgH, 1000, 500, 400)
	if !ok {
		t.Fatal("ok=false on a valid tilted grid blob")
	}
	if th < 16 || th > 32 {
		t.Errorf("45° grid blob maps to %.1f° want ~24° (an isotropic remap leaves it at 45°)", th)
	}
}

func TestMomentPoolSeedIsExactEllipse(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	kinds := []model.ShapeKind{model.KindEllipse, model.KindTriangle, model.KindRectangle}
	cdf := buildKindCDF(kinds, nil)
	pool := momentPool(rng, 50, 60, 20, 8, 30, 100, kinds, cdf, 10, 200, 200, false, 0, nil)
	if len(pool) != 10 {
		t.Fatalf("pool size %d want 10", len(pool))
	}
	s := pool[0]
	if s.Kind != model.KindEllipse {
		t.Errorf("candidate 0 kind %v want ellipse", s.Kind)
	}
	if s.P[0] != 50 || s.P[1] != 60 || s.P[2] != 20 || s.P[3] != 8 || s.P[4] != 30 {
		t.Errorf("candidate 0 P=%v want the exact seed [50 60 20 8 30 0]", s.P)
	}
	if s.Color.A != 1 {
		t.Errorf("seed alpha %.2f want 1 (opaque)", s.Color.A)
	}
}

func TestMomentEllipseWeightedCentroid(t *testing.T) {
	// Two equal point masses -> centroid at their midpoint.
	fw, fh := 21, 21
	w := make([]float32, fw*fh)
	w[10*fw+4] = 1
	w[10*fw+16] = 1
	cx, cy, _, _, _, ok := momentEllipse(w, fw, fh)
	if !ok {
		t.Fatal("ok=false")
	}
	if math.Abs(float64(cx)-10) > 0.01 || math.Abs(float64(cy)-10) > 0.01 {
		t.Errorf("centroid (%.2f,%.2f) want (10,10)", cx, cy)
	}
}
