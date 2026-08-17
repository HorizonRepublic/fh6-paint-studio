package preset

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/imageio"
)

func plane(n int, r, g, b, a float32) []float32 {
	px := make([]float32, n*4)
	for i := 0; i < n; i++ {
		px[i*4], px[i*4+1], px[i*4+2], px[i*4+3] = r, g, b, a
	}
	return px
}

// The keep-inside surround must not decide the shadow-weight pair. It is transparent black and it is
// roughly a third of a padded canvas, so counting it as dark pushed every padded run over the 0.35
// threshold — a threshold calibrated on unpadded images, through a CLI that does not pad.
func TestDarkFracIgnoresTheTransparentSurround(t *testing.T) {
	const side = 64
	light := plane(side*side, 0.6, 0.6, 0.6, 1)
	bare := &imageio.Prepared{W: side, H: side, Pixels: light}
	if f := DarkFrac(bare.Pixels); f != 0 {
		t.Fatalf("bare light image: DarkFrac = %.3f, want 0", f)
	}
	padded, pad := imageio.PadTransparent(bare, 0.10)
	if pad <= 0 {
		t.Fatal("no surround was added")
	}
	margin := 1 - float64(side*side)/float64(padded.W*padded.H)
	if margin < 0.25 {
		t.Fatalf("surround is only %.1f%% of the canvas — the test premise is wrong", margin*100)
	}
	f := DarkFrac(padded.Pixels)
	if f != 0 {
		t.Errorf("padded light image: DarkFrac = %.3f, want 0 (the margin is %.1f%% of the canvas)", f, margin*100)
	}
	if clamp, cap := DarkWeightParams(f); clamp != 0.02 || cap != 16 {
		t.Errorf("padded light image resolved the STRONG shadow pair (%.4f/%.0f)", clamp, cap)
	}
}

// Opaque content is bit-identical to what it was, which is what keeps the 0.35 calibration valid.
func TestDarkFracUnchangedOnOpaqueContent(t *testing.T) {
	const n = 1000
	px := plane(n, 0, 0, 0, 1)
	for i := 0; i < 700; i++ { // 300 dark, 700 light
		px[i*4], px[i*4+1], px[i*4+2] = 0.5, 0.5, 0.5
	}
	if f := DarkFrac(px); math.Abs(f-0.3) > 1e-9 {
		t.Errorf("DarkFrac = %.6f, want 0.3", f)
	}
}

// A genuine cutout carries the user's own transparency, and it was hitting the same arithmetic.
func TestDarkFracOnACutout(t *testing.T) {
	const n = 1000
	px := plane(n, 0.7, 0.7, 0.7, 1)
	for i := 0; i < 800; i++ { // 80% of the frame is empty around the subject
		px[i*4], px[i*4+1], px[i*4+2], px[i*4+3] = 0, 0, 0, 0
	}
	if f := DarkFrac(px); f != 0 {
		t.Errorf("cutout with a light subject: DarkFrac = %.3f, want 0", f)
	}
	if f := DarkFrac(plane(n, 0, 0, 0, 0)); f != 0 {
		t.Errorf("a fully transparent plane: DarkFrac = %.3f, want 0", f)
	}
}
