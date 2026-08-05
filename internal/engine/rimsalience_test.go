package engine

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

// A synthetic frame where the target is perfectly smooth and the reconstruction draws one hard disc:
// the disc's own outline is the only false contour in the picture, so it must be the only shape
// carrying debt, and a shape sitting elsewhere on the same smooth ground must carry none.
func TestRimDebtScoresTheContourNotTheArea(t *testing.T) {
	const w, h = 64, 64
	target := make([]float32, w*h) // uniformly smooth
	recon := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-20), float64(y-20)
			if math.Hypot(dx, dy) <= 9 {
				recon[y*w+x] = 0.8
			}
		}
	}
	shapes := []model.Shape{
		{Type: 1, Data: []float64{0, 0, w, h}},
		{Type: model.TypeRotatedEllipse, Data: []float64{20, 20, 9, 9, 0}}, // draws the contour
		{Type: model.TypeRotatedEllipse, Data: []float64{46, 46, 9, 9, 0}}, // sits on untouched ground
	}
	debt := shapeRimDebt(shapes, recon, target, w, h)
	if debt[1] <= 0 {
		t.Fatalf("the shape whose outline is the false contour scored %v, want > 0", debt[1])
	}
	if debt[2] != 0 {
		t.Errorf("a shape drawing nothing scored %v, want 0", debt[2])
	}
}

// Where the TARGET already has the edge, the shape is doing its job and must not be blamed for it —
// this is the restriction that makes the measure something other than an edge detector.
func TestRimDebtIgnoresContoursThePictureAlreadyHas(t *testing.T) {
	const w, h = 64, 64
	target := make([]float32, w*h)
	recon := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x-32), float64(y-32)
			if math.Hypot(dx, dy) <= 12 {
				target[y*w+x] = 0.8
				recon[y*w+x] = 0.8
			}
		}
	}
	shapes := []model.Shape{
		{Type: 1, Data: []float64{0, 0, w, h}},
		{Type: model.TypeRotatedEllipse, Data: []float64{32, 32, 12, 12, 0}},
	}
	if d := shapeRimDebt(shapes, recon, target, w, h)[1]; d != 0 {
		t.Errorf("a shape tracing a real edge scored %v, want 0", d)
	}
}

// The background is never a candidate: it has no rim anyone can see and swapping it is meaningless.
func TestRimDebtNeverBlamesTheBackground(t *testing.T) {
	const w, h = 32, 32
	target := make([]float32, w*h)
	recon := make([]float32, w*h)
	for i := range recon {
		recon[i] = float32(i%7) / 7 // noisy reconstruction over a smooth target
	}
	shapes := []model.Shape{{Type: 1, Data: []float64{0, 0, w, h}}}
	if d := shapeRimDebt(shapes, recon, target, w, h); d[0] != 0 {
		t.Errorf("background scored %v, want 0", d[0])
	}
}
