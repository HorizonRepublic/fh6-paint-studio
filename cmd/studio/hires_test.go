package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"fh6-paint-studio/internal/ui"
)

// writeTestImage writes a w×h PNG whose every border pixel differs from its neighbours, so the
// auto-crop is a guaranteed no-op and the prepared dims depend only on the maxRes cap.
func writeTestImage(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x*7 + y*13), uint8(x * 3), uint8(y * 5), 255})
		}
	}
	p := filepath.Join(t.TempDir(), "src.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

// The hi-res fit must engage exactly when it can help: flat/anime on a source the display cap
// truncated. Photo, small sources and the demo path (no file) stay on the display prep.
func TestHiResPrep(t *testing.T) {
	big := writeTestImage(t, 1400, 2400) // display load would cap at 642x1100
	st := &ui.AppState{ImgPath: big}
	full := image.Rect(0, 0, 1400, 2400)

	hi := hiResPrep(st, "flat", full, 642, 1100)
	if hi == nil {
		t.Fatal("flat + big source: want a hi-res prep, got nil")
	}
	if hi.H != genMaxRes {
		t.Errorf("hi-res prep %dx%d, want long side %d", hi.W, hi.H, genMaxRes)
	}

	if got := hiResPrep(st, "photo", full, 642, 1100); got != nil {
		t.Errorf("photo mode: want nil (stays at display res), got %dx%d", got.W, got.H)
	}

	// Source at/below the display cap: the gate must skip without touching the file.
	stSmall := &ui.AppState{ImgPath: filepath.Join(t.TempDir(), "missing.png")}
	if got := hiResPrep(stSmall, "flat", image.Rect(0, 0, 800, 900), 800, 900); got != nil {
		t.Errorf("small source: want nil, got %dx%d", got.W, got.H)
	}

	// Demo path (no source file on disk).
	if got := hiResPrep(&ui.AppState{}, "anime", full, 642, 1100); got != nil {
		t.Errorf("no ImgPath: want nil, got %dx%d", got.W, got.H)
	}

	// Cropped state re-decodes the exact absolute rect (top half) — native 1400x1200 fits under the
	// cap. The display shows that crop capped to 1100x943.
	stCrop := &ui.AppState{ImgPath: big, Cropped: true}
	got := hiResPrep(stCrop, "anime", image.Rect(0, 0, 1400, 1200), 1100, 943)
	if got == nil {
		t.Fatal("cropped + big source: want a hi-res prep, got nil")
	}
	if got.W != 1400 || got.H != 1200 {
		t.Errorf("cropped prep %dx%d, want native 1400x1200", got.W, got.H)
	}
}
