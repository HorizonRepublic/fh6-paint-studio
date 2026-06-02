package cpu

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestEvaluateAndApplyTriangle(t *testing.T) {
	c := newSolid(20, 20, 0, 1, 0) // green target, black canvas
	cand := model.Candidate{Kind: model.KindTriangle, P: [6]float32{2, 2, 18, 2, 10, 18}, Color: model.RGBA{A: 1}}
	res, _ := c.Evaluate([]model.Candidate{cand})
	if res[0].Color.G < 0.99 || res[0].Color.R > 0.01 || res[0].Color.B > 0.01 {
		t.Fatalf("triangle optimal color = %+v, want ~green", res[0].Color)
	}
	if res[0].Score >= 0 {
		t.Fatalf("triangle score = %v, want negative (improvement)", res[0].Score)
	}
	before := totalSSE(c)
	cand.Color = res[0].Color
	if err := c.Apply(cand); err != nil {
		t.Fatal(err)
	}
	if after := totalSSE(c); after >= before {
		t.Fatalf("triangle apply did not reduce error: before=%v after=%v", before, after)
	}
}
