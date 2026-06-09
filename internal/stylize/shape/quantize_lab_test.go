package shape

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
)

func rgbDist(a, b model.RGBA) float64 {
	dr, dg, db := float64(a.R-b.R), float64(a.G-b.G), float64(a.B-b.B)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

func threeColourImage() (*stylize.SrcImage, [3]model.RGBA) {
	const W, H = 30, 30
	cols := [3]model.RGBA{{R: 0.9, G: 0.1, B: 0.1, A: 1}, {R: 0.1, G: 0.8, B: 0.15, A: 1}, {R: 0.12, G: 0.15, B: 0.85, A: 1}}
	pix := make([]model.RGBA, W*H)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			pix[y*W+x] = cols[x/10]
		}
	}
	return &stylize.SrcImage{W: W, H: H, Pix: pix}, cols
}

func TestQuantizeLabThreeColours(t *testing.T) {
	src, cols := threeColourImage()
	pal, idx := QuantizeLab(src, 3)
	if len(pal) != 3 {
		t.Fatalf("want 3 palette colours, got %d", len(pal))
	}
	for c := 0; c < 3; c++ {
		i := c * 10 // a pixel of colour c (x=c*10, y=0)
		if d := rgbDist(pal[idx[i]], cols[c]); d > 0.12 {
			t.Errorf("colour %d mapped to %+v, dist %.3f from %+v", c, pal[idx[i]], d, cols[c])
		}
	}
	pal2, _ := QuantizeLab(src, 3)
	for j := range pal {
		if pal[j] != pal2[j] {
			t.Errorf("non-deterministic palette at %d: %v vs %v", j, pal[j], pal2[j])
		}
	}
}

func TestQuantizeByDispatch(t *testing.T) {
	src, _ := threeColourImage()
	if p, _ := QuantizeBy("lab", src, 3); len(p) != 3 {
		t.Errorf("QuantizeBy lab: got %d colours", len(p))
	}
	if p, _ := QuantizeBy("median", src, 3); len(p) != 3 {
		t.Errorf("QuantizeBy median: got %d colours", len(p))
	}
	if p, _ := QuantizeBy("nonexistent", src, 3); len(p) != 3 { // falls back to median-cut
		t.Errorf("QuantizeBy fallback: got %d colours", len(p))
	}
}
