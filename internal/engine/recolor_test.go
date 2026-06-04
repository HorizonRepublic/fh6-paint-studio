package engine

import (
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// TestRecolorFixesDrift: a big shape placed early gets a blended color, then a
// later shape covers part of it. Re-solve should repaint the big shape with the
// mean of only its still-visible pixels.
func TestRecolorFixesDrift(t *testing.T) {
	w, h := 4, 1
	// target: px0,px1 red ; px2,px3 blue
	target := []float32{
		1, 0, 0, 1,
		1, 0, 0, 1,
		0, 0, 1, 1,
		0, 0, 1, 1,
	}
	weight := []float32{1, 1, 1, 1}
	// TypeRotatedRectangle -> KindRectangle; P = [cx,cy,halfW,halfH,angle].
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, 4, 1}, Color: []int{0, 0, 0, 255}},                   // bg (owns nothing here)
		{Type: model.TypeRotatedRectangle, Data: []float64{2, 0.5, 2, 0.5, 0}, Color: []int{120, 0, 135, 255}}, // big: drifted, covers all 4
		{Type: model.TypeRotatedRectangle, Data: []float64{3, 0.5, 1, 0.5, 0}, Color: []int{0, 0, 255, 255}},   // top: covers px2,px3
	}
	recolorVisible(shapes, target, weight, w, h, 0)

	// Shape 1 now owns px0,px1 (px2,px3 taken by shape 2) -> mean(red,red) = red.
	if big := shapes[1].Color; big[0] < 250 || big[2] > 5 {
		t.Fatalf("drifted shape should be repainted ~red, got %v", big)
	}
	// Shape 2 owns px2,px3 -> blue, unchanged.
	if shapes[2].Color[2] < 250 || shapes[2].Color[0] > 5 {
		t.Fatalf("top shape should stay blue, got %v", shapes[2].Color)
	}
}

// TestRecolorNeverIncreasesError: on a real-ish setup, re-solving must not raise
// total weighted SSE of the final composite (monotonic safety).
func TestRecolorMonotonic(t *testing.T) {
	w, h := 8, 8
	target := make([]float32, w*h*4)
	weight := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		target[i*4] = float32(i%4) / 3 // varied reds
		target[i*4+3] = 1
		weight[i] = 1
	}
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, 8, 8}, Color: []int{120, 120, 120, 255}},
		{Type: model.TypeRotatedEllipse, Data: []float64{4, 4, 3, 3, 0}, Color: []int{200, 50, 50, 255}},
	}
	before := compositeWeightedSSE(shapes, target, weight, w, h)
	recolorVisible(shapes, target, weight, w, h, 0)
	after := compositeWeightedSSE(shapes, target, weight, w, h)
	if after > before+1e-6 {
		t.Fatalf("re-solve increased error: before=%v after=%v", before, after)
	}
}

// compositeWeightedSSE renders shapes opaquely top-of-stack and sums weighted SSE.
func compositeWeightedSSE(shapes []model.Shape, target, weight []float32, w, h int) float64 {
	canvas := make([]float32, w*h*4)
	for _, s := range shapes {
		c := shapeToCandidate(s)
		kind := c.Kind
		xMin, yMin, xMax, yMax := raster.BBox(kind, c.P, w, h)
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				if raster.Inside(kind, c.P, x, y) {
					p := (y*w + x) * 4
					canvas[p], canvas[p+1], canvas[p+2], canvas[p+3] = c.Color.R, c.Color.G, c.Color.B, 1
				}
			}
		}
	}
	var sse float64
	for i := 0; i < w*h; i++ {
		wt := float64(weight[i])
		for k := 0; k < 3; k++ {
			d := float64(target[i*4+k] - canvas[i*4+k])
			sse += wt * d * d
		}
	}
	return sse
}
