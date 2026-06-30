package imageio

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestWriteGeometryRoundtrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "g.json")
	g := model.Geometry{Shapes: []model.Shape{{Type: 16, Data: []float64{1, 2, 3, 4, 0}, Color: []int{1, 2, 3, 255}}}}
	if err := WriteGeometry(p, g); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

func TestSavePreview(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.png")
	px := make([]float32, 4*4*4)
	for i := range px {
		px[i] = 1
	}
	if err := SavePreview(p, px, 4, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("preview not written: %v", err)
	}
}

func TestCompositeShapeOnto(t *testing.T) {
	base := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < 8*8; i++ { // opaque black base
		base.Pix[i*4+3] = 255
	}
	sh := model.Shape{Type: model.TypeRotatedEllipse, Data: []float64{4, 4, 3, 3, 0}, Color: []int{255, 0, 0, 255}}
	CompositeShapeOnto(base, sh, 8, 8)
	if r, _, _, _ := base.At(4, 4).RGBA(); r>>8 < 200 {
		t.Fatalf("centre R = %d, want the composited red", r>>8)
	}
	if r, _, _, _ := base.At(0, 0).RGBA(); r>>8 > 20 {
		t.Fatalf("corner R = %d, want untouched black (outside the ellipse)", r>>8)
	}
}

func TestRenderFH6ImageDims(t *testing.T) {
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, 8, 8}, Color: []int{10, 20, 30, 255}}, // bg
		{Type: model.TypeRotatedEllipse, Data: []float64{4, 4, 2, 2, 0}, Color: []int{200, 0, 0, 255}},
	}
	img := RenderFH6Image(shapes, false, 8, 8, 1)
	if img.Bounds().Dx() != 8 || img.Bounds().Dy() != 8 {
		t.Fatalf("dims = %v, want 8x8", img.Bounds())
	}
	// centre pixel should carry the ellipse's red, not the background.
	r, _, _, _ := img.At(4, 4).RGBA()
	if r>>8 < 100 {
		t.Fatalf("centre R = %d, want the red ellipse on top", r>>8)
	}
}
