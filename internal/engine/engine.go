package engine

import (
	"math"
	"time"

	"fh6-paint-studio/internal/applog"
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
	RegionKinds                   bool              // region-gated kind selection (anime default): precompute metric.HardEdgeMap of the target; a candidate draws from the full kind pool with probability hard[centre] and is forced to ELLIPSE otherwise — rect/triangle rims only where the target itself has line-work/wedges, smooth shading built from ellipses. The generation-side fix for the standout-rect complaint (repair passes cap at ~1% of shapes; the kinds A/B showed the ellipse-only win/loss is decided by exactly this local split). On-device generators gate per-cell via fp_set_kind_gate; a device with on-device search but no gate export disables the feature for the run (speed-safe) — host generation gates natively.
	// SmoothGlowTau/-Prob gate the deep-smooth glow swap that rides RegionKinds: cells whose
	// hard-structure reading is below Tau swap their forced ellipse for a RIMLESS glow with
	// probability Prob. The presets set both per content mode; 0 keeps the engine default pair
	// (0.10/0.80) so a hand-built Options behaves as before. FH6_SMOOTHGLOW pins them for lab A/Bs.
	// NB this is load-bearing for smooth-zone quality: the swap density is what dissolves the
	// ellipse-rim patchwork the eye reads as facets, and SSE is blind to it — tune by eye.
	SmoothGlowTau  float64
	SmoothGlowProb float64
	// SegHard swaps the source of the gate map (needs RegionKinds): instead of metric.HardEdgeMap's
	// Sobel edge DENSITY in a 12px cell, the score comes from distance to an actual segmentation
	// boundary weighted by the colour contrast across it. Measured 2026-08-03 on three of the owner's
	// generations, HardEdgeMap barely discriminates — mean 0.67 over region interiors against 0.79 on
	// real boundaries (img_9) — because one line crossing a cell saturates it. Every anti-artifact
	// mechanism keys on this map (kind gate, glow swap, region-weighted polish terms), so all three
	// go quiet in exactly the smooth zones the owner keeps pointing at. FH6_SEGHARD
	// "k,minSize,contrast,falloff" pins the parameters for lab A/Bs. See metric/segboundary.go.
	SegHard bool
	// ProposerPath points at the trained candidate proposer (export_weights.py output). Empty = off.
	// ProposerFrac is the share of each candidate batch drawn from it, ProposerEvery how many shapes
	// pass between refreshes of the proposal map -- the canvas moves slowly, so a sweep every N steps
	// amortises to nothing next to the scoring it replaces.
	ProposerPath string
	// ProposerBlob is the same export carried in memory rather than on disk, so a GUI build can
	// embed the model instead of shipping a third file next to the executable.
	ProposerBlob   []byte
	ProposerFrac   float64
	ProposerEvery  int
	ProposerJitter float64 // spread around each proposal, in patch widths (0 -> 0.05)
	// ProposerConfGate hands the decision of WHERE proposals are used to the network's own
	// confidence head instead of the hand-made region gate. Needs a model exported with one.
	ProposerConfGate bool
	// ProposerConfTau is how much predicted advantage the learned gate demands. See gen.comp: the
	// head's baseline was a handful of random draws, the engine's is thousands, so zero is a
	// permissive threshold rather than a neutral one.
	ProposerConfTau float64
	// WarpEval picks the CUDA eval kernel: false (default) = block-per-candidate, which is both
	// faster here (large early shapes want 128 threads each, not 32) and the only one carrying the
	// per-pixel-alpha branch the gradient kinds need. true = warp-per-candidate, reference only.
	// Set on every run so no consumer inherits a pooled backend's — or the DLL's — previous state.
	WarpEval bool
	// Size-conditioned glow swap: an ellipse candidate whose sqrt(rx·ry) exceeds BigGlowTau·min(w,h)
	// becomes a rimless glow with probability BigGlowProb. Independent of RegionKinds — the hardness
	// gate asks whether the REGION has structure, while this asks whether THIS shape is big enough
	// for its rim to stop being a local edge and become a long closed contour (the "oval on the
	// neck" the eye traces even where Sobel-based false-edge barely charges it, and even in textured
	// zones the hardness gate marks structured). On-device via fp_set_big_glow; host path mirrors it.
	BigGlowTau        float64
	BigGlowProb       float64
	BigGlowDisk       bool          // emit KindDisk instead of KindGlow: an opaque core out to ~0.4R with a feathered rim covers like the ellipse it replaces (cheaper in SSE) while still drawing no step
	BigGlowAllKinds   bool          // extend the swap from ellipses to rects/triangles (a big triangle's straight rims are the same contour artifact); a triangle is re-emitted as the glow inscribed in its vertex box
	RampGlow          bool          // ramp-aware hotter glow swap (opt-in, BUST — not defaulted; needs RegionKinds): precompute metric.RampMap of the target and, where a cell reads as a genuine smooth gradient (ramp > thresh), run the deep-smooth glow swap at a HOTTER tau/prob than the global pair. Aimed to recover the img_10 win from a global tau-raise without its structured-content regression; measured noise (img_10 SSE +0.01% parity) because the global win came from moderate-hardness cells RampMap excludes. Code kept + CLI-reachable; on-device via fp_set_ramp_glow (rides fp_set_kind_gate + fp_set_glow_swap), inert when off. See regionkinds.go.
	SoftSwapTol       float64       // post-polish SOFT-SWAP standout repair (opt-in, 0 = off): replace the worst standout rect/triangle shapes (rim draws an edge the target lacks) with a soft/round shape moment-fitted to the SAME footprint (ellipse / feathered disk / glow; same colour + z), gated so the GLOBAL error rises at most this fraction. Substitution keeps the coverage, so — unlike StandoutTol's recolour/fade/remove menu, which live polish starves at the gate — many repairs fit in the same budget. ~0.005-0.02. Judge by eye; see softswap.go.
	RimAim            bool          // aim the soft-swap by RIM DEBT instead of interior false-edge mass, and let it consider ELLIPSES (rimsalience.go). The artefact the owner names — a contour of the reconstruction standing on ground the picture leaves smooth — is a property of a shape's boundary and sits mostly on ellipses, which the original ordering never even offered as candidates. Measured out of the engine (post-hoc on finished stacks): softening the worst offenders removes ~23% of the rim debt at no SSE cost, while softening the same NUMBER of random shapes makes the rim debt worse — so the aim, not the softening, is what does the work. Needs SoftSwapTol.
	SkewRefine        bool          // post-polish monotone SHEAR refine (skewrefine.go): line-search slot 5 for every rectangle and bank word, keep it only where the exact occlusion-aware local error falls, then gate the whole pass end to end. Ellipse/glow/disk are excluded — a sheared ellipse is another rotated ellipse, so the shear buys them nothing. Chosen over a sixth polish DOF, which cleared the bar on only 4 of 7 pairs because the shear trades against rotation and width and moves the whole basin.
	GeomRefine        bool          // post-polish monotone COORDINATE refine (skewrefine.go): the same monotone machinery walking EVERY geometry parameter of every shape, not just the shear. Motivated by a measurement — a line search over an ellipse shear, a parameter that buys the ellipse no new shape, still recovered 0.5-3%, so the polish leaves its shapes short of the local optimum and any spare direction cashes that in. Supersedes SkewRefine when both are set.
	SoftSwapPre       bool          // soft-swap PRE-polish variant: run the swap on the GREEDY result and let the joint polish co-adapt around the substitutions, gated end-to-end (polish(greedy) vs polish(swap(greedy)): keep the swap branch only if SSE lands within SoftSwapTol AND the global false-edge ratio improves). The post-polish pass starves at the cumulative gate (~4-7 swaps — every substitution on a converged optimum costs irreducible SSE); pre-polish the redistribution is the polish's job. Needs Polish; no-op with BackFit (trio partition).
	ZSwapTrials       int           // z-order local swap EXPERIMENT (opt-in, 0 = off): after polish, try swapping up to this many z-adjacent overlapping pairs (ranked by local error), keeping only swaps that lower the hard-rendered error. Each trial is a full re-render — keep the cap modest. Aimed at opaque/flat content where stack order owns contested pixels.
	PersistGain       float64       // persistent-error sampling EXPERIMENT (opt-in, 0 = off): upweight sampling cells whose error stagnates across refreshes by (1 + gain·stagnation), so small stubborn details (a saturated iris) stop losing the importance lottery to big soft regions. Sampling-only — the accept gate, knee and progress stay on the raw grid. See persist.go.
	SaliencyQuota     float64       // saliency QUOTA (opt-in, 0 = off): the final quota-fraction of the budget places shapes ONLY inside the top-detail cells of the target (sampling grid hard-masked to the salient region; the accept gate stays raw). Unlike a sampling BIAS (measured wash, see persist.go), zeroing the rest forces the per-shape argmax to spend the reserved shapes on the most visible detail (eyes/faces at mid budgets). Needs the detail map (auto-built when set).
	ShadePrepass      bool          // shading PRE-PASS (opt-in): before the greedy, claim coherent linear-ramp regions of the target as a two-shape stack — an opaque base rect + the bank's linear-gradient word on top, both colours exact-solved by the backend. In linear light the stack IS a linear interpolation between the two colours, so one claim replaces the many translucent facets greedy spends on smooth shading. Only regions with a coherent non-zero gradient are claimed and the ramp must beat the flat cover by a margin (never claims flat fills — the region-fill lesson). Needs a mask-capable backend. See shadepre.go.
	LooRefit          int           // LOO refit rounds (opt-in, 0=off; needs Polish): after the polish, measure every shape's exact leave-one-out fit contribution in the FINAL stack (occlusion-aware — greedy scores at placement time and later shapes overpaint), prune the harmful + tiny tail (≤25%/round), regrow the freed budget against the residual, re-polish, and gate end-to-end. Directly reclaims the wasted budget the owner sees during generation. See loorefit.go.
	AnalyticAlpha     bool          // analytic per-candidate alpha (opt-in): eval re-solves the optimal color for a small alpha grid over [alphaMin,1] and keeps the ΔSSE-min (alpha, color) pair — alpha becomes (grid-)exact instead of sampled ~U(alphaMin,1). Organic modes only (needs free alpha). See alphagrid.go.
	ArtifactFix       bool          // artifact-repair pass (opt-in): rank shapes by false-edge energy they own in the final render (contrast the target doesn't have — eye-visible specks/rims SSE undercharges), then delete / soften / glow-swap the offenders under local FE+SSE gates. See artifactfix.go.
	MergeRefit        bool          // merge consolidation inside the LOO-refit rounds (opt-in; needs LooRefit>0): collapse near-duplicate pairs (same kind, near-same color, high overlap — 6-8% of a @3000 stack) into one moment-fitted shape, freeing slots the round's regrow re-spends. See mergerefit.go.
	TermRegionWeight  bool          // region-weighted FE/EAGLE polish terms (opt-in): build PolishOpts.TermWeight = 1−metric.HardEdgeMap(target) so the perceptual λ terms press hard in SMOOTH zones (where the translucent-rim patchwork lives) and ~vanish on legitimate line-work. Lets λ run far stronger than the global-λ compromise. Applies only when FE/EAGLE λ > 0; device without fp_set_term_weight runs unweighted (log, GPU polish kept).
	SmoothBase        bool          // smooth-region gradient BASE (opt-in): before the greedy, segment LARGE smooth regions (low HardEdgeMap cells, colour-continuous BFS) and claim each with a minimal stack — an opaque base + up to 3 gradient primitives (linear-ramp word / arc bands / glow / disk), ALL colours solved jointly in linear light (stacksolve.go). One 2-4 shape stack replaces the hundreds of translucent facets whose rims are the smooth-zone patchwork artifact; every layer must deepen the earn ≥25% and the gradient layers must carry ≥20% of it, else the stack rolls back (the region-fill lesson). See smoothbase.go.
	GlobalAlphaSweeps int           // closed-form ALPHA sweeps alternating with the colour solve on the frozen stack (0 = off, needs GlobalColorIters > 0). The composite is affine in one layer's alpha as well as in its colour, so the optimum has a closed form the polish only approximates with Adam on a soft-coverage surrogate. Clamped to the preset's alpha floor; skipped for cutouts, whose silhouette must stay solid.
	GlobalColorInLoo  bool          // ALSO run the joint colour re-solve at the head of every LOO-refit round, not only once after them (needs GlobalColorIters > 0). The LOO ranking measures each shape's contribution; a shape whose colour is stale looks harmful for a reason unrelated to redundancy, so the prune ranks staleness instead. Separate gate from GlobalColorIters so the measured single-pass default is unaffected.
	OrientAspect      float64       // anisotropy prior from the structure tensor (opt-in, <=1 = off): the coherence of the local tensor decides how ELONGATED a candidate is drawn and how tightly its angle hugs the edge direction — this value is the maximum aspect ratio at full coherence. The orientation seed alone has always been applied everywhere, including flat regions where the angle is noise; approximation theory puts the n^-2 rate on elements matched to locally ANISOTROPIC structure, and our measured slope is -0.98 (isotropic), so "how elongated, and how confidently" is the missing input rather than "which way".
	FoBaEvery         int           // run a BACKWARD step every N placed shapes (0 = off): measure the stack's exact leave-one-out contributions mid-greedy and drop the shapes that are already harmful, returning their slots to the greedy immediately. Same measurement LooRefit uses after the fact — the biggest quality win on record (-10.5% SSE) — but a shape dead since shape 300 otherwise holds its slot for 700 more placements. Only strictly-negative contributions are dropped, capped at 5% of the stack per step. See foba.go.
	CompSeeds         int           // residual connected-component seeds added to every greedy step's candidate pool (0 = off). Our moment seeding fits a covariance ellipse inside a LOCAL WINDOW, which has no relationship to the picture and happily straddles two unrelated regions; a connected component of the residual is a region the image itself delimits. Purely ADDITIVE — the seeds are scored by the same evaluator and only win if they beat the search's own answer — so it can cost wall time but not quality. See compseed.go.
	GlobalColorIters  int           // global joint colour re-solve (opt-in, 0=off): after the LOO refit, re-solve EVERY layer's RGB jointly for the frozen geometry instead of leaving each shape coloured against the canvas that existed when the greedy placed it. Compositing is exactly affine in the layer colours, so this is a convex box-constrained least-squares solved matrix-free by projected FISTA; the iteration count is this value. Gated on the hard render like every pass. See globalcolor.go.
	GlyphPrepass      bool          // glyph PRE-PASS (opt-in): before the greedy, claim flat-colour components of the TARGET that match a dictionary silhouette (signature match + strict IoU verification) as single mask-word shapes — one word instead of the many primitives the greedy would spend. Needs a mask-capable backend; flat/logo content is the target audience. See glyphpre.go.
	GlyphDict         bool          // glyph-dictionary EXPERIMENT (opt-in): offer the bank's mask words as greedy candidates — each word moment-fitted onto a residual blob, competing by exact ΔSSE against the primitive winner. Needs a backend that scores mask kinds (CPU; CUDA with the atlas); silently off otherwise. See glyph.go.
	CompactPenalty    bool          // bias the per-shape pick toward compact shapes (esp. the first few) — cleaner coarse stage
	OnDeviceSearch    bool          // run the random-candidate phase entirely on the GPU if the backend supports it; falls back to the host path otherwise
	MomentSeed        bool          // moment-seeding: replace the blind random candidate batch with a closed-form covariance-ellipse seed (fitted from the residual error grid) plus a small LOCALISED refine pool. Far fewer candidates per shape -> large eval speedup; quality is held by the seed being the maximum-likelihood ellipse the random search targets anyway. Works on CPU+CUDA (bypasses on-device random; Evaluate/Apply untouched -> golden-diff safe). Opt-in for A/B.
	MomentRefine      int           // moment-seeding refine-pool size (candidates per shape incl. the seed; 0 -> 256). The exact seed + this many localised kind-weighted refinements, scored via the normal Evaluate path + the existing hill-climb mutate.
	MomentSeeds       int           // moment-seeding: number of error-sampled SEED CENTRES per shape (0/1 -> single). The MomentRefine budget is split across them — a single moment fit anchors the search to one centre (where blind random, sampling many centres, beats it); spreading the budget over K centres restores multi-location exploration at the same candidate cost.
	MomentDetailStart float32       // moment-seeding HYBRID schedule: past this progress (0..1) the per-shape search HANDS OFF from the moment pool to the blind random search (the full RandomSamples brute force). The moment fit excels at the smooth coarse base but, being a 2nd-moment blob summary, never proposes the sharp SMALL shape a fine detail needs — and those late shapes are the CHEAP ones (progressive sampling), so the random handoff buys back perceptual detail crispness for little time. 0 = off (moment all the way, the speed-max default). ~0.6-0.7 = moment base + random detail. Only meaningful with MomentSeed.
	CoarseSearch      bool          // coarse-to-fine on-device search: score the candidate batch at a CHEAP pixel cap to filter, then re-score only the survivors at the full SampleBudget and pick from those. The winner is full-budget scored (quality-safe), the bulk pays only the coarse cost — the dominant eval speed lever at high shape counts. CUDA-only; no-op on the CPU backend.
	CoarseBudget      int           // coarse-filter pixel cap (0 -> 4000). Lower = cheaper filter; must stay high enough that the true winner is its partition's coarse-min.
	CoarseK           int           // coarse survivors re-scored at full budget (0 -> 2048). Higher = smaller partitions -> the winner is more reliably included (quality), at a modest extra full-budget re-eval cost (the bulk stays cheap).
	CoarseFP16        bool          // run the coarse FILTER pass in FP16/half2 (~2x ALU throughput; the FP32 re-eval still picks the winner). Lossy ranking — validate it doesn't miss winners (raise CoarseK if so). CUDA-only.
	Gaussian          bool          // NICHE MODE: bypass the greedy entirely and reconstruct the image as StopAt soft GLOW splats jointly trained by the polish (engine.GenerateGaussian). For SMOOTH / gradient / painterly content only (8x better than greedy there; loses on fine detail). PolishOpts.Iters = the training budget. Additive — does NOT touch engine.Run.
	Polish            bool          // run the joint differentiable polish pass after greedy
	PolishOpts        PolishOptions // polish config (zero value -> DefaultPolishOptions)
	BackFit           bool          // run gated back-fitting passes (remove lowest-contribution shapes + regrow against the completed-canvas residual) before polish
	BackFitPasses     int           // number of back-fitting passes (0 -> 1 when BackFit)
	BackFitFrac       float64       // fraction of shapes removed+regrown per pass (0 -> 0.1)
	LiveBase          int           // with LiveBatch>0: run the LIVE co-adaptation only for the first this-many shapes (the structural BASE), then hand off to the normal greedy loop for the remaining budget (the detail). 0 = LIVE all the way. The two-phase economy idea: an efficient co-adapted base costs fewer shapes, freeing the rest of the budget for genuine detail — affordable at the full budget because LIVE (with its per-batch polish) only runs on the cheap base.
	LiveBatch         int           // EXPERIMENTAL LIVE-style component-init scheduler (0 = off, REPLACES greedy when >0): add shapes in batches of this size seeded from the largest residual components (big regions first) and re-polish ALL shapes jointly after every batch (co-adaptation), instead of the freeze-after-placement greedy. The proven low-primitive-count win (LIVE: 5 paths vs DiffVG 256). For the low-budget economy regime; per-batch polish is too costly at full budget. Host-side -> golden-diff safe. ~6-10 is a reasonable batch size.
	AnnealIters       int           // EXPERIMENTAL basin-hopping / iterated local search (0 = off): after greedy+polish, run N outer iterations that randomly kick (remove low-value shapes + regrow vs residual), short-re-polish, and Metropolis-accept (escaping the greedy local minimum), keeping the best. For the LOW-budget "economy" regime (50-300 shapes) where greedy is most stuck; too costly at full budget. Keep-best gated -> never finishes worse. Host-side -> golden-diff safe.
	LockColor         *model.RGBA   // MONO single-colour mode (brand logo / decal): when set, EVERY reconstruction shape is snapped to this exact working-space colour at the end of the run. Pairs with a target binarized to the same colour (engine.BinarizeForLock at the call site) so the grey antialiased-edge shapes never appear. nil = off. Host-side -> golden-diff safe.
	Progress          func(shapes int, currentError float64)
	Status            func(stage string)  // optional: called at the START of each post-greedy phase (polish / back-fit / standout) with a human label, so a UI can show "what it's doing now" instead of a bar stuck at 100%. nil = ignored.
	OnPhase           func(PhaseProgress) // optional: run-wide progress + a time estimate covering EVERY phase, not just the greedy loop (see eta.go). nil = not computed.
	Cancel            func() bool         // optional: checked at the loop top + before the polish/backfit post-process; return true to stop early (keeps the shapes placed so far). nil = never cancel.
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
	Setup        time.Duration    // one-time: initial canvas/grid + backend wiring (metric maps split into Maps)
	Maps         time.Duration    // setup's metric maps: orientation/coherence, hard-edge, detail grid, boundary distance, ramp
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

	// Pre-greedy claims and post-greedy passes. Each is the pass's OWN cost: the polish, colour solve
	// and merge a pass nests are billed to their own field and subtracted here, so the fields stay
	// disjoint and Total-Accounted is a real blind spot rather than double counting.
	SmoothBase  time.Duration // smooth-region gradient base claims (smoothbase.go)
	ShadePre    time.Duration // shading pre-pass (shadepre.go)
	GlyphPre    time.Duration // glyph pre-pass (glyphpre.go)
	LooRefit    time.Duration // LOO refit rounds (loorefit.go)
	LooRounds   int           // LOO rounds entered (the loop stops early when a round finds nothing to regrow or fails its gate)
	MergeRefit  time.Duration // near-duplicate merge inside the LOO rounds (mergerefit.go)
	GlobalColor time.Duration // joint colour/alpha re-solve (globalcolor.go), incl. the in-LOO solves
	ArtifactFix time.Duration // artifact-repair pass (artifactfix.go)
	Anneal      time.Duration // basin-hopping / iterated local search (anneal.go)
	ZSwap       time.Duration // z-order swap trials (zswap.go)
	SoftSwap    time.Duration // standout soft-swap, pre- and post-polish forms (softswap.go)
	Standout    time.Duration // standout suppression (standout.go)
	SkewRefine  time.Duration // monotone shear refine (skewrefine.go)

	Total time.Duration
}

