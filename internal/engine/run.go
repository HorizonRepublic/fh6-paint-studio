package engine

import (
	"math"
	"math/rand"
	"time"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/metric"
	"fh6-paint-studio/internal/model"
)

// run carries the working state of a single greedy reconstruction across its phases
// (setup -> greedy -> post-process -> refine). Threading the ~30 live values through free
// functions would mean unwieldy parameter lists; holding them on one short-lived value lets
// each phase read and update the shared state directly while Run stays a thin orchestrator.
type run struct {
	be  backend.Backend
	opt Options
	rng *rand.Rand
	w   int
	h   int
	tm  Timings

	// Normalised options + schedule, resolved once in newRun.
	kinds       []model.ShapeKind
	kindWeights []float32
	kindCDF     []float32
	allowAlpha  bool
	alphaMin    float32
	maxNI       int
	genTarget   int
	moveStep    float32
	radiusStep  float32
	rounds      int
	perRound    int
	detailStart float32

	// Precomputed target fields + on-device search handles (nil = unavailable / off).
	orient     []float32
	detailGrid []float32
	boundCtx   *boundaryCtx
	devSearch  randomSearcher
	devMoment  momentSearcher

	// Generation strategy: random / moment / hybrid (selected once from the options).
	src shapeSource

	// Evolving reconstruction state.
	initCanvas []float32
	shapes     []model.Shape
	grid       []float32
	gw         int
	gh         int
	sampler    *ErrorSampler
	initialErr float64
	finalErr   float64
}

// Run reconstructs opt's target image as a stack of filled shapes on the given backend. It is a
// thin orchestrator over the run phases; the per-phase logic lives in the run methods below.
func Run(be backend.Backend, opt Options) Result {
	runStart := time.Now()
	r := newRun(be, opt)
	if opt.LiveBatch > 0 {
		r.live() // EXPERIMENTAL co-adaptation scheduler for the structural base...
		if len(r.shapes)-1 < r.genTarget {
			r.greedy() // ...then greedy fills the remaining budget with detail (two-phase economy)
		}
	} else {
		r.greedy()
	}
	r.postProcess()
	r.refine()
	r.tm.Total = time.Since(runStart)
	return Result{Shapes: r.shapes, InitialError: r.initialErr, FinalError: r.finalErr, Timings: r.tm}
}

