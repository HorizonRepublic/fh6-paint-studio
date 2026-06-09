package engine

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

// TestBinarizeForLockCutout: an input WITH alpha thresholds on alpha — opaque pixels become the
// exact lock colour, the rest fully transparent (the mono cutout).
func TestBinarizeForLockCutout(t *testing.T) {
	lock := model.RGBA{R: 1, G: 1, B: 1}
	px := []float32{
		0.6, 0.6, 0.6, 1.0, // opaque grey -> lock white, alpha 1
		0.9, 0.2, 0.2, 0.3, // faint edge (alpha < 0.5) -> transparent
		0.0, 0.0, 0.0, 0.0, // already transparent -> stays transparent
		0.5, 0.5, 0.5, 0.5, // exactly at threshold -> ink
	}
	BinarizeForLock(px, 4, 1, lock, true)
	want := []float32{
		1, 1, 1, 1,
		0, 0, 0, 0,
		0, 0, 0, 0,
		1, 1, 1, 1,
	}
	for i := range want {
		if px[i] != want[i] {
			t.Fatalf("cutout binarize px[%d]=%v want %v (%v)", i, px[i], want[i], px)
		}
	}
}

// TestBinarizeForLockOpaque: an input WITHOUT alpha keys out the corner background — pixels far from
// the corner colour become ink (lock), near-background pixels become transparent.
func TestBinarizeForLockOpaque(t *testing.T) {
	lock := model.RGBA{R: 1, G: 0, B: 0}
	// corners are black; pixel[1] is white (far from black) = ink, pixel[2] near-black = background.
	px := []float32{
		0, 0, 0, 1, // corner (black) -> background -> transparent
		1, 1, 1, 1, // white -> ink -> lock red, opaque
		0.05, 0.05, 0.05, 1, // near black -> background -> transparent
		0, 0, 0, 1, // corner (black) -> transparent
	}
	BinarizeForLock(px, 4, 1, lock, false)
	if !(px[4] == 1 && px[5] == 0 && px[6] == 0 && px[7] == 1) {
		t.Fatalf("opaque ink pixel = %v, want lock red opaque", px[4:8])
	}
	for _, off := range []int{0, 8, 12} {
		if px[off+3] != 0 {
			t.Fatalf("opaque background pixel at %d kept alpha %v, want transparent", off, px[off+3])
		}
	}
}

// TestDominantInkCutout: the auto colour is the mean of the OPAQUE pixels only (faint edges excluded).
func TestDominantInkCutout(t *testing.T) {
	px := []float32{
		1.0, 0.0, 0.0, 1.0, // opaque red
		1.0, 0.0, 0.0, 1.0, // opaque red
		0.0, 1.0, 0.0, 0.2, // faint green (alpha < 0.5) -> excluded
	}
	got := DominantInk(px, 3, 1, true)
	if got.R < 0.99 || got.G > 0.01 || got.B > 0.01 {
		t.Fatalf("dominant ink = %+v, want ~red", got)
	}
}

// TestDominantInkEmpty: no ink -> white fallback (never a zero/black lock).
func TestDominantInkEmpty(t *testing.T) {
	px := []float32{0, 0, 0, 0, 0, 0, 0, 0}
	got := DominantInk(px, 2, 1, true)
	if got.R != 1 || got.G != 1 || got.B != 1 {
		t.Fatalf("empty dominant ink = %+v, want white", got)
	}
}
