package raster

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestFalloffGlowShape(t *testing.T) {
	if got := FalloffGlow(0); math.Abs(got-glowPeak) > 1e-9 {
		t.Errorf("FalloffGlow(0) = %v, want peak %v", got, glowPeak)
	}
	if got := FalloffGlow(-0.5); got != glowPeak {
		t.Errorf("FalloffGlow(t<0) = %v, want peak %v", got, glowPeak)
	}
	if got := FalloffGlow(1); got != 0 {
		t.Errorf("FalloffGlow(1) = %v, want 0", got)
	}
	if got := FalloffGlow(1.5); got != 0 {
		t.Errorf("FalloffGlow(t>1) = %v, want 0", got)
	}
	// Strictly decreasing across the support, and always ≤ peak.
	prev := math.Inf(1)
	for i := 0; i <= 20; i++ {
		ti := float64(i) / 20
		v := FalloffGlow(ti)
		if v > glowPeak+1e-9 {
			t.Errorf("FalloffGlow(%v) = %v exceeds peak", ti, v)
		}
		if v > prev+1e-9 {
			t.Errorf("FalloffGlow not monotone: f(%v)=%v > prev %v", ti, v, prev)
		}
		prev = v
	}
}

func TestFalloffDiskShape(t *testing.T) {
	if got := FalloffDisk(0); got != 1 {
		t.Errorf("FalloffDisk(0) = %v, want 1 (opaque core)", got)
	}
	if got := FalloffDisk(diskCore); got != 1 {
		t.Errorf("FalloffDisk(core edge) = %v, want 1", got)
	}
	if got := FalloffDisk(1); got != 0 {
		t.Errorf("FalloffDisk(1) = %v, want 0", got)
	}
	// Rim falls monotonically from 1 (at the core edge) to 0 (at the footprint edge).
	prev := 1.0
	for i := 0; i <= 20; i++ {
		ti := diskCore + (1-diskCore)*float64(i)/20
		v := FalloffDisk(ti)
		if v > prev+1e-9 {
			t.Errorf("FalloffDisk not monotone in rim: f(%v)=%v > prev %v", ti, v, prev)
		}
		prev = v
	}
}

// A unit-circle glow at the origin: centre coverage ≈ peak, edge coverage ≈ 0, outside = 0.
func TestCoverageGlowRadial(t *testing.T) {
	// rx=ry=100 at (100,100); a pixel at the centre vs near the edge.
	p := [6]float32{100, 100, 100, 100, 0, 0}
	center := Coverage(model.KindGlow, p, 100, 100)
	if math.Abs(center-glowPeak) > 0.05 {
		t.Errorf("glow centre coverage = %v, want ≈ %v", center, glowPeak)
	}
	edge := Coverage(model.KindGlow, p, 198, 100) // ~t≈0.98
	if edge < 0 || edge > 0.1 {
		t.Errorf("glow near-edge coverage = %v, want small", edge)
	}
	outside := Coverage(model.KindGlow, p, 250, 100) // t>1
	if outside != 0 {
		t.Errorf("glow outside coverage = %v, want 0", outside)
	}
	if center <= edge {
		t.Errorf("glow coverage should decay outward: centre %v edge %v", center, edge)
	}
}

// KindDisk has an opaque core then a feathered rim.
func TestCoverageDiskCore(t *testing.T) {
	p := [6]float32{100, 100, 100, 100, 0, 0}
	if c := Coverage(model.KindDisk, p, 100, 100); c != 1 {
		t.Errorf("disk centre coverage = %v, want 1 (opaque)", c)
	}
	if c := Coverage(model.KindDisk, p, 130, 100); c != 1 { // t=0.30 < core 0.40
		t.Errorf("disk in-core coverage = %v, want 1", c)
	}
	if c := Coverage(model.KindDisk, p, 250, 100); c != 0 { // outside
		t.Errorf("disk outside coverage = %v, want 0", c)
	}
}

// Hard kinds return a binary 1/0 from Coverage so the backend can treat every kind uniformly.
func TestCoverageHardBinary(t *testing.T) {
	p := [6]float32{100, 100, 50, 50, 0, 0}
	if c := Coverage(model.KindEllipse, p, 100, 100); c != 1 {
		t.Errorf("ellipse inside Coverage = %v, want 1", c)
	}
	if c := Coverage(model.KindEllipse, p, 100, 200); c != 0 {
		t.Errorf("ellipse outside Coverage = %v, want 0", c)
	}
}

func TestIsGradient(t *testing.T) {
	for _, k := range []model.ShapeKind{model.KindGlow, model.KindDisk} {
		if !IsGradient(k) {
			t.Errorf("IsGradient(%v) = false, want true", k)
		}
	}
	for _, k := range []model.ShapeKind{model.KindEllipse, model.KindRectangle, model.KindTriangle, model.KindLine} {
		if IsGradient(k) {
			t.Errorf("IsGradient(%v) = true, want false", k)
		}
	}
}
