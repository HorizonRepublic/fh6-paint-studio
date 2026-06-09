package fill

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
)

func luma601(c model.RGBA) float32 { return 0.299*c.R + 0.587*c.G + 0.114*c.B }

func TestBoostChromaPreservesLuma(t *testing.T) {
	c := model.RGBA{R: 0.7, G: 0.3, B: 0.5, A: 1}
	b := boostChroma(c, 1.5)
	if math.Abs(float64(luma601(c)-luma601(b))) > 1e-5 {
		t.Errorf("luma changed: %.4f -> %.4f", luma601(c), luma601(b))
	}
}

func TestBoostChromaIncreasesSpread(t *testing.T) {
	c := model.RGBA{R: 0.7, G: 0.3, B: 0.5, A: 1}
	l := luma601(c)
	b := boostChroma(c, 1.5)
	// each channel should move 1.5× farther from luma (before clamping; this colour stays in-gamut).
	if math.Abs(float64((b.R-l)-1.5*(c.R-l))) > 1e-5 {
		t.Errorf("R not scaled: got %.4f want %.4f", b.R-l, 1.5*(c.R-l))
	}
}

func TestBoostChromaGreyUnchanged(t *testing.T) {
	c := model.RGBA{R: 0.5, G: 0.5, B: 0.5, A: 1}
	b := boostChroma(c, 2.0)
	if b.R != c.R || b.G != c.G || b.B != c.B {
		t.Errorf("grey changed under chroma boost: %+v", b)
	}
}

func TestBoostChromaIdentity(t *testing.T) {
	c := model.RGBA{R: 0.2, G: 0.8, B: 0.4, A: 1}
	b := boostChroma(c, 1.0)
	if math.Abs(float64(b.R-c.R)) > 1e-6 || math.Abs(float64(b.G-c.G)) > 1e-6 || math.Abs(float64(b.B-c.B)) > 1e-6 {
		t.Errorf("saturation=1 not identity: %+v vs %+v", b, c)
	}
}

func TestCoarseBaseCoversCanvas(t *testing.T) {
	w, h := 64, 48
	pix := make([]model.RGBA, w*h)
	for i := range pix {
		pix[i] = model.RGBA{R: 0.3, G: 0.6, B: 0.2, A: 1}
	}
	src := &stylize.SrcImage{W: w, H: h, Pix: pix}
	base := coarseBase(src, 8)
	if len(base) == 0 {
		t.Fatal("coarseBase produced no cells")
	}
	// every cell is an opaque rect; the union must span the whole canvas (each cell over-sized by 1).
	var maxX, maxY float64
	for _, s := range base {
		if s.Type != model.TypeRotatedRectangle || s.Color[3] != 255 {
			t.Fatalf("base cell not an opaque rect: type=%d a=%d", s.Type, s.Color[3])
		}
		if r := s.Data[0] + s.Data[2]; r > maxX {
			maxX = r
		}
		if b := s.Data[1] + s.Data[3]; b > maxY {
			maxY = b
		}
	}
	if maxX < float64(w) || maxY < float64(h) {
		t.Errorf("coarseBase does not reach the canvas edge: maxX=%.0f maxY=%.0f want %d,%d", maxX, maxY, w, h)
	}
}

// TestThinRegionNoOverfillTriangle reproduces the line-art bug: a thin black ink web has a contour that,
// triangulated, floods its whole bbox with black. With the TriMinFill gate, such a low-fill-ratio region
// is covered by blocks instead — so no dark triangle is ever emitted.
func TestThinRegionNoOverfillTriangle(t *testing.T) {
	const w, h = 48, 48
	pix := make([]model.RGBA, w*h)
	for i := range pix {
		pix[i] = model.RGBA{R: 1, G: 1, B: 1, A: 1} // white field
	}
	for y := 0; y < h; y++ { // thin black plus (low fill-ratio sprawling region)
		for x := 0; x < w; x++ {
			if (x >= w/2-1 && x <= w/2) || (y >= h/2-1 && y <= h/2) {
				pix[y*w+x] = model.RGBA{A: 1}
			}
		}
	}
	src := &stylize.SrcImage{W: w, H: h, Pix: pix}
	e := &engine{cfg: Defaults()}
	shapes, err := e.Generate(&stylize.Context{Src: src, Budget: 500})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range shapes {
		if s.Type != model.TypeTriangle {
			continue
		}
		lum := 0.299*float32(s.Color[0]) + 0.587*float32(s.Color[1]) + 0.114*float32(s.Color[2])
		if lum < 80 { // a dark triangle == the overfill flood the gate must prevent
			t.Errorf("dark triangle emitted (overfill regression): color=%v", s.Color)
		}
	}
}
