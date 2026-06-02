package cpu

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestWeightedOptimalColorFavorsHighWeight(t *testing.T) {
	w, h := 2, 1
	target := []float32{
		1, 0, 0, 1, // px0 red
		0, 0, 1, 1, // px1 blue
	}
	c := New(target, w, h, 1)
	c.SetWeight([]float32{9, 1}) // px0 weighted 9x

	// Rectangle covering both pixels, opaque.
	cand := model.Candidate{Kind: model.KindRectangle, P: [6]float32{1, 0.5, 2, 1, 0, 0}, Color: model.RGBA{A: 1}}
	res, _ := c.Evaluate([]model.Candidate{cand})
	if res[0].Color.R <= res[0].Color.B {
		t.Fatalf("weighted optimal color should lean red, got %+v", res[0].Color)
	}
	if res[0].Color.R < 0.85 {
		t.Fatalf("expected R~0.9 (9:1 weighting), got %v", res[0].Color.R)
	}
}

func TestSetWeightRejectsWrongLength(t *testing.T) {
	c := newSolid(4, 4, 1, 0, 0)
	c.SetWeight([]float32{1, 2, 3}) // wrong length -> ignored
	// uniform-weight eval still works (no panic, sensible color)
	cand := model.Candidate{Kind: model.KindEllipse, P: [6]float32{2, 2, 3, 3, 0, 0}, Color: model.RGBA{A: 1}}
	res, _ := c.Evaluate([]model.Candidate{cand})
	if res[0].Color.R < 0.9 {
		t.Fatalf("expected ~red after ignored bad weight, got %+v", res[0].Color)
	}
}
