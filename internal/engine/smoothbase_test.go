package engine

import (
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// A large smooth radial-shaded target must be claimed by the smooth-base pre-pass with a stack that
// includes an earning gradient layer, and the stack itself (StopAt leaves the greedy ~one shape)
// must collapse most of the error. A flat target must claim nothing — flat covers are never
// pre-placed (the region-fill lesson).
func TestSmoothBaseClaimsRadialShading(t *testing.T) {
	w, h := 128, 128
	img := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float32(x-64)/64, float32(y-64)/64
			v := 0.75 - 0.5*(dx*dx+dy*dy)
			if v < 0.1 {
				v = 0.1
			}
			p := (y*w + x) * 4
			img[p], img[p+1], img[p+2], img[p+3] = v, v*0.8, v*0.6, 1
		}
	}
	opt := Options{
		Width: w, Height: h, Background: bgFromTarget(img, w, h),
		StopAt: 6, RandomSamples: 16, MutatedSamples: 8, Seed: 1, MaxNoImprove: 1,
		Kinds:      []model.ShapeKind{model.KindEllipse},
		SmoothBase: true,
	}
	res := Run(newTestBackend(t, img, w, h, 8), opt)
	sawSoft := false
	for _, s := range res.Shapes[1:] {
		k := model.KindFromType(s.Type)
		if k == model.KindGlow || k == model.KindDisk || model.IsMask(k) {
			sawSoft = true
		}
	}
	if !sawSoft {
		t.Fatalf("radial shading should be claimed with a gradient layer (shapes=%d)", len(res.Shapes))
	}
	if res.FinalError > res.InitialError*0.35 {
		t.Fatalf("claimed stack should collapse most of the error: %.1f -> %.1f", res.InitialError, res.FinalError)
	}

	flat := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		flat[i*4], flat[i*4+1], flat[i*4+2], flat[i*4+3] = 0.42, 0.5, 0.58, 1
	}
	res2 := Run(newTestBackend(t, flat, w, h, 8), Options{
		Width: w, Height: h, Background: bgFromTarget(flat, w, h),
		StopAt: 6, RandomSamples: 16, MutatedSamples: 8, Seed: 1, MaxNoImprove: 1,
		Kinds:      []model.ShapeKind{model.KindEllipse},
		SmoothBase: true,
	})
	for _, s := range res2.Shapes[1:] {
		k := model.KindFromType(s.Type)
		if k == model.KindGlow || k == model.KindDisk || model.IsMask(k) {
			t.Fatal("flat target must not be claimed by the smooth-base pre-pass")
		}
	}
	_ = raster.IsGradient // keep the import obvious: soft kinds are the per-pixel-alpha family
}
