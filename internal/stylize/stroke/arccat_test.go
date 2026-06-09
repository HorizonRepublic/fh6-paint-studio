package stroke

import (
	"math"
	"testing"
)

func findArc(t *testing.T, word uint16) arcWord {
	t.Helper()
	for _, a := range arcCatalog() {
		if a.word == word {
			return a
		}
	}
	t.Fatalf("arc 0x%04x not in catalog", word)
	return arcWord{}
}

func TestArcCatalogMeasured(t *testing.T) {
	cat := arcCatalog()
	if len(cat) == 0 {
		t.Fatal("arc catalog empty — maskbank words not found?")
	}
	for _, a := range cat {
		t.Logf("word=0x%04x sweep=%.1f° radius=%.1f a=(%.1f,%.1f) b=(%.1f,%.1f) native=%.0fx%.0f",
			a.word, a.sweep/deg2rad, a.radius, a.a[0], a.a[1], a.b[0], a.b[1], a.nativeW, a.nativeH)
		if a.sweep <= 0 || a.sweep >= 2*math.Pi {
			t.Errorf("word 0x%04x: implausible sweep %.1f°", a.word, a.sweep/deg2rad)
		}
	}
}

// TestArcSweepRanges pins the measured spans against the calibration ground truth (the dictionary's own
// arc geometry), so a bad mask-bank regen or a circle-fit regression is caught.
func TestArcSweepRanges(t *testing.T) {
	for _, c := range []struct {
		word   uint16
		lo, hi float64
		name   string
	}{
		{0x08b7, 35, 75, "arc-shallow"},
		{0x0853, 45, 80, "gentlearc1"},
		{0x089b, 90, 125, "arc-90"},
		{0x08a7, 130, 170, "arc-180"},
		{0x089a, 160, 200, "arc-dome"},
	} {
		deg := findArc(t, c.word).sweep / deg2rad
		if deg < c.lo || deg > c.hi {
			t.Errorf("%s (0x%04x): sweep %.1f° outside [%.0f,%.0f]", c.name, c.word, deg, c.lo, c.hi)
		}
	}
}

func TestFitCircleRecoversKnownCircle(t *testing.T) {
	var pts [][2]float64
	cx, cy, r := 12.0, -5.0, 30.0
	for k := 0; k < 24; k++ {
		ang := float64(k) / 24 * 1.6 // a partial arc, not the full circle
		pts = append(pts, [2]float64{cx + r*math.Cos(ang), cy + r*math.Sin(ang)})
	}
	gx, gy, gr, ok := fitCircle(pts)
	if !ok {
		t.Fatal("fitCircle failed on a clean arc")
	}
	if math.Abs(gx-cx) > 0.1 || math.Abs(gy-cy) > 0.1 || math.Abs(gr-r) > 0.1 {
		t.Errorf("fit (%.2f,%.2f,r%.2f) != truth (%.2f,%.2f,r%.2f)", gx, gy, gr, cx, cy, r)
	}
}

func TestFitCircleRejectsCollinear(t *testing.T) {
	pts := [][2]float64{{0, 0}, {1, 1}, {2, 2}, {3, 3}, {4, 4}}
	if _, _, _, ok := fitCircle(pts); ok {
		t.Error("fitCircle should reject collinear points")
	}
}
