package engine

import (
	"math"
	"math/rand"
	"time"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/metric"
	"fh6-paint-studio/internal/model"
)

type Options struct {
	Width, Height                 int
	Background                    model.RGBA
	StopAt                        int
	RandomSamples, MutatedSamples int
	Seed                          int64             // 0 selects a fixed default seed
	Kinds                         []model.ShapeKind // empty defaults to {KindEllipse}
	KindWeights                   []float32         // parallel to Kinds; nil = uniform pick
	TransparentBG                 bool              // true: cutout image — keep background transparent, no bg fill
	Overdraw                      float32           // generate StopAt*Overdraw shapes, then prune to StopAt (>1 enables; 0/1 = off)
	AllowAlpha                    bool              // allow semi-transparent shapes (alpha ~U(AlphaMin,1)). Forced off for cutouts.
	AlphaMin                      float32           // lower bound for candidate alpha when AllowAlpha (0 -> 0.3)
	AspectMax                     float32           // >1 biases ellipse/rect candidates toward thin elongated slivers (minor=major/U(1,AspectMax)) along the edge orientation — traces sharp contours. <=1 keeps round-ish axes (smooth content).
	MaxNoImprove                  int               // consecutive non-improving shapes before early-stop (0 -> default). High = fill the full budget.
	ShapeKneeTol                  float64           // auto-shape-count: stop the greedy loop when the relative marginal improvement rate r = ΔErr/(window·currentErr) per shape stays below this for kneeSustain shapes. 0 = off (fill the StopAt budget). ~2e-4 = conservative (trims only saturated flat/logo content); ~5e-4 = aggressive draft. StopAt is the ceiling.
	RecolorVarSkip                float64           // recolor: skip the weighted-mean repaint for shapes whose owned target pixels have color variance above this (boundary-straddling fur/contour slivers) — keeps their crisp greedy/polish color instead of a muddy mean. 0 = off (repaint all).
	SampleBudget                  int               // per-shape scoring pixel budget for progressive sampling (0 -> backend default 4000; large = ~full-res)
	DetailStrength                float32           // detail-weighted sampling: bias late candidate centres toward high-detail TARGET cells (faces/linework) by scaling the sampling grid ×(1+s·detail), s ramping to this. 0 = off. ~0.35 suits organic content. Reduces face softness + smooth-region faceting; no benefit (slight cost) on flat content.
	DetailSamplingStart           float32           // progress fraction at which detail-weighted sampling engages (0 -> 0.6). Earlier = stronger detail focus but less coarse-base coverage.
	BoundaryRadius                bool              // boundary-aware radius: cap each candidate's size by its centre's distance to the nearest target boundary (luma edge / cutout silhouette) so shapes can't balloon ACROSS edges (cleaner flat/logo/cutout silhouettes, less organic "veil" overshoot).
	BoundaryPadding               float32           // px a shape may still reach past a boundary (0 -> 16). Larger = looser cap.
	BoundaryStart                 float32           // progress at which the cap engages, ramping to full by progress 1 (0 -> 0.42). Earlier = tighter silhouettes sooner, but constrains the coarse base.
	CanvasPad                     float32           // canvas-edge radius clamp: shrink any ellipse/rect whose rotated bbox extends past the canvas by more than CanvasPad*min(w,h) px on a side. Stops shapes ballooning outside the image rectangle (visible in-game, clipped in the preview) + saves budget on near-out-of-frame shapes. 0 = off. ~0.04 keeps a small edge bleed; helps opaque/busy content most.
	EconomyTol                    float64           // post-polish economy prune: drop FINAL shapes whose removal keeps the rendered error within EconomyTol (a fraction of the final error), reclaiming genuinely-redundant layers (lighter/cleaner import at ~no quality cost). Runs AFTER polish (where contribution is accurate, unlike the softened pre-polish prune). Gated end-to-end. 0 = off. ~0.005 = within the noise band.
	StandoutTol                   float64           // post-polish PERCEPTUAL standout suppression: detect shapes whose rim draws an edge the TARGET lacks (a visible circle/square the SSE metric is blind to) and recolour-to-local-mean or remove them, gated so the GLOBAL error rises at most this fraction. Opt-in (0 = off). The metric will NOT show the win — validate by eye; the gate only bounds the loss. ~0.005 = conservative.
	CompactPenalty                bool              // bias the per-shape pick toward compact shapes (esp. the first few) — cleaner coarse stage
	OnDeviceSearch                bool              // run the random-candidate phase entirely on the GPU if the backend supports it; falls back to the host path otherwise
	CoarseSearch                  bool              // coarse-to-fine on-device search: score the candidate batch at a CHEAP pixel cap to filter, then re-score only the survivors at the full SampleBudget and pick from those. The winner is full-budget scored (quality-safe), the bulk pays only the coarse cost — the dominant eval speed lever at high shape counts. CUDA-only; no-op on the CPU backend.
	CoarseBudget                  int               // coarse-filter pixel cap (0 -> 4000). Lower = cheaper filter; must stay high enough that the true winner is its partition's coarse-min.
	CoarseK                       int               // coarse survivors re-scored at full budget (0 -> 2048). Higher = smaller partitions -> the winner is more reliably included (quality), at a modest extra full-budget re-eval cost (the bulk stays cheap).
	CoarseFP16                    bool              // run the coarse FILTER pass in FP16/half2 (~2x ALU throughput; the FP32 re-eval still picks the winner). Lossy ranking — validate it doesn't miss winners (raise CoarseK if so). CUDA-only.
	Polish                        bool              // run the joint differentiable polish pass after greedy
	PolishOpts                    PolishOptions     // polish config (zero value -> DefaultPolishOptions)
	BackFit                       bool              // run gated back-fitting passes (remove lowest-contribution shapes + regrow against the completed-canvas residual) before polish
	BackFitPasses                 int               // number of back-fitting passes (0 -> 1 when BackFit)
	BackFitFrac                   float64           // fraction of shapes removed+regrown per pass (0 -> 0.1)
	Progress                      func(shapes int, currentError float64)
	Status                        func(stage string) // optional: called at the START of each post-greedy phase (polish / back-fit / standout / economy) with a human label, so a UI can show "what it's doing now" instead of a bar stuck at 100%. nil = ignored.
	Cancel                        func() bool        // optional: checked at the loop top + before the polish/backfit post-process; return true to stop early (keeps the shapes placed so far). nil = never cancel.
}

