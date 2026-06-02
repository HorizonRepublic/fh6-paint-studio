package engine

import (
	"math/rand"
	"sort"
	"time"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// greedyEnv bundles the loop-invariant state needed to place ONE greedy shape against
// the backend's current canvas. It exists so the back-fitting regrowth shares the exact
// search logic (on-device or host + hill-climb mutation) with the main greedy loop
// instead of duplicating it. The main loop currently keeps its own inline copy; once
// back-fitting proves out it can be refactored onto searchOne too (a determinism-checked
// follow-up). Holding the search behind one method keeps the two paths in lockstep.
type greedyEnv struct {
	be          backend.Backend
	rng         *rand.Rand
	w, h        int
	kinds       []model.ShapeKind
	kindWeights []float32
	kindCDF     []float32
	orient      []float32
	devSearch   randomSearcher
	allowAlpha  bool
	alphaMin    float32
	aspectMax   float32
	compact     bool
	moveStep    float32
	radiusStep  float32
	rounds      int
	perRound    int
	randomN     int
	canvasPad   float32
	tm          *Timings
}

// searchOne runs one shape's random search (+ hill-climb mutation) against the CURRENT
// backend canvas and returns the best candidate with its RAW score (negative = it lowers
// the error). It mirrors the main loop body exactly — same RNG draw order (devSearch seed
// or host RandomShapes, then `rounds` MutateShape rounds), same compact-penalty selection,
// same timing accumulation — so behavior is identical to placing a shape in the main loop.
// It does NOT apply the candidate; the caller decides whether to accept and refresh.
func (g *greedyEnv) searchOne(sampler *ErrorSampler, grid []float32, gw, gh, shapeCount int, progress float32) (model.Candidate, float32) {
	var penalty func(model.Candidate) float32
	if g.compact {
		penalty = func(c model.Candidate) float32 { return compactPenalty(c, shapeCount, g.w, g.h) }
	}
	var best model.Candidate
	var bestScore float32
	var t0 time.Time
	if g.devSearch != nil {
		t0 = time.Now()
		c, sc, ok := g.devSearch.SearchRandom(g.rng.Int63(), g.randomN, g.kinds, g.kindCDF,
			annealMaxR(g.w, g.h, progress), g.allowAlpha, g.alphaMin, g.aspectMax, g.compact, shapeCount, grid, gw, gh, 0, 0, g.canvasPad)
		if g.tm != nil {
			g.tm.Evaluate += time.Since(t0)
		}
		if ok {
			best, bestScore = c, sc
		} else {
			g.devSearch = nil // older DLL without the export — fall back for the rest of the run
		}
	}
	if g.devSearch == nil {
		t0 = time.Now()
		cands := RandomShapes(g.rng, g.w, g.h, g.randomN, g.kinds, g.kindWeights, sampler, progress, g.orient, g.allowAlpha, g.alphaMin, g.aspectMax, nil)
		clampCandidatesToCanvas(cands, float32(g.w), float32(g.h), g.canvasPad)
		if g.tm != nil {
			g.tm.Generate += time.Since(t0)
		}
		t0 = time.Now()
		best, bestScore = pickBest(g.be, cands, penalty)
		if g.tm != nil {
			g.tm.Evaluate += time.Since(t0)
		}
	}
	for r := 0; r < g.rounds && bestScore < 0; r++ {
		t0 = time.Now()
		mut := MutateShape(g.rng, best, g.perRound, float32(g.w), float32(g.h), g.moveStep, g.radiusStep, g.allowAlpha, g.alphaMin)
		clampCandidatesToCanvas(mut, float32(g.w), float32(g.h), g.canvasPad)
		if g.tm != nil {
			g.tm.Mutate += time.Since(t0)
		}
		t0 = time.Now()
		mb, ms := pickBest(g.be, mut, penalty)
		if g.tm != nil {
			g.tm.Evaluate += time.Since(t0)
		}
		if ms < bestScore {
			best, bestScore = mb, ms
		}
	}
	return best, bestScore
}

// shapeContributionsBlend ranks every shape by how much weighted SSE it removes versus
// showing what is beneath it — the alpha-aware generalization of shapeContributions used
// for back-fitting's removal ranking. Two differences from the opaque-only prune version:
//  1. Ownership (owner1/owner2 = the top two shapes per pixel) is built from ALL shapes,
//     not just opaque ones, so it works under the semi-transparent default (photo/anime).
//  2. The "with shape j" color is the alpha blend a·cj + (1-a)·below, so a faint shape that
//     barely changes its pixels ranks as low contribution (a prime removal candidate) — exactly
//     the redundant layers back-fitting wants to reclaim. For opaque shapes (a=1) this reduces
//     to the exact prune formula. shapes[0] (background) is never scored.
//
// The result is a removal heuristic, not an exact leave-one-out (it approximates the
// multi-layer alpha stack beneath j by owner2's flat color). That is acceptable because the
// whole back-fitting pass is gated: a bad removal choice is recovered by regrowth or reverted,
// so ranking quality only affects efficiency, never correctness.
func shapeContributionsBlend(shapes []model.Shape, target, weight []float32, w, h int, bg model.RGBA, transparent bool) []float64 {
	n := len(shapes)
	owner1 := make([]int32, w*h)
	owner2 := make([]int32, w*h)
	for i := range owner1 {
		owner1[i] = -1
		owner2[i] = -1
	}
	for j := n - 1; j >= 1; j-- {
		kind := model.KindFromType(shapes[j].Type)
		p := model.ParamsFromShape(shapes[j])
		xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				idx := y*w + x
				if owner1[idx] != -1 && owner2[idx] != -1 {
					continue
				}
				if !raster.Inside(kind, p, x, y) {
					continue
				}
				if owner1[idx] == -1 {
					owner1[idx] = int32(j)
				} else if owner2[idx] == -1 {
					owner2[idx] = int32(j)
				}
			}
		}
	}

	cr := make([]float64, n)
	cg := make([]float64, n)
	cb := make([]float64, n)
	ca := make([]float64, n)
	for j := 0; j < n; j++ {
		if len(shapes[j].Color) >= 3 {
			cr[j] = float64(shapes[j].Color[0]) / 255
			cg[j] = float64(shapes[j].Color[1]) / 255
			cb[j] = float64(shapes[j].Color[2]) / 255
		}
		ca[j] = 1
		if len(shapes[j].Color) >= 4 {
			ca[j] = float64(shapes[j].Color[3]) / 255
		}
	}
	bgr, bgg, bgb := float64(bg.R), float64(bg.G), float64(bg.B)

	contrib := make([]float64, n)
	for idx := 0; idx < w*h; idx++ {
		j := owner1[idx]
		if j < 1 {
			continue
		}
		p := idx * 4
		tr, tg, tb := float64(target[p]), float64(target[p+1]), float64(target[p+2])
		wt := float64(weight[idx])

		// Color beneath j: owner2, or the fallback (empty for cutouts, bg otherwise).
		var rr, rg, rb float64
		if o2 := owner2[idx]; o2 >= 1 {
			rr, rg, rb = cr[o2], cg[o2], cb[o2]
		} else if transparent {
			rr, rg, rb = 0, 0, 0
		} else {
			rr, rg, rb = bgr, bgg, bgb
		}
		a := ca[j]
		// Error with j present (alpha blend over the color beneath).
		wr, wg, wb := a*cr[j]+(1-a)*rr, a*cg[j]+(1-a)*rg, a*cb[j]+(1-a)*rb
		dr, dg, db := tr-wr, tg-wg, tb-wb
		sseWith := dr*dr + dg*dg + db*db
		// Error if j removed (the color beneath shows through).
		dr, dg, db = tr-rr, tg-rg, tb-rb
		sseWithout := dr*dr + dg*dg + db*db

		contrib[j] += wt * (sseWithout - sseWith)
	}
	return contrib
}

