package engine

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestShapeContributions(t *testing.T) {
	w, h := 4, 1
	// target: px0,px1 red ; px2,px3 blue
	target := []float32{
		1, 0, 0, 1,
		1, 0, 0, 1,
		0, 0, 1, 1,
		0, 0, 1, 1,
	}
	weight := []float32{1, 1, 1, 1}
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, 4, 1}, Color: []int{255, 255, 255, 255}},               // bg white
		{Type: model.TypeRotatedRectangle, Data: []float64{0.5, 0.5, 0.5, 0.5, 0}, Color: []int{0, 255, 0, 255}}, // D: hidden under big (px0)
		{Type: model.TypeRotatedRectangle, Data: []float64{2, 0.5, 2, 0.5, 0}, Color: []int{255, 0, 0, 255}},     // big red: covers all
		{Type: model.TypeRotatedRectangle, Data: []float64{3, 0.5, 1, 0.5, 0}, Color: []int{0, 0, 255, 255}},     // top blue: covers px2,3
	}
	c := shapeContributions(shapes, target, weight, w, h, model.RGBA{R: 1, G: 1, B: 1, A: 1}, false)
	if c[1] != 0 {
		t.Fatalf("hidden shape D should contribute 0, got %v", c[1])
	}
	if c[2] <= 0 {
		t.Fatalf("big red shape should contribute >0, got %v", c[2])
	}
	if c[3] <= 0 {
		t.Fatalf("top blue shape should contribute >0, got %v", c[3])
	}
}

func TestPruneToBudgetKeepsUpToBudgetThenCaps(t *testing.T) {
	w, h := 4, 1
	target := []float32{1, 0, 0, 1, 1, 0, 0, 1, 0, 0, 1, 1, 0, 0, 1, 1}
	weight := []float32{1, 1, 1, 1}
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, 4, 1}, Color: []int{255, 255, 255, 255}},
		{Type: model.TypeRotatedRectangle, Data: []float64{0.5, 0.5, 0.5, 0.5, 0}, Color: []int{0, 255, 0, 255}}, // low-contribution
		{Type: model.TypeRotatedRectangle, Data: []float64{2, 0.5, 2, 0.5, 0}, Color: []int{255, 0, 0, 255}},
		{Type: model.TypeRotatedRectangle, Data: []float64{3, 0.5, 1, 0.5, 0}, Color: []int{0, 0, 255, 255}},
	}
	// Under budget: keep ALL non-bg shapes (polish refines even low-contribution ones; full occlusion
	// is pruneOccluded's job, not this function's). Was 3 before the over-prune fix.
	out := pruneToBudget(shapes, target, weight, w, h, 10, model.RGBA{R: 1, G: 1, B: 1, A: 1}, false)
	if len(out) != 4 {
		t.Fatalf("under budget should keep all 4 (bg + 3), got %d", len(out))
	}
	// Budget cap to 1 -> bg + the single highest-contribution shape.
	out = pruneToBudget(shapes, target, weight, w, h, 1, model.RGBA{R: 1, G: 1, B: 1, A: 1}, false)
	if len(out) != 2 {
		t.Fatalf("budget 1 -> expected 2 shapes (bg + best), got %d", len(out))
	}
}