// newRun builds the run state: it normalises the options, initialises the backend canvas and the
// background shape, computes the initial error grid + importance sampler, resolves the hill-climb
// schedule and the optional precomputed fields (orientation / detail / boundary), and wires up the
// on-device search handles. It consumes no randomness — the RNG is first used in the greedy loop.
func newRun(be backend.Backend, opt Options) *run {
	var tm Timings
	rng := rand.New(rand.NewSource(seed(opt.Seed)))
	w, h := opt.Width, opt.Height
	if opt.RandomSamples < 1 {
		opt.RandomSamples = 1
	}
	kinds := opt.Kinds
	if len(kinds) == 0 {
		kinds = []model.ShapeKind{model.KindEllipse}
	}
	kindWeights := opt.KindWeights

	// Background shape + canvas init differ by mode:
	//   opaque image  -> solid background rectangle; canvas filled with bg color
	//                   so uncovered regions match (no black corners).
	//   transparent   -> transparent background rectangle (alpha 0); canvas starts
	//                   transparent so uncovered regions stay empty. The alpha
	//                   channel in the SSE then naturally keeps shapes off the
	//                   transparent background (a shape there raises alpha where
	//                   the target alpha is 0 — penalized).
	bgAlpha := 255
	initCanvas := backgroundCanvas(opt.Background, w, h)
	if opt.TransparentBG {
		bgAlpha = 0
		initCanvas = make([]float32, w*h*4) // all zero = transparent
	}
	bg := model.Shape{Type: model.TypeRectangle, Data: []float64{0, 0, float64(w), float64(h)},
		Color: []int{model.EncByte(opt.Background.R), model.EncByte(opt.Background.G), model.EncByte(opt.Background.B), bgAlpha}, Score: 0}
	shapes := []model.Shape{bg}

	setupStart := time.Now()
	_ = be.Reset(initCanvas)

	grid, gw, gh, _ := be.ErrorGrid()
	initialErr := sumGrid(grid)
	sampler := NewErrorSampler(grid, gw, gh, w, h)

	diag := math.Sqrt(float64(w*w + h*h))
	moveStep := float32(math.Max(mutateStepFloor, diag*mutateMoveFrac))
	radiusStep := float32(math.Max(mutateStepFloor, diag*mutateRadiusFrac))
	rounds, perRound := planHillClimb(opt.MutatedSamples)

	// Semi-transparent shapes: alpha ~U(alphaMin,1). Forced opaque for cutouts
	// (the reconstructed object must stay alpha=1 so the cutout silhouette is solid).
	allowAlpha := opt.AllowAlpha && !opt.TransparentBG
	alphaMin := opt.AlphaMin
	if alphaMin <= 0 || alphaMin > 1 {
		alphaMin = defaultAlphaMin
	}

	// Early-stop budget: how many consecutive non-improving shapes before we give
	// up filling the budget. A high value places the FULL shape budget (a deep search
	// almost always finds an improving shape); the default of 100 is conservative,
	// quality runs raise it.
	maxNI := opt.MaxNoImprove
	if maxNI <= 0 {
		maxNI = maxNoImprove
	}

	// Progressive-sampling pixel budget: push it to the backend if requested. Raising
	// this (or going effectively full-res) fixes the blobby/mis-placed big EARLY shapes
	// that strided subsampling scores inaccurately.
	if sb, ok := be.(sampleBudgeter); ok && opt.SampleBudget != 0 {
		sb.SetSampleBudget(opt.SampleBudget)
	}

	// Coarse-to-fine search (CUDA-only): score the random batch cheaply to filter, then
	// re-score the survivors at the full budget. Set unconditionally (enable OR disable) so a
	// reused backend never carries stale coarse state from a prior run.
	if cs, ok := be.(coarseSearcher); ok {
		cs.SetCoarse(opt.CoarseSearch, opt.CoarseBudget, opt.CoarseK)
		cs.SetCoarseFP16(opt.CoarseFP16)
	}

	// The greedy is hard-only, so keep the backend on its FAST hard-eval path (warp kernel). The
	// gradient coalesce post-pass flips this on for its own gradient evals. Set unconditionally so a
	// reused backend never carries stale gradient state into a fresh hard run.
	if gs, ok := be.(gradientEvaluator); ok {
		gs.SetGradients(false)
	}

	genTarget := opt.StopAt
	if opt.Overdraw > 1 && !allowAlpha {
		// Over-generate + contribution-prune is an opaque-only path: the contribution
		// model (shapeContributions) assumes opaque replace-ownership and would mis-rank
		// (and drop) semi-transparent shapes. With alpha we place exactly the budget and
		// prune only fully-occluded shapes.
		genTarget = int(float32(opt.StopAt) * opt.Overdraw)
	}

	// Edge-orientation map: seed elongated shapes along local edges (hair, folds).
	orient := metric.OrientationMap(be.Target(), w, h)

	// Detail-weighted sampling (opt-in via DetailStrength>0): precompute a target-detail
	// field at grid resolution ONCE. Past DetailSamplingStart progress, it biases the
	// candidate-centre sampler toward intrinsically detailed regions (faces, linework) so
	// late shapes keep refining detail instead of piling angular faceting into already-solved
	// smooth areas. nil when off -> the sampling grid is the raw error grid (unchanged behavior).
	var detailGrid []float32
	detailStart := opt.DetailSamplingStart
	if detailStart <= 0 {
		detailStart = defaultDetailStart
	}
	if opt.DetailStrength > 0 {
		detailGrid = metric.DetailGrid(be.Target(), w, h, gw, gh)
	}

	// Boundary-aware radius (opt-in via BoundaryRadius): precompute a distance-to-boundary
	// field ONCE; past BoundaryStart progress, each candidate's radius is capped to its
	// centre's distance + padding so shapes can't balloon across a target edge (cleaner
	// silhouettes on flat/logo/cutout, less translucent overshoot on organic). nil when off.
	var boundCtx *boundaryCtx
	if opt.BoundaryRadius {
		pad := opt.BoundaryPadding
		if pad <= 0 {
			pad = defaultBoundaryPad
		}
		bstart := opt.BoundaryStart
		if bstart <= 0 {
			bstart = defaultBoundaryStart
		}
		if dist := metric.BoundaryDistance(be.Target(), w, h, boundaryEdgeThreshold); dist != nil {
			boundCtx = &boundaryCtx{dist: dist, padding: pad, start: bstart}
		}
	}

	// On-device search: if requested and the backend supports it, each shape's
	// random-candidate phase runs entirely on the GPU (generate+score+argmin, one call,
	// no per-chunk host transfer). Upload the fixed orientation map once and build the
	// kind CDF once; both feed the device generator. The CPU backend does not implement
	// randomSearcher, so this is automatically a no-op there (the host path runs).
	var devSearch randomSearcher
	var devMoment momentSearcher
	if opt.OnDeviceSearch {
		if s, ok := be.(randomSearcher); ok {
			devSearch = s
			devSearch.SetOrient(orient)
			if boundCtx != nil {
				devSearch.SetBoundaryDist(boundCtx.dist) // also serves the on-device moment kernel (same backend)
			}
		}
		// On-device moment-seeded search (independent of devSearch; same CUDA backend). Absent on
		// the CPU backend or an older DLL -> the MomentSeed branch falls back to the host pool.
		if m, ok := be.(momentSearcher); ok {
			devMoment = m
		}
	}
	kindCDF := buildKindCDF(kinds, kindWeights)
	tm.Setup = time.Since(setupStart)

	return &run{
		be: be, opt: opt, rng: rng, w: w, h: h, tm: tm,
		kinds: kinds, kindWeights: kindWeights, kindCDF: kindCDF,
		allowAlpha: allowAlpha, alphaMin: alphaMin, maxNI: maxNI, genTarget: genTarget,
		moveStep: moveStep, radiusStep: radiusStep, rounds: rounds, perRound: perRound,
		detailStart: detailStart,
		orient:      orient, detailGrid: detailGrid, boundCtx: boundCtx,
		devSearch: devSearch, devMoment: devMoment, src: newShapeSource(opt),
		initCanvas: initCanvas, shapes: shapes, grid: grid, gw: gw, gh: gh,
		sampler: sampler, initialErr: initialErr,
	}
}

