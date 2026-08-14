package engine

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

// A clean linear-ramp target must be claimed by the shading pre-pass (a rect+ramp-word stack that
// beats the flat cover), leaving the greedy a much smaller residual; a flat target must claim
// nothing (the region-fill lesson: no flat pre-placement).
func TestShadePrepassClaimsLinearRamp(t *testing.T) {
	if _, ok := model.MaskKind(shadeWord); !ok {
		t.Skip("mask bank lacks the linear ramp word")
	}
	w, h := 96, 96
	ramp := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 0.15 + 0.7*float32(x)/float32(w-1)
			p := (y*w + x) * 4
			ramp[p], ramp[p+1], ramp[p+2], ramp[p+3] = v, v, v, 1
		}
	}
	// StopAt 4 leaves the greedy at most one shape after the claim: the error collapse below can
	// only come from the claimed stack itself (guards the opaque-alpha Apply path, not just the gate).
	opt := Options{
		Width: w, Height: h, Background: bgFromTarget(ramp, w, h),
		StopAt: 4, RandomSamples: 16, MutatedSamples: 8, Seed: 1, MaxNoImprove: 1,
		Kinds:        []model.ShapeKind{model.KindEllipse},
		ShadePrepass: true,
	}
	res := Run(newTestBackend(t, ramp, w, h, 8), opt)
	sawMask := false
	for _, s := range res.Shapes[1:] {
		if model.IsMask(model.KindFromType(s.Type)) {
			sawMask = true
		}
	}
	if !sawMask {
		t.Fatalf("linear ramp target should be claimed with a gradient word (shapes=%d)", len(res.Shapes))
	}
	// The 2204 ramp's alpha floor (~0.27 — it never fades fully to 0) leaves a small residual the
	// single remaining greedy shape can't finish; ~85-90% collapse is the honest stack ceiling.
	if res.FinalError > res.InitialError*0.15 {
		t.Fatalf("claimed ramp should collapse the error: %.1f -> %.1f", res.InitialError, res.FinalError)
	}

	flat := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		flat[i*4], flat[i*4+1], flat[i*4+2], flat[i*4+3] = 0.5, 0.5, 0.5, 1
	}
	res2 := Run(newTestBackend(t, flat, w, h, 8), Options{
		Width: w, Height: h, Background: bgFromTarget(flat, w, h),
		StopAt: 24, RandomSamples: 16, MutatedSamples: 8, Seed: 1, MaxNoImprove: 1,
		Kinds:        []model.ShapeKind{model.KindEllipse},
		ShadePrepass: true,
	})
	for _, s := range res2.Shapes[1:] {
		if model.IsMask(model.KindFromType(s.Type)) {
			t.Fatal("flat target must not be claimed (no coherent gradient)")
		}
	}
}
