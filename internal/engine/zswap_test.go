//go:build cuda

package engine

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

// TestZOrderSwapRecoversOccludedShape: a red square the target wants is occluded by a later
// blue cover — the one ordering greedy can never revisit. The swap pass must reorder the pair
// and recover the red square, dropping the hard error.
func TestZOrderSwapRecoversOccludedShape(t *testing.T) {
	w, h := 32, 32
	bg := model.RGBA{R: 0, G: 0, B: 1, A: 1}
	target := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := (y*w + x) * 4
			if x >= 12 && x < 20 && y >= 12 && y < 20 {
				target[p+0], target[p+3] = 1, 1 // red centre square
			} else {
				target[p+2], target[p+3] = 1, 1 // blue elsewhere
			}
		}
	}

	rect := func(cx, cy, rx, ry float32, col model.RGBA) model.Shape {
		return model.Candidate{Kind: model.KindRectangle, P: [6]float32{cx, cy, rx, ry, 0, 0}, Color: col}.ToShape(0)
	}
	shapes := []model.Shape{
		rect(16, 16, 16, 16, bg),                   // background
		rect(16, 16, 4, 4, model.RGBA{R: 1, A: 1}), // the red square the target wants...
		rect(16, 16, 8, 8, model.RGBA{B: 1, A: 1}), // ...occluded by a later blue cover
	}

	be := newTestBackend(t, target, w, h, 8)
	initCanvas := backgroundCanvas(bg, w, h)
	inErr := renderExcept(be, initCanvas, shapes, -1)
	if inErr <= 0 {
		t.Fatal("test setup broken: occlusion should leave error")
	}

	out, outErr := zOrderSwap(be, shapes, inErr, initCanvas, Options{ZSwapTrials: 8}, w, h)
	if outErr >= inErr {
		t.Fatalf("swap pass failed to recover the occluded square: in=%.1f out=%.1f", inErr, outErr)
	}
	if model.KindFromType(out[1].Type) != model.KindRectangle || out[1].Color[2] != 255 {
		t.Fatalf("expected the blue cover to move under the red square, got order %+v", out[1])
	}
}
