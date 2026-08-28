package engine

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

// The background rect is not a shape recolorVisible may touch: every render rebuilds from
// initCanvas and applies shapes[1:], so a repaint of shapes[0] never reaches the score while
// RenderFH6 and the injector both read it. solveBackground owns that colour now.
func TestRecolorVisibleLeavesTheBackground(t *testing.T) {
	const w, h = 16, 16
	target := make([]float32, w*h*4)
	weight := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		target[i*4], target[i*4+1], target[i*4+2], target[i*4+3] = 0.9, 0.1, 0.1, 1
		weight[i] = 1
	}
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, w, h}, Color: []int{10, 20, 30, 255}},
		{Type: model.TypeRotatedEllipse, Data: []float64{8, 8, 4, 4, 0, 0}, Color: []int{200, 200, 200, 255}},
	}
	recolorVisible(shapes, target, weight, w, h, 0)

	if got := shapes[0].Color[:3]; got[0] != 10 || got[1] != 20 || got[2] != 30 {
		t.Errorf("background repainted to %v; want [10 20 30] untouched", got)
	}
	if shapes[1].Color[0] == 200 {
		t.Error("the ellipse was not recoloured; the pass did nothing at all")
	}
}
