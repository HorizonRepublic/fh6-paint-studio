package shape

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestSegmentSplitsBlobsDropsSpeck(t *testing.T) {
	w, h := 6, 3
	idx := make([]int, w*h)
	for _, p := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}, {4, 0}, {5, 0}, {4, 1}, {5, 1}, {3, 2}} {
		idx[p[1]*w+p[0]] = 1 // two 2x2 blobs of colour 1 + a 1-px speck at (3,2)
	}
	pal := []model.RGBA{{R: 0, G: 0, B: 0, A: 1}, {R: 1, G: 1, B: 1, A: 1}}
	regs := Segment(w, h, idx, pal, 2)
	c1 := 0
	for i := range regs {
		if regs[i].Color.R == 1 {
			c1++
		}
	}
	if c1 != 2 {
		t.Errorf("want 2 colour-1 regions (speck dropped), got %d", c1)
	}
}

// TestDistanceTransformBoundedRect is the regression for the inf-radius bug: a region that fills its
// whole bbox must still have a finite (border-bounded) max distance, not 1e9.

// TestCoverBlocksCoversRegion checks the coverage guarantee: every in-region pixel ends up under an
// emitted rect (no holes), here for an L-shaped region.
func TestCoverBlocksCoversRegion(t *testing.T) {
	bw, bh := 16, 16
	mask := make([]bool, bw*bh)
	for y := 0; y < bh; y++ {
		for x := 0; x < bw; x++ {
			if x < 10 || y >= 10 { // L shape
				mask[y*bw+x] = true
			}
		}
	}
	area := 0
	for _, m := range mask {
		if m {
			area++
		}
	}
	r := &Region{Color: model.RGBA{R: 1, A: 1}, X0: 0, Y0: 0, BW: bw, BH: bh, Mask: mask, Area: area}
	shapes := CoverBlocks(r, 4000, 3, 0.6)
	if len(shapes) == 0 {
		t.Fatal("no shapes emitted")
	}
	cov := make([]bool, bw*bh)
	for _, s := range shapes {
		cx, cy, hw, hh := s.Data[0], s.Data[1], s.Data[2], s.Data[3]
		for y := 0; y < bh; y++ {
			for x := 0; x < bw; x++ {
				if math.Abs(float64(x)+0.5-cx) <= hw && math.Abs(float64(y)+0.5-cy) <= hh {
					cov[y*bw+x] = true
				}
			}
		}
	}
	miss := 0
	for i := range mask {
		if mask[i] && !cov[i] {
			miss++
		}
	}
	if miss > 0 {
		t.Errorf("%d in-region pixels left uncovered", miss)
	}
}
