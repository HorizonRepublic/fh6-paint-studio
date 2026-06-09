package inject

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

// TestDropInvisible verifies the cutout/transparent BACKGROUND rectangle (alpha 0) is dropped — it would
// otherwise inject as a faint grey box (the editor's ~0.78% alpha floor) around a transparent-bg logo —
// while opaque backgrounds and semi-transparent shapes are kept.
func TestDropInvisible(t *testing.T) {
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Color: []int{255, 255, 255, 0}},     // transparent cutout background -> DROP
		{Type: model.TypeRotatedEllipse, Color: []int{10, 20, 30, 255}}, // opaque shape -> keep
		{Type: model.TypeTriangle, Color: []int{40, 50, 60, 120}},       // semi-transparent shape -> keep
		{Type: model.TypeRectangle, Color: []int{0, 0, 0, 255}},         // opaque background -> keep
	}
	got := dropInvisible(shapes)
	if len(got) != 3 {
		t.Fatalf("dropInvisible kept %d shapes, want 3", len(got))
	}
	for _, s := range got {
		if len(s.Color) >= 4 && s.Color[3] == 0 {
			t.Fatalf("dropInvisible kept a fully-transparent shape: %+v", s)
		}
	}
}
