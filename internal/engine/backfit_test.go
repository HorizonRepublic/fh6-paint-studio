//go:build cuda

package engine

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

// TestBackFitNeverIncreasesError verifies the back-fitting pass is strictly gated:
// because each pass is kept only if it lowers the rendered error, a run WITH
// back-fitting can never finish worse than the same run WITHOUT it. The two runs
// share a seed, so the greedy phase is byte-identical (back-fitting's RNG draws
// happen only after greedy+post-process), making the comparison exact.
func TestBackFitNeverIncreasesError(t *testing.T) {
	w, h := 48, 48
	target := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := (y*w + x) * 4
			switch { // four colored quadrants
			case x < w/2 && y < h/2:
				target[p+0] = 1
			case x >= w/2 && y < h/2:
				target[p+1] = 1
			case x < w/2 && y >= h/2:
				target[p+2] = 1
			default:
				target[p+0], target[p+1] = 1, 1
			}
			target[p+3] = 1
		}
	}
	opts := func(bf bool) Options {
		return Options{
			Width: w, Height: h, Background: bgFromTarget(target, w, h),
			StopAt: 24, RandomSamples: 200, MutatedSamples: 100, Seed: 1,
			Kinds:         []model.ShapeKind{model.KindEllipse, model.KindRectangle},
			BackFit:       bf,
			BackFitPasses: 2,
			BackFitFrac:   0.25,
		}
	}
	base := Run(newTestBackend(t, target, w, h, 8), opts(false))
	bf := Run(newTestBackend(t, target, w, h, 8), opts(true))
	if bf.FinalError > base.FinalError+1e-6 {
		t.Fatalf("back-fitting increased error: %.6f (backfit) > %.6f (baseline)", bf.FinalError, base.FinalError)
	}
	// The pass must place no more shapes than the budget (removal + regrowth nets to <= count).
	if len(bf.Shapes) > len(base.Shapes) {
		t.Fatalf("back-fitting grew the shape count: %d > %d", len(bf.Shapes), len(base.Shapes))
	}
}

// TestBackFitPolishEndToEndGate verifies the END-TO-END gate: with both back-fitting and
// polish enabled, a run can never finish worse than polish-only. Same seed => identical greedy
// phase and identical baseline-branch polish (polish is deterministic); back-fitting is kept
// only if it wins AFTER polish — the fix for pre-polish gating misjudging the final result.
func TestBackFitPolishEndToEndGate(t *testing.T) {
	w, h := 48, 48
	target := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := (y*w + x) * 4
			if (x/8+y/8)%2 == 0 { // checkerboard of red / blue blocks
				target[p+0] = 1
			} else {
				target[p+2] = 1
			}
			target[p+3] = 1
		}
	}
	po := PolishOptions{Iters: 20, Tau0: 2.0, Tau1: 0.15}
	opts := func(bf bool) Options {
		return Options{
			Width: w, Height: h, Background: bgFromTarget(target, w, h),
			StopAt: 20, RandomSamples: 200, MutatedSamples: 100, Seed: 1,
			Kinds:         []model.ShapeKind{model.KindEllipse, model.KindRectangle},
			Polish:        true,
			PolishOpts:    po,
			BackFit:       bf,
			BackFitPasses: 2,
			BackFitFrac:   0.25,
		}
	}
	base := Run(newTestBackend(t, target, w, h, 8), opts(false)) // polish only
	bf := Run(newTestBackend(t, target, w, h, 8), opts(true))    // polish + back-fitting (end-to-end gated)
	if bf.FinalError > base.FinalError+1e-6 {
		t.Fatalf("end-to-end gate failed: backfit+polish %.6f > polish-only %.6f", bf.FinalError, base.FinalError)
	}
}
