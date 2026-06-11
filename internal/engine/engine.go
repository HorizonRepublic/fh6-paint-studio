package engine

import (
	"math"
	"time"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/backend"
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
	ShapeKneeFloor                float64           // auto-shape-count ABSOLUTE floor (companion to ShapeKneeTol): floor the knee's denominator at this fraction of the INITIAL error — rate = ΔErr/(window·max(currentErr, ShapeKneeFloor·initialErr)). Without it the relative rate ÷currentErr BLOWS UP as currentErr→0 (clean line-art / solved flat), so the knee never trips and wastes budget on imperceptible shapes. Once the recon is better than this fraction of the initial error the denominator pins to a fixed baseline, so the SAME tol trips on near-solved content while detailed photos (currentErr ≫ floor) keep filling the budget. 0 = off (pure relative). ~0.02 = treat <2% residual as "solved".
	MinShapeGain                  float64           // low-contrast shape GATE: reject an accepted candidate whose mean per-pixel SSE improvement (−score/coveredArea) is below this — a shape whose colour barely differs from what it covers (a faint "ghost facet" over an already-solved region). A rejected shape counts as no-improvement, so the budget REALLOCATES to genuine detail elsewhere (the residual sampler steers there) or the run auto-stops once the whole image is contrast-saturated (no high-contrast shape left within MaxNoImprove tries). 0 = off. Surgical fix for flat-background over-fill / ghost facets; tune by EYE so it never erodes real soft gradients (those still carry meaningful per-pixel gain). Host-side selection logic only → golden-diff safe.
	RecolorVarSkip                float64           // recolor: skip the weighted-mean repaint for shapes whose owned target pixels have color variance above this (boundary-straddling fur/contour slivers) — keeps their crisp greedy/polish color instead of a muddy mean. 0 = off (repaint all).
	SampleBudget                  int               // per-shape scoring pixel budget for progressive sampling (0 -> backend default 4000; large = ~full-res)
	DetailStrength                float32           // detail-weighted sampling: bias late candidate centres toward high-detail TARGET cells (faces/linework) by scaling the sampling grid ×(1+s·detail), s ramping to this. 0 = off. ~0.35 suits organic content. Reduces face softness + smooth-region faceting; no benefit (slight cost) on flat content.
	DetailSamplingStart           float32           // progress fraction at which detail-weighted sampling engages (0 -> 0.6). Earlier = stronger detail focus but less coarse-base coverage.
	BoundaryRadius                bool              // boundary-aware radius: cap each candidate's size by its centre's distance to the nearest target boundary (luma edge / cutout silhouette) so shapes can't balloon ACROSS edges (cleaner flat/logo/cutout silhouettes, less organic "veil" overshoot).
	BoundaryPadding               float32           // px a shape may still reach past a boundary (0 -> 16). Larger = looser cap.
	BoundaryStart                 float32           // progress at which the cap engages, ramping to full by progress 1 (0 -> 0.42). Earlier = tighter silhouettes sooner, but constrains the coarse base.
	CanvasPad                     float32           // canvas-edge radius clamp: shrink any ellipse/rect whose rotated bbox extends past the canvas by more than CanvasPad*min(w,h) px on a side. Stops shapes ballooning outside the image rectangle (visible in-game, clipped in the preview) + saves budget on near-out-of-frame shapes. 0 = off. ~0.04 keeps a small edge bleed; helps opaque/busy content most.
	StandoutTol                   float64           // post-polish PERCEPTUAL standout suppression: detect shapes whose rim draws an edge the TARGET lacks (a visible circle/square the SSE metric is blind to) and recolour-to-local-mean or remove them, gated so the GLOBAL error rises at most this fraction. Opt-in (0 = off). The metric will NOT show the win — validate by eye; the gate only bounds the loss. ~0.005 = conservative.
	ZSwapTrials                   int               // z-order local swap EXPERIMENT (opt-in, 0 = off): after polish, try swapping up to this many z-adjacent overlapping pairs (ranked by local error), keeping only swaps that lower the hard-rendered error. Each trial is a full re-render — keep the cap modest. Aimed at opaque/flat content where stack order owns contested pixels.
	PersistGain                   float64           // persistent-error sampling EXPERIMENT (opt-in, 0 = off): upweight sampling cells whose error stagnates across refreshes by (1 + gain·stagnation), so small stubborn details (a saturated iris) stop losing the importance lottery to big soft regions. Sampling-only — the accept gate, knee and progress stay on the raw grid. See persist.go.
	GlyphDict                     bool              // glyph-dictionary EXPERIMENT (opt-in): offer the bank's mask words as greedy candidates — each word moment-fitted onto a residual blob, competing by exact ΔSSE against the primitive winner. Needs a backend that scores mask kinds (CPU; CUDA with the atlas); silently off otherwise. See glyph.go.
	CompactPenalty                bool              // bias the per-shape pick toward compact shapes (esp. the first few) — cleaner coarse stage
	OnDeviceSearch                bool              // run the random-candidate phase entirely on the GPU if the backend supports it; falls back to the host path otherwise
	MomentSeed                    bool              // moment-seeding: replace the blind random candidate batch with a closed-form covariance-ellipse seed (fitted from the residual error grid) plus a small LOCALISED refine pool. Far fewer candidates per shape -> large eval speedup; quality is held by the seed being the maximum-likelihood ellipse the random search targets anyway. Works on CPU+CUDA (bypasses on-device random; Evaluate/Apply untouched -> golden-diff safe). Opt-in for A/B.
	MomentRefine                  int               // moment-seeding refine-pool size (candidates per shape incl. the seed; 0 -> 256). The exact seed + this many localised kind-weighted refinements, scored via the normal Evaluate path + the existing hill-climb mutate.
	MomentSeeds                   int               // moment-seeding: number of error-sampled SEED CENTRES per shape (0/1 -> single). The MomentRefine budget is split across them — a single moment fit anchors the search to one centre (where blind random, sampling many centres, beats it); spreading the budget over K centres restores multi-location exploration at the same candidate cost.
	MomentDetailStart             float32           // moment-seeding HYBRID schedule: past this progress (0..1) the per-shape search HANDS OFF from the moment pool to the blind random search (the full RandomSamples brute force). The moment fit excels at the smooth coarse base but, being a 2nd-moment blob summary, never proposes the sharp SMALL shape a fine detail needs — and those late shapes are the CHEAP ones (progressive sampling), so the random handoff buys back perceptual detail crispness for little time. 0 = off (moment all the way, the speed-max default). ~0.6-0.7 = moment base + random detail. Only meaningful with MomentSeed.
	CoarseSearch                  bool              // coarse-to-fine on-device search: score the candidate batch at a CHEAP pixel cap to filter, then re-score only the survivors at the full SampleBudget and pick from those. The winner is full-budget scored (quality-safe), the bulk pays only the coarse cost — the dominant eval speed lever at high shape counts. CUDA-only; no-op on the CPU backend.
	CoarseBudget                  int               // coarse-filter pixel cap (0 -> 4000). Lower = cheaper filter; must stay high enough that the true winner is its partition's coarse-min.
	CoarseK                       int               // coarse survivors re-scored at full budget (0 -> 2048). Higher = smaller partitions -> the winner is more reliably included (quality), at a modest extra full-budget re-eval cost (the bulk stays cheap).
	CoarseFP16                    bool              // run the coarse FILTER pass in FP16/half2 (~2x ALU throughput; the FP32 re-eval still picks the winner). Lossy ranking — validate it doesn't miss winners (raise CoarseK if so). CUDA-only.
	Gaussian                      bool              // NICHE MODE: bypass the greedy entirely and reconstruct the image as StopAt soft GLOW splats jointly trained by the polish (engine.GenerateGaussian). For SMOOTH / gradient / painterly content only (8x better than greedy there; loses on fine detail). PolishOpts.Iters = the training budget. Additive — does NOT touch engine.Run.
	Polish                        bool              // run the joint differentiable polish pass after greedy
	PolishOpts                    PolishOptions     // polish config (zero value -> DefaultPolishOptions)
	BackFit                       bool              // run gated back-fitting passes (remove lowest-contribution shapes + regrow against the completed-canvas residual) before polish
	BackFitPasses                 int               // number of back-fitting passes (0 -> 1 when BackFit)
	BackFitFrac                   float64           // fraction of shapes removed+regrown per pass (0 -> 0.1)
	LiveBase                      int               // with LiveBatch>0: run the LIVE co-adaptation only for the first this-many shapes (the structural BASE), then hand off to the normal greedy loop for the remaining budget (the detail). 0 = LIVE all the way. The two-phase economy idea: an efficient co-adapted base costs fewer shapes, freeing the rest of the budget for genuine detail — affordable at the full budget because LIVE (with its per-batch polish) only runs on the cheap base.
	LiveBatch                     int               // EXPERIMENTAL LIVE-style component-init scheduler (0 = off, REPLACES greedy when >0): add shapes in batches of this size seeded from the largest residual components (big regions first) and re-polish ALL shapes jointly after every batch (co-adaptation), instead of the freeze-after-placement greedy. The proven low-primitive-count win (LIVE: 5 paths vs DiffVG 256). For the low-budget economy regime; per-batch polish is too costly at full budget. Host-side -> golden-diff safe. ~6-10 is a reasonable batch size.
	AnnealIters                   int               // EXPERIMENTAL basin-hopping / iterated local search (0 = off): after greedy+polish, run N outer iterations that randomly kick (remove low-value shapes + regrow vs residual), short-re-polish, and Metropolis-accept (escaping the greedy local minimum), keeping the best. For the LOW-budget "economy" regime (50-300 shapes) where greedy is most stuck; too costly at full budget. Keep-best gated -> never finishes worse. Host-side -> golden-diff safe.
	LockColor                     *model.RGBA       // MONO single-colour mode (brand logo / decal): when set, EVERY reconstruction shape is snapped to this exact working-space colour at the end of the run. Pairs with a target binarized to the same colour (engine.BinarizeForLock at the call site) so the grey antialiased-edge shapes never appear. nil = off. Host-side -> golden-diff safe.
	Progress                      func(shapes int, currentError float64)
	Status                        func(stage string) // optional: called at the START of each post-greedy phase (polish / back-fit / standout) with a human label, so a UI can show "what it's doing now" instead of a bar stuck at 100%. nil = ignored.
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

// gradientEvaluator is the optional capability of a backend to evaluate the radial-gradient kinds
// (per-pixel-alpha falloff). The CUDA backend routes eval to its block kernel's gradient branch; the
// CPU backend does not implement it (it already evaluates gradients natively). Set every run.
type gradientEvaluator interface{ SetGradients(on bool) bool }

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

// momentSearcher is the optional capability of a backend to run the on-device MOMENT-seeded
// search for one shape (fit `centers` covariance-ellipse seeds from the residual grid + a
// localised refine pool, score + argmin on-device — no per-candidate host transfer, which is
// what caps the host moment path at large pools). The CUDA backend implements it; absent on the
// CPU backend or an older DLL, so the engine type-asserts and falls back to the host moment pool.
// Independent of randomSearcher — either on-device path can be removed without affecting the other.
type momentSearcher interface {
	SearchMoment(seed int64, n, centers int, kinds []model.ShapeKind, kindCDF []float32,
		maxR float32, allowAlpha bool, alphaMin float32, compact bool, shapeCount int,
		grid []float32, gw, gh int, boundPad, boundMix, canvasPad float32) (model.Candidate, float32, bool)
}

// annealMaxR is the per-shape max radius schedule shared by the host generator
// (RandomShapes) and the on-device search: shapes shrink as the reconstruction
// progresses (coarse base first, fine detail later). Kept in one place so the two
// generation paths stay in lockstep.
func annealMaxR(w, h int, progress float32) float32 {
	diag := float32(math.Sqrt(float64(w*w + h*h)))
	scale := float32(annealRadiusStart - annealRadiusRange*math.Pow(float64(clampF(progress, 0, 1)), annealRadiusExp))
	maxR := diag * scale
	if maxR < annealRadiusFloor {
		maxR = annealRadiusFloor
	}
	return maxR
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
	// onto the device. A non-zero false-edge λ needs the device-side term (fp_set_polish_false_edge);
	// when the backend lacks it the CPU driver carries the experiment.
	feOK := opt.PolishOpts.FalseEdgeLambda == 0
	if !feOK {
		if s, ok := be.(interface{ PolishSetFalseEdge(lambda float64) bool }); ok {
			feOK = s.PolishSetFalseEdge(0) // capability probe; PolishWithBackend sets the real λ after setup
		}
	}
	ssimOK := opt.PolishOpts.SSIMLambda == 0
	if !ssimOK {
		if s, ok := be.(interface{ PolishSetSSIM(lambda float64) bool }); ok {
			ssimOK = s.PolishSetSSIM(0)
		}
	}
	var pr PolishResult
	if acc, ok := be.(PolishAccel); ok && acc.PolishSupported() && feOK && ssimOK {
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
	if polishDebug {
		applog.Printf("polish-debug gate: in=%.1f polished=%.1f (pre-soft=%.1f post-soft=%.1f) -> keep=%v", finalErr, postErr, pr.PreLoss, pr.PostLoss, postErr <= finalErr)
	}
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
// (polish -> recolor -> gate) exactly, so the CLI's -polish-json mode reproduces the shipped
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