// greedy runs the placement loop: until the shape budget is met (or Cancel, the auto-shape-count
// knee, or the no-improvement limit stops it early) it searches one shape, applies the winner, and
// refreshes the error grid + sampler. Each accepted shape strictly lowers the hard-rendered error.
func (r *run) greedy() {
	// Auto-shape-count knee detector (opt-in via ShapeKneeTol>0): stops the loop when the
	// per-shape RELATIVE marginal improvement plateaus (see kneeDetector).
	knee := newKneeDetector(r.opt.ShapeKneeTol, r.opt.ShapeKneeFloor, r.initialErr)

	// Compact-shape bias for the per-shape pick (selection-only, never accumulated):
	// penalize large shapes — heavily for the first 8 — so the coarse stage doesn't
	// place giant blobs.
	var penalty func(model.Candidate) float32

	noImprove := 0
	for len(r.shapes)-1 < r.genTarget {
		if r.opt.Cancel != nil && r.opt.Cancel() {
			break
		}
		progress := float32(len(r.shapes)-1) / float32(r.genTarget)
		if r.opt.CompactPenalty {
			shapeCount := len(r.shapes) - 1
			penalty = func(c model.Candidate) float32 { return compactPenalty(c, shapeCount, r.w, r.h) }
		}
		// Detail-weighted sampling: late in the run, steer candidate centres toward
		// detailed target cells. sampGrid feeds BOTH the device search and the host
		// sampler so the two paths stay identical; when the bias is 0 (off/early)
		// sampGrid IS grid and the per-iteration sampler is reused.
		sampGrid := r.grid
		if r.detailGrid != nil {
			if s := detailBias(progress, r.detailStart, r.opt.DetailStrength); s > 0 {
				sampGrid = blendDetailGrid(r.grid, r.detailGrid, s)
				r.sampler = NewErrorSampler(sampGrid, r.gw, r.gh, r.w, r.h)
			}
		}
		best, bestScore := r.searchOne(progress, sampGrid, penalty)
		if bestScore >= -1e-7 {
			if noImprove++; noImprove >= r.maxNI {
				break
			}
			continue
		}
		// Low-contrast gate: the best findable shape improves the canvas, but if it barely
		// differs from what it covers (mean per-pixel SSE gain below MinShapeGain — a faint
		// ghost facet over an already-solved region) skip it. Counts as no-improvement, so
		// the search reallocates to genuine detail or stops once nothing high-contrast remains.
		if r.opt.MinShapeGain > 0 {
			if area := candidateArea(best, r.w, r.h); area > 0 && -float64(bestScore)/area < r.opt.MinShapeGain {
				if noImprove++; noImprove >= r.maxNI {
					break
				}
				continue
			}
		}
		noImprove = 0
		t0 := time.Now()
		_ = r.be.Apply(best)
		r.tm.Apply += time.Since(t0)
		r.shapes = append(r.shapes, best.ToShape(float64(bestScore)))
		t0 = time.Now()
		r.grid, r.gw, r.gh, _ = r.be.ErrorGrid()
		r.tm.ErrorGrid += time.Since(t0)
		t0 = time.Now()
		r.sampler = NewErrorSampler(r.grid, r.gw, r.gh, r.w, r.h)
		r.tm.Sampler += time.Since(t0)
		curErr := sumGrid(r.grid)
		if r.opt.Progress != nil {
			r.opt.Progress(len(r.shapes)-1, curErr)
		}
		if knee.push(curErr) {
			break // sustained diminishing returns — auto-stop at the knee
		}
	}
}

