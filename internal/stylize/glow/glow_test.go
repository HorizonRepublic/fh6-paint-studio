package glow

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
)

// meanResidual = mean sRGB distance between two equal-length RGBA planes.
func meanResidual(a, b []model.RGBA) float64 {
	var s float64
	for i := range a {
		dr := float64(a[i].R - b[i].R)
		dg := float64(a[i].G - b[i].G)
		db := float64(a[i].B - b[i].B)
		s += math.Sqrt(dr*dr + dg*dg + db*db)
	}
	return s / float64(len(a))
}

// A smooth vertical luma ramp, posterized to K bands, BANDS hard. Glow refinement should paint the smooth
// shading back over the banded base, cutting the residual to the true ramp well below the banded base.
func TestFitResidualGlowsReducesBanding(t *testing.T) {
	w, h := 64, 64
	src := make([]model.RGBA, w*h)
	for y := 0; y < h; y++ {
		v := float32(y) / float32(h-1) // 0..1 smooth ramp
		for x := 0; x < w; x++ {
			src[y*w+x] = model.RGBA{R: v, G: v, B: v, A: 1}
		}
	}
	si := &stylize.SrcImage{W: w, H: h, Pix: src}

	// flat base = posterize to 5 luma bands (what a flat-cell fill renders) → hard banding.
	base := make([]model.RGBA, w*h)
	for i, p := range src {
		q := float32(math.Round(float64(p.R)*4)) / 4
		base[i] = model.RGBA{R: q, G: q, B: q, A: 1}
	}
	before := meanResidual(src, base)

	cfg := Defaults()
	cfg.Budget = 80
	canvas := make([]model.RGBA, w*h)
	copy(canvas, base)
	glows := fitResidualGlows(si, canvas, flatGrad(w*h), nil, w, h, cfg)
	after := meanResidual(src, canvas)

	if len(glows) == 0 {
		t.Fatal("expected glows to be emitted on a banded gradient")
	}
	if after >= before {
		t.Fatalf("glow refinement did not reduce residual: before=%.4f after=%.4f", before, after)
	}
	if after > before*0.7 {
		t.Errorf("glow refinement weak: before=%.4f after=%.4f (want ≤70%%)", before, after)
	}
	for _, g := range glows {
		if g.Type != model.TypeGradGlow {
			t.Fatalf("emitted non-glow shape type %v", g.Type)
		}
	}
}

// A perfectly flat image has zero residual → no glows should be spent.
func TestFitResidualGlowsNoneOnFlat(t *testing.T) {
	w, h := 32, 32
	src := make([]model.RGBA, w*h)
	for i := range src {
		src[i] = model.RGBA{R: 0.5, G: 0.5, B: 0.5, A: 1}
	}
	si := &stylize.SrcImage{W: w, H: h, Pix: src}
	canvas := make([]model.RGBA, w*h)
	copy(canvas, src) // base already equals source
	cfg := Defaults()
	cfg.Budget = 50
	glows := fitResidualGlows(si, canvas, flatGrad(w*h), nil, w, h, cfg)
	if len(glows) != 0 {
		t.Fatalf("flat image needs no glows, got %d", len(glows))
	}
}

func flatGrad(n int) []float64 { return make([]float64, n) }
