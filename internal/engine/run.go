package engine

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"time"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/metric"
	"fh6-paint-studio/internal/model"
)

// resolveAlphaMin applies the candidate alpha floor's fallback: unset or out of range takes the
// photo default, which the presets override per mode.
func resolveAlphaMin(v float32) float32 {
	if v <= 0 || v > 1 {
		return defaultAlphaMin
	}
	return v
}

// polishAlphaFloor returns the floor the polish descent may push alpha to, given the candidate
// floor the greedy used. FH6_PALPHA pins it for A/Bs from the studio, which has no flags; 0 there
// restores the historical behaviour of a free descent down to 0.05.
func polishAlphaFloor(candMin float32) float64 {
	if s := os.Getenv("FH6_PALPHA"); s != "" {
		var v float64
		if n, err := fmt.Sscanf(s, "%f", &v); n == 1 && err == nil && v >= 0 && v < 1 {
			return v
		}
	}
	return float64(resolveAlphaMin(candMin))
}

// jitterOr resolves the proposal spread: the network emits a fixed set of modes per location, so a
// batch without spread would carry only as many distinct candidates as there are heads.
func jitterOr(v float64) float64 {
	if v <= 0 {
		return 0.05
	}
	return v
}

// gradEvalSearch (FH6_GRAD_EVAL=1) scores the radial-gradient candidates with their true
// per-pixel alpha during the greedy search instead of as solid ellipses.
var gradEvalSearch = os.Getenv("FH6_GRAD_EVAL") == "1"

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
	kindGate   *kindGate
	devSearch  randomSearcher
	devProp    deviceProposer
	propEvery  int
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
	coh        []float32 // structure-tensor coherence, nil unless OrientAspect > 1
	aspectCap  float32
	persist    *persistCtx
	salient    []bool // lazy saliency-quota cell mask (see saliency.go)
	glyphs     bool   // glyph-dictionary proposer active (Options.GlyphDict + a mask-capable backend)
	initialErr float64
	finalErr   float64

	// Component-seed bookkeeping: a seeding idea that never wins the argmax is inert, and only a
	// counter distinguishes that from one that wins and does not help.
	compSeedTried int
	compSeedWon   int
	fobaDropped   int
	fobaNext      int // high-water mark for the next backward step (see foba.go)
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
		if r.opt.SmoothBase {
			r.smoothBase() // broad smooth-region stacks claim FIRST (deepest in the z-stack)
		}
		if r.opt.ShadePrepass && r.glyphs {
			r.shadePrepass()
		}
		if r.opt.GlyphPrepass && r.glyphs {
			r.glyphPrepass()
		}
		r.greedy()
	}
	if r.opt.CompSeeds > 0 {
		applog.Printf("comp-seeds: %d proposed, %d won the step", r.compSeedTried, r.compSeedWon)
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
	if opt.StopAt < 1 {
		opt.StopAt = 1 // self-defend: callers clamp, but a 0/negative budget would NaN the progress fraction
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
	var persist *persistCtx
	if opt.PersistGain > 0 {
		persist = newPersistCtx(opt.PersistGain, gw*gh)
		persist.update(grid)
	}

	diag := math.Sqrt(float64(w*w + h*h))
	moveStep := float32(math.Max(mutateStepFloor, diag*mutateMoveFrac))
	radiusStep := float32(math.Max(mutateStepFloor, diag*mutateRadiusFrac))
	rounds, perRound := planHillClimb(opt.MutatedSamples)

	// Semi-transparent shapes: alpha ~U(alphaMin,1). Forced opaque for cutouts
	// (the reconstructed object must stay alpha=1 so the cutout silhouette is solid).
	allowAlpha := opt.AllowAlpha && !opt.TransparentBG
	alphaMin := resolveAlphaMin(opt.AlphaMin)

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

	// The greedy runs on the FAST hard-eval path (warp kernel). That path has no per-pixel-alpha
	// branch, so a glow-swapped candidate is SCORED as a solid ellipse and then COMPOSITED with its
	// radial falloff — the search ranks it by a shape it will not place. FH6_GRAD_EVAL=1 scores
	// those candidates honestly instead, at the cost of the slower block kernel for every batch.
	// Experiment: measure quality against the wall-time it costs before defaulting either way.
	if gs, ok := be.(gradientEvaluator); ok {
		gs.SetGradients(gradEvalSearch)
	}

	// Eval-kernel choice. Set UNCONDITIONALLY: this used to be left to the DLL's own default, which
	// only the CLI ever overrode — so the studio silently ran a different (slower, and gradient-blind)
	// kernel than every CLI benchmark. Never let a backend's internal default decide engine behaviour.
	if we, ok := be.(interface{ SetWarpEval(bool) }); ok {
		we.SetWarpEval(opt.WarpEval)
	}

	genTarget := opt.StopAt
	if opt.Overdraw > 1 && !allowAlpha {
		// Over-generate + contribution-prune is an opaque-only path: the contribution
		// model (shapeContributions) assumes opaque replace-ownership and would mis-rank
		// (and drop) semi-transparent shapes. With alpha we place exactly the budget and
		// prune only fully-occluded shapes.
		genTarget = int(float32(opt.StopAt) * opt.Overdraw)
	}

	// Edge-orientation map: seed elongated shapes along local edges (hair, folds). With
	// OrientAspect > 1 the same structure tensor also yields the per-pixel COHERENCE, which the
	// generator uses to decide HOW elongated a candidate should be — the orientation alone is
	// defined even in flat regions, where it is noise.
	var orient, coh []float32
	aspectCap := float32(opt.OrientAspect)
	if aspectCap > 1 {
		orient, coh = metric.OrientationCoherenceMap(be.Target(), w, h)
	} else {
		orient = metric.OrientationMap(be.Target(), w, h)
		aspectCap = 0
	}

	// Region-weighted polish terms: build the 1−HardEdgeMap ONCE here instead of on every polish call.
	// applyPolish takes opt by value, so base, back-fit, LOO re-polish, anneal and soft-swap each
	// rebuilt this native-res Sobel map from scratch. The map depends only on the frozen target, so one
	// build feeds them all — a path that derives its options inherits it through the slice, and any that
	// does not simply rebuilds as before (identical value, so output is unchanged either way).
	if opt.TermRegionWeight && opt.PolishOpts.TermWeight == nil &&
		(opt.PolishOpts.FalseEdgeLambda > 0 || opt.PolishOpts.EagleLambda > 0) {
		hard := metric.HardEdgeMap(be.Target(), w, h)
		tw := make([]float32, len(hard))
		for i, hv := range hard {
			tw[i] = 1 - hv
		}
		opt.PolishOpts.TermWeight = tw
	}

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
	if opt.DetailStrength > 0 || opt.SaliencyQuota > 0 {
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

	// Region-gated kinds (RegionKinds; anime default): precompute the target's hard-structure map
	// ONCE. Candidate kind picks draw the full pool only where the target itself has line-work/
	// wedges and fall back to ellipse in smooth shading (regionkinds.go) — the generation-side fix
	// for the standout rect/tri artifact. The on-device generators gate per-cell via
	// fp_set_kind_gate; a device that searches on-device but lacks the export DISABLES the gate
	// (a silent 2× host-generation wall regression is worse than losing the default — rebuild the
	// DLL, or force host gating with OnDeviceSearch off).
	var kg *kindGate
	if opt.RegionKinds {
		glowTau, glowProb := resolveSmoothGlow(opt.SmoothGlowTau, opt.SmoothGlowProb)
		kg = &kindGate{hard: gateHardMap(be.Target(), w, h, opt), w: w, h: h, tau: glowTau, prob: glowProb}
		if opt.RampGlow {
			kg.ramp = metric.RampMap(be.Target(), w, h) // hotter glow swap in genuine gradient zones
		}
	}
	// The size-conditioned glow swap does not need the hardness map, so it gets a gate of its own
	// when region-kinds is off (kindGate.pick falls straight through when hard is nil).
	bigTau, bigProb := resolveBigGlow(opt)
	if bigTau > 0 && bigProb > 0 {
		if kg == nil {
			kg = &kindGate{w: w, h: h}
		}
		kg.bigTau, kg.bigProb, kg.bigAllKinds = float32(bigTau), float32(bigProb), opt.BigGlowAllKinds
		kg.bigKind = bigGlowKind(opt)
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
			// The anisotropy prior needs the coherence map on the device. A DLL that predates the
			// export cannot take it, and keeping the device path there would leave the option
			// silently inert — so that case drops to the host generator instead, which carries the
			// same prior at a wall cost.
			if aspectCap > 1 {
				cs, ok := be.(coherenceSetter)
				if !ok || !cs.SetCoherence(coh, aspectCap) {
					applog.Printf("orient-aspect %.2f: device generator lacks fp_set_coherence — host generation", aspectCap)
					devSearch = nil
				}
			}
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
	// Region gate × device generators: upload the map (fp_set_kind_gate) when supported, else
	// disable the gate (see the comment above). ALWAYS clear a stale map when the gate is off —
	// a pooled backend must not carry the previous run's gate.
	if gk, ok := be.(deviceKindGater); ok {
		if kg != nil && (devSearch != nil || devMoment != nil) {
			if !gk.SetKindGate(kg.hard) {
				applog.Printf("region-kinds: device search lacks fp_set_kind_gate — gate disabled (rebuild the DLL, or disable on-device search to force host gating)")
				kg = nil
			}
		}
		if kg == nil {
			_ = gk.SetKindGate(nil)
		}
		// Deep-smooth glow swap rides the gate: same degradation contract (an old DLL without the
		// export runs the plain ellipse gate — visibly weaker in smooth zones, never slower).
		if gs, ok2 := be.(interface{ SetGlowSwap(tau, prob float32) bool }); ok2 {
			tau, prob := float32(0), float32(0)
			if kg != nil {
				tau, prob = kg.tau, kg.prob
			}
			if !gs.SetGlowSwap(tau, prob) && kg != nil && prob > 0 {
				applog.Printf("region-kinds: device lacks fp_set_glow_swap — glow swap disabled on-device (rebuild the DLL)")
			}
		}
		// Ramp-aware hotter glow swap (Options.RampGlow) rides the same gate: upload the ramp map +
		// hot params where present, else clear (a pooled backend must not carry a stale ramp map).
		if rg, ok3 := be.(interface {
			SetRampGlow(ramp []float32, thresh, tau, prob float32) bool
		}); ok3 {
			if kg != nil && kg.ramp != nil {
				if !rg.SetRampGlow(kg.ramp, rampGlowThresh, smoothGlowTauHot, smoothGlowProbHot) {
					applog.Printf("region-kinds: device lacks fp_set_ramp_glow — plain glow swap kept (rebuild the DLL)")
				}
			} else {
				_ = rg.SetRampGlow(nil, 0, 0, 0)
			}
		}
	}
	// Neural candidate proposer: install the weights once, and let the greedy loop refresh the
	// proposal map every ProposerEvery shapes. Installing costs nothing when the option is empty, and
	// a backend or DLL without the export simply keeps the random search.
	var devProp deviceProposer
	if (opt.ProposerPath != "" || len(opt.ProposerBlob) > 0) && opt.OnDeviceSearch {
		if dp, ok := be.(deviceProposer); ok {
			// An embedded blob wins over a path: the studio ships the model inside the binary so the
			// release stays two files, while the CLI keeps -proposer for lab work on other exports.
			blob, err := opt.ProposerBlob, error(nil)
			if len(blob) == 0 {
				blob, err = os.ReadFile(opt.ProposerPath)
			}
			switch {
			case err != nil:
				applog.Printf("proposer: %v — random search kept", err)
			case !dp.SetProposer(blob):
				// Two different failures used to share this line, and the message sent me looking for
				// a missing export when the real cause was a PyTorch checkpoint handed over instead of
				// the exported blob.
				applog.Printf("proposer: %s rejected — either the DLL predates fp_set_proposer or the "+
					"file is not an export_weights.py blob (expect the FH6P magic) — random search kept",
					opt.ProposerPath)
			default:
				devProp = dp
				frac := float32(opt.ProposerFrac)
				if frac <= 0 {
					frac = 0.5 // half the batch; the random half keeps the exploration the net lacks
				}
				dp.SetProposerEnabled(true, 0, frac, float32(jitterOr(opt.ProposerJitter)))
				if g, ok := dp.(proposerGater); ok {
					g.SetProposerGate(opt.ProposerConfGate, float32(opt.ProposerConfTau))
				}
			}
		}
	}

	// Size-conditioned glow swap: independent of the hardness gate, so it is wired outside the block
	// above and ALWAYS pushed (0/0 clears a pooled backend's previous run).
	if bg, ok := be.(deviceBigGlower); ok {
		tau, prob := float32(0), float32(0)
		if bigTau > 0 && bigProb > 0 {
			tau, prob = float32(bigTau), float32(bigProb)
		}
		if !bg.SetBigGlow(tau, prob, opt.BigGlowAllKinds, int32(bigGlowKind(opt))) && prob > 0 {
			applog.Printf("big-glow: device lacks fp_set_big_glow — size-conditioned swap disabled on-device (rebuild the DLL)")
		}
	}
	// Analytic-alpha grid: eval re-solves the optimal color per grid alpha and keeps the ΔSSE-min
	// (alpha, color) pair — each candidate's alpha becomes exact instead of sampled. Only sensible
	// where alpha is free (organic modes); ALWAYS cleared when off so a pooled backend never
	// carries the previous run's grid.
	if ag, ok := be.(interface{ SetAlphaGrid([]float32) error }); ok {
		if opt.AnalyticAlpha && allowAlpha {
			if err := ag.SetAlphaGrid(alphaGridValues(alphaMin)); err != nil {
				applog.Printf("analytic-alpha: %v — disabled (rebuild the DLL)", err)
			}
		} else {
			_ = ag.SetAlphaGrid(nil)
		}
	}
	kindCDF := buildKindCDF(kinds, kindWeights)
	tm.Setup = time.Since(setupStart)

	glyphs := false
	if opt.GlyphDict || opt.GlyphPrepass || opt.ShadePrepass || opt.SmoothBase {
		if dme, ok := be.(deviceMaskEvaluator); ok && dme.MasksOnDevice() {
			glyphs = true
		}
	}

	return &run{
		be: be, opt: opt, rng: rng, w: w, h: h, tm: tm,
		kinds: kinds, kindWeights: kindWeights, kindCDF: kindCDF,
		allowAlpha: allowAlpha, alphaMin: alphaMin, maxNI: maxNI, genTarget: genTarget,
		moveStep: moveStep, radiusStep: radiusStep, rounds: rounds, perRound: perRound,
		detailStart: detailStart,
		orient:      orient, coh: coh, aspectCap: aspectCap, detailGrid: detailGrid, boundCtx: boundCtx, kindGate: kg,
		devProp: devProp, propEvery: opt.ProposerEvery,
		devSearch: devSearch, devMoment: devMoment, src: newShapeSource(opt),
		initCanvas: initCanvas, shapes: shapes, grid: grid, gw: gw, gh: gh,
		sampler: sampler, persist: persist, glyphs: glyphs, initialErr: initialErr,
		fobaNext: opt.FoBaEvery,
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
	// How often the proposal map is refreshed. The canvas changes by one shape per step, so the map
	// goes stale slowly; sweeping every shape would spend more than the scoring it saves.
	propEvery := r.propEvery
	if propEvery <= 0 {
		propEvery = 25
	}
	for len(r.shapes)-1 < r.genTarget {
		if r.opt.Cancel != nil && r.opt.Cancel() {
			break
		}
		if r.devProp != nil {
			if placed := len(r.shapes) - 1; placed%propEvery == 0 {
				prog := float32(placed) / float32(maxInt(r.genTarget, 1))
				r.devProp.SetProposerEnabled(true, prog, float32(r.opt.ProposerFrac), float32(jitterOr(r.opt.ProposerJitter)))
				r.devProp.RunProposer(prog)
			}
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
		// Persistent-error upweight (see persist.go): folds into sampGrid so the device search
		// and the host sampler stay identical; the raw grid (gate/knee/progress) is untouched.
		if r.persist != nil {
			sampGrid = r.persist.apply(sampGrid)
			r.sampler = NewErrorSampler(sampGrid, r.gw, r.gh, r.w, r.h)
		}
		// Saliency quota: the reserved tail of the budget samples ONLY inside the salient cells —
		// a hard mask, not a bias, so the per-shape argmax must spend these shapes on the most
		// visible detail. The error grid still ranks WITHIN the region and the accept gate stays raw.
		if q := r.opt.SaliencyQuota; q > 0 && r.detailGrid != nil && progress > 1-float32(q) {
			sampGrid = r.applySalient(sampGrid)
			r.sampler = NewErrorSampler(sampGrid, r.gw, r.gh, r.w, r.h)
		}
		best, bestScore := r.searchOne(progress, sampGrid, penalty)
		// bestScore is the backend's PROGRESSIVELY-SAMPLED ΔSSE (SampleBudget pixels), not the exact
		// full-res delta, so "every accepted shape strictly lowers the hard error" is statistical, not
		// exact, when SampleBudget < shape area. A rare net-neutral shape is absorbed by later shapes +
		// the postProcess recolor + the honestly-measured FinalError.
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
		if r.persist != nil {
			r.persist.update(r.grid)
		}
		r.tm.Sampler += time.Since(t0)
		curErr := sumGrid(r.grid)
		if r.opt.Progress != nil {
			r.opt.Progress(len(r.shapes)-1, curErr)
		}
		if knee.push(curErr) {
			break // sustained diminishing returns — auto-stop at the knee
		}
		// Backward step (foba.go): drop what is already dead so the greedy re-spends the slot now
		// rather than leaving it occupied to the end of the run.
		r.fobaMaybe(len(r.shapes) - 1)
	}
	if r.opt.FoBaEvery > 0 {
		applog.Printf("foba: %d shapes dropped mid-greedy in total", r.fobaDropped)
	}
}

// searchOne finds the best candidate for the next shape. It delegates the candidate generation and
// scoring to the configured shapeSource (random / moment / hybrid), then hill-climb-mutates the
// winner. The returned score is RAW (the compact bias is selection-only); a score < 0 means the
// candidate improves the canvas. Mutate timings accumulate into r.tm (the source records its own).
func (r *run) searchOne(progress float32, sampGrid []float32, penalty func(model.Candidate) float32) (model.Candidate, float32) {
	w, h := r.w, r.h
	best, bestScore := r.src.search(r, progress, sampGrid, penalty)
	// Residual component seeds join the same competition (compseed.go): the search's answer stands
	// unless a seed scores strictly better on the same evaluator.
	if r.opt.CompSeeds > 0 {
		t0 := time.Now()
		pool := compSeeds(sampGrid, r.gw, r.gh, w, h, annealMaxR(w, h, progress), r.opt.CompSeeds,
			r.kinds, r.kindCDF, r.alphaMin)
		if len(pool) > 0 {
			clampCandidatesToCanvas(pool, float32(w), float32(h), r.opt.CanvasPad)
			r.compSeedTried += len(pool)
			if c, sc := pickBest(r.be, pool, penalty); sc < bestScore {
				best, bestScore = c, sc
				r.compSeedWon++
			}
		}
		r.tm.Evaluate += time.Since(t0)
	}
	if r.glyphs && r.opt.GlyphDict {
		t0 := time.Now()
		if gb, gs, ok := r.glyphPropose(progress, sampGrid, penalty); ok && gs < bestScore {
			best, bestScore = gb, gs
		}
		r.tm.Evaluate += time.Since(t0)
	}
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
	// MONO mode: snap every shape to the exact lock colour LAST, after polish/back-fit/standout have
	// finished moving colours — guaranteeing one pure brand colour in the output.
	r.lockColors()
}

// setStatus reports the current post-greedy phase to the optional Options.Status callback (a UI
// hook), so a progress bar stuck at 100% can show what the run is doing. nil callback = ignored.
func (r *run) setStatus(s string) {
	if r.opt.Status != nil {
		r.opt.Status(s)
	}
}