// pruneRedundant is the post-polish ECONOMY pass: it removes FINAL shapes whose removal keeps the
// rendered error within opt.EconomyTol (a fraction of the final error), reclaiming genuinely-redundant
// layers — a lighter, cleaner import at ~no quality cost. Unlike the pre-polish pruneToBudget (softened
// to keep-full-budget, because pre-polish contribution under-counts shapes the joint polish later makes
// useful), this runs AFTER polish, where each shape is final and its contribution is accurate. Removal
// candidates are taken in ascending contribution order up to a budget of finalErr*tol (negatives —
// shapes that HURT — are always removed); the whole batch is then GATED on the actual re-rendered
// error, so the pass can never make the result meaningfully worse. Leaves the backend rendering the
// returned shapes.
func pruneRedundant(be backend.Backend, shapes []model.Shape, finalErr float64, initCanvas []float32, opt Options, w, h int) ([]model.Shape, float64) {
	if opt.EconomyTol <= 0 || len(shapes) <= 2 {
		return shapes, finalErr
	}
	contrib := shapeContributionsBlend(shapes, be.Target(), be.Weight(), w, h, opt.Background, opt.TransparentBG)
	order := make([]int, 0, len(shapes)-1)
	for j := 1; j < len(shapes); j++ {
		order = append(order, j)
	}
	sort.Slice(order, func(a, b int) bool { return contrib[order[a]] < contrib[order[b]] })

	// Remove the lowest-contribution shapes INCREMENTALLY in small batches, re-rendering and gating
	// each step on the REAL error, and stop at the first batch that would breach the gate. A single
	// big batch over-prunes — the contribution estimate ignores overlap, so removing many at once
	// raises the error more than the sum of their individual contributions. Incremental gating
	// reclaims exactly the subset that is genuinely redundant.
	gate := finalErr * (1 + opt.EconomyTol)
	renderPrefix := func(k int) float64 { // render `shapes` minus the k lowest-contribution, return error
		drop := make([]bool, len(shapes))
		for i := 0; i < k; i++ {
			drop[order[i]] = true
		}
		_ = be.Reset(initCanvas)
		for j := 1; j < len(shapes); j++ {
			if !drop[j] {
				_ = be.Apply(shapeToCandidate(shapes[j]))
			}
		}
		grid, _, _, _ := be.ErrorGrid()
		return sumGrid(grid)
	}
	// Binary-search the largest prefix of the ascending-contribution order droppable within the gate
	// (error rises ~monotonically as more low-value shapes are removed).
	lo, hi, accepted, curErr := 1, len(order), 0, finalErr
	for lo <= hi {
		mid := (lo + hi) / 2
		if err := renderPrefix(mid); err <= gate {
			accepted, curErr = mid, err
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if accepted == 0 {
		_ = renderPrefix(0) // restore the backend to the original set (last render was a failing prefix)
		return shapes, finalErr
	}
	if accepted < len(order) {
		_ = renderPrefix(accepted) // re-render the accepted set (loop ended on a larger, failing prefix)
	}
	dropped := make([]bool, len(shapes))
	for i := 0; i < accepted; i++ {
		dropped[order[i]] = true
	}
	kept := make([]model.Shape, 0, len(shapes)-accepted)
	kept = append(kept, shapes[0])
	for j := 1; j < len(shapes); j++ {
		if !dropped[j] {
			kept = append(kept, shapes[j])
		}
	}
	return kept, curErr
}

// backFit runs ONE back-fitting pass: it removes the `removeFrac` fraction of shapes with
// the lowest contribution (each was greedy-optimal WHEN placed, but later shapes changed the
// canvas, so the weakest are now wasted budget), re-renders the surviving residual, and
// REGROWS the freed slots with fresh greedy search against that residual — which the error
// sampler steers onto the regions that are now worst-fit. This breaks the greedy plateau:
// the regrown shapes are chosen against the COMPLETE reconstruction, a discrete relocation
// the per-shape polish (gradient-only) cannot make.
//
// It returns the candidate shape set and its rendered error, and leaves the backend canvas
// holding that candidate. The CALLER gates the result (keep only if the error dropped) and,
// on rejection, restores the backend to the previous shapes — so a pass can never regress.
func backFit(be backend.Backend, env *greedyEnv, shapes []model.Shape, initCanvas, target, weight []float32,
	w, h int, bg model.RGBA, transparent bool, removeFrac, recolorVarSkip float64) ([]model.Shape, float64) {
	n := len(shapes)
	targetCount := n
	if n <= 2 || removeFrac <= 0 {
		return shapes, rerender(be, initCanvas, shapes)
	}
	k := int(float64(n-1) * removeFrac)
	if k < 1 {
		return shapes, rerender(be, initCanvas, shapes)
	}

	// 1. Rank shapes (1..n-1) by contribution; mark the k lowest for removal.
	contrib := shapeContributionsBlend(shapes, target, weight, w, h, bg, transparent)
	type ranked struct {
		idx int
		c   float64
	}
	rks := make([]ranked, 0, n-1)
	for j := 1; j < n; j++ {
		rks = append(rks, ranked{j, contrib[j]})
	}
	sort.Slice(rks, func(a, b int) bool { return rks[a].c < rks[b].c })
	drop := make(map[int]bool, k)
	for i := 0; i < k; i++ {
		drop[rks[i].idx] = true
	}
	kept := make([]model.Shape, 0, n)
	for j := 0; j < n; j++ {
		if j == 0 || !drop[j] {
			// Deep-clone so backFit never mutates the caller's shapes (recolorVisible below
			// rewrites Color in place); a rejected pass must leave the input pristine.
			kept = append(kept, cloneShape(shapes[j]))
		}
	}

	// 2. Re-render the surviving residual into the backend; build the importance sampler.
	_ = be.Reset(initCanvas)
	for _, s := range kept[1:] {
		_ = be.Apply(shapeToCandidate(s))
	}
	grid, gw, gh, _ := be.ErrorGrid()
	sampler := NewErrorSampler(grid, gw, gh, w, h)

	// 3. Regrow the freed slots: greedy-search shapes against the residual until the budget
	// is restored or no improving shape remains (residual exhausted).
	for len(kept) < targetCount {
		progress := float32(len(kept)-1) / float32(targetCount-1)
		best, bestScore := env.searchOne(sampler, grid, gw, gh, len(kept)-1, progress)
		if bestScore >= -1e-7 {
			break
		}
		_ = be.Apply(best)
		kept = append(kept, best.ToShape(float64(bestScore)))
		grid, gw, gh, _ = be.ErrorGrid()
		sampler = NewErrorSampler(grid, gw, gh, w, h)
	}

	// 4. Color re-solve (opaque path only — same constraint as the main post-process) + final
	// render so the returned error reflects the recolor.
	if !env.allowAlpha {
		recolorVisible(kept, target, weight, w, h, recolorVarSkip)
		_ = be.Reset(initCanvas)
		for _, s := range kept[1:] {
			_ = be.Apply(shapeToCandidate(s))
		}
	}
	g2, _, _, _ := be.ErrorGrid()
	return kept, sumGrid(g2)
}

// rerender resets the backend to initCanvas, applies shapes[1:], and returns the rendered
// error. Used as the no-op return path so backFit always leaves the backend in a defined state.
func rerender(be backend.Backend, initCanvas []float32, shapes []model.Shape) float64 {
	_ = be.Reset(initCanvas)
	for _, s := range shapes[1:] {
		_ = be.Apply(shapeToCandidate(s))
	}
	g, _, _, _ := be.ErrorGrid()
	return sumGrid(g)
}

// runBackfitPasses runs up to opt.BackFitPasses gated back-fitting passes on `shapes` (each
// removes opt.BackFitFrac of the lowest-contribution shapes and regrows them against the
// residual), returning the best shape set and its hard-rendered error. Each pass is gated on
// the rendered error: a pass that does not lower it is rejected (the backend is restored to
// the current shapes) and the loop stops. backFit never mutates the input, so a rejected
// first pass leaves `shapes` pristine. NOTE this gate is PRE-polish; when polish follows, the
// caller must gate end-to-end (Run does, because polish can prefer a different starting point).
func runBackfitPasses(be backend.Backend, env *greedyEnv, shapes []model.Shape, finalErr float64,
	initCanvas []float32, opt Options, w, h int) ([]model.Shape, float64) {
	passes := opt.BackFitPasses
	if passes <= 0 {
		passes = 1
	}
	frac := opt.BackFitFrac
	if frac <= 0 {
		frac = 0.1
	}
	for pass := 0; pass < passes; pass++ {
		cand, candErr := backFit(be, env, shapes, initCanvas, be.Target(), be.Weight(),
			w, h, opt.Background, opt.TransparentBG, frac, opt.RecolorVarSkip)
		if candErr+1e-9 < finalErr {
			shapes, finalErr = cand, candErr
		} else {
			rerender(be, initCanvas, shapes) // restore the backend to the pristine input
			break
		}
	}
	return shapes, finalErr
}

// cloneShape returns a deep copy of s (its Data and Color slices are copied), so callers can
// mutate the clone (recolor, polish) without touching the original.
func cloneShape(s model.Shape) model.Shape {
	out := s
	if s.Data != nil {
		out.Data = append([]float64(nil), s.Data...)
	}
	if s.Color != nil {
		out.Color = append([]int(nil), s.Color...)
	}
	return out
}

// cloneShapes deep-copies a shape slice (see cloneShape) so independent pipeline branches
// (e.g. polish-with vs polish-without back-fitting) can be built and compared without aliasing.
func cloneShapes(src []model.Shape) []model.Shape {
	out := make([]model.Shape, len(src))
	for i, s := range src {
		out[i] = cloneShape(s)
	}
	return out
}
