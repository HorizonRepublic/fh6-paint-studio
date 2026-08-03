package engine

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// A target manufactured as an exact rect+glow composite must be recovered by the joint solve: the
// returned colours match the ones baked in, and the exact ΔSSE wipes (almost) the whole footprint
// residual. The greedy per-layer solve CANNOT do this (the base colour alone scores positive) —
// this is the property the claim stacks rely on.
func TestSolveStackRecoversCompositeColours(t *testing.T) {
	w, h := 80, 80
	base := model.Candidate{Kind: model.KindRectangle, Color: model.RGBA{R: 0.8, G: 0.2, B: 0.1, A: 1},
		P: [6]float32{40, 40, 30, 24, 15, 0}}
	glow := model.Candidate{Kind: model.KindGlow, Color: model.RGBA{R: 0.1, G: 0.3, B: 0.9, A: 1},
		P: [6]float32{40, 40, 26, 20, 15, 0}}

	canvas := make([]float32, w*h*4)
	target := make([]float32, w*h*4)
	weight := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		weight[i] = 1
		canvas[i*4], canvas[i*4+1], canvas[i*4+2], canvas[i*4+3] = 0.5, 0.5, 0.5, 1
	}
	copy(target, canvas)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := (y*w + x) * 4
			if raster.Inside(base.Kind, base.P, x, y) {
				target[p], target[p+1], target[p+2] = base.Color.R, base.Color.G, base.Color.B
			}
			a := float32(raster.Coverage(glow.Kind, glow.P, x, y)) * glow.Color.A
			if a > 0 {
				target[p] = target[p]*(1-a) + glow.Color.R*a
				target[p+1] = target[p+1]*(1-a) + glow.Color.G*a
				target[p+2] = target[p+2]*(1-a) + glow.Color.B*a
			}
		}
	}

	layers := []model.Candidate{
		{Kind: base.Kind, P: base.P, Color: model.RGBA{A: 1}},
		{Kind: glow.Kind, P: glow.P, Color: model.RGBA{A: 1}},
	}
	cols, delta, ok := solveStack(canvas, target, weight, w, h, layers, 0, nil, 0)
	if !ok {
		t.Fatal("solve failed on a well-posed stack")
	}
	wantCols := []model.RGBA{base.Color, glow.Color}
	for k := range cols {
		if math.Abs(float64(cols[k].R-wantCols[k].R)) > 0.02 ||
			math.Abs(float64(cols[k].G-wantCols[k].G)) > 0.02 ||
			math.Abs(float64(cols[k].B-wantCols[k].B)) > 0.02 {
			t.Errorf("layer %d colour = %+v, want %+v", k, cols[k], wantCols[k])
		}
	}

	var footprint float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !raster.Inside(base.Kind, base.P, x, y) && raster.Coverage(glow.Kind, glow.P, x, y) <= 0 {
				continue
			}
			p := (y*w + x) * 4
			for c := 0; c < 3; c++ {
				d := float64(target[p+c] - canvas[p+c])
				footprint += d * d
			}
		}
	}
	if delta > -0.95*footprint {
		t.Fatalf("joint ΔSSE %.2f should wipe ≥95%% of the footprint residual %.2f", delta, footprint)
	}

	// Sample-strided solve must land on the same colours (ratios are stride-invariant).
	colsS, _, okS := solveStack(canvas, target, weight, w, h, layers, 512, nil, 0)
	if !okS {
		t.Fatal("strided solve failed")
	}
	for k := range colsS {
		if math.Abs(float64(colsS[k].R-cols[k].R)) > 0.05 || math.Abs(float64(colsS[k].B-cols[k].B)) > 0.05 {
			t.Errorf("strided layer %d colour drifted: %+v vs %+v", k, colsS[k], cols[k])
		}
	}
}

// Two identical layers have colinear coverages — the solver must refuse instead of returning an
// arbitrary split.
func TestSolveStackRejectsDegenerate(t *testing.T) {
	w, h := 32, 32
	canvas := make([]float32, w*h*4)
	target := make([]float32, w*h*4)
	weight := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		weight[i] = 1
		canvas[i*4+3], target[i*4+3] = 1, 1
		target[i*4] = 0.7
	}
	l := model.Candidate{Kind: model.KindRectangle, Color: model.RGBA{A: 1}, P: [6]float32{16, 16, 10, 8, 0, 0}}
	if _, _, ok := solveStack(canvas, target, weight, w, h, []model.Candidate{l, l}, 0, nil, 0); ok {
		t.Fatal("degenerate stack should not solve")
	}
}
