package shape

import (
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
)

// TestChromaWeightPrefersHue is the mechanism: a saturated pixel whose LUMA matches a grey centroid but
// whose CHROMA matches a colour centroid is assigned to the grey under plain Lab (w=1, luma wins → the
// colour is lost) but to the colour under the chroma weight (w=2.2 → hue wins → the colour survives).
func TestChromaWeightPrefersHue(t *testing.T) {
	cent := [][3]float32{{49, 0, 0}, {70, 18, 0}} // 0 = grey (luma match), 1 = colour (very different luma)
	p := [3]float32{49, 15, 0}                    // saturated, same luma as the grey
	if got := nearestLab(p, cent, 1.0); got != 0 {
		t.Errorf("plain Lab (w=1) should pick the luma-matching grey (0), got %d", got)
	}
	if got := nearestLab(p, cent, 2.2); got != 1 {
		t.Errorf("chroma-weighted (w=2.2) should pick the hue-matching colour (1), got %d", got)
	}
}

func TestLabVividWellFormed(t *testing.T) {
	w, h := 32, 32
	pix := make([]model.RGBA, w*h)
	for i := range pix {
		pix[i] = model.RGBA{R: float32(i%7) / 6, G: 0.4, B: float32(i%5) / 4, A: 1}
	}
	src := &stylize.SrcImage{W: w, H: h, Pix: pix}
	pal, idx := QuantizeBy("labvivid", src, 5)
	if len(pal) == 0 || len(idx) != w*h {
		t.Fatalf("labvivid malformed: pal=%d idx=%d", len(pal), len(idx))
	}
	for _, j := range idx {
		if j < 0 || j >= len(pal) {
			t.Fatalf("idx out of range: %d (pal %d)", j, len(pal))
		}
	}
}
