package imageio

import (
	"image"
	"image/color"
	"testing"
)

// checkerFixture paints a T-period editor lattice with an opaque content disc in the middle and
// (optionally) a content ring leaving an enclosed lattice hole at the very center.
func checkerFixture(w, h, period int, hole bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	c := [2]color.NRGBA{{254, 254, 254, 255}, {237, 237, 237, 255}}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c[(x/period+y/period)%2])
		}
	}
	cx, cy := w/2, h/2
	rOut, rIn := w/4, w/8
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d2 := (x-cx)*(x-cx) + (y-cy)*(y-cy)
			inside := d2 <= rOut*rOut
			if hole && d2 < rIn*rIn {
				inside = false // enclosed lattice hole inside the content ring
			}
			if inside {
				img.SetNRGBA(x, y, color.NRGBA{200, 30, 30, 255})
			}
		}
	}
	return img
}

func TestCheckerStrip(t *testing.T) {
	img := checkerFixture(240, 200, 20, true)
	out := stripBakedChecker(img)
	if out == img.SubImage(img.Bounds()) || out == image.Image(img) {
		t.Fatal("checkerboard fixture must be stripped, got the input back")
	}
	n := out.(*image.NRGBA)
	// Lattice corner -> transparent; content disc -> opaque; the enclosed hole -> transparent.
	if a := n.NRGBAAt(2, 2).A; a != 0 {
		t.Errorf("perimeter lattice pixel alpha = %d, want 0", a)
	}
	if a := n.NRGBAAt(120, 60).A; a != 255 {
		t.Errorf("content pixel alpha = %d, want 255", a)
	}
	if a := n.NRGBAAt(120, 100).A; a != 0 {
		t.Errorf("enclosed-hole lattice pixel alpha = %d, want 0", a)
	}

	// End-to-end: the prepared image must now classify as a transparent cutout.
	prep := PrepareFromImage(img, 0)
	if !prep.HasTransparency {
		t.Error("prepared checker fixture should report HasTransparency")
	}
}

func TestCheckerStripNegatives(t *testing.T) {
	// Real transparency -> untouched.
	withAlpha := checkerFixture(240, 200, 20, false)
	withAlpha.SetNRGBA(5, 5, color.NRGBA{254, 254, 254, 0})
	if out := stripBakedChecker(withAlpha); out != image.Image(withAlpha) {
		t.Error("image with real alpha must pass through")
	}

	// Dark chessboard (a legit texture, not an editor lattice) -> luma gate rejects.
	dark := image.NewNRGBA(image.Rect(0, 0, 240, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 240; x++ {
			c := color.NRGBA{30, 30, 30, 255}
			if (x/20+y/20)%2 == 1 {
				c = color.NRGBA{250, 250, 250, 255}
			}
			dark.SetNRGBA(x, y, c)
		}
	}
	if out := stripBakedChecker(dark); out != image.Image(dark) {
		t.Error("dark chessboard must not be treated as baked transparency")
	}

	// Uniform background -> no alternation, untouched.
	flat := image.NewNRGBA(image.Rect(0, 0, 240, 200))
	for i := range flat.Pix {
		flat.Pix[i] = 255
	}
	if out := stripBakedChecker(flat); out != image.Image(flat) {
		t.Error("uniform image must pass through")
	}

	// Saturated two-color alternation (legit art, not a checker) -> saturation gate rejects.
	sat := image.NewNRGBA(image.Rect(0, 0, 240, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 240; x++ {
			c := color.NRGBA{250, 120, 120, 255}
			if (x/20+y/20)%2 == 1 {
				c = color.NRGBA{250, 250, 250, 255}
			}
			sat.SetNRGBA(x, y, c)
		}
	}
	if out := stripBakedChecker(sat); out != image.Image(sat) {
		t.Error("saturated lattice must not be stripped")
	}
}
