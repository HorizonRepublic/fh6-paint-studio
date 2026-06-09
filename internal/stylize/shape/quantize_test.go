package shape

import (
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
)

func TestQuantizeThreeColours(t *testing.T) {
	w, h := 30, 1
	pix := make([]model.RGBA, w*h)
	cols := []model.RGBA{{R: 1, A: 1}, {G: 1, A: 1}, {B: 1, A: 1}}
	for x := 0; x < w; x++ {
		pix[x] = cols[x/10]
	}
	src := &stylize.SrcImage{W: w, H: h, Pix: pix}
	pal, idx := Quantize(src, 3)
	if len(pal) != 3 {
		t.Fatalf("palette size %d, want 3", len(pal))
	}
	if idx[5] == idx[15] || idx[15] == idx[25] || idx[5] == idx[25] {
		t.Errorf("three colour thirds not assigned distinct indices: %d %d %d", idx[5], idx[15], idx[25])
	}
}
