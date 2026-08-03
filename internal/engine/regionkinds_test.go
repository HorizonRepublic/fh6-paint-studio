package engine

import (
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/model"
)

// hard=0 everywhere must force every candidate to ellipse; hard=1 must leave the pool mix intact.
func TestKindGateForcesEllipseInSmoothRegions(t *testing.T) {
	w, h := 40, 40
	grid := make([]float32, 16)
	for i := range grid {
		grid[i] = 1
	}
	s := NewErrorSampler(grid, 4, 4, w, h)
	rng := rand.New(rand.NewSource(1))
	kinds := []model.ShapeKind{model.KindTriangle, model.KindRectangle}

	flat := make([]float32, w*h) // hard = 0: DEEP-smooth — forced ellipse or the rimless glow swap
	glowTau, glowProb := resolveSmoothGlow(0, 0)
	kgSmooth := &kindGate{hard: flat, w: w, h: h, tau: glowTau, prob: glowProb}
	sawGlow := false
	for _, c := range RandomShapes(rng, w, h, 100, kinds, nil, s, 0, nil, false, 1, 0, nil, kgSmooth) {
		switch c.Kind {
		case model.KindEllipse:
		case model.KindGlow:
			sawGlow = true
		default:
			t.Fatalf("smooth region produced kind %v, want ellipse/glow", c.Kind)
		}
	}
	if glowProb > 0 && !sawGlow {
		t.Fatalf("deep-smooth region should swap some ellipses for glows (prob=%.2f)", glowProb)
	}

	ones := make([]float32, w*h)
	for i := range ones {
		ones[i] = 1
	}
	kgHard := &kindGate{hard: ones, w: w, h: h, tau: glowTau, prob: glowProb}
	sawTri, sawRect := false, false
	for _, c := range RandomShapes(rng, w, h, 200, kinds, nil, s, 0, nil, false, 1, 0, nil, kgHard) {
		switch c.Kind {
		case model.KindTriangle:
			sawTri = true
		case model.KindRectangle:
			sawRect = true
		case model.KindEllipse:
			t.Fatalf("hard region must never force ellipse")
		}
	}
	if !sawTri || !sawRect {
		t.Fatalf("hard region should keep the full pool (tri=%v rect=%v)", sawTri, sawRect)
	}
}

// nil gate must be transparent (existing behaviour).
func TestKindGateNilPassthrough(t *testing.T) {
	var kg *kindGate
	rng := rand.New(rand.NewSource(2))
	kinds := []model.ShapeKind{model.KindRectangle}
	cdf := buildKindCDF(kinds, nil)
	for i := 0; i < 20; i++ {
		if k := kg.pick(rng, 5, 5, kinds, cdf); k != model.KindRectangle {
			t.Fatalf("nil gate changed the pick: %v", k)
		}
	}
}
