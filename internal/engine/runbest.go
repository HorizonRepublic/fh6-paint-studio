package engine

import (
	"fmt"

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
	for i := 0; i < n; i++ {
		o := opt
		if status != nil {
			attempt := i + 1
			o.Status = func(stage string) { status(fmt.Sprintf("[%d/%d] %s", attempt, n, stage)) }
			o.Status("starting")
		}
		o.Seed = opt.Seed + int64(i)*7919
		res := Run(be, o)
		if i == 0 || res.FinalError < best.FinalError {
			best = res
		}
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
	for _, s := range best.Shapes[1:] {
		_ = be.Apply(shapeToCandidate(s))
	}
	return best
}
