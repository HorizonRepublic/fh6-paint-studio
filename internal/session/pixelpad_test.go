package session

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"fh6-paint-studio/internal/pixel"
	"fh6-paint-studio/internal/preset"
)

// Pixel mode must not get the keep-inside surround. The mode recovers the sprite's logical pixel size
// as the GCD of every colour-change boundary, and a margin contributes boundaries of its own — so a
// padded sprite detects a step of 1 and the decomposition explodes past the shape budget. Keep-inside
// is the client's default, so this is the difference between the mode working and the mode failing.
func TestPixelModeSkipsTheSurround(t *testing.T) {
	const step, cells = 8, 16
	side := step * cells

	img := image.NewNRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			cx, cy := x/step, y/step
			v := uint8((cx*3 + cy*5) % 4 * 60)
			img.SetNRGBA(x, y, color.NRGBA{v, v, v, 255})
		}
	}
	path := filepath.Join(t.TempDir(), "sprite.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	ch := preset.DefaultChoices()
	ch.Mode = "pixel"
	run, err := Prepare(Request{Path: path, KeepInside: true, Choices: ch})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if run.PadPx != 0 {
		t.Errorf("pixel run padded by %dpx — the surround breaks the grid detection", run.PadPx)
	}
	if got := pixel.DetectGrid(run.Prep.Pixels, run.Prep.W, run.Prep.H); got != step {
		t.Errorf("DetectGrid on the prepared image = %d, want %d", got, step)
	}
	if _, err := pixel.Generate(run.Prep.Pixels, run.Prep.W, run.Prep.H, preset.MaxShapes); err != nil {
		t.Errorf("Generate: %v", err)
	}
}

// Every other mode still gets it — the surround is what bounds shapes to the content rectangle.
func TestNonPixelModeStillPads(t *testing.T) {
	path := writeTestImage(t, 64, 64)
	ch := preset.DefaultChoices()
	ch.Mode = "anime"
	run, err := Prepare(Request{Path: path, KeepInside: true, Choices: ch})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if run.PadPx <= 0 {
		t.Errorf("anime run was not padded (PadPx=%d)", run.PadPx)
	}
}