type Result struct {
	Shapes                   []model.Shape
	InitialError, FinalError float64
	Timings                  Timings
}

// Timings is a per-phase wall-clock breakdown of a Run, for benchmark-driven
// tuning. The host phases (Generate/Mutate/Sampler/PostProcess) and the backend
// phases (Evaluate/Apply/ErrorGrid) are split so we can see whether the GPU or
// the host serial work dominates. Sum of phases ≈ Total (minus tiny untimed glue).
type Timings struct {
	Setup        time.Duration    // one-time: initial canvas/grid + orientation map
	Generate     time.Duration    // RandomShapes — host candidate generation
	Mutate       time.Duration    // MutateShape — host hill-climb mutation
	Evaluate     time.Duration    // backend.Evaluate — scoring (GPU eval + transfer)
	Apply        time.Duration    // backend.Apply — compositing the chosen shape
	ErrorGrid    time.Duration    // backend.ErrorGrid — per-cell SSE for sampling
	Sampler      time.Duration    // NewErrorSampler — host importance-sampling setup
	PostProcess  time.Duration    // prune-to-budget + color re-solve + re-render
	BackFit      time.Duration    // back-fitting passes (remove + regrow lowest-contribution shapes), if enabled
	BackFitBase  float64          // baseline error = polish(greedy) without back-fitting (0 if not run)
	BackFitTrial float64          // trial error = polish(backfit(greedy)). Measured on the SAME greedy result as BackFitBase, so (BackFitBase - min) isolates the back-fitting gain free of cross-run GPU non-determinism
	Polish       time.Duration    // joint differentiable polish (if enabled)
	PolishPre    float64          // soft-render weighted SSE before polish
	PolishPost   float64          // soft-render weighted SSE after polish
	PolishPhases [7]time.Duration // GPU-polish per-phase breakdown: upload,forward,loss,backward,readgrad,adam,hardloss
	PolishIters  int              // actual polish iterations run (plateau early-stop may cut the configured Iters short)
	Total        time.Duration
}

