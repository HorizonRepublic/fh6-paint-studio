package stroke

import (
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
)

func TestZhangSuenThinsBar(t *testing.T) {
	const w, h = 20, 7
	mask := make([]bool, w*h)
	for y := 2; y < 5; y++ { // a 3-px-thick horizontal bar
		for x := 2; x < 18; x++ {
			mask[y*w+x] = true
		}
	}
	skel := zhangSuenThin(mask, w, h)
	n := 0
	for _, v := range skel {
		if v {
			n++
		}
	}
	// a 16x3 bar (48 px) should thin to ~16 (one row), certainly < half.
	if n == 0 || n > 24 {
		t.Errorf("thinned bar has %d px, want a ~1-px centerline (~16)", n)
	}
}

func TestTraceSkeletonLine(t *testing.T) {
	const w, h = 20, 5
	skel := make([]bool, w*h)
	for x := 2; x < 18; x++ {
		skel[2*w+x] = true // a straight 1-px line
	}
	polys := traceSkeleton(skel, w, h, 3)
	if len(polys) != 1 {
		t.Fatalf("a single line should trace to 1 polyline, got %d", len(polys))
	}
	if len(polys[0]) < 10 {
		t.Errorf("traced line too short: %d points", len(polys[0]))
	}
}

func TestXDoGFindsDarkLine(t *testing.T) {
	const w, h = 24, 24
	pix := make([]model.RGBA, w*h)
	for i := range pix {
		pix[i] = model.RGBA{R: 0.92, G: 0.92, B: 0.92, A: 1} // light ground
	}
	for y := 0; y < h; y++ { // a 2-px dark vertical line at x=11..12
		for x := 11; x <= 12; x++ {
			pix[y*w+x] = model.RGBA{R: 0.08, G: 0.08, B: 0.08, A: 1}
		}
	}
	mask := xdogMask(&stylize.SrcImage{W: w, H: h, Pix: pix}, defaultXDoG())
	onLine, offLine := 0, 0
	for y := 4; y < h-4; y++ {
		for x := 10; x <= 13; x++ {
			if mask[y*w+x] {
				onLine++
			}
		}
		if mask[y*w+2] {
			offLine++
		}
	}
	if onLine < 8 {
		t.Errorf("XDoG should ink the dark line, got %d on-line pixels", onLine)
	}
	if offLine > 2 {
		t.Errorf("XDoG should leave the flat ground blank, got %d off-line pixels", offLine)
	}
}
