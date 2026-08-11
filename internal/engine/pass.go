package engine

import (
	"time"

	"fh6-paint-studio/internal/applog"
)

// pass is one gated post-greedy refinement step. Each pass declares when it is enabled() for a given
// Options and applies itself to the run, mutating r.shapes / r.finalErr in place. Every pass is
// internally gated so it can never raise the hard-rendered error. Passes run in pipeline order; the
// back-fit / polish trio is mutually exclusive (their enabled predicates partition the
// BackFit-by-Polish combinations), so at most one of the three runs before the standout pass.
type pass interface {
	enabled(opt Options) bool
	apply(r *run)
}

// postPasses is the post-greedy pipeline, assembled in run order — the single place that wires the
// refinement steps together. Adding or removing a refinement is a one-line edit here.
func postPasses() []pass {
	return []pass{
		backfitPolishPass{},
		backfitPass{},
		softSwapPolishPass{},
		polishPass{},
		looRefitPass{},
		globalColorPass{},
		artifactFixPass{},
		annealPass{},
		zswapPass{},
		softSwapPass{},
		standoutPass{},
	}
}

// passTimer returns the Timings field a pass bills its own cost to, or nil when the pass is already
// billed from inside (the back-fit/polish trio times its two components at their call sites).
func (r *run) passTimer(p pass) *time.Duration {
	switch p.(type) {
	case looRefitPass:
		return &r.tm.LooRefit
	case globalColorPass:
		return &r.tm.GlobalColor
	case artifactFixPass:
		return &r.tm.ArtifactFix
	case annealPass:
		return &r.tm.Anneal
	case zswapPass:
		return &r.tm.ZSwap
	case softSwapPass, softSwapPolishPass:
		return &r.tm.SoftSwap
	case standoutPass:
		return &r.tm.Standout
	}
	return nil
}

// timePass bills f's wall time to d, minus whatever f nested into a field of its own — a pass that
// re-polishes or re-solves colours must not be credited with that time twice.
func (r *run) timePass(d *time.Duration, f func()) {
	nested := r.tm.Polish + r.tm.GlobalColor + r.tm.MergeRefit
	t0 := time.Now()
	f()
	*d += time.Since(t0) - (r.tm.Polish + r.tm.GlobalColor + r.tm.MergeRefit - nested)
}

// newBackfitEnv captures the greedy search context the back-fitting re-greedy needs: the same
// backend, RNG, kind mix, schedule, and on-device search as the main loop. tm is nil so the back-fit
// search time is billed to the BackFit bucket rather than the main phase timings.
func (r *run) newBackfitEnv() *greedyEnv {
	return &greedyEnv{
		be: r.be, rng: r.rng, w: r.w, h: r.h,
		kinds: r.kinds, kindWeights: r.kindWeights, kindCDF: r.kindCDF, orient: r.orient, coh: r.coh, aspectCap: r.aspectCap, compSeeds: r.opt.CompSeeds, kg: r.kindGate,
		devSearch: r.devSearch, allowAlpha: r.allowAlpha, alphaMin: r.alphaMin, aspectMax: r.opt.AspectMax,
		compact: r.opt.CompactPenalty, moveStep: r.moveStep, radiusStep: r.radiusStep,
		rounds: r.rounds, perRound: r.perRound, randomN: r.opt.RandomSamples, canvasPad: r.opt.CanvasPad, tm: nil,
	}
}

// backfitPolishPass handles back-fitting and polish both enabled. The two INTERACT: a back-fit that
// lowers the hard error can still move polish's basin and yield a WORSE final result, so they are
// gated end-to-end — polish the greedy result, polish the back-fitted result, keep whichever wins
// after polish.
type backfitPolishPass struct{}

func (backfitPolishPass) enabled(opt Options) bool { return opt.BackFit && opt.Polish }

func (backfitPolishPass) apply(r *run) {
	r.setStatus("Refining (back-fit + polish)…")
	w, h := r.w, r.h
	// Branch A: polish the greedy result (the no-back-fitting baseline).
	baseShapes, baseErr := applyPolish(r.be, cloneShapes(r.shapes), r.finalErr, r.initCanvas, r.opt, w, h, &r.tm)
	// Branch B: back-fit the greedy result, then polish it.
	bfStart := time.Now()
	bfCand, bfPre := runBackfitPasses(r.be, r.newBackfitEnv(), cloneShapes(r.shapes), r.finalErr, r.initCanvas, r.opt, w, h)
	r.tm.BackFit = time.Since(bfStart)
	bfShapes, bfErr := applyPolish(r.be, bfCand, bfPre, r.initCanvas, r.opt, w, h, &r.tm)
	r.tm.BackFitBase, r.tm.BackFitTrial = baseErr, bfErr // within-run comparison (noise-free with deterministic polish)
	// End-to-end gate: keep back-fitting only if it wins AFTER polish.
	if bfErr+1e-9 < baseErr {
		r.shapes, r.finalErr = bfShapes, bfErr
		return
	}
	r.shapes, r.finalErr = baseShapes, baseErr
	// Branch B left the backend rendering bfShapes — restore the winning baseline.
	_ = r.be.Reset(r.initCanvas)
	for _, s := range r.shapes[1:] {
		_ = r.be.Apply(shapeToCandidate(s))
	}
}

// backfitPass handles back-fitting with polish off: gate it directly on the hard-rendered error.
type backfitPass struct{}

func (backfitPass) enabled(opt Options) bool { return opt.BackFit && !opt.Polish }

