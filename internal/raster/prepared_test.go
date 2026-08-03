package raster

import (
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/model"
)

// A prepared shape must agree with the plain per-pixel path EXACTLY — it is the same math with the
// per-shape constants hoisted, and the engine's gates compare deltas that would drift if it were
// merely close.
func TestPreparedMatchesPlain(t *testing.T) {
	kinds := []model.ShapeKind{model.KindEllipse, model.KindRectangle, model.KindTriangle, model.KindLine, model.KindGlow, model.KindDisk}
	n := 0
	for k := range maskTexByKind { // a handful of bank words is enough to cover the mask path
		kinds = append(kinds, k)
		if n++; n >= 5 {
			break
		}
	}
	rng := rand.New(rand.NewSource(7))
	for _, kind := range kinds {
		for trial := 0; trial < 40; trial++ {
			var p [6]float32
			for i := range p {
				p[i] = float32(rng.Float64()*160 - 20)
			}
			if kind != model.KindTriangle && kind != model.KindLine {
				p[2] = float32(rng.Float64()*40 + 1) // extents stay positive
				p[3] = float32(rng.Float64()*40 + 1)
				p[4] = float32(rng.Float64() * 360)
			}
			pr := Prep(kind, p)
			for s := 0; s < 60; s++ {
				x, y := rng.Intn(120), rng.Intn(120)
				if got, want := pr.Coverage(x, y), Coverage(kind, p, x, y); got != want {
					t.Fatalf("kind %v coverage at (%d,%d): prepared %v, plain %v (p=%v)", kind, x, y, got, want, p)
				}
				if got, want := pr.Inside(x, y), Inside(kind, p, x, y); got != want {
					t.Fatalf("kind %v inside at (%d,%d): prepared %v, plain %v (p=%v)", kind, x, y, got, want, p)
				}
			}
		}
	}
}
