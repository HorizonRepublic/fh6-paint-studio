package engine

import (
	"time"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/model"
)

// FoBa: a backward step taken DURING the greedy instead of only after it.
//
// The greedy is forward-only, and its objective is not submodular: a shape scored against the canvas
// of the moment can be made useless by the shapes placed after it, and nothing in the loop ever
// revisits that. LooRefit already exploits this — measuring true leave-one-out contributions in the
// FINAL stack, pruning the harmful ones and regrowing — and it is the largest quality win the project
// has recorded (-10.5% SSE). The obvious question it leaves open is timing: a shape that has been
// dead since shape 300 still occupies a slot for the remaining 700 placements, and the budget it
// wastes is budget the greedy never gets to spend on the residual it would have exposed.
//
// So this runs the same exact measurement periodically during the loop and drops what is already
// harmful. The slots return to the greedy immediately, which is the difference from LooRefit: there
// the freed budget is re-spent by a separate regrow against a finished residual, here it is spent by
// the greedy itself, in order, against the residual as it actually stands.
//
// Deliberately conservative: only shapes whose exact leave-one-out contribution is NEGATIVE are
// dropped — a shape that helps, however little, is left alone. There is no tolerance to tune and no
// way for the step to trade quality for slots; the only thing it can cost is the wall time of the
// measurement, which is why it runs every FoBaEvery shapes rather than every shape.

// fobaStep measures the current stack's exact leave-one-out contributions and removes the shapes
// that are actively harmful. It returns the surviving stack and the number dropped; the caller is
// responsible for re-rendering the backend, since dropping a shape invalidates the canvas.
//
// The background (index 0) is never a candidate: it is not a placed shape and the canvas is derived
// from it.
func fobaStep(shapes []model.Shape, target, weight []float32, w, h int, maxDrop int) ([]model.Shape, int) {
	if len(shapes) <= 2 || maxDrop <= 0 {
		return shapes, 0
	}
	fit := looFitContrib(shapes, target, weight, w, h)
	if len(fit) != len(shapes) {
		return shapes, 0
	}
	// Worst first, so a cap on the number dropped removes the most harmful rather than the earliest.
	type cand struct {
		idx  int
		gain float64
	}
	var bad []cand
	for i := 1; i < len(shapes); i++ {
		if fit[i] < 0 {
			bad = append(bad, cand{i, fit[i]})
		}
	}
	if len(bad) == 0 {
		return shapes, 0
	}
	for i := 1; i < len(bad); i++ {
		for j := i; j > 0 && bad[j].gain < bad[j-1].gain; j-- {
			bad[j], bad[j-1] = bad[j-1], bad[j]
		}
	}
	if len(bad) > maxDrop {
		bad = bad[:maxDrop]
	}
	drop := make(map[int]bool, len(bad))
	for _, b := range bad {
		drop[b.idx] = true
	}
	out := make([]model.Shape, 0, len(shapes))
	for i, s := range shapes {
		if !drop[i] {
			out = append(out, s)
		}
	}
	return out, len(drop)
}

// fobaMaybe runs a backward step if the schedule calls for one at this shape count, re-rendering the
// backend when anything was dropped. Returns the (possibly shortened) stack and its exact error.
//
// The error is re-measured rather than adjusted: dropping a shape changes what every shape above it
// composites over, so an incremental estimate would drift, and the greedy's accept threshold is
// compared against this number for the rest of the run.
func (r *run) fobaMaybe(placed int) {
	every := r.opt.FoBaEvery
	// Schedule on a HIGH-WATER mark, not on `placed % every`. Dropping shapes lowers the count, the
	// greedy grows it back to the same value, and a modulo test then fires again immediately — the
	// step thrashed a stack of 1000 into 859 drops that way. The mark only moves forward, so each
	// step is separated by `every` genuinely NEW placements.
	if every <= 0 || placed <= 0 || placed < r.fobaNext {
		return
	}
	r.fobaNext = placed + every
	t0 := time.Now()
	// Cap each step at a fraction of what has been placed: a single step that removed hundreds of
	// shapes would hand the greedy a residual unlike anything it has been scoring against, and the
	// LOO deltas themselves interact once many are removed at once.
	maxDrop := len(r.shapes) / 20
	if maxDrop < 1 {
		maxDrop = 1
	}
	kept, dropped := fobaStep(r.shapes, r.be.Target(), r.be.Weight(), r.w, r.h, maxDrop)
	if dropped == 0 {
		r.tm.PostProcess += time.Since(t0)
		return
	}
	r.shapes = kept
	r.finalErr = rerender(r.be, r.initCanvas, r.shapes)
	r.grid, r.gw, r.gh, _ = r.be.ErrorGrid()
	r.sampler = NewErrorSampler(r.grid, r.gw, r.gh, r.w, r.h)
	r.fobaDropped += dropped
	r.tm.PostProcess += time.Since(t0)
	applog.Printf("foba: at %d shapes dropped %d already-harmful (stack %d, err %.1f)",
		placed, dropped, len(r.shapes)-1, r.finalErr)
}