func (backfitPass) apply(r *run) {
	r.setStatus("Back-fitting…")
	bfStart := time.Now()
	r.shapes, r.finalErr = runBackfitPasses(r.be, r.newBackfitEnv(), r.shapes, r.finalErr, r.initCanvas, r.opt, r.w, r.h)
	r.tm.BackFit = time.Since(bfStart)
}

// polishPass handles the joint differentiable polish with back-fitting off.
type polishPass struct{}

func (polishPass) enabled(opt Options) bool {
	return opt.Polish && !opt.BackFit && !(opt.SoftSwapPre && opt.SoftSwapTol > 0)
}

func (polishPass) apply(r *run) {
	r.setStatus("Polishing…")
	r.shapes, r.finalErr = applyPolish(r.be, r.shapes, r.finalErr, r.initCanvas, r.opt, r.w, r.h, &r.tm)
}

// annealPass is the EXPERIMENTAL basin-hopping / iterated-local-search pass (opt-in via AnnealIters>0):
// it wraps the polished result in an outer kick + re-polish + Metropolis-accept loop to escape the greedy
// local minimum, keeping the best. For the low-budget "economy" regime. Runs AFTER the polish trio so it
// starts from a fully-polished optimum.
type annealPass struct{}

func (annealPass) enabled(opt Options) bool { return opt.AnnealIters > 0 }

func (annealPass) apply(r *run) {
	r.setStatus("Annealing…")
	r.shapes, r.finalErr = anneal(r.be, r.newBackfitEnv(), r.shapes, r.finalErr,
		r.initCanvas, r.be.Target(), r.be.Weight(), r.opt, r.w, r.h, &r.tm, r.rng)
}

// softSwapPolishPass is the PRE-polish soft-swap + polish combination (SoftSwapPre): it replaces
// polishPass in the trio partition and gates end-to-end inside softSwapPrePolish.
type softSwapPolishPass struct{}

func (softSwapPolishPass) enabled(opt Options) bool {
	return opt.SoftSwapPre && opt.SoftSwapTol > 0 && opt.Polish && !opt.BackFit
}

func (softSwapPolishPass) apply(r *run) {
	r.setStatus("Soft-swapping + polishing…")
	softSwapPrePolish(r)
}

// softSwapPass replaces standout rect/triangle shapes with a soft shape moment-fitted to the same
// footprint (substitution keeps the coverage, so the SSE gate starves it far less than the standout
// pass's remove/recolour menu). Runs before standoutPass so that pass can still clean what a swap
// couldn't fix. Opt-in via SoftSwapTol > 0 (post-polish form; SoftSwapPre routes to the combined pass).
type softSwapPass struct{}

func (softSwapPass) enabled(opt Options) bool { return opt.SoftSwapTol > 0 && !opt.SoftSwapPre }

func (softSwapPass) apply(r *run) {
	r.setStatus("Soft-swapping standouts…")
	r.shapes, r.finalErr = softSwapStandouts(r.be, r.shapes, r.finalErr, r.initCanvas, r.opt, r.w, r.h)
}

// standoutPass is the final perceptual pass: suppress standout shapes whose rim draws an edge the
// target lacks (a circle/square the SSE metric is blind to), gated so the global error rises at most
// StandoutTol. Opt-in via StandoutTol > 0.
type standoutPass struct{}

func (standoutPass) enabled(opt Options) bool { return opt.StandoutTol > 0 }

func (standoutPass) apply(r *run) {
	r.setStatus("Smoothing standouts…")
	r.shapes, r.finalErr = suppressStandouts(r.be, r.shapes, r.finalErr, r.initCanvas, r.opt, r.w, r.h)
}

// globalColorPass re-solves every layer's colour jointly for the frozen geometry (globalcolor.go).
// It runs AFTER the LOO refit, because both the pruning and the regrow change which shapes are in
// the stack, and the solve is only meaningful for the stack that ships.
//
// Gated on the backend's own hard render, like every other pass: the solve minimises a weighted
// SSE over SOFT-free, exact coverage, but the shipped error is what the device measures, and the
// two can disagree at the sub-quantisation level once colours are rounded back to bytes.
type globalColorPass struct{}

func (globalColorPass) enabled(opt Options) bool { return opt.GlobalColorIters > 0 }

func (globalColorPass) apply(r *run) {
	r.setStatus("Re-solving colours…")
	t0 := time.Now()
	// Alpha rides the same frozen geometry (block coordinate descent, colours then alphas). Its
	// floor is the preset's organic one, so the sweep cannot re-open the hole the polish used to
	// leave by optimising alpha down to 0.05.
	cand, before, after, ok := globalColorAlphaSolve(r.shapes, r.be.Target(), r.be.Weight(), r.w, r.h,
		r.opt.Background, r.opt.TransparentBG, r.opt.GlobalColorIters,
		r.opt.GlobalAlphaSweeps, polishAlphaFloor(r.opt.AlphaMin))
	if !ok {
		return
	}
	candErr := rerender(r.be, r.initCanvas, cand)
	applog.Printf("global-color: solve %.1f -> %.1f, hard %.1f -> %.1f (%.2f%%) in %.1fs",
		before, after, r.finalErr, candErr, 100*(candErr-r.finalErr)/r.finalErr, time.Since(t0).Seconds())
	if candErr < r.finalErr {
		r.shapes, r.finalErr = cand, candErr
		return
	}
	_ = rerender(r.be, r.initCanvas, r.shapes) // leave the backend rendering what we keep
}
