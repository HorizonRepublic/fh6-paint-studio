package cpu

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

func totalSSE(c *CPU) float64 {
	var s float64
	for i := 0; i < c.w*c.h*4; i++ {
		d := float64(c.target[i] - c.canvas[i])
		s += d * d
	}
	return s
}

func TestApplyReducesError(t *testing.T) {
	c := newSolid(20, 20, 1, 0, 0)
	before := totalSSE(c)
	cand := model.Candidate{Kind: model.KindEllipse, P: [6]float32{10, 10, 12, 12, 0, 0}, Color: model.RGBA{A: 1}}
	res, _ := c.Evaluate([]model.Candidate{cand})
	cand.Color = res[0].Color
	if err := c.Apply(cand); err != nil {
		t.Fatal(err)
	}
	if after := totalSSE(c); after >= before {
		t.Fatalf("error did not drop: before=%v after=%v", before, after)
	}
}

func TestErrorGridDims(t *testing.T) {
	c := newSolid(20, 20, 1, 0, 0)
	g, gw, gh, err := c.ErrorGrid()
	if err != nil || gw != 8 || gh != 8 || len(g) != 64 {
		t.Fatalf("grid dims gw=%d gh=%d len=%d err=%v", gw, gh, len(g), err)
	}
}
