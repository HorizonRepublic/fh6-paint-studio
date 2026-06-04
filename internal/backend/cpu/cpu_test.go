package cpu

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

// newSolid builds a w*h all-(r,g,b,1) target with an all-black canvas.
func newSolid(w, h int, r, g, b float32) *CPU {
	target := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		target[i*4+0], target[i*4+1], target[i*4+2], target[i*4+3] = r, g, b, 1
	}
	return New(target, w, h, 8)
}

func TestEvaluateRecoversOptimalColor(t *testing.T) {
	c := newSolid(20, 20, 1, 0, 0) // red target, black canvas
	cand := model.Candidate{Kind: model.KindEllipse, P: [6]float32{10, 10, 8, 8, 0, 0}, Color: model.RGBA{A: 1}}
	res, err := c.Evaluate([]model.Candidate{cand})
	if err != nil {
		t.Fatal(err)
	}
	got := res[0]
	if got.Color.R < 0.99 || got.Color.G > 0.01 || got.Color.B > 0.01 {
		t.Fatalf("optimal color = %+v, want ~red", got.Color)
	}
	if got.Score >= 0 {
		t.Fatalf("score = %v, want negative (improvement)", got.Score)
	}
}

func TestEvaluateRejectsEmptyEllipse(t *testing.T) {
	c := newSolid(20, 20, 1, 0, 0)
	// Ellipse fully off-canvas -> no covered pixels -> rejected score.
	cand := model.Candidate{Kind: model.KindEllipse, P: [6]float32{-100, -100, 2, 2, 0, 0}, Color: model.RGBA{A: 1}}
	res, _ := c.Evaluate([]model.Candidate{cand})
	if res[0].Score != rejected {
		t.Fatalf("score = %v, want rejected (%v) for off-canvas ellipse", res[0].Score, rejected)
	}
}
