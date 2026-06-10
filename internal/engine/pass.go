package engine

import "time"

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
		polishPass{},
		annealPass{},
		zswapPass{},
		standoutPass{},
	}
}

// newBackfitEnv captures the greedy search context the back-fitting re-greedy needs: the same
// backend, RNG, kind mix, schedule, and on-device search as the main loop. tm is nil so the back-fit
// search time is billed to the BackFit bucket rather than the main phase timings.
func (r *run) newBackfitEnv() *greedyEnv {
	return &greedyEnv{
		be: r.be, rng: r.rng, w: r.w, h: r.h,
		kinds: r.kinds, kindWeights: r.kindWeights, kindCDF: r.kindCDF, orient: r.orient,
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

func (polishPass) enabled(opt Options) bool { return opt.Polish && !opt.BackFit }

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

// standoutPass is the final perceptual pass: suppress standout shapes whose rim draws an edge the
// target lacks (a circle/square the SSE metric is blind to), gated so the global error rises at most
// StandoutTol. Opt-in via StandoutTol > 0.
type standoutPass struct{}

func (standoutPass) enabled(opt Options) bool { return opt.StandoutTol > 0 }

func (standoutPass) apply(r *run) {
	r.setStatus("Smoothing standouts…")
	r.shapes, r.finalErr = suppressStandouts(r.be, r.shapes, r.finalErr, r.initCanvas, r.opt, r.w, r.h)
}