const maxNoImprove = 100

// sampleBudgeter is the optional capability of a backend to have its progressive-
// sampling pixel budget set at runtime (CPU and CUDA both implement it). The engine
// type-asserts it so the core Backend interface stays minimal. A higher budget means
// big early shapes are scored on more (or all) of their pixels; full-res scoring is
// most accurate, while the 4000-pixel default trades a little accuracy for speed.
type sampleBudgeter interface{ SetSampleBudget(n int) }

// coarseSearcher is the optional capability of a backend to run the on-device random search
// in two passes — a cheap coarse filter then a full-budget re-score of the survivors. Only
// the CUDA backend implements it; the CPU backend does not (the engine type-asserts, so it
// is simply a no-op there). Set every run so a pooled backend can't carry stale state.
type coarseSearcher interface {
	SetCoarse(enable bool, budget, kpart int)
	SetCoarseFP16(on bool)
}

// randomSearcher is the optional capability of a backend to run the random-candidate
// phase of one shape entirely on-device (generate + score + argmin in one call),
// returning just the best candidate. The CUDA backend implements it; the CPU backend
// does not, so the engine type-asserts and falls back to the host RandomShapes/pickBest
// path when absent. Keeping it all on-device removes the per-chunk host transfer that
// otherwise caps candidate throughput, making very high candidate volumes affordable.
// ok=false (e.g. an older DLL without the export) also triggers the host fallback.
type randomSearcher interface {
	SetOrient(orient []float32)
	SetBoundaryDist(dist []float32) // upload the distance-to-boundary field once (boundary-aware radius)
	SearchRandom(seed int64, n int, kinds []model.ShapeKind, kindCDF []float32,
		maxR float32, allowAlpha bool, alphaMin, aspectMax float32, compact bool, shapeCount int,
		grid []float32, gw, gh int, boundPad, boundMix, canvasPad float32) (model.Candidate, float32, bool)
}

// annealMaxR is the per-shape max radius schedule shared by the host generator
// (RandomShapes) and the on-device search: shapes shrink as the reconstruction
// progresses (coarse base first, fine detail later). Kept in one place so the two
// generation paths stay in lockstep.
func annealMaxR(w, h int, progress float32) float32 {
	diag := float32(math.Sqrt(float64(w*w + h*h)))
	scale := float32(0.25 - 0.20*math.Pow(float64(clampF(progress, 0, 1)), 1.5))
	maxR := diag * scale
	if maxR < 4 {
		maxR = 4
	}
	return maxR
}

