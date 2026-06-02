package engine_test

import (
	"testing"

	"fh6-paint-studio/internal/backend/cpu"
	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/model"
)

// makeGradientTarget builds a smooth w*h RGBA gradient. Smoothness matters: the
// greedy loop keeps finding improving shapes for many iterations, so a no-cancel
// control run places clearly more shapes than a run cancelled after a few — the
// signal the cancel test asserts on.
func makeGradientTarget(w, h int) []float32 {
	t := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			t[i] = float32(x) / float32(w)
			t[i+1] = float32(y) / float32(h)
			t[i+2] = float32(x+y) / float32(w+h)
			t[i+3] = 1
		}
	}
	return t
}

func cancelBaseOpts(w, h, stop int) engine.Options {
	return engine.Options{
		Width: w, Height: h, StopAt: stop,
		RandomSamples: 120, MutatedSamples: 0, Seed: 1,
		Kinds:      []model.ShapeKind{model.KindEllipse},
		Background: model.RGBA{R: 0, G: 0, B: 0, A: 1},
	}
}

// TestCancelStopsEarly: Options.Cancel returning true after ~5 placed shapes ends
// the run promptly, while an identical un-cancelled run places many more.
func TestCancelStopsEarly(t *testing.T) {
	const w, h = 32, 32
	tgt := makeGradientTarget(w, h)

	beC := cpu.New(append([]float32(nil), tgt...), w, h, 16)
	var placed int
	optC := cancelBaseOpts(w, h, 30)
	optC.Progress = func(n int, _ float64) { placed = n }
	optC.Cancel = func() bool { return placed >= 5 }
	resC := engine.Run(beC, optC)
	if got := len(resC.Shapes); got > 8 {
		t.Fatalf("cancelled run placed too many shapes: got %d (want <=8: bg + ~5)", got)
	}

	beN := cpu.New(append([]float32(nil), tgt...), w, h, 16)
	resN := engine.Run(beN, cancelBaseOpts(w, h, 30))
	if len(resN.Shapes) < 12 {
		t.Fatalf("control run should keep improving on a gradient, placed only %d", len(resN.Shapes))
	}
	if len(resN.Shapes) <= len(resC.Shapes) {
		t.Fatalf("control (%d) should place more than cancelled (%d)", len(resN.Shapes), len(resC.Shapes))
	}
}

// TestCancelNilUnchanged: a nil Cancel behaves exactly as before (no panic, shapes placed).
func TestCancelNilUnchanged(t *testing.T) {
	const w, h = 32, 32
	be := cpu.New(makeGradientTarget(w, h), w, h, 16)
	res := engine.Run(be, cancelBaseOpts(w, h, 20))
	if len(res.Shapes) < 2 {
		t.Fatalf("nil-cancel run should place shapes, got %d", len(res.Shapes))
	}
}