// searchOne finds the best candidate for the next shape. It delegates the candidate generation and
// scoring to the configured shapeSource (random / moment / hybrid), then hill-climb-mutates the
// winner. The returned score is RAW (the compact bias is selection-only); a score < 0 means the
// candidate improves the canvas. Mutate timings accumulate into r.tm (the source records its own).
func (r *run) searchOne(progress float32, sampGrid []float32, penalty func(model.Candidate) float32) (model.Candidate, float32) {
	w, h := r.w, r.h
	best, bestScore := r.src.search(r, progress, sampGrid, penalty)
	for i := 0; i < r.rounds && bestScore < 0; i++ {
		t0 := time.Now()
		mut := MutateShape(r.rng, best, r.perRound, float32(w), float32(h), r.moveStep, r.radiusStep, r.allowAlpha, r.alphaMin)
		clampCandidatesToCanvas(mut, float32(w), float32(h), r.opt.CanvasPad)
		r.tm.Mutate += time.Since(t0)
		t0 = time.Now()
		mb, ms := pickBest(r.be, mut, penalty)
		r.tm.Evaluate += time.Since(t0)
		if ms < bestScore {
			best, bestScore = mb, ms
		}
	}
	return best, bestScore
}

// postProcess finalises the greedy output: it prunes to the budget, re-renders the canvas from the
// same init so the error reflects the colour re-solve, and records the hard final error.
func (r *run) postProcess() {
	ppStart := time.Now()
	w, h := r.w, r.h
	if r.allowAlpha {
		// Semi-transparent shapes violate the opaque replace-ownership assumption of
		// both shapeContributions (prune-to-budget) and recolorVisible: a transparent
		// shape "owns" no pixels under that model, so it would be ranked contrib=0 and
		// DROPPED, and recolor would mis-tint opaque shapes sitting under it. So use the
		// alpha-safe occlusion prune (keeps any shape with a visible pixel; only fully
		// opaque shapes occlude) and skip the opaque-only color re-solve. The greedy
		// loop already placed exactly the budget (Overdraw is disabled when allowAlpha).
		r.shapes = pruneOccluded(r.shapes, w, h)
	} else {
		// Keep the most useful shapes up to the budget (drops occluded + low-value
		// shapes; with Overdraw>1 this selects the best subset of an over-generated set).
		r.shapes = pruneToBudget(r.shapes, r.be.Target(), r.be.Weight(), w, h, r.opt.StopAt, r.opt.Background, r.opt.TransparentBG)

		// Joint color re-solve: each opaque shape is repainted with the weighted-mean
		// target color over the pixels it actually owns (is the topmost opaque layer
		// for) in the final stack. This corrects greedy color drift — a shape's color
		// was optimal for the canvas when placed, but later shapes changed what shows.
		// Each owner pixel-set is disjoint, so the per-shape weighted mean is the exact
		// SSE minimum for fixed geometry: FinalError cannot increase.
		recolorVisible(r.shapes, r.be.Target(), r.be.Weight(), w, h, r.opt.RecolorVarSkip)
	}

	// Re-render the canvas (from the same init) with the new colors so FinalError
	// reflects the re-solve.
	_ = r.be.Reset(r.initCanvas)
	for _, s := range r.shapes[1:] {
		_ = r.be.Apply(shapeToCandidate(s))
	}

	r.grid, _, _, _ = r.be.ErrorGrid()
	r.finalErr = sumGrid(r.grid)
	r.tm.PostProcess = time.Since(ppStart)
}

// refine runs the gated post-greedy passes from the explicit pipeline (postPasses): at most one of
// back-fit / polish / back-fit+polish reshapes the stack, then the optional perceptual standout
// suppression. A run cancelled via Options.Cancel keeps the finalized partial result as-is and skips
// every pass. Each pass is internally gated so it can never raise the hard-rendered error.
func (r *run) refine() {
	if r.opt.Cancel != nil && r.opt.Cancel() {
		// Stopped early — keep the finalized partial result (prune + recolor + re-render from
		// postProcess) and skip the heavy refinement.
		return
	}
	for _, p := range postPasses() {
		if p.enabled(r.opt) {
			p.apply(r)
		}
	}
}

// setStatus reports the current post-greedy phase to the optional Options.Status callback (a UI
// hook), so a progress bar stuck at 100% can show what the run is doing. nil callback = ignored.
func (r *run) setStatus(s string) {
	if r.opt.Status != nil {
		r.opt.Status(s)
	}
}
