package inject

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

// Gradient shapes map to their in-game words and use the gradient scale base (66), not the circle's 64.
func TestGradientShapeToLayer(t *testing.T) {
	cm := CanvasMap{W: 200, H: 200, K: 1} // K=1 -> editor units == pixels
	cases := []struct {
		typ  int
		word uint16
	}{
		{model.TypeGradGlow, WordGradGlow},
		{model.TypeGradDisk, WordGradDisk},
	}
	for _, tc := range cases {
		s := model.Shape{Type: tc.typ, Data: []float64{100, 100, 66, 33, 0}, Color: []int{10, 20, 30, 200}}
		lw, ok := ShapeToLayer(s, cm)
		if !ok {
			t.Fatalf("type %d: ShapeToLayer not ok", tc.typ)
		}
		if lw.Word != tc.word {
			t.Errorf("type %d word = 0x%04x, want 0x%04x", tc.typ, lw.Word, tc.word)
		}
		// rx=66 with K=1 -> SX = 66/GradScaleBase = 1 (a circle's base 64 would give ~1.031).
		if math.Abs(float64(lw.SX)-1) > 1e-4 {
			t.Errorf("type %d SX = %v, want 1 (GradScaleBase=66)", tc.typ, lw.SX)
		}
		if math.Abs(float64(lw.SY)-0.5) > 1e-4 {
			t.Errorf("type %d SY = %v, want 0.5", tc.typ, lw.SY)
		}
		if w, ok := wordForType(tc.typ, model.ParamsFromShape(s)); !ok || w != tc.word {
			t.Errorf("type %d wordForType = 0x%04x ok=%v, want 0x%04x", tc.typ, w, ok, tc.word)
		}
	}
}
