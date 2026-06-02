package cpu

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestProgressiveSamplingLargeShape(t *testing.T) {
	w, h := 200, 200
	target := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		target[i*4], target[i*4+3] = 1, 1 // solid red, opaque
	}
	c := New(target, w, h, 8)
	// Big ellipse: bbox ~180x180 = 32400 px > targetSamples(4000) -> step > 1.
	cand := model.Candidate{Kind: model.KindEllipse, P: [6]float32{100, 100, 90, 90, 0, 0}, Color: model.RGBA{A: 1}}
	res, _ := c.Evaluate([]model.Candidate{cand})
	if res[0].Color.R < 0.99 || res[0].Color.G > 0.01 {
		t.Fatalf("sampled large-shape optimal color should be ~red, got %+v", res[0].Color)
	}
	if res[0].Score >= 0 {
		t.Fatalf("sampled large-shape score should be negative (improvement), got %v", res[0].Score)
	}
}

func TestSampleStepUnitForSmall(t *testing.T) {
	c := New(make([]float32, 4), 1, 1, 1)      // default sampleBudget 4000
	if s := c.sampleStep(0, 0, 9, 9); s != 1 { // 100 px <= 4000
		t.Fatalf("small bbox step = %d, want 1", s)
	}
	if s := c.sampleStep(0, 0, 999, 999); s <= 1 { // 1,000,000 px -> large step
		t.Fatalf("large bbox step = %d, want > 1", s)
	}
	// A budget >= bbox area makes scoring full-resolution (step 1).
	c.SetSampleBudget(1 << 20) // 1,048,576 >= 1,000,000
	if s := c.sampleStep(0, 0, 999, 999); s != 1 {
		t.Fatalf("full-res budget step = %d, want 1", s)
	}
}
