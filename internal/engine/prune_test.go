package engine

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestPruneOccludedRemovesHidden(t *testing.T) {
	w, h := 20, 20
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, 20, 20}, Color: []int{0, 0, 0, 255}},           // bg
		{Type: model.TypeRotatedEllipse, Data: []float64{10, 10, 2, 2, 0}, Color: []int{255, 0, 0, 255}}, // small, will be hidden
		{Type: model.TypeRotatedEllipse, Data: []float64{10, 10, 9, 9, 0}, Color: []int{0, 255, 0, 255}}, // big opaque, covers small
	}
	out := pruneOccluded(shapes, w, h)
	if len(out) != 2 {
		t.Fatalf("expected 2 shapes after prune (bg + big), got %d", len(out))
	}
	if out[0].Type != model.TypeRectangle {
		t.Fatalf("background should be kept first, got type %d", out[0].Type)
	}
	if out[1].Color[1] != 255 {
		t.Fatalf("the big green ellipse should survive, got %+v", out[1])
	}
}

func TestPruneKeepsVisible(t *testing.T) {
	w, h := 20, 20
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, 20, 20}, Color: []int{0, 0, 0, 255}},
		{Type: model.TypeRotatedEllipse, Data: []float64{5, 5, 3, 3, 0}, Color: []int{255, 0, 0, 255}},   // left
		{Type: model.TypeRotatedEllipse, Data: []float64{15, 15, 3, 3, 0}, Color: []int{0, 255, 0, 255}}, // right, disjoint
	}
	if out := pruneOccluded(shapes, w, h); len(out) != 3 {
		t.Fatalf("disjoint visible shapes should all be kept, got %d", len(out))
	}
}