// Accounted is the sum of every measured phase. Total minus this is the run's UNMEASURED time — the
// number that makes a new blind spot visible instead of silently absorbing the next expensive pass.
func (t Timings) Accounted() time.Duration {
	return t.Setup + t.Maps + t.Generate + t.Mutate + t.Evaluate + t.Apply + t.ErrorGrid + t.Sampler +
		t.PostProcess + t.BackFit + t.Polish + t.SmoothBase + t.ShadePre + t.GlyphPre + t.LooRefit +
		t.MergeRefit + t.GlobalColor + t.ArtifactFix + t.Anneal + t.ZSwap + t.SoftSwap + t.Standout +
		t.SkewRefine
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

// deviceKindGater is the optional capability of a backend to gate on-device candidate kind picks
// with the per-pixel region-kinds map (fp_set_kind_gate). SetKindGate(nil) clears a stale map;
// false = the export is missing (older DLL / Vulkan) and the engine disables the gate for the run.
type deviceKindGater interface{ SetKindGate(hard []float32) bool }

// deviceProposer is the optional capability of a backend to run the neural candidate proposer in its
// on-device generator. The engine spends ~96% of a run exactly scoring candidates, and that cost is
// linear in how many are scored; the network's job is to make a much smaller batch worth scoring.
//
// The contract that makes this safe: every proposal is still scored by the same exact eval as a
// random draw, and only part of the batch comes from the network, so a weak or stale model costs
// wall-clock and cannot damage the output.
// proposerGater is the optional capability of a backend to switch the proposal gate. Separate from
// deviceProposer so a backend that predates the confidence head keeps satisfying that interface.
type proposerGater interface {
	SetProposerGate(on bool, tau float32) bool
}

type deviceProposer interface {
	SetProposer(blob []byte) bool
	SetProposerEnabled(on bool, progress, frac, jitter float32) bool
	RunProposer(progress float32) bool
}

// deviceBigGlower is the optional capability of a backend to run the size-conditioned glow swap in
// its on-device generators (fp_set_big_glow). Independent of deviceKindGater; (0,0) clears it.
type deviceBigGlower interface {
	SetBigGlow(tau, prob float32, allKinds bool, kind int32) bool
}

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

// coherenceSetter is the optional capability of a backend whose generator can take the structure
// tensor's per-pixel coherence, so candidate ELONGATION follows the local anisotropy instead of one
// global aspect. Returns false when the loaded shim predates the export — the caller then falls back
// to host generation rather than running without the prior and reporting success.
type coherenceSetter interface {
	SetCoherence(coh []float32, aspectCap float32) bool
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
	// The greedy places candidates above the preset's organic alpha floor, and the descent used to
	// walk them back down to a hard-coded 0.05 — the floor was shipped as a quality default and the
	// next stage undid it. Cutouts keep the historical bound: their candidates are opaque by
	// construction, so a floor here would be a different change with no measurement behind it.
	if opt.PolishOpts.AlphaMin == 0 && opt.AllowAlpha && !opt.TransparentBG {
		opt.PolishOpts.AlphaMin = polishAlphaFloor(opt.AlphaMin)
	}
	// Polish runs on the device (the only backend). A non-zero false-edge λ needs the device-side
	// term (fp_set_polish_false_edge); when the backend lacks it the term is dropped, not the polish.
	// Region-weighted terms: build the 1−hard map once per run (Options.TermRegionWeight); the
	// setters below ship it to the device.
	if opt.TermRegionWeight && opt.PolishOpts.TermWeight == nil &&
		(opt.PolishOpts.FalseEdgeLambda > 0 || opt.PolishOpts.EagleLambda > 0) {
		hard := metric.HardEdgeMap(be.Target(), w, h)
		tw := make([]float32, len(hard))
		for i, hv := range hard {
			tw[i] = 1 - hv
		}
		opt.PolishOpts.TermWeight = tw
	}
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
	eagleOK := opt.PolishOpts.EagleLambda == 0
	if !eagleOK {
		if s, ok := be.(interface{ PolishSetEagle(lambda float64) bool }); ok {
			eagleOK = s.PolishSetEagle(0) // capability probe; PolishWithBackend sets the real λ after setup
		}
		// EAGLE ships as an anime-mode DEFAULT, so an accel backend without the export (older DLL,
		// Vulkan until its port) must NOT drag the whole polish onto the CPU driver — drop the term
		// and keep the GPU polish instead. (FE/SSIM keep the CPU-fallback contract: their absence
		// means a truly old DLL, and an EXPLICIT experiment λ still deserves the exact term.)
		if !eagleOK {
			if acc, ok := be.(PolishAccel); ok && acc.PolishSupported() {
				applog.Printf("polish: device lacks fp_set_polish_eagle — EAGLE term disabled (GPU polish kept)")
				opt.PolishOpts.EagleLambda = 0
				eagleOK = true
			}
		}
	}
	var pr PolishResult
	if acc, ok := be.(PolishAccel); ok && acc.PolishSupported() && feOK && ssimOK && eagleOK {
		pr = PolishWithBackend(shapes, be.Target(), be.Weight(), w, h, opt.Background, opt.TransparentBG, opt.PolishOpts, acc)
	} else {
		applog.Printf("polish: device lacks polish support — skipping polish (shapes returned unpolished)")
		pr = PolishResult{Shapes: shapes}
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
		for i := range pr.Phases { // accumulate: Timings.Polish sums every call, so the split must too
			tm.PolishPhases[i] += pr.Phases[i]
		}
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
