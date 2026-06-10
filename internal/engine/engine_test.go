package engine

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/backend/cpu"
	"fh6-paint-studio/internal/model"
)

// bgFromTarget computes the mean color of a target buffer.
func bgFromTarget(target []float32, w, h int) model.RGBA {
	var r, g, b, a float64
	n := float64(w * h)
	for i := 0; i < w*h; i++ {
		r, g, b, a = r+float64(target[i*4]), g+float64(target[i*4+1]), b+float64(target[i*4+2]), a+float64(target[i*4+3])
	}
	return model.RGBA{R: float32(r / n), G: float32(g / n), B: float32(b / n), A: float32(a / n)}
}

// stopIndex feeds an error sequence to a fresh detector and returns the 1-based shape
// count at which it fires (or -1 if it never fires).
func stopIndex(tol float64, errs []float64) int {
	k := newKneeDetector(tol, 0, 0) // floor off → legacy pure-relative behaviour
	for i, e := range errs {
		if k.push(e) {
			return i + 1
		}
	}
	return -1
}

// TestKneeDetector verifies the auto-shape-count knee logic on synthetic error curves:
// a plateauing (exp-decay) curve must fire after window+sustain; a steadily-improving
// curve must NOT fire; tol<=0 never fires; and the RELATIVE rate makes the stop point
// invariant to the absolute error scale (the property that lets one tol fit all content).
func TestKneeDetector(t *testing.T) {
	// Exponential decay toward a floor: per-shape relative improvement decays -> must fire.
	expDecay := func(floor, amp, scale float64, n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = floor + amp*math.Exp(-float64(i)/scale)
		}
		return out
	}
	curve := expDecay(100, 1000, 300, 2200)
	got := stopIndex(2e-3, curve)
	if got < 300 { // cannot fire before window(100)+sustain(200)
		t.Fatalf("plateau curve fired too early: %d (need >= 300)", got)
	}
	if got < 0 || got >= len(curve) {
		t.Fatalf("plateau curve never fired (or at end): %d", got)
	}

	// Scale invariance: multiplying every error by 1000 must give the SAME stop index,
	// because the rate is relative to current error. This is the cross-content property.
	scaled := make([]float64, len(curve))
	for i, e := range curve {
		scaled[i] = e * 1000
	}
	if g2 := stopIndex(2e-3, scaled); g2 != got {
		t.Fatalf("rate not scale-invariant: base=%d scaled=%d", got, g2)
	}

	// A steadily-improving curve (constant relative improvement) must NEVER fire — each
	// shape keeps reducing error by a fixed fraction, so the rate stays above tol.
	geo := make([]float64, 2200)
	geo[0] = 1e6
	for i := 1; i < len(geo); i++ {
		geo[i] = geo[i-1] * 0.997 // 0.3%/shape -> rate ~3e-3/shape, above 2e-3 tol
	}
	if g := stopIndex(2e-3, geo); g != -1 {
		t.Fatalf("steadily-improving curve should not fire, fired at %d", g)
	}

	// tol<=0 disables the detector.
	if g := stopIndex(0, curve); g != -1 {
		t.Fatalf("tol=0 should never fire, fired at %d", g)
	}

	// Lower tol => stops later (more shapes) on the same curve.
	loose := stopIndex(5e-3, curve)
	tight := stopIndex(5e-4, curve)
	if !(loose > 0 && tight > loose) {
		t.Fatalf("expected tighter tol to stop later: loose(5e-3)=%d tight(5e-4)=%d", loose, tight)
	}
}

func TestRunReducesErrorAndProducesShapes(t *testing.T) {
	w, h := 32, 32
	target := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := (y*w + x) * 4
			if x < w/2 {
				target[p+0] = 1 // left half red
			} else {
				target[p+2] = 1 // right half blue
			}
			target[p+3] = 1
		}
	}
	be := cpu.New(target, w, h, 8)
	res := Run(be, Options{Width: w, Height: h, Background: bgFromTarget(target, w, h), StopAt: 40, RandomSamples: 200, MutatedSamples: 100, Seed: 1})
	if len(res.Shapes) < 2 {
		t.Fatalf("got %d shapes, want >=2 (background + ellipses)", len(res.Shapes))
	}
	if res.FinalError >= res.InitialError {
		t.Fatalf("final error %v not below initial %v", res.FinalError, res.InitialError)
	}
}

