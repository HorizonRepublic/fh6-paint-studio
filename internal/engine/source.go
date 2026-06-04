package engine

import (
	"time"

	"fh6-paint-studio/internal/model"
)

// shapeSource proposes the best candidate for the next greedy shape at a given progress. Putting the
// per-shape search behind this interface lets the generation strategy be swapped without touching the
// greedy loop: the blind random brute force, the moment-seeded search, or the hybrid that hands off
// between them. The returned score is RAW (a value below zero improves the canvas); search accumulates
// its own generate/evaluate timings into r.tm. The hill-climb mutate that refines the winner is shared
// by the loop (run.searchOne), not by the source.
type shapeSource interface {
	search(r *run, progress float32, sampGrid []float32, penalty func(model.Candidate) float32) (model.Candidate, float32)
}

// newShapeSource picks the generation strategy from the options: the blind random search by default,
// or, when moment-seeding is enabled, the hybrid that lays a moment-fitted coarse base and then hands
// off to the random search past MomentDetailStart (a start of 0 keeps the moment search the whole way).
func newShapeSource(opt Options) shapeSource {
	if !opt.MomentSeed {
		return randomSource{}
	}
	return hybridSource{detailStart: opt.MomentDetailStart}
}

// randomSource is the blind random search: generate RandomSamples candidates — on-device in a single
// call when the backend supports it, otherwise on the host — score them, and keep the best. On an
// older DLL without the on-device export it clears r.devSearch and uses the host path for the rest of
// the run.
type randomSource struct{}

func (randomSource) search(r *run, progress float32, sampGrid []float32, penalty func(model.Candidate) float32) (model.Candidate, float32) {
	w, h := r.w, r.h
	if r.devSearch != nil {
		// GPU: generate+score+argmin for the whole random batch in one call. compactPenalty is
		// applied on-device for selection; the returned score is RAW (matches pickBest), so the
		// accept threshold and error accounting in the loop are unaffected.
		t0 := time.Now()
		var boundPad, boundMix float32
		if r.boundCtx != nil {
			boundPad, boundMix = r.boundCtx.padding, boundaryMix(progress, r.boundCtx.start)
		}
		c, sc, ok := r.devSearch.SearchRandom(r.rng.Int63(), r.opt.RandomSamples, r.kinds, r.kindCDF,
			annealMaxR(w, h, progress), r.allowAlpha, r.alphaMin, r.opt.AspectMax, r.opt.CompactPenalty, len(r.shapes)-1, sampGrid, r.gw, r.gh, boundPad, boundMix, r.opt.CanvasPad)
		r.tm.Evaluate += time.Since(t0)
		if ok {
			return c, sc
		}
		r.devSearch = nil // older DLL without the export — fall back for the rest of the run
	}
	t0 := time.Now()
	cands := RandomShapes(r.rng, w, h, r.opt.RandomSamples, r.kinds, r.kindWeights, r.sampler, progress, r.orient, r.allowAlpha, r.alphaMin, r.opt.AspectMax, r.boundCtx)
	clampCandidatesToCanvas(cands, float32(w), float32(h), r.opt.CanvasPad)
	r.tm.Generate += time.Since(t0)
	t0 = time.Now()
	best, bestScore := pickBest(r.be, cands, penalty)
	r.tm.Evaluate += time.Since(t0)
	return best, bestScore
}

// momentSource is the moment-seeded search: fit covariance-ellipse seeds from the residual grid and
// score a small localised refine pool around them, spreading the refine budget across MomentSeeds
// error-sampled centres (a single centre anchors the search; many centres restore exploration). It
// runs on-device in a single call when the backend exposes it, otherwise builds the pool on the host.
type momentSource struct{}

func (momentSource) search(r *run, progress float32, sampGrid []float32, penalty func(model.Candidate) float32) (model.Candidate, float32) {
	w, h := r.w, r.h
	refineN := r.opt.MomentRefine
	if refineN <= 0 {
		refineN = defaultMomentRefine // quality-neutral knee (~-33% eval vs the 50k search)
	}
	seeds := r.opt.MomentSeeds
	if seeds < 1 {
		seeds = defaultMomentSeeds // spread the pool across centres (single-centre anchoring loses to random)
	}
	if r.devMoment != nil {
		// GPU: fit seeds + generate localised pool + score + argmin in one call.
		t0 := time.Now()
		var boundPad, boundMix float32
		if r.boundCtx != nil {
			boundPad, boundMix = r.boundCtx.padding, boundaryMix(progress, r.boundCtx.start)
		}
		c, sc, ok := r.devMoment.SearchMoment(r.rng.Int63(), refineN, seeds, r.kinds, r.kindCDF,
			annealMaxR(w, h, progress), r.allowAlpha, r.alphaMin, r.opt.CompactPenalty, len(r.shapes)-1,
			sampGrid, r.gw, r.gh, boundPad, boundMix, r.opt.CanvasPad)
		r.tm.Evaluate += time.Since(t0)
		if ok {
			return c, sc
		}
		r.devMoment = nil // older DLL without the export — host fallback for the rest of the run
	}
	// Host: fit each seed + build the localised pool on the host, score via Evaluate.
	t0 := time.Now()
	perSeed := refineN / seeds
	if perSeed < 2 {
		perSeed = 2
	}
	mR := annealMaxR(w, h, progress)
	pool := make([]model.Candidate, 0, seeds*perSeed)
	for k := 0; k < seeds; k++ {
		px, py := r.sampler.Sample(r.rng)
		px = clampF(px, 0, float32(w-1))
		py = clampF(py, 0, float32(h-1))
		cR := mR
		if r.boundCtx != nil && r.boundCtx.dist != nil {
			if idx := int(py)*w + int(px); idx >= 0 && idx < len(r.boundCtx.dist) {
				cR = boundaryRadiusCap(mR, r.boundCtx.dist[idx], r.boundCtx.padding, progress, r.boundCtx.start)
			}
		}
		if scx, scy, srx, sry, sth, ok := momentSeedFromGrid(sampGrid, r.gw, r.gh, w, h, px, py, cR); ok {
			pool = append(pool, momentPool(r.rng, scx, scy, srx, sry, sth, cR, r.kinds, r.kindCDF, perSeed, float32(w), float32(h), r.allowAlpha, r.alphaMin)...)
		} else {
			pool = append(pool, RandomShapes(r.rng, w, h, perSeed, r.kinds, r.kindWeights, r.sampler, progress, r.orient, r.allowAlpha, r.alphaMin, r.opt.AspectMax, r.boundCtx)...)
		}
	}
	clampCandidatesToCanvas(pool, float32(w), float32(h), r.opt.CanvasPad)
	r.tm.Generate += time.Since(t0)
	t0 = time.Now()
	best, bestScore := pickBest(r.be, pool, penalty)
	r.tm.Evaluate += time.Since(t0)
	return best, bestScore
}

// hybridSource lays the smooth coarse base with the moment search, then hands off to the blind random
// search once progress reaches detailStart, where the sharp small detail shapes the 2nd-moment blob
// fit never proposes are needed. Those late shapes are the cheap ones (progressive sampling), so the
// handoff costs little time for real crispness. A detailStart of 0 keeps the moment search throughout.
type hybridSource struct {
	moment      momentSource
	random      randomSource
	detailStart float32
}

func (s hybridSource) search(r *run, progress float32, sampGrid []float32, penalty func(model.Candidate) float32) (model.Candidate, float32) {
	if s.detailStart > 0 && progress >= s.detailStart {
		return s.random.search(r, progress, sampGrid, penalty)
	}
	return s.moment.search(r, progress, sampGrid, penalty)
}