func Run(be backend.Backend, opt Options) Result {
	runStart := time.Now()
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

	// Background shape + canvas init differ by mode:
	//   opaque image  → solid background rectangle; canvas filled with bg color
	//                   so uncovered regions match (no black corners).
	//   transparent   → transparent background rectangle (alpha 0); canvas starts
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
	moveStep := float32(math.Max(2, diag*0.012))
	radiusStep := float32(math.Max(2, diag*0.010))
	rounds, perRound := planHillClimb(opt.MutatedSamples)

	// Semi-transparent shapes: alpha ~U(alphaMin,1). Forced opaque for cutouts
	// (the reconstructed object must stay alpha=1 so the cutout silhouette is solid).
	allowAlpha := opt.AllowAlpha && !opt.TransparentBG
	alphaMin := opt.AlphaMin
	if alphaMin <= 0 || alphaMin > 1 {
		alphaMin = 0.3
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

	genTarget := opt.StopAt
	if opt.Overdraw > 1 && !allowAlpha {
		// Over-generate + contribution-prune is an opaque-only path: the contribution
		// model (shapeContributions) assumes opaque replace-ownership and would mis-rank
		// (and drop) semi-transparent shapes. With alpha we place exactly the budget and
		// prune only fully-occluded shapes.
		genTarget = int(float32(opt.StopAt) * opt.Overdraw)
	}

	// Compact-shape bias for the per-shape pick (selection-only, never accumulated):
	// penalize large shapes — heavily for the first 8 — so the coarse stage doesn't
	// place giant blobs.
	var penalty func(model.Candidate) float32

	// Edge-orientation map: seed elongated shapes along local edges (hair, folds).
	orient := metric.OrientationMap(be.Target(), w, h)

	// Detail-weighted sampling (opt-in via DetailStrength>0): precompute a target-detail
	// field at grid resolution ONCE. Past DetailSamplingStart progress, it biases the
	// candidate-centre sampler toward intrinsically detailed regions (faces, linework) so
	// late shapes keep refining detail instead of piling angular faceting into already-solved
	// smooth areas. nil when off → the sampling grid is the raw error grid (unchanged behavior).
	var detailGrid []float32
	detailStart := opt.DetailSamplingStart
	if detailStart <= 0 {
		detailStart = 0.6
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
			pad = 16
		}
		bstart := opt.BoundaryStart
		if bstart <= 0 {
			bstart = 0.42
		}
		if dist := metric.BoundaryDistance(be.Target(), w, h, 0.18); dist != nil {
			boundCtx = &boundaryCtx{dist: dist, padding: pad, start: bstart}
		}
	}

	// On-device search: if requested and the backend supports it, each shape's
	// random-candidate phase runs entirely on the GPU (generate+score+argmin, one call,
	// no per-chunk host transfer). Upload the fixed orientation map once and build the
	// kind CDF once; both feed the device generator. The CPU backend does not implement
	// randomSearcher, so this is automatically a no-op there (the host path runs).
	var devSearch randomSearcher
	if opt.OnDeviceSearch {
		if s, ok := be.(randomSearcher); ok {
			devSearch = s
			devSearch.SetOrient(orient)
			if boundCtx != nil {
				devSearch.SetBoundaryDist(boundCtx.dist)
			}
		}
	}
	kindCDF := buildKindCDF(kinds, opt.KindWeights)
	tm.Setup = time.Since(setupStart)

	// Auto-shape-count knee detector (opt-in via ShapeKneeTol>0): stops the greedy loop
	// when the per-shape RELATIVE marginal improvement plateaus (see kneeDetector).
	knee := newKneeDetector(opt.ShapeKneeTol)

	noImprove := 0
	for len(shapes)-1 < genTarget {
		if opt.Cancel != nil && opt.Cancel() {
			break
		}
		progress := float32(len(shapes)-1) / float32(genTarget)
		if opt.CompactPenalty {
			shapeCount := len(shapes) - 1
			penalty = func(c model.Candidate) float32 { return compactPenalty(c, shapeCount, w, h) }
		}
		// Detail-weighted sampling: late in the run, steer candidate centres toward
		// detailed target cells. sampGrid feeds BOTH the device search and the host
		// sampler so the two paths stay identical; when the bias is 0 (off/early)
		// sampGrid IS grid and the per-iteration sampler (rebuilt below the loop) is reused.
		sampGrid := grid
		if detailGrid != nil {
			if s := detailBias(progress, detailStart, opt.DetailStrength); s > 0 {
				sampGrid = blendDetailGrid(grid, detailGrid, s)
				sampler = NewErrorSampler(sampGrid, gw, gh, w, h)
			}
		}
		var best model.Candidate
		var bestScore float32
		var t0 time.Time
		if devSearch != nil {
			// GPU: generate+score+argmin for the whole random batch in one call.
			// compactPenalty is applied on-device for selection; the returned score is
			// RAW (matches pickBest), so the accept threshold and error accounting below
			// are unaffected. The mutate phase stays on the host path (B1 scope).
			t0 = time.Now()
			var boundPad, boundMix float32
			if boundCtx != nil {
				boundPad, boundMix = boundCtx.padding, boundaryMix(progress, boundCtx.start)
			}
			c, sc, ok := devSearch.SearchRandom(rng.Int63(), opt.RandomSamples, kinds, kindCDF,
				annealMaxR(w, h, progress), allowAlpha, alphaMin, opt.AspectMax, opt.CompactPenalty, len(shapes)-1, sampGrid, gw, gh, boundPad, boundMix, opt.CanvasPad)
			tm.Evaluate += time.Since(t0)
			if ok {
				best, bestScore = c, sc
			} else {
				devSearch = nil // older DLL without the export — fall back for the rest of the run
			}
		}
		if devSearch == nil {
			t0 = time.Now()
			cands := RandomShapes(rng, w, h, opt.RandomSamples, kinds, opt.KindWeights, sampler, progress, orient, allowAlpha, alphaMin, opt.AspectMax, boundCtx)
			clampCandidatesToCanvas(cands, float32(w), float32(h), opt.CanvasPad)
			tm.Generate += time.Since(t0)
			t0 = time.Now()
			best, bestScore = pickBest(be, cands, penalty)
			tm.Evaluate += time.Since(t0)
		}
		for r := 0; r < rounds && bestScore < 0; r++ {
			t0 = time.Now()
			mut := MutateShape(rng, best, perRound, float32(w), float32(h), moveStep, radiusStep, allowAlpha, alphaMin)
			clampCandidatesToCanvas(mut, float32(w), float32(h), opt.CanvasPad)
			tm.Mutate += time.Since(t0)
			t0 = time.Now()
			mb, ms := pickBest(be, mut, penalty)
			tm.Evaluate += time.Since(t0)
			if ms < bestScore {
				best, bestScore = mb, ms
			}
		}
		if bestScore >= -1e-7 {
			if noImprove++; noImprove >= maxNI {
				break
			}
			continue
		}
		noImprove = 0
		t0 = time.Now()
		_ = be.Apply(best)
		tm.Apply += time.Since(t0)
		shapes = append(shapes, best.ToShape(float64(bestScore)))
		t0 = time.Now()
		grid, gw, gh, _ = be.ErrorGrid()
		tm.ErrorGrid += time.Since(t0)
		t0 = time.Now()
		sampler = NewErrorSampler(grid, gw, gh, w, h)
		tm.Sampler += time.Since(t0)
		curErr := sumGrid(grid)
		if opt.Progress != nil {
			opt.Progress(len(shapes)-1, curErr)
		}
		if knee.push(curErr) {
			break // sustained diminishing returns — auto-stop at the knee
		}
	}
	ppStart := time.Now()
	if allowAlpha {
		// Semi-transparent shapes violate the opaque replace-ownership assumption of
		// both shapeContributions (prune-to-budget) and recolorVisible: a transparent
		// shape "owns" no pixels under that model, so it would be ranked contrib=0 and
		// DROPPED, and recolor would mis-tint opaque shapes sitting under it. So use the
		// alpha-safe occlusion prune (keeps any shape with a visible pixel; only fully
		// opaque shapes occlude) and skip the opaque-only color re-solve. The greedy
		// loop already placed exactly the budget (Overdraw is disabled when allowAlpha).
		shapes = pruneOccluded(shapes, w, h)
	} else {
		// Keep the most useful shapes up to the budget (drops occluded + low-value
		// shapes; with Overdraw>1 this selects the best subset of an over-generated set).
		shapes = pruneToBudget(shapes, be.Target(), be.Weight(), w, h, opt.StopAt, opt.Background, opt.TransparentBG)

		// Joint color re-solve: each opaque shape is repainted with the weighted-mean
		// target color over the pixels it actually owns (is the topmost opaque layer
		// for) in the final stack. This corrects greedy color drift — a shape's color
		// was optimal for the canvas when placed, but later shapes changed what shows.
		// Each owner pixel-set is disjoint, so the per-shape weighted mean is the exact
		// SSE minimum for fixed geometry: FinalError cannot increase.
		recolorVisible(shapes, be.Target(), be.Weight(), w, h, opt.RecolorVarSkip)
	}

	// Re-render the canvas (from the same init) with the new colors so FinalError
	// reflects the re-solve.
	_ = be.Reset(initCanvas)
	for _, s := range shapes[1:] {
		_ = be.Apply(shapeToCandidate(s))
	}

	grid, _, _, _ = be.ErrorGrid()
	finalErr := sumGrid(grid)
	tm.PostProcess = time.Since(ppStart)

	// Back-fitting and polish INTERACT: a back-fitting pass that lowers the HARD-rendered error
	// can still shift polish's starting point and yield a WORSE final result, so the two must be
	// evaluated together. When both are enabled, back-fitting is gated END-TO-END: polish the
	// greedy result AND the back-fitted result, then keep whichever wins after polish. With only
	// one enabled, a single gated pass suffices.
	var bfEnv *greedyEnv
	if opt.BackFit {
		// tm is nil so back-fit search time lands in the BackFit bucket, not the main phases.
		bfEnv = &greedyEnv{
			be: be, rng: rng, w: w, h: h,
			kinds: kinds, kindWeights: opt.KindWeights, kindCDF: kindCDF, orient: orient,
			devSearch: devSearch, allowAlpha: allowAlpha, alphaMin: alphaMin, aspectMax: opt.AspectMax,
			compact: opt.CompactPenalty, moveStep: moveStep, radiusStep: radiusStep,
			rounds: rounds, perRound: perRound, randomN: opt.RandomSamples, canvasPad: opt.CanvasPad, tm: nil,
		}
	}
	cancelled := opt.Cancel != nil && opt.Cancel()
	setStatus := func(s string) {
		if opt.Status != nil {
			opt.Status(s)
		}
	}
	switch {
	case cancelled:
		// Stopped early via Options.Cancel — skip the heavy polish/backfit post-process;
		// the finalized partial result (prune + recolor + re-render above) is kept as-is.
	case opt.BackFit && opt.Polish:
		setStatus("Refining (back-fit + polish)…")
		// Branch A: polish the greedy result (the no-back-fitting baseline).
		baseShapes, baseErr := applyPolish(be, cloneShapes(shapes), finalErr, initCanvas, opt, w, h, &tm)
		// Branch B: back-fit the greedy result, then polish it.
		bfStart := time.Now()
		bfCand, bfPre := runBackfitPasses(be, bfEnv, cloneShapes(shapes), finalErr, initCanvas, opt, w, h)
		tm.BackFit = time.Since(bfStart)
		bfShapes, bfErr := applyPolish(be, bfCand, bfPre, initCanvas, opt, w, h, &tm)
		tm.BackFitBase, tm.BackFitTrial = baseErr, bfErr // within-run comparison (noise-free with deterministic polish)
		// End-to-end gate: keep back-fitting only if it wins AFTER polish.
		if bfErr+1e-9 < baseErr {
			shapes, finalErr = bfShapes, bfErr
		} else {
			shapes, finalErr = baseShapes, baseErr
			// Branch B left the backend rendering bfShapes — restore the winning baseline.
			_ = be.Reset(initCanvas)
			for _, s := range shapes[1:] {
				_ = be.Apply(shapeToCandidate(s))
			}
		}
	case opt.BackFit:
		setStatus("Back-fitting…")
		// Polish off — gate back-fitting directly on the hard-rendered error.
		bfStart := time.Now()
		shapes, finalErr = runBackfitPasses(be, bfEnv, shapes, finalErr, initCanvas, opt, w, h)
		tm.BackFit = time.Since(bfStart)
	case opt.Polish:
		setStatus("Polishing…")
		shapes, finalErr = applyPolish(be, shapes, finalErr, initCanvas, opt, w, h, &tm)
	}

	// Post-polish PERCEPTUAL pass: suppress standout shapes the SSE metric can't see (gated).
	// Runs before economy so a faded/recoloured standout can still be reclaimed if it goes redundant.
	if !cancelled && opt.StandoutTol > 0 {
		setStatus("Smoothing standouts…")
		shapes, finalErr = suppressStandouts(be, shapes, finalErr, initCanvas, opt, w, h)
	}

	// Post-polish economy: reclaim genuinely-redundant FINAL shapes (gated, never meaningfully worse).
	if !cancelled && opt.EconomyTol > 0 {
		setStatus("Trimming layers…")
		shapes, finalErr = pruneRedundant(be, shapes, finalErr, initCanvas, opt, w, h)
	}

	tm.Total = time.Since(runStart)
	return Result{Shapes: shapes, InitialError: initialErr, FinalError: finalErr, Timings: tm}
}

