package engine

import (
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestRandomShapesRespectsKinds(t *testing.T) {
	grid := make([]float32, 16)
	grid[5] = 1
	s := NewErrorSampler(grid, 4, 4, 40, 40)
	rng := rand.New(rand.NewSource(3))
	allowed := map[model.ShapeKind]bool{model.KindTriangle: true, model.KindRectangle: true}
	cands := RandomShapes(rng, 40, 40, 80, []model.ShapeKind{model.KindTriangle, model.KindRectangle}, nil, s, 0, nil, nil, 0, false, 1, 0, nil, nil)
	if len(cands) != 80 {
		t.Fatalf("got %d candidates, want 80", len(cands))
	}
	for _, c := range cands {
		if !allowed[c.Kind] {
			t.Fatalf("unexpected kind %d", c.Kind)
		}
	}
}

func TestRandomShapesInBounds(t *testing.T) {
	grid := make([]float32, 16) // total 0 -> uniform sampling
	s := NewErrorSampler(grid, 4, 4, 40, 40)
	rng := rand.New(rand.NewSource(4))
	cands := RandomShapes(rng, 40, 40, 100, []model.ShapeKind{model.KindEllipse, model.KindLine}, nil, s, 0.5, nil, nil, 0, false, 1, 0, nil, nil)
	for _, c := range cands {
		if c.P[0] < 0 || c.P[0] > 39 || c.P[1] < 0 || c.P[1] > 39 {
			t.Fatalf("primary point out of bounds: %+v", c.P)
		}
	}
}

func TestMutateShapeTriangleStaysInBounds(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	base := model.Candidate{Kind: model.KindTriangle, P: [6]float32{5, 5, 20, 5, 12, 30}, Color: model.RGBA{A: 1}}
	for _, m := range MutateShape(rng, base, 50, 40, 40, 8, 8, false, 1) {
		for j := 0; j < 6; j += 2 {
			if m.P[j] < 0 || m.P[j] > 39 || m.P[j+1] < 0 || m.P[j+1] > 39 {
				t.Fatalf("triangle vertex out of bounds: %+v", m.P)
			}
		}
	}
}

// TestAllowAlphaProducesSemiTransparent guards the semi-transparent path: with
// allowAlpha the generator must draw alpha in [alphaMin,1] (and actually vary it),
// and mutation must keep it in range; with allowAlpha=false every shape stays opaque.
func TestAllowAlphaProducesSemiTransparent(t *testing.T) {
	grid := make([]float32, 16)
	s := NewErrorSampler(grid, 4, 4, 40, 40)
	rng := rand.New(rand.NewSource(7))
	const alphaMin = 0.3
	sawBelow1 := false
	for _, c := range RandomShapes(rng, 40, 40, 200, []model.ShapeKind{model.KindEllipse}, nil, s, 0, nil, nil, 0, true, alphaMin, 0, nil, nil) {
		if c.Color.A < alphaMin-1e-6 || c.Color.A > 1+1e-6 {
			t.Fatalf("alpha %.3f out of [%.2f,1]", c.Color.A, alphaMin)
		}
		if c.Color.A < 1-1e-6 {
			sawBelow1 = true
		}
	}
	if !sawBelow1 {
		t.Fatal("allowAlpha never produced a semi-transparent shape")
	}
	// Opaque path: every shape alpha == 1.
	for _, c := range RandomShapes(rng, 40, 40, 50, []model.ShapeKind{model.KindEllipse}, nil, s, 0, nil, nil, 0, false, 1, 0, nil, nil) {
		if c.Color.A != 1 {
			t.Fatalf("opaque path produced alpha %.3f", c.Color.A)
		}
	}
	// Mutation keeps alpha in range.
	base := model.Candidate{Kind: model.KindEllipse, P: [6]float32{20, 20, 8, 6, 0}, Color: model.RGBA{A: 0.5}}
	for _, m := range MutateShape(rng, base, 100, 40, 40, 4, 4, true, alphaMin) {
		if m.Color.A < alphaMin-1e-6 || m.Color.A > 1+1e-6 {
			t.Fatalf("mutated alpha %.3f out of [%.2f,1]", m.Color.A, alphaMin)
		}
	}
}

func TestRandomEllipsesWrapperStillEllipse(t *testing.T) {
	grid := make([]float32, 16)
	s := NewErrorSampler(grid, 4, 4, 40, 40)
	rng := rand.New(rand.NewSource(6))
	for _, c := range RandomEllipses(rng, 40, 40, 20, s, 0) {
		if c.Kind != model.KindEllipse {
			t.Fatalf("RandomEllipses produced non-ellipse kind %d", c.Kind)
		}
	}
}
