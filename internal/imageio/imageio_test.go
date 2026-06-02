package imageio

import (
	"image"
	"image/color"
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestPrepareFromImageNoDownscale(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	img.Set(1, 0, color.RGBA{0, 0, 255, 255})
	p := PrepareFromImage(img, 100)
	if p.W != 2 || p.H != 1 {
		t.Fatalf("dims = %dx%d, want 2x1", p.W, p.H)
	}
	if p.Pixels[0] != 1 || p.Pixels[1] != 0 || p.Pixels[2] != 0 || p.Pixels[3] != 1 {
		t.Fatalf("px0 = %v", p.Pixels[0:4])
	}
	if d := p.Background.R - 0.5; d > 1e-6 || d < -1e-6 {
		t.Fatalf("bg.R = %v, want 0.5", p.Background.R)
	}
}

func TestPrepareDownscales(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	p := PrepareFromImage(img, 100)
	if p.W != 100 || p.H != 50 {
		t.Fatalf("dims = %dx%d, want 100x50 (aspect preserved)", p.W, p.H)
	}
}

func TestPadTransparent(t *testing.T) {
	p := &Prepared{W: 10, H: 10, Pixels: make([]float32, 10*10*4), Background: model.RGBA{R: 1, A: 1}}
	for i := 0; i < 100; i++ {
		p.Pixels[i*4+0], p.Pixels[i*4+3] = 1, 1 // opaque red
	}
	pad, padPx := PadTransparent(p, 0.2) // pad = round(0.2*10) = 2 -> 14x14
	if pad.W != 14 || pad.H != 14 || padPx != 2 {
		t.Fatalf("padded = %dx%d pad=%d, want 14x14 pad=2", pad.W, pad.H, padPx)
	}
	if !pad.HasTransparency {
		t.Fatal("padded prep must be flagged HasTransparency (cutout)")
	}
	if pad.Pixels[3] != 0 { // corner is transparent border
		t.Fatalf("corner alpha = %v, want 0", pad.Pixels[3])
	}
	c := (2*pad.W + 2) * 4 // content origin (2,2) stays opaque red
	if pad.Pixels[c+0] != 1 || pad.Pixels[c+3] != 1 {
		t.Fatalf("content (2,2) = %v, want opaque red", pad.Pixels[c:c+4])
	}
	if gp, gpx := PadTransparent(p, 0); gp != p || gpx != 0 {
		t.Fatal("padFrac<=0 must return (input, 0)")
	}
}

func TestTranslateShapes(t *testing.T) {
	shapes := []model.Shape{
		{Type: model.TypeRotatedEllipse, Data: []float64{50, 60, 5, 5, 0}},
		{Type: model.TypeTriangle, Data: []float64{10, 11, 20, 21, 30, 31}},
		{Type: model.TypeLine, Data: []float64{4, 5, 6, 7, 1}},
	}
	TranslateShapes(shapes, -2, -3)
	if shapes[0].Data[0] != 48 || shapes[0].Data[1] != 57 || shapes[0].Data[2] != 5 {
		t.Fatalf("ellipse = %v, want [48 57 5 5 0]", shapes[0].Data)
	}
	if shapes[1].Data[0] != 8 || shapes[1].Data[3] != 18 || shapes[1].Data[4] != 28 {
		t.Fatalf("triangle = %v, want all verts shifted", shapes[1].Data)
	}
	if shapes[2].Data[0] != 2 || shapes[2].Data[2] != 4 || shapes[2].Data[4] != 1 {
		t.Fatalf("line = %v, want endpoints shifted, width intact", shapes[2].Data)
	}
}

func TestUnpadCanvas(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 14, 14))
	src.SetNRGBA(2, 2, color.NRGBA{200, 0, 0, 255}) // content origin after pad=2
	dst := UnpadCanvas(src, 2, 10, 10)
	if dst.Bounds().Dx() != 10 || dst.Bounds().Dy() != 10 {
		t.Fatalf("dims = %v, want 10x10", dst.Bounds())
	}
	if r := dst.NRGBAAt(0, 0); r.R != 200 || r.A != 255 {
		t.Fatalf("dst(0,0) = %v, want the padded src(2,2) red", r)
	}
}
