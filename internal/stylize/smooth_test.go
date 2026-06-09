package stylize

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestDomainTransformFlattensKeepsEdge(t *testing.T) {
	const w, h = 40, 20
	pix := make([]model.RGBA, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			base := float32(0.3)
			if x >= w/2 {
				base = 0.7
			}
			n := float32((x*7+y*13)%11-5) * 0.01 // ±0.05 deterministic "noise"
			pix[y*w+x] = model.RGBA{R: base + n, G: base + n, B: base + n, A: 1}
		}
	}
	out := Smooth(&SrcImage{W: w, H: h, Pix: pix}, SmoothConfig{Method: "dt", Spatial: 10, Range: 0.3, Iters: 3})

	varOf := func(p []model.RGBA, x0, x1 int) float64 {
		var s, s2 float64
		n := 0
		for y := 0; y < h; y++ {
			for x := x0; x < x1; x++ {
				v := float64(p[y*w+x].R)
				s += v
				s2 += v * v
				n++
			}
		}
		m := s / float64(n)
		return s2/float64(n) - m*m
	}
	meanOf := func(p []model.RGBA, x0, x1 int) float64 {
		var s float64
		n := 0
		for y := 0; y < h; y++ {
			for x := x0; x < x1; x++ {
				s += float64(p[y*w+x].R)
				n++
			}
		}
		return s / float64(n)
	}

	if vIn, vOut := varOf(pix, 2, w/2-2), varOf(out.Pix, 2, w/2-2); vOut > vIn*0.5 {
		t.Errorf("DT should cut flat-region variance: %.5f -> %.5f", vIn, vOut)
	}
	if step := meanOf(out.Pix, w/2+2, w-2) - meanOf(out.Pix, 2, w/2-2); step < 0.3 {
		t.Errorf("DT should preserve the step edge (got %.3f, want ~0.4)", step)
	}
}
