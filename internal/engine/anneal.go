package engine

import (
	"math"
	"math/rand"
	"sort"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/model"
)

// anneal.go — EXPERIMENTAL basin-hopping / iterated local search (Options.AnnealIters, default 0/off).
// At LOW shape budgets greedy gets stuck in a local minimum: each shape is placed locally-optimally then
// frozen, so the global layout (which shape covers what) is never reconsidered. This wraps the polished
// result in an outer search — each iteration RANDOMLY removes a few low-value shapes and REGROWS them
// against the residual (a "kick" that escapes the greedy basin), short-polishes the trial, then accepts it
// by a Metropolis criterion (accepting some WORSENING moves to escape local minima), keeping the best-ever.
// Reuses the back-fit search (env.searchOne) + the joint polish (applyPolish) + the contribution ranking.
// Host-side selection logic only -> golden-diff safe. Most valuable at 50-300 shapes (the "economy" regime);
// the per-iter re-polish is too costly at the full 3000 budget. The keep-best gate means it can never finish
// WORSE than its input.

const (
	annealTemp0 = 0.01 // initial Metropolis temperature as a fraction of the input error (controls how readily worsening moves are accepted early)
	annealCool  = 0.92 // geometric cooling per iteration
)

// anneal runs opt.AnnealIters basin-hopping iterations on `shapes` (already greedy+polished, hard error
// curErr, backend rendering them). Returns the best shape set found and its hard error, leaving the backend
// rendering it.
func anneal(be backend.Backend, env *greedyEnv, shapes []model.Shape, curErr float64,
	initCanvas, target, weight []float32, opt Options, w, h int, tm *Timings, rng *rand.Rand) ([]model.Shape, float64) {
	iters := opt.AnnealIters
	if iters <= 0 || len(shapes) <= 3 {
		return shapes, curErr
	}
	// Short inner polish (the per-iter local optimisation): a fraction of the full budget, since each trial
	// starts near-optimal. The caller's polish pass already gave the input a full polish.
	innerOpt := opt
	innerIters := opt.PolishOpts.Iters / 4
	if innerIters < 30 {
		innerIters = 30
	}
	innerOpt.PolishOpts.Iters = innerIters

	best := cloneShapes(shapes)
	bestErr := curErr
	cur := cloneShapes(shapes)
	curE := curErr

	kick := 1 + (len(shapes)-1)/40 // shapes kicked per iter (~2.5% of the budget, >=1)
	T := math.Max(bestErr*annealTemp0, 1e-9)

	for i := 0; i < iters; i++ {
		if opt.Cancel != nil && opt.Cancel() {
			break // Stop mid-anneal: the best-ever set below is a complete result
		}
		trial := annealKick(be, env, cloneShapes(cur), initCanvas, target, weight, opt, w, h, kick, rng)
		trialP, trialErr := applyPolish(be, trial, rerender(be, initCanvas, trial), initCanvas, innerOpt, w, h, tm)
		dE := trialErr - curE
		if dE < 0 || rng.Float64() < math.Exp(-dE/T) {
			cur, curE = trialP, trialErr
			if curE < bestErr {
				best, bestErr = cloneShapes(cur), curE
			}
		}
		T *= annealCool
	}
	rerender(be, initCanvas, best) // leave the backend rendering the winner
	return best, bestErr
}

// annealKick removes `kick` shapes chosen RANDOMLY from the bottom third by contribution (exploration: not
// strictly the worst, so successive iterations probe different kicks) and regrows the freed slots with fresh
// greedy search against the residual. Mirrors one back-fit micro-pass; the randomness is the basin-hopping
// perturbation. Returns the new shape set, leaving the backend rendering it.
func annealKick(be backend.Backend, env *greedyEnv, shapes []model.Shape, initCanvas, target, weight []float32,
	opt Options, w, h, kick int, rng *rand.Rand) []model.Shape {
	n := len(shapes)
	if n <= 2 {
		return shapes
	}
	contrib := shapeContributionsBlend(shapes, target, weight, w, h, opt.Background, opt.TransparentBG)
	type ranked struct {
		idx int
		c   float64
	}
	rks := make([]ranked, 0, n-1)
	for j := 1; j < n; j++ {
		rks = append(rks, ranked{j, contrib[j]})
	}
	sort.Slice(rks, func(a, b int) bool { return rks[a].c < rks[b].c })
	pool := (n - 1) / 3 // bottom third is the removal pool
	if pool < kick {
		pool = kick
	}
	if pool > n-1 {
		pool = n - 1
	}
	drop := make(map[int]bool, kick)
	for len(drop) < kick && len(drop) < pool {
		drop[rks[rng.Intn(pool)].idx] = true
	}
	kept := make([]model.Shape, 0, n)
	for j := 0; j < n; j++ {
		if j == 0 || !drop[j] {
			kept = append(kept, cloneShape(shapes[j]))
		}
	}
	// Re-render the survivors, then regrow the freed slots against the residual (the sampler steers the
	// regrowth onto the now-worst-fit regions — the discrete relocation polish's gradients cannot make).
	_ = be.Reset(initCanvas)
	applyShapes(be, kept[1:])
	grid, gw, gh, _ := be.ErrorGrid()
	sampler := NewErrorSampler(grid, gw, gh, w, h)
	for len(kept) < n {
		progress := float32(len(kept)-1) / float32(n-1)
		bestC, bestScore := env.searchOne(sampler, grid, gw, gh, len(kept)-1, progress)
		if bestScore >= -1e-7 {
			break
		}
		_ = be.Apply(bestC)
		kept = append(kept, bestC.ToShape(float64(bestScore)))
		grid, gw, gh, _ = be.ErrorGrid()
		sampler = NewErrorSampler(grid, gw, gh, w, h)
	}
	return kept
}