// applyPolish runs the gated joint polish on `shapes`: it returns the polished shapes if they
// lower the hard-rendered error (else `shapes` unchanged) and the resulting error, recording
// polish timings into tm (accumulating, so two branch calls report the true total cost) and
// leaving the backend rendering the returned shapes. Self-contained so independent pipeline
// branches (with/without back-fitting) can each be polished and compared end-to-end.
func applyPolish(be backend.Backend, shapes []model.Shape, finalErr float64, initCanvas []float32,
	opt Options, w, h int, tm *Timings) ([]model.Shape, float64) {
	t0 := time.Now()
	// Use the GPU polish primitives when the backend provides them (CUDA), else the pure-Go
	// reference. Both run the same algorithm; the GPU path just moves forward/loss/backward
	// onto the device.
	var pr PolishResult
	if acc, ok := be.(PolishAccel); ok && acc.PolishSupported() {
		pr = PolishWithBackend(shapes, be.Target(), be.Weight(), w, h, opt.Background, opt.TransparentBG, opt.PolishOpts, acc)
	} else {
		pr = Polish(shapes, be.Target(), be.Weight(), w, h, opt.Background, opt.TransparentBG, opt.PolishOpts)
	}
	recolorVisible(pr.Shapes, be.Target(), be.Weight(), w, h, opt.RecolorVarSkip)
	_ = be.Reset(initCanvas)
	for _, s := range pr.Shapes[1:] {
		_ = be.Apply(shapeToCandidate(s))
	}
	g2, _, _, _ := be.ErrorGrid()
	postErr := sumGrid(g2)
	if tm != nil {
		tm.Polish += time.Since(t0)
		tm.PolishPre, tm.PolishPost = pr.PreLoss, pr.PostLoss
		tm.PolishPhases = pr.Phases
		tm.PolishIters = pr.Iters
	}
	if postErr <= finalErr {
		return pr.Shapes, postErr
	}
	// Regression — discard polish, re-render the input shapes and keep them.
	_ = be.Reset(initCanvas)
	for _, s := range shapes[1:] {
		_ = be.Apply(shapeToCandidate(s))
	}
	return shapes, finalErr
}

// PolishGeometry runs the gated joint polish on a standalone shape set (a saved greedy JSON)
// using the backend's stored target/weight, returning the polished shapes, their hard error,
// and the run timings (pre/post soft loss, iters). It mirrors the in-pipeline applyPolish
// (polish → recolor → gate) exactly, so the CLI's -polish-json mode reproduces the shipped
// polish against a FIXED greedy input in isolation — the greedy is deterministic, so any
// final-error delta is purely the polish change. shapes[0] must be the background.
func PolishGeometry(be backend.Backend, shapes []model.Shape, opt Options, w, h int) ([]model.Shape, float64, Timings) {
	initCanvas := backgroundCanvas(opt.Background, w, h)
	if opt.TransparentBG {
		initCanvas = make([]float32, w*h*4) // all zero = transparent (cutout)
	}
	_ = be.Reset(initCanvas)
	for _, s := range shapes[1:] {
		_ = be.Apply(shapeToCandidate(s))
	}
	grid, _, _, _ := be.ErrorGrid()
	finalErr := sumGrid(grid)
	var tm Timings
	out, errOut := applyPolish(be, shapes, finalErr, initCanvas, opt, w, h, &tm)
	return out, errOut, tm
}
