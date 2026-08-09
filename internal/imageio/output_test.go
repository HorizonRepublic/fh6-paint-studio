package imageio

import (
	"image"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
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

// The banded render must be BIT-identical to the serial one: RenderFH6 is the
// WYSIWYG ground truth, and a parallelism bug here would change what the
// editor shows versus what the game draws.
func TestRenderFH6BandParity(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	shapes := []model.Shape{{Type: model.TypeRectangle, Data: []float64{0, 0, 160, 120}, Color: []int{30, 40, 50, 255}}}
	kinds := []int{model.TypeRotatedRectangle, model.TypeRotatedEllipse, model.TypeTriangle, 0xE4, 0xE2}
	for i := 0; i < 120; i++ {
		k := kinds[i%len(kinds)]
		cx, cy := rng.Float64()*160, rng.Float64()*120
		a, b := 2+rng.Float64()*40, 2+rng.Float64()*40
		var data []float64
		if k == model.TypeTriangle {
			data = []float64{cx, cy, cx + a, cy + b, cx - b, cy + a}
		} else {
			data = []float64{cx, cy, a, b, rng.Float64() * 360}
		}
		shapes = append(shapes, model.Shape{
			Type:  k,
			Data:  data,
			Color: []int{rng.Intn(256), rng.Intn(256), rng.Intn(256), 40 + rng.Intn(216)},
		})
	}
	banded := RenderFH6(shapes, false, 160, 120, 2)
	old := runtime.GOMAXPROCS(1) // parallelRows degrades to the serial loop
	serial := RenderFH6(shapes, false, 160, 120, 2)
	runtime.GOMAXPROCS(old)
	if len(banded) != len(serial) {
		t.Fatalf("length %d vs %d", len(banded), len(serial))
	}
	for i := range banded {
		if banded[i] != serial[i] {
			t.Fatalf("banded render diverges from serial at %d: %g vs %g", i, banded[i], serial[i])
		}
	}
}
