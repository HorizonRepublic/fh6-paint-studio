package imageio

import (
	"image"
	"image/color"
	"testing"

	"fh6-paint-studio/internal/model"
)

// A subject over a plain backdrop: the mean lands between the two colours and matches neither, which
// is the case that costs the greedy hundreds of shapes.
func TestDominantBeatsMeanOnPlainBackdrop(t *testing.T) {
	const w, h = 100, 100
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{255, 255, 255, 255}
			if x >= 30 && x < 70 && y >= 30 && y < 70 { // 16% subject
				c = color.NRGBA{0, 0, 0, 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}

	DominantBGFrac = 0
	defer func() { DominantBGFrac = 0 }()
	mean := PrepareFromImage(img, 0).Background

	DominantBGFrac = 0.25
	dom := PrepareFromImage(img, 0).Background

	if mean.R > 0.99 {
		t.Fatalf("mean background %.3f already white — fixture does not exercise the gap", mean.R)
	}
	if dom.R < 0.99 || dom.G < 0.99 || dom.B < 0.99 {
		t.Errorf("dominant background = (%.3f,%.3f,%.3f), want white", dom.R, dom.G, dom.B)
	}
}

// No colour dominates: the mean is the better start and must be left alone.
func TestDominantDeclinesWithoutAMajority(t *testing.T) {
	const w, h = 60, 60
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	shades := []color.NRGBA{{20, 20, 20, 255}, {90, 90, 90, 255}, {160, 160, 160, 255}, {230, 230, 230, 255}}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, shades[(x/15)%len(shades)])
		}
	}
	DominantBGFrac = 0
	defer func() { DominantBGFrac = 0 }()
	mean := PrepareFromImage(img, 0).Background
	DominantBGFrac = 0.5
	got := PrepareFromImage(img, 0).Background
	if got != mean {
		t.Errorf("background changed to (%.3f,%.3f,%.3f) with no dominant colour, want the mean", got.R, got.G, got.B)
	}
}

// Transparent pixels carry undefined colour and must not vote.
func TestDominantIgnoresTransparentPixels(t *testing.T) {
	const w, h = 40, 40
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if y < 30 {
				img.SetNRGBA(x, y, color.NRGBA{7, 200, 7, 0}) // 75% transparent green
			} else {
				img.SetNRGBA(x, y, color.NRGBA{255, 0, 0, 255})
			}
		}
	}
	DominantBGFrac = 0.5
	defer func() { DominantBGFrac = 0 }()
	bg := PrepareFromImage(img, 0).Background
	want := model.SRGBToLinear(1)
	if model.LinearLight {
		if bg.R < want-0.01 || bg.G > 0.01 {
			t.Errorf("background = (%.3f,%.3f,%.3f), want the opaque red", bg.R, bg.G, bg.B)
		}
	} else if bg.R < 0.99 || bg.G > 0.01 {
		t.Errorf("background = (%.3f,%.3f,%.3f), want the opaque red", bg.R, bg.G, bg.B)
	}
}
