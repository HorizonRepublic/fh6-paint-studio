package engine

// live.go — EXPERIMENTAL LIVE-style co-adaptation scheduler (Options.LiveBatch, default 0/off; replaces the
// greedy loop when >0). The greedy adds ONE shape at a time then FREEZES it until the single final polish, so
// at low budgets the early layout is locked in before later shapes reveal it was wrong. LIVE (Ma et al.,
// CVPR 2022) instead re-polishes ALL shapes jointly FREQUENTLY (after every small batch), so every shape keeps
// co-adapting throughout the build — the property that reaches a recognizable image in far fewer primitives.
// This variant keeps the greedy's proven per-shape search (which already sizes a shape to its residual region,
// big-first via the anneal radius schedule) and injects a joint polish every LiveBatch shapes. Reuses
// searchOne + applyPolish + ErrorGrid; host-side selection only -> golden-diff safe. For the low-budget
// "economy" regime (the per-batch polish is too costly at the full budget).

// live places shapes in batches of r.opt.LiveBatch, re-polishing ALL shapes jointly after each batch, filling
// r.shapes up to r.genTarget. Mirrors greedy()'s contract: leaves r.shapes placed + the backend rendering them.
func (r *run) live() {
	K := r.opt.LiveBatch
	if K < 1 {
		K = 1
	}
	batchOpt := r.opt // short per-batch polish (co-adaptation); the final refine() polish is full
	bi := r.opt.PolishOpts.Iters / 4
	if bi < 30 {
		bi = 30
	}
	batchOpt.PolishOpts.Iters = bi

	// Two-phase: LIVE builds only the structural BASE (LiveBase shapes), then Run() hands off to greedy for
	// the detail. 0 = LIVE the whole budget.
	target := r.genTarget
	if r.opt.LiveBase > 0 && r.opt.LiveBase < target {
		target = r.opt.LiveBase
	}

	noImprove := 0
	for len(r.shapes)-1 < target {
		if r.opt.Cancel != nil && r.opt.Cancel() {
			break
		}
		// 1. Place up to K shapes via the normal greedy search (correct residual-fit sizing).
		placed := 0
		for b := 0; b < K && len(r.shapes)-1 < target; b++ {
			progress := float32(len(r.shapes)-1) / float32(r.genTarget)
			best, bestScore := r.searchOne(progress, r.grid, nil)
			if bestScore >= -1e-7 {
				if noImprove++; noImprove >= r.maxNI {
					break
				}
				continue
			}
			noImprove = 0
			_ = r.be.Apply(best)
			r.shapes = append(r.shapes, best.ToShape(float64(bestScore)))
			r.grid, r.gw, r.gh, _ = r.be.ErrorGrid()
			r.sampler = NewErrorSampler(r.grid, r.gw, r.gh, r.w, r.h)
			placed++
		}
		if placed == 0 {
			break // residual exhausted (or hit the no-improve limit)
		}
		// 2. Joint polish of ALL shapes (the LIVE co-adaptation): the backend renders r.shapes, so its
		//    error is the gate baseline; applyPolish keeps the polish only if it lowers that error.
		g, _, _, _ := r.be.ErrorGrid()
		r.shapes, r.finalErr = applyPolish(r.be, r.shapes, sumGrid(g), r.initCanvas, batchOpt, r.w, r.h, &r.tm)
		// 3. Refresh the residual grid + sampler for the next batch's search (polish moved the shapes).
		r.grid, r.gw, r.gh, _ = r.be.ErrorGrid()
		r.sampler = NewErrorSampler(r.grid, r.gw, r.gh, r.w, r.h)
		if r.opt.Progress != nil {
			r.opt.Progress(len(r.shapes)-1, sumGrid(r.grid))
		}
	}
}
