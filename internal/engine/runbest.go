package engine

import (
	"fmt"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/backend"
)

// RunBest runs the full pipeline n times with decorrelated seeds and returns the attempt with the
// lowest final rendered error. Seed-to-seed spread is still ~6% after every shipped optimization
// (img_5 @3000: 1288.9..1372.6 across three seeds) — with wall-clock explicitly deprioritized by
// the owner, best-of-N converts that variance straight into quality. All attempts share the
// backend (Run re-renders from its own init canvas), and the winner's shapes are re-applied so
// the backend leaves holding the returned result.
func RunBest(be backend.Backend, opt Options, n int) Result {
	if n <= 1 {
		return Run(be, opt)
	}
	status := opt.Status
	var best Result
	have := false
	for i := 0; i < n; i++ {
		o := opt
		if status != nil {
			attempt := i + 1
			o.Status = func(stage string) { status(fmt.Sprintf("[%d/%d] %s", attempt, n, stage)) }
			o.Status("starting")
		}
		o.Seed = opt.Seed + int64(i)*7919
		res := Run(be, o)
		if res.DevErr != nil {
			// The device is gone for every remaining attempt too — stop looping. But a COMPLETE
			// best from an earlier attempt is a valid deliverable: hand it over clean rather than
			// discarding finished work; only fail the whole call when no attempt survived.
			//
			// The comparison has to come AFTER this branch. A run that loses the device returns
			// early with FinalError = r.finalErr, and finalErr is first assigned in postProcess —
			// so a loss during the greedy reports 0, which beat every finished attempt. Two good
			// attempts and a TDR on the third therefore threw both away and reported Failed, which
			// is exactly what the paragraph above says must not happen.
			if have {
				applog.Printf("best-of: attempt %d/%d lost the GPU device — keeping the completed best (error %.1f)", i+1, n, best.FinalError)
				return best
			}
			return res
		}
		if !have || res.FinalError < best.FinalError {
			best, have = res, true
		}
	}
	if !have {
		return best
	}
	// Leave the backend holding the winner (callers read the canvas / error grid after Run).
	bg := make([]float32, len(be.Target()))
	for j := 0; j < len(bg); j += 4 {
		bg[j], bg[j+1], bg[j+2], bg[j+3] = opt.Background.R, opt.Background.G, opt.Background.B, 1
	}
	if opt.TransparentBG {
		for j := range bg {
			bg[j] = 0
		}
	}
	_ = be.Reset(bg)
	applyShapes(be, best.Shapes[1:])
	return best
}
