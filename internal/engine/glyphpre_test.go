package engine

import (
	"testing"

	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// An anti-aliased dictionary stamp on a flat background must be claimed by the pre-pass:
// the AA fringe is absorbed back into the component (a raw quantized labeling would erode the
// silhouette and fail the IoU gate on a feature this small) and the placement refinement must
// recover the rotation between two 15° signature bins.
func TestGlyphPrepassClaimsAStamp(t *testing.T) {
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
	// both rotation directions: the signature shift NEGATES the blob rotation — a sign
	// error here claims only rotation-symmetric shapes and is invisible on ring-like comps
	for _, rot := range []float32{22, 287} {
		w, h := 128, 128
		target := make([]float32, w*h*4)
		for i := 0; i < w*h; i++ {
			target[i*4+0], target[i*4+1], target[i*4+2], target[i*4+3] = 0.9, 0.9, 0.9, 1
		}
		stamp := model.Candidate{
			Kind:  entry.Kind,
			P:     [6]float32{64, 64, 80, 80, rot, 0}, // off the 15° signature bins
			Color: model.RGBA{R: 0.1, G: 0.2, B: 0.7, A: 1},
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
		GlyphPrepassDemandStart()
		Run(be, Options{Width: w, Height: h, StopAt: 6, Seed: 5, GlyphPrepass: true, MaxNoImprove: 8})
		_, window, _, _, claimed, iouBest, _ := GlyphPrepassDemandReport()
		if window == 0 {
			t.Fatalf("rot=%v: the stamp never reached the matching window", rot)
		}
		if claimed == 0 {
			t.Fatalf("rot=%v: pre-pass claimed nothing; best IoU of misses: %v", rot, iouBest)
		}
	}
}
