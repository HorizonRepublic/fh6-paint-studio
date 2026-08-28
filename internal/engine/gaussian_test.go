package engine

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

// Index 0 is the one slot the pipeline treats as "not a shape" — the polish builds its parameter
// set from shapes[1:] — so a transparent gaussian run must still put a background rect there or its
// first glow ships untrained.
func TestGaussianTransparentKeepsEveryGlowTrainable(t *testing.T) {
	const w, h = 48, 48
	target := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		target[i*4], target[i*4+1], target[i*4+2], target[i*4+3] = 0.5, 0.3, 0.7, 1
	}
	be := newTestBackend(t, target, w, h, 16)
	res := GenerateGaussian(be, Options{
		Width: w, Height: h, StopAt: 8, TransparentBG: true,
		PolishOpts: PolishOptions{Iters: 4},
	})
	if len(res.Shapes) < 2 {
		t.Fatalf("got %d shapes, want a background rect plus glows", len(res.Shapes))
	}
	if res.Shapes[0].Type != model.TypeRectangle {
		t.Errorf("shapes[0] type %d; want the background rect (%d)", res.Shapes[0].Type, model.TypeRectangle)
	}
	if a := res.Shapes[0].Color[3]; a != 0 {
		t.Errorf("transparent run background alpha %d; want 0", a)
	}
	for i := 1; i < len(res.Shapes); i++ {
		if res.Shapes[i].Type != model.TypeGradGlow {
			t.Errorf("shapes[%d] type %d; want a glow", i, res.Shapes[i].Type)
		}
	}
}
