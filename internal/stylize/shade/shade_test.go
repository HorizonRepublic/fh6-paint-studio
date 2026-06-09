package shade

import (
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
)

func gradientSrc(w, h int) *stylize.SrcImage {
	pix := make([]model.RGBA, w*h)
	for y := 0; y < h; y++ {
		v := float32(y) / float32(h-1) // smooth top→bottom luma ramp
		for x := 0; x < w; x++ {
			pix[y*w+x] = model.RGBA{R: v, G: v, B: v, A: 1}
		}
	}
	return &stylize.SrcImage{W: w, H: h, Pix: pix}
}

func TestShadeFlatImageEmpty(t *testing.T) {
	w, h := 32, 32
	pix := make([]model.RGBA, w*h)
	for i := range pix {
		pix[i] = model.RGBA{R: 0.5, G: 0.5, B: 0.5, A: 1}
	}
	src := &stylize.SrcImage{W: w, H: h, Pix: pix}
	e := &engine{cfg: Defaults()}
	shapes, err := e.Generate(&stylize.Context{Src: src, Budget: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(shapes) != 0 {
		t.Errorf("flat image produced %d shade shapes, want 0", len(shapes))
	}
}

func TestShadeGradientTranslucentTriangles(t *testing.T) {
	src := gradientSrc(64, 64)
	cfg := Defaults()
	cfg.K = 4 // coarse quantization → large residual the shade bands must recover
	e := &engine{cfg: cfg}
	shapes, err := e.Generate(&stylize.Context{Src: src, Budget: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(shapes) == 0 {
		t.Fatal("gradient produced no shade shapes")
	}
	for _, s := range shapes {
		if s.Type != model.TypeTriangle {
			t.Fatalf("shade shape type = %d, want triangle", s.Type)
		}
		if a := s.Color[3]; a <= 0 || a >= 255 {
			t.Fatalf("shade alpha = %d, want strictly translucent (0,255)", a)
		}
	}
}
