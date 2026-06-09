package stroke

import (
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
)

// solidLineSrc builds a WxH white image with a vertical dark line at x=W/2 (a few px wide).
func solidLineSrc(w, h int) *stylize.SrcImage {
	pix := make([]model.RGBA, w*h)
	for i := range pix {
		pix[i] = model.RGBA{R: 1, G: 1, B: 1, A: 1}
	}
	for y := 0; y < h; y++ {
		for _, dx := range []int{-1, 0, 1} {
			x := w/2 + dx
			if x >= 0 && x < w {
				pix[y*w+x] = model.RGBA{A: 1} // black
			}
		}
	}
	return &stylize.SrcImage{W: w, H: h, Pix: pix}
}

func TestFDoGDetectsVerticalLine(t *testing.T) {
	w, h := 60, 60
	src := solidLineSrc(w, h)
	mask := fdogMask(src, defaultFDoG())
	// Ink should appear ON the line column band and essentially nowhere in the far-flat field.
	var onLine, offLine int
	for y := 5; y < h-5; y++ {
		for x := 0; x < w; x++ {
			if !mask[y*w+x] {
				continue
			}
			if x >= w/2-3 && x <= w/2+3 {
				onLine++
			} else if x < w/2-8 || x > w/2+8 {
				offLine++
			}
		}
	}
	if onLine < 20 {
		t.Fatalf("FDoG found too little ink on the line: %d", onLine)
	}
	if offLine > onLine/4 {
		t.Errorf("FDoG ink leaks into the flat field: on=%d off=%d", onLine, offLine)
	}
}

func TestFDoGFlatImageNoLines(t *testing.T) {
	w, h := 40, 40
	pix := make([]model.RGBA, w*h)
	for i := range pix {
		pix[i] = model.RGBA{R: 0.6, G: 0.6, B: 0.6, A: 1}
	}
	src := &stylize.SrcImage{W: w, H: h, Pix: pix}
	mask := fdogMask(src, defaultFDoG())
	n := 0
	for _, b := range mask {
		if b {
			n++
		}
	}
	if n > 0 {
		t.Errorf("flat image produced %d ink pixels, want 0", n)
	}
}

func TestEdgeTangentFlowAlignsAlongLine(t *testing.T) {
	w, h := 40, 40
	src := solidLineSrc(w, h)
	luma := lumaOf(src)
	tx, ty := edgeTangentFlow(luma, w, h, 4, 3)
	// On the line, the tangent should be (near) vertical — |ty| dominates |tx|.
	i := (h/2)*w + w/2
	if absf(ty[i]) < absf(tx[i]) {
		t.Errorf("tangent on vertical line not vertical: tx=%.3f ty=%.3f", tx[i], ty[i])
	}
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
