package imageio

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func fillRect(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

// Opaque image with a solid border + a contrasting content block -> crops to the content.
func TestAutoCropOpaqueBorder(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	fillRect(img, img.Bounds(), color.NRGBA{40, 40, 40, 255})                // uniform border
	fillRect(img, image.Rect(20, 20, 80, 80), color.NRGBA{220, 30, 30, 255}) // 60x60 content
	r := AutoCropRect(img)
	if r.Eq(img.Bounds()) {
		t.Fatalf("expected a crop, got full bounds %v", r)
	}
	if r.Min.X > 20 || r.Min.Y > 20 || r.Max.X < 80 || r.Max.Y < 80 {
		t.Errorf("crop %v must contain the content [20,20]-[80,80]", r)
	}
	if r.Dx() > 75 || r.Dy() > 75 { // 60px content + small margin must stay tight
		t.Errorf("crop %v too loose for 60px content", r)
	}
}

// Transparent cutout -> crops to the opaque silhouette.
func TestAutoCropTransparent(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100)) // all zero = transparent
	fillRect(img, image.Rect(30, 30, 80, 80), color.NRGBA{10, 200, 90, 255})
	r := AutoCropRect(img)
	if r.Eq(img.Bounds()) {
		t.Fatalf("expected a crop of the cutout, got full bounds")
	}
	if r.Min.X > 30 || r.Min.Y > 30 || r.Max.X < 80 || r.Max.Y < 80 {
		t.Errorf("crop %v must contain the cutout [30,30]-[80,80]", r)
	}
}

// LoadRegionAutoCropped crops fractions of the AUTO-CROPPED content rect (not the raw bounds) and
// preserves full resolution (maxRes=0 = no downscale).
func TestLoadRegionAutoCropped(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	fillRect(img, img.Bounds(), color.NRGBA{40, 40, 40, 255})                // border
	fillRect(img, image.Rect(20, 20, 80, 80), color.NRGBA{220, 30, 30, 255}) // 60x60 content
	path := filepath.Join(t.TempDir(), "bordered.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()

	base := AutoCropRect(img)
	full, rect, err := LoadRegionAutoCropped(path, 0, 0, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if full.W != base.Dx() || full.H != base.Dy() {
		t.Fatalf("full region = %dx%d, want the content rect %dx%d", full.W, full.H, base.Dx(), base.Dy())
	}
	if !rect.Eq(base) {
		t.Fatalf("full region rect = %v, want AutoCropRect %v", rect, base)
	}
	// A quarter (top-left) of the content rect is ~half the dims in each axis.
	q, _, err := LoadRegionAutoCropped(path, 0, 0, 0, 0.5, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if q.W < base.Dx()/2-1 || q.W > base.Dx()/2+1 || q.H < base.Dy()/2-1 || q.H > base.Dy()/2+1 {
		t.Fatalf("quarter region = %dx%d, want ~%dx%d", q.W, q.H, base.Dx()/2, base.Dy()/2)
	}
}

// Full-bleed gradient (no uniform border) -> no-op (full bounds).
func TestAutoCropFullBleedNoop(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 2), uint8(y * 2), 128, 255})
		}
	}
	if r := AutoCropRect(img); !r.Eq(img.Bounds()) {
		t.Errorf("full-bleed gradient must be a no-op, got crop %v", r)
	}
}
