package engine

import (
	"testing"

	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// A target that IS a dictionary stamp must be reconstructible by the glyph proposer far better
// than by any single primitive: after a handful of greedy shapes the winner set must contain a
// mask word. This is the end-to-end sanity for the moment-fit placement math.
func TestGlyphProposerNailsAStamp(t *testing.T) {
	bank := maskbank.All()
	var entry maskbank.Entry
	for _, e := range bank {
		if e.Word == 0x0081 { // comma1 — asymmetric, clearly non-elliptic
			entry = e
		}
	}
	if entry.W == 0 {
		t.Fatal("comma1 not in bank")
	}
	w, h := 96, 96
	target := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		target[i*4+0], target[i*4+1], target[i*4+2], target[i*4+3] = 1, 1, 1, 1 // white
	}
	stamp := model.Candidate{
		Kind:  entry.Kind,
		P:     [6]float32{48, 48, 64, 64, 30, 0},
		Color: model.RGBA{R: 0.1, G: 0.2, B: 0.8, A: 1},
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cov := raster.Coverage(stamp.Kind, stamp.P, x, y)
			if cov <= 0 {
				continue
			}
			a := float32(cov)
			p := (y*w + x) * 4
			target[p+0] = target[p+0]*(1-a) + stamp.Color.R*a
			target[p+1] = target[p+1]*(1-a) + stamp.Color.G*a
			target[p+2] = target[p+2]*(1-a) + stamp.Color.B*a
		}
	}

	be := newTestBackend(t, target, w, h, 8)
	res := Run(be, Options{Width: 96, Height: 96, StopAt: 6, Seed: 5, GlyphDict: true, MaxNoImprove: 8})
	found := false
	for _, s := range res.Shapes {
		if model.IsMask(model.KindFromType(s.Type)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no mask word among %d shapes reconstructing a pure dictionary stamp", len(res.Shapes))
	}
}
