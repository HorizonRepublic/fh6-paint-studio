package spark

import (
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
)

// darkWithDot builds a dark field with one small bright dot (a catchlight) at the centre.
func darkWithDot(w, h, dotR int) *stylize.SrcImage {
	pix := make([]model.RGBA, w*h)
	for i := range pix {
		pix[i] = model.RGBA{R: 0.1, G: 0.1, B: 0.12, A: 1}
	}
	cx, cy := w/2, h/2
	for y := cy - dotR; y <= cy+dotR; y++ {
		for x := cx - dotR; x <= cx+dotR; x++ {
			if x >= 0 && y >= 0 && x < w && y < h {
				pix[y*w+x] = model.RGBA{R: 0.97, G: 0.97, B: 1, A: 1}
			}
		}
	}
	return &stylize.SrcImage{W: w, H: h, Pix: pix}
}

func TestSparkFindsCatchlight(t *testing.T) {
	src := darkWithDot(48, 48, 2)
	e := &engine{cfg: Defaults()}
	shapes, err := e.Generate(&stylize.Context{Orig: src, Src: src, Budget: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(shapes) != 1 {
		t.Fatalf("want 1 catchlight, got %d", len(shapes))
	}
	s := shapes[0]
	if s.Type != model.TypeRotatedEllipse {
		t.Errorf("highlight type = %d, want ellipse", s.Type)
	}
	if s.Data[0] < 20 || s.Data[0] > 28 || s.Data[1] < 20 || s.Data[1] > 28 {
		t.Errorf("highlight not centred: %.1f,%.1f", s.Data[0], s.Data[1])
	}
	if s.Color[0] < 200 { // bright
		t.Errorf("highlight not bright: %v", s.Color)
	}
}

func TestSparkIgnoresLargeBrightArea(t *testing.T) {
	// A uniformly bright image has no LOCAL bright spot → no highlights.
	w, h := 40, 40
	pix := make([]model.RGBA, w*h)
	for i := range pix {
		pix[i] = model.RGBA{R: 0.9, G: 0.9, B: 0.9, A: 1}
	}
	src := &stylize.SrcImage{W: w, H: h, Pix: pix}
	e := &engine{cfg: Defaults()}
	shapes, _ := e.Generate(&stylize.Context{Orig: src, Src: src, Budget: 100})
	if len(shapes) != 0 {
		t.Errorf("uniform bright field produced %d highlights, want 0", len(shapes))
	}
}