// renderHardErr renders shapes through the CPU backend (the WYSIWYG hard raster) and returns
// the unweighted SSE vs target — the same measurement applyPolish's accept gate uses.
func renderHardErr(shapes []model.Shape, target []float32, w, h int, bg model.RGBA) float64 {
	be := cpu.New(target, w, h, 8)
	init := backgroundCanvas(bg, w, h)
	_ = be.Reset(init)
	for _, s := range shapes[1:] {
		_ = be.Apply(shapeToCandidate(s))
	}
	grid, _, _, _ := be.ErrorGrid()
	return sumGrid(grid)
}

// TestPolishTightInputImproves pins the fine-exploit phase: on a SATURATED input — many shapes
// whose geometry already fits the target exactly, only the colours slightly off — polish must
// come back IMPROVED. The historical failure mode: the tau-anneal excursion kicked every param
// of every shape by the full Adam LR, exploded the hard loss ~3×, and the whole iteration
// budget went into crawling back — polish never re-beat its own input, the gate discarded it,
// and the pass was a silent no-op on every full-budget run (exactly what users saw as "quality
// degrades during polishing"). The fine phase descends carefully from the best-known point, so
// the recoverable colour error here MUST be harvested.
func TestPolishTightInputImproves(t *testing.T) {
	w, h := 96, 96
	const grid = 12
	cell := w / grid
	target := make([]float32, w*h*4)
	colAt := func(gx, gy int) (float32, float32, float32) {
		return float32(gx) / grid, float32(gy) / grid, 0.5
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := colAt(x/cell, y/cell)
			p := (y*w + x) * 4
			target[p+0], target[p+1], target[p+2], target[p+3] = r, g, b, 1
		}
	}
	bg := model.RGBA{R: 0.5, G: 0.5, B: 0.5, A: 1}

	shapes := []model.Shape{{Type: model.TypeRectangle, Data: []float64{0, 0, float64(w), float64(h)},
		Color: []int{model.EncByte(bg.R), model.EncByte(bg.G), model.EncByte(bg.B), 255}}}
	for gy := 0; gy < grid; gy++ {
		for gx := 0; gx < grid; gx++ {
			r, g, b := colAt(gx, gy)
			// Exact cell geometry; colour off by a small recoverable amount per channel.
			c := model.Candidate{
				Kind:  model.KindRectangle,
				P:     [6]float32{float32(gx*cell) + float32(cell)/2, float32(gy*cell) + float32(cell)/2, float32(cell) / 2, float32(cell) / 2, 0, 0},
				Color: model.RGBA{R: r + 0.06, G: g - 0.05, B: b + 0.05, A: 1},
			}
			shapes = append(shapes, c.ToShape(0))
		}
	}

	inErr := renderHardErr(shapes, target, w, h, bg)
	if inErr <= 0 {
		t.Fatal("test setup broken: input already perfect")
	}
	pr := Polish(shapes, target, onesWeight(w, h), w, h, bg, false, PolishOptions{
		Iters: 200, Tau0: 2.0, Tau1: 0.08,
		LRPos: 0.5, LRRad: 0.5, LRAng: 0.5, LRColor: 0.01, LRAlpha: 0.01,
		GradClip: 8, STE: true,
	})
	outErr := renderHardErr(pr.Shapes, target, w, h, bg)
	if outErr >= inErr*0.7 {
		t.Fatalf("polish failed to harvest the recoverable colour error on a tight input: in=%.1f out=%.1f (want < 70%%)", inErr, outErr)
	}
}

func onesWeight(w, h int) []float32 {
	out := make([]float32, w*h)
	for i := range out {
		out[i] = 1
	}
	return out
}
