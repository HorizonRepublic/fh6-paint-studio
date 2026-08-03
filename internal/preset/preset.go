// Package preset turns high-level UI/CLI choices into a fully-resolved engine.Options plus the
// saliency weight map and the backend grid size. It is the SINGLE SOURCE OF TRUTH for the tuned
// generation defaults: the per-mode constants (ModeDefaultsFor), the quality preset counts
// (PresetCounts), and the kind/weight parsers (ParseKinds/ParseKindWeights) are all shared by the
// GUI (which calls Resolve) and the CLI (cmd/fh6paint, which sources the same constants), so the
// two can never drift. The CLI keeps its own flag-override plumbing on top of these.
package preset

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/metric"
	"fh6-paint-studio/internal/model"
)

// MaxShapes is the FH6 per-group layer ceiling; Resolve clamps Shapes to it.
const MaxShapes = 3000

// MaxInkRatio caps the hybrid Lines<->Fill knob: at most half the budget goes to FDoG ink, so the
// geometrize fill (the colour/detail that renders alive eyes) always keeps the majority of the shapes.
const MaxInkRatio = 0.5

// Choices is the high-level configuration the UI exposes. Curated fields drive the
// common path; the advanced fields default to the quality preset / content mode unless
// set. Tri-state toggles use *bool (nil = mode default). Float "auto" sentinels: Aspect
// and WeightStrength use -1; PolishTau0/Tau1 use 0; Overdraw uses 0/1 for off.

type Choices struct {
	Shapes    int     // budget; clamped to [1, MaxShapes]
	Mode      string  // anime|photo|flat (3 manual presets; legacy names collapse via presetMode; "" -> anime)
	MonoColor string  // MONO single-colour logo/decal: "" = off; "auto" = the logo's dominant ink colour; "#RRGGBB" = exact. Forces a flat single-colour cutout (every shape one solid colour, no grey edges). Orthogonal to Mode — implies flat.
	InkRatio  float64 // hybrid family only: fraction of the budget spent on FDoG ink lines (0..MaxInkRatio); fill gets the rest. 0 = no lines / non-hybrid.
	Quality   string  // fast|balanced|max|quality|ultra ("" -> balanced)
	Alpha     *bool   // nil = mode default; set = override (forced off for cutouts)
	Seed      int64
	Polish    *bool // nil = on
	Backfit   *bool // nil = mode default (auto for flat/logo/line/cutout)
	Boundary  *bool // nil = off (opt-in). boundary-aware radius — best on smooth photo/anime characters (smoother gradients, less veil overshoot); regresses on text/flat, so not auto-defaulted
	Economy   bool  // OPT-IN (default off): the low-budget global-search schedule (LIVE co-adaptation / anneal at ≤~1500 shapes). Lifts quality but re-polishes ALL shapes per batch (~4x slower), so it's off by default and enabled only when the user asks for it.
	SS        int   // preview supersample factor (UI-side; carried through Resolved.SS)

	// Advanced (zero = preset/mode default, except Aspect/WeightStrength/AlphaMin = -1)
	Random, Mutated, SampleBudget, MaxNoImprove int
	Grid                                        int
	Kinds                                       string // CSV; "" = ellipse,triangle,rectangle
	KindWeights                                 string // CSV parallel to Kinds; "" = mode default
	Aspect                                      float64
	WeightStrength                              float64
	AlphaMin                                    float64 // semi-transparent alpha floor; -1 = mode default
	StandoutTol                                 float64 // post-polish standout suppression; 0 = off
	PolishIters                                 int
	PolishTau0                                  float64
	PolishTau1                                  float64
	Weighted                                    *bool // nil = true
	Compact                                     *bool // nil = true (engine still skips it for cutouts)
	Overdraw                                    float64
	BestOf                                      int     // full-pipeline attempts with decorrelated seeds, keep the best; 0 = mode default (single run)
	RampWeight                                  float64 // -1 = mode default; >=0 overrides the smooth-gradient weight boost
}

// DefaultChoices returns the GUI's starting configuration (matches the CLI flag defaults).
func DefaultChoices() Choices {
	return Choices{
		Shapes: 1000, Mode: "anime", Quality: "balanced", Seed: 1, SS: 1,
		Grid: 48, Aspect: -1, WeightStrength: -1, AlphaMin: -1, Overdraw: 1, RampWeight: -1,
	}
}

// Resolved is everything a run needs: the engine options, the per-pixel weight map and
// grid size for the backend constructor, the concrete (auto-resolved) mode, the preview
// supersample factor, and a human-readable settings summary for the log.
type Resolved struct {
	Options  engine.Options
	Weight   []float32
	Grid     int
	Mode     string
	SS       int
	BestOf   int  // run the pipeline this many times with decorrelated seeds and keep the best (≤1 = single run)
	PixelArt bool // pixel-art EXACT mode: bypass the engine entirely (internal/pixel rect decomposition)
	Summary  []string
	// Target is the working-space pixel buffer the backend should fit — usually the loaded pixels,
	// but the MONO path replaces it with a binarized single-colour copy. The runner uses this so the
	// target is binarized ONCE (here) instead of again at backend-build time.
	Target []float32
}

// Resolve maps Choices + the loaded image to a Resolved run configuration.
func Resolve(prep imageio.Prepared, c Choices) Resolved {
	w, h := prep.W, prep.H

	shapes := clampShapes(c.Shapes)

	// Quality preset -> base sample counts; explicit advanced values override.
	random, mutated, sampleBudget, maxNI := resolveSampleCounts(c)

	cs := metric.ContentClass(prep.Pixels, w, h) // cs.Colors (palette count) feeds the flat vector/textured split
	// Content preset is USER-EXPLICIT (anime|photo|flat). There is no auto content-classifier: classifying
	// by content alone misfires (memes look like anime, text like photo, cutouts like anime), so the three
	// presets are chosen explicitly. presetMode collapses legacy names to anime|photo|flat. Transparency is
	// ORTHOGONAL — auto-detected from the alpha channel (prep.HasTransparency forces opaque shapes + the
	// transparent-bg pipeline), independent of which content preset the user picked.
	resolved := PresetMode(c.Mode)
	if resolved == "gaussian" {
		return resolveGaussian(prep, c, w, h, shapes)
	}
	if resolved == "pixel" {
		// EXACT pixel-art mode: no engine, no backend — the runner calls internal/pixel directly.
		// Options carries only what the shared Done path needs (canvas dims + cutout semantics for
		// the WYSIWYG preview); budget/quality knobs are meaningless (the art defines the count).
		return Resolved{
			Options:  engine.Options{Width: w, Height: h, Background: prep.Background, TransparentBG: true, StopAt: MaxShapes},
			Mode:     "pixel",
			SS:       c.SS,
			PixelArt: true,
			Summary:  []string{"mode=pixel (exact rect decomposition — the art defines the shape count)"},
			Target:   prep.Pixels,
		}
	}
	flatMode := resolved == "flat"
	transparent := prep.HasTransparency && !prep.PaddedOpaque // padded-opaque keeps content tuning; spill penalty still fires

	// MONO single-colour logo/decal (c.MonoColor): binarize the target to a clean single-colour cutout
	// (no grey antialiased-edge shapes) and snap every shape to that exact colour at the end of the run.
	// Implies flat + opaque cutout. Work on a COPY so the loaded source is never mutated (studio re-runs
	// + the source thumbnail share prep.Pixels); the colour is sampled from the ORIGINAL pixels.
	var monoLock *model.RGBA
	if lc, ok := engine.ParseLockColor(c.MonoColor, prep.Pixels, w, h, prep.HasTransparency); ok {
		binar := append([]float32(nil), prep.Pixels...)
		engine.BinarizeForLock(binar, w, h, lc, prep.HasTransparency)
		prep.Pixels = binar // local copy only — md/sp/weight below now fit the clean mono mask
		prep.HasTransparency = true
		monoLock, transparent, resolved, flatMode = &lc, true, "flat", true
	}

	// All benchmark-hardwired per-mode constants come from ModeDefaultsFor (the single source of truth
	// shared with the CLI). The override logic (explicit Choices fields) stays here.
	md := ModeDefaultsFor(resolved, cs.Colors, transparent)

	sp := resolveShapeParams(md, c, flatMode, transparent)

	weight := buildWeightMap(prep, w, h, c, flatMode || transparent, sp.wstr, sp.rampWeight)

	compact := true
	if c.Compact != nil {
		compact = *c.Compact
	}

	grid := c.Grid
	if grid <= 0 {
		grid = 48
	}

	overdraw := c.Overdraw
	if overdraw <= 0 {
		overdraw = 1
	}

	ss := c.SS
	if ss < 1 {
		ss = 1
	}

	qual := strings.ToLower(strings.TrimSpace(c.Quality))
	if qual == "" {
		qual = "balanced"
	}

	// Economy schedule (OPT-IN, c.Economy): at low-mid budgets a co-adapted LIVE base (or anneal at the
	// tightest budgets) lifts quality — fewer shapes on the structural base so the greedy detail builds
	// on a better foundation. It re-polishes ALL shapes per batch (~4x slower), so it is OFF by default
	// and only applied when the user enables it. Still off for flat (the knee handles it) and >economyLiveMax.
	var ecoLB, ecoBase, ecoAnneal int
	if c.Economy {
		ecoLB, ecoBase, ecoAnneal = EconomyParams(resolved, shapes)
	}

	opt := engine.Options{
		Width: w, Height: h, Background: prep.Background,
		StopAt: shapes, RandomSamples: random, MutatedSamples: mutated, Seed: c.Seed,
		Kinds:         ParseKinds(sp.kindsCSV),
		KindWeights:   sp.kindWeights,
		TransparentBG: prep.HasTransparency,
		Overdraw:      float32(overdraw),
		AllowAlpha:    sp.allowAlpha,
		AlphaMin:      sp.alphaMin,
		AspectMax:     sp.aspectMax,
		// recolor-var 0.03 helps every content type: it keeps the crisp greedy/polish colour on
		// boundary-straddling fur/contour slivers instead of replacing it with a muddy weighted mean.
		RecolorVarSkip: 0.03,
		MaxNoImprove:   maxNI,
		// Auto-shape-count knee — per-content (md): flat/line-art trims the white-background ghost-facet
		// over-fill (3000→~600 on img_2, EYE-equal); 0/off for anime/photo to protect detail/eyes.
		ShapeKneeTol:   md.KneeTol,
		ShapeKneeFloor: md.KneeFloor,
		SampleBudget:   sampleBudget,
		CompactPenalty: compact && !transparent,
		OnDeviceSearch: true, // CUDA build uses it; CPU ignores
		// Coarse-to-fine search (CUDA-only; CPU ignores): the dominant eval-speed lever at high
		// candidate volume — score the batch at a cheap 3000-px filter, then re-score the K
		// survivors at the full budget and pick from those. The winner is full-budget scored, so
		// it stays quality-neutral while the bulk pays only the coarse cost (roughly halves eval
		// wall-time). The DLL gate (n>4*K) auto-disables it below ~33k candidates, so only the
		// quality/ultra presets activate it; fast/balanced/max fall through to the single pass.
		// budget 3000 is the filter floor: it selects the same survivors as a larger filter, while
		// going below 3000 starts missing the true winner.
		CoarseSearch: true,
		CoarseBudget: 3000,
		CoarseK:      8192,
		// FP16/half2 coarse FILTER (the FP32 re-eval still picks the winner): the eval is ALU-bound,
		// so halving the per-pixel FMA work is the speed lever. Quality stays within coarse-to-fine's
		// own ranking band; -coarse-fp16=false (CLI) restores FP32 filtering.
		CoarseFP16: true,
		// Moment-seeding (opt-in, experimental, default off): replace the blind random candidate
		// batch with closed-form covariance-ellipse seeds + a localised refine pool. ~-33%
		// generation time at quality-neutral (the engine applies the tuned knee: refine 2048, 16
		// centres). Bypasses on-device random; quality is held by the seed being the ML ellipse the
		// random search targets anyway. Validate by eye.
		// NEXTGEN is the baked default generator (replaced the studio's "Fast generation" toggle):
		// moment-seeding lays the smooth coarse base, then hands off to the random search at progress
		// 0.55 for the sharp detail half (faster + sharper, breaks standout patches). Slowgen (pure
		// random) stays reachable via the CLI -moment-seed=false for A/B.
		MomentSeed:        true,
		MomentDetailStart: 0.55,
		Polish:            sp.polish,
		PolishOpts:        polishOpts(sp.iters, c.PolishTau0, sp.tau1, sp.ste, sp.falseEdge, sp.ssim, sp.eagle, sp.lostDetail),
		ShadePrepass:      sp.shadePre,
		SmoothBase:        sp.smoothBase,
		RegionKinds:       sp.regionKinds,
		SmoothGlowTau:     sp.glowTau,
		SmoothGlowProb:    sp.glowProb,
		BigGlowTau:        sp.bigGlowTau,
		BigGlowProb:       sp.bigGlowProb,
		BigGlowAllKinds:   sp.bigGlowAll,
		RampGlow:          sp.rampGlow,
		TermRegionWeight:  sp.termRegionW,
		LooRefit:          sp.looRefit,
		MergeRefit:        sp.mergeRefit,
		AnalyticAlpha:     sp.analyticAlpha,
		SaliencyQuota:     md.SaliencyQuota,
		BackFit:           sp.backfit,
		BackFitPasses:     2,
		BackFitFrac:       0.1,
		LiveBatch:         ecoLB,
		LiveBase:          ecoBase,
		AnnealIters:       ecoAnneal,
		LockColor:         monoLock,
		StandoutTol:       c.StandoutTol,
		// Boundary-aware radius: opt-in (caller toggle). A real win on smooth photo/anime character
		// content but a regression on text/flat, with no clean way to gate automatically — so it is
		// never auto-enabled, only honoured when the caller explicitly sets it.
		BoundaryRadius: sp.boundaryOn,
		BoundaryStart:  0.42,
		// Canvas-edge clamp: stop ellipses/rects ballooning OUTSIDE the image rectangle (drawn in full
		// in-game where there's no clip, but invisible in the W×H preview). 0.04 leaves a small edge
		// bleed and is quality-neutral while cutting the worst overflow several-fold. Applies to all
		// content; helps opaque/busy images most, ~inert on tight cutouts.
		CanvasPad: 0.04,
	}

	summary := []string{
		fmt.Sprintf("mode=%s  shapes=%d  quality=%s  linear-light=%v", resolved, shapes, qual, model.LinearLight),
		fmt.Sprintf("alpha=%v (min %.2f)  kinds=%s  weights=%v  aspect=%.1f", sp.allowAlpha, sp.alphaMin, sp.kindsCSV, sp.kindWeights, sp.aspectMax),
		fmt.Sprintf("polish=%v (iters=%d tau1=%.3f ste=%v)  backfit=%v  boundary=%v  weight-strength=%.2f", sp.polish, sp.iters, sp.tau1, sp.ste, sp.backfit, sp.boundaryOn, sp.wstr),
		fmt.Sprintf("random=%d  mutated=%d  sample-budget=%d  grid=%d", random, mutated, sampleBudget, grid),
	}

	return Resolved{Options: opt, Weight: weight, Grid: grid, Mode: resolved, SS: ss, BestOf: sp.bestOf, Summary: summary, Target: prep.Pixels}
}

// clampShapes constrains the budget to [1, MaxShapes] — the FH6 per-group layer ceiling.
func clampShapes(shapes int) int {
	if shapes < 1 {
		return 1
	}
	if shapes > MaxShapes {
		return MaxShapes
	}
	return shapes
}

// resolveSampleCounts picks the search-volume base from the quality preset, then lets any positive
// advanced Choices field override its slot (the CLI's explicit knobs always win over the preset).
func resolveSampleCounts(c Choices) (random, mutated, sampleBudget, maxNI int) {
	random, mutated, sampleBudget, maxNI = PresetCounts(c.Quality)
	if c.Random > 0 {
		random = c.Random
	}
	if c.Mutated > 0 {
		mutated = c.Mutated
	}
	if c.SampleBudget > 0 {
		sampleBudget = c.SampleBudget
	}
	if c.MaxNoImprove > 0 {
		maxNI = c.MaxNoImprove
	}
	return random, mutated, sampleBudget, maxNI
}

// shapeParams are the per-mode generation knobs resolved from the tuned ModeDefaults plus the
// explicit Choices overrides. Grouped into one value so resolveShapeParams stays a single cohesive
// resolver instead of a function returning a dozen results (an anti-pattern in its own right).
type shapeParams struct {
	allowAlpha    bool
	alphaMin      float32
	kindsCSV      string
	kindWeights   []float32
	wstr          float64 // edge-weight blend toward uniform; also drives the saliency map
	aspectMax     float32
	polish        bool
	boundaryOn    bool
	backfit       bool
	iters         int
	tau1          float64
	falseEdge     float64
	ssim          float64
	eagle         float64
	lostDetail    float64
	shadePre      bool
	smoothBase    bool
	regionKinds   bool
	glowTau       float64
	glowProb      float64
	bigGlowTau    float64
	bigGlowProb   float64
	bigGlowAll    bool
	rampGlow      bool
	termRegionW   bool
	looRefit      int
	mergeRefit    bool
	rampWeight    float64
	bestOf        int
	analyticAlpha bool
	ste           bool
}

// resolveShapeParams layers the explicit Choices overrides on top of the mode defaults. The
// per-mode rationale for each base value lives in ModeDefaults / ModeDefaultsFor; the notes here
// cover the override semantics (which sentinel means "unset").
func resolveShapeParams(md ModeDefaults, c Choices, flatMode, transparent bool) shapeParams {
	sp := shapeParams{
		alphaMin:      md.AlphaMin,
		kindWeights:   md.KindWeights,
		wstr:          md.WeightStr,
		aspectMax:     md.AspectMax,
		boundaryOn:    md.Boundary,
		backfit:       md.Backfit,
		iters:         md.PolishIters,
		tau1:          md.PolishTau1,
		falseEdge:     md.FalseEdge,
		ssim:          md.SSIM,
		eagle:         md.Eagle,
		lostDetail:    md.LostDetail,
		shadePre:      md.ShadePre,
		smoothBase:    md.SmoothBase,
		regionKinds:   md.RegionKinds,
		glowTau:       md.SmoothGlowTau,
		glowProb:      md.SmoothGlowProb,
		bigGlowTau:    md.BigGlowTau,
		bigGlowProb:   md.BigGlowProb,
		bigGlowAll:    md.BigGlowAllKinds,
		rampGlow:      md.RampGlow,
		termRegionW:   md.TermRegionWeight,
		looRefit:      md.LooRefit,
		mergeRefit:    md.MergeRefit,
		rampWeight:    md.RampWeight,
		bestOf:        md.BestOf,
		analyticAlpha: md.AnalyticAlpha,
		ste:           true,
	}
	if c.BestOf > 0 {
		sp.bestOf = c.BestOf
	}

	// Alpha: organic (anime/photo) = semi-transparent for smooth gradients; flat = OPAQUE (crisp
	// edges); transparent cutouts stay opaque. alphaMin is the tuned organic floor (flat ignores it).
	sp.allowAlpha = !flatMode
	if transparent {
		sp.allowAlpha = false // cutout object stays opaque
	}
	if c.Alpha != nil {
		sp.allowAlpha = *c.Alpha && !transparent
	}
	// Alpha floor: organic candidates draw alpha ~U(alphaMin,1). -1 keeps the tuned mode floor.
	if c.AlphaMin >= 0 {
		sp.alphaMin = float32(c.AlphaMin)
	}

	// Kind mix: triangle-rich for organic content (triangles co-adapt under polish and improve it),
	// rect-rich for flat/logo (triangles fragment straight edges). Triangles render correctly in-game
	// word-only — the editor selects the mesh from the shape-word, so the per-layer geometry resource
	// (0xA8) is never written. Explicit Kinds / KindWeights always win.
	sp.kindsCSV = c.Kinds
	if strings.TrimSpace(sp.kindsCSV) == "" {
		sp.kindsCSV = "ellipse,triangle,rectangle"
	}
	if kw := ParseKindWeights(c.KindWeights); kw != nil {
		sp.kindWeights = kw
	}

	// Weight-strength: the ONLY anime≠photo knob (anime 0.15, photo 0.40, flat 0). c.WeightStrength
	// uses -1 as the "auto" sentinel, so any value >= 0 overrides.
	if c.WeightStrength >= 0 {
		sp.wstr = c.WeightStrength
	}

	// Ramp-weight boost: -1 = mode default; any >= 0 overrides (0 disables).
	if c.RampWeight >= 0 {
		sp.rampWeight = c.RampWeight
	}

	// Aspect slivers: flat 8, organic 6; explicit (>=0, incl 0 for round) overrides.
	if c.Aspect >= 0 {
		sp.aspectMax = float32(c.Aspect)
	}

	sp.polish = true
	if c.Polish != nil {
		sp.polish = *c.Polish
	}
	// Boundary-aware radius (OFF organic, ON flat — see ModeDefaultsFor). c.Boundary overrides.
	if c.Boundary != nil {
		sp.boundaryOn = *c.Boundary
	}
	// Back-fitting auto-on for flat + transparent cutouts, gated end-to-end. c.Backfit overrides.
	if c.Backfit != nil {
		sp.backfit = *c.Backfit
	}
	// Polish iters — the PERCEPTUAL knee (organic 200, vector-flat 600, textured-flat 300). Explicit
	// c.PolishIters overrides; early-stop trims the tail.
	if c.PolishIters != 0 {
		sp.iters = c.PolishIters
	}
	// Final tau (flat/cutout 0.06, organic 0.08). Explicit overrides.
	if c.PolishTau1 != 0 {
		sp.tau1 = c.PolishTau1
	}

	// Straight-through (HARD-coverage forward) polish, used for ALL content. SOFT polish optimises a
	// tau-blurred composite, which MUSHES crisp edges and (because the flat gate uses uniform weight,
	// wstr=0) is not rejected by the gate. STE instead optimises the EXACT hard render the editor
	// ships, so it refines without blurring, and it is gated so it never regresses. It improves every
	// content type; the soft default is left only as a historical option.

	return sp
}

// The shadow clamp/cap pair of the sRGB-derivative perceptual weight is CONTENT-ADAPTIVE
// (see the linear-light block in buildWeightMap). The original 0.02/16 pair UNDER-weights the
// darkest zones relative to the true sRGB metric (a near-black dress sits below the clamp and
// loses ~6× of its honest weight) — on DARK-DOMINANT art that is exactly where the translucent
// facet ghosts survive, and 0.005/64 visibly deepens/cleans them (img_10: dress ghosts gone by
// eye, face intact). But globally the stronger pair steals weight from bright content: light art
// (img_5) pays ΔE 2.09→2.51 with SSIM/banding worse. The bank splits cleanly by the dark-pixel
// fraction (linear luma < 0.02): dark arts sit at 0.43-0.63, everything else ≤ 0.24 — the 0.35
// threshold has a wide margin on both sides (the borderline img_24 stays on the mild pair, where
// the strong one measured mixed).
const (
	darkFracTau = 0.35 // fraction of linear-luma<0.02 pixels at which the art counts as dark-dominant
)

// DarkWeightParams returns the shadow clamp/cap pair of the perceptual weight for a target with
// the given dark-pixel fraction — the SINGLE source for the studio (buildWeightMap) and the CLI's
// mirrored -linear path. FH6_DARKW ("clamp,cap") overrides for lab A/Bs.
func DarkWeightParams(darkFrac float64) (clamp, cap float64) {
	clamp, cap = 0.02, 16
	if darkFrac >= darkFracTau {
		clamp, cap = 0.005, 64
	}
	if s := os.Getenv("FH6_DARKW"); s != "" {
		fmt.Sscanf(s, "%f,%f", &clamp, &cap)
	}
	return clamp, cap
}

// DarkFrac measures the fraction of pixels whose LINEAR luma sits below 0.02 — the dark-dominance
// feature DarkWeightParams keys on. pixels is the linear RGBA plane (len w*h*4).
func DarkFrac(pixels []float32) float64 {
	n := len(pixels) / 4
	if n == 0 {
		return 0
	}
	dark := 0
	for i := 0; i < n; i++ {
		y := 0.2126*pixels[i*4] + 0.7152*pixels[i*4+1] + 0.0722*pixels[i*4+2]
		if y < 0.02 {
			dark++
		}
	}
	return float64(dark) / float64(n)
}

// buildWeightMap produces the per-pixel saliency weight. It optionally builds an edge-saliency map
// (richer dilated V2 for flat/cutout, Sobel for smooth content), blends it toward uniform by
// weight-strength, then — under linear-light output — multiplies in the perceptual sRGB-derivative
// weight. Returns nil only when saliency weighting is off AND linear-light is off (the engine then
// treats the run as uniform).
func buildWeightMap(prep imageio.Prepared, w, h int, c Choices, useV2 bool, wstr, rampWeight float64) []float32 {
	weighted := true
	if c.Weighted != nil {
		weighted = *c.Weighted
	}
	var weight []float32
	if weighted {
		if useV2 {
			weight = metric.WeightMapV2(prep.Pixels, w, h)
		} else {
			weight = metric.WeightMap(prep.Pixels, w, h)
		}
	}
	if weight != nil && wstr < 1 {
		s := float32(math.Max(0, math.Min(1, float64(wstr))))
		for i := range weight {
			weight[i] = (1 - s) + s*weight[i]
		}
	}

	// Linear-light PERCEPTUAL weight (mirrors cmd/fh6paint's -linear path). When compositing in
	// linear (FH6's space; model.LinearLight is set on for FH6 output), minimise a PERCEPTUAL
	// error by weighting each pixel's linear-SSE by (d sRGB/d linear)². The sRGB EOTF is steep
	// in darks / flat in brights, so this up-weights shadow detail and down-weights bright
	// regions as perception does — the analytic weighted-mean then solves for the sRGB-displayed
	// result while the blend stays linear. prep.Pixels are LINEAR here (imageio decoded them).
	if model.LinearLight {
		if weight == nil {
			weight = make([]float32, w*h)
			for i := range weight {
				weight[i] = 1
			}
		}
		// Dark-zone fidelity of the sRGB-derivative weight: the clamp/cap pair bounds how much the
		// deep shadows may dominate, keyed on dark-dominance (single source: DarkWeightParams).
		clampY, capF := DarkWeightParams(DarkFrac(prep.Pixels))
		wp := make([]float32, w*h)
		var sum float64
		for i := 0; i < w*h; i++ {
			y := 0.2126*prep.Pixels[i*4] + 0.7152*prep.Pixels[i*4+1] + 0.0722*prep.Pixels[i*4+2]
			yd := float64(y)
			if yd < clampY {
				yd = clampY // clamp the dark blow-up of the sRGB derivative
			}
			d := 0.4396 * math.Pow(yd, -0.5833) // d/dlin of 1.055*lin^(1/2.4)-0.055
			f := float32(d * d)
			if f > float32(capF) {
				f = float32(capF)
			}
			wp[i] = f
			sum += float64(f)
		}
		mean := float32(sum / float64(len(wp)))
		if mean > 0 {
			for i := range weight {
				weight[i] *= wp[i] / mean // normalised so the overall weight scale is ~unchanged
			}
		}
	}

	// Ramp-weight boost: the edge-biased weight starves SMOOTH GRADIENT
	// zones of shapes, so their few big facets stand out (the owner's complaint). Detect the ramp
	// cells (metric.RampMap) and multiply their weight by (1 + rampWeight·ramp) so the sampler spends
	// more of the budget there — "understand where the gradient is and push shapes into it". Content-
	// adaptive by construction: cel/flat art has ~no ramp cells so this is inert there. rampWeight ≤ 0
	// = off. Applied AFTER normalisation so it is a deliberate, un-normalised local up-weight.
	if rampWeight > 0 {
		if weight == nil {
			weight = make([]float32, w*h)
			for i := range weight {
				weight[i] = 1
			}
		}
		rm := metric.RampMap(prep.Pixels, w, h)
		b := float32(rampWeight)
		for i := range weight {
			weight[i] *= 1 + b*rm[i]
		}
	}
	return weight
}

// BoundaryDefault is the content-adaptive default for boundary-aware radius. Exported so the CLI
// (cmd/fh6paint) applies the IDENTICAL default as the studio (single source of truth).
func BoundaryDefault(resolvedMode string) bool {
	// OFF for organic (anime/photo), ON for flat: the flat presets gain from the tighter silhouettes,
	// while organic does better with it off.
	return PresetMode(resolvedMode) == "flat"
}

// ModeDefaults holds the tuned per-mode generation defaults — the single source of truth for these
// constants, shared by Resolve and the CLI so the studio and cmd/fh6paint can never drift. Each field
// is the value to use when the caller has NOT overridden it.
type ModeDefaults struct {
	AlphaMin    float32   // candidate alpha floor (organic; flat ignores it, opaque)
	KindWeights []float32 // per-kind pick weights (parallel to ellipse,triangle,rectangle)
	WeightStr   float64   // edge-weight blend toward uniform
	AspectMax   float32   // max sliver aspect
	PolishIters int       // joint-polish iterations (perceptual knee)
	PolishTau1  float64   // final polish edge softness
	FalseEdge   float64   // false-edge additive polish loss λ (0 = off)
	SSIM        float64   // SSIM additive polish loss λ (0 = off)
	Eagle       float64   // EAGLE additive polish loss λ (0 = off)
	// LostDetail is the MIRROR of the false-edge λ: it charges structure the recon ERASED. A
	// rimless glow laid over detail draws no false edge and is cheap in SSE, so before this term
	// nothing in the objective could see blur-over-structure. 0 = off.
	LostDetail  float64
	Boundary    bool    // boundary-aware radius
	Backfit     bool    // post-polish back-fitting
	KneeTol     float64 // auto-shape-count knee tolerance (0 = off / fill budget)
	KneeFloor   float64 // knee absolute floor (frac of initialErr) so it trips on near-SOLVED flat content
	ShadePre    bool    // shading pre-pass: claim coherent linear-ramp regions as base+gradient-word stacks
	SmoothBase  bool    // smooth-region gradient base: claim LARGE smooth regions with jointly-solved base+gradient stacks
	RegionKinds bool    // region-gated kind selection: rect/tri only where the target has hard structure
	// SmoothGlowTau/-Prob gate the deep-smooth glow swap riding RegionKinds: hard<Tau cells swap
	// their forced ellipse for a rimless glow with probability Prob. 0 = the engine default pair.
	SmoothGlowTau  float64
	SmoothGlowProb float64
	// BigGlowTau/-Prob gate the SIZE-conditioned glow swap, which does NOT ride RegionKinds: an
	// ellipse candidate larger than Tau*min(w,h) becomes a rimless glow with probability Prob.
	// 0 = off.
	BigGlowTau      float64
	BigGlowProb     float64
	BigGlowAllKinds bool // let the swap eat big rects/triangles too, not just ellipses
	RampGlow        bool // ramp-aware hotter glow swap: dissolve gradient patchwork with rimless glows where metric.RampMap flags a genuine gradient (needs RegionKinds)

	SaliencyQuota    float64 // reserve this budget fraction for top-detail cells (eyes/faces); 0 = off
	TermRegionWeight bool    // region-weighted FE/EAGLE polish terms: λ × (1−HardEdgeMap) — strong in smooth zones, ~zero on line-work
	LooRefit         int     // exact leave-one-out prune→regrow→re-polish rounds after the polish (0 = off)
	MergeRefit       bool    // merge near-duplicate pairs inside the LOO rounds (extra slot source; needs LooRefit>0)
	RampWeight       float64 // boost the shape-budget weight in smooth-gradient (ramp) cells by (1+this·rampMap); 0 = off
	AnalyticAlpha    bool    // analytic per-candidate alpha: eval picks the ΔSSE-min alpha from a grid over [alphaMin, 0.75] instead of a random draw
	BestOf           int     // full-pipeline attempts with decorrelated seeds, keep the best (≤1 = single; never defaulted >1 — owner 2026-07-20: manual only, studio Advanced / CLI -best-of)
}

// ModeDefaultsFor returns the tuned defaults for a resolved preset (anime|photo|flat). palette is the
// metric.ContentClass colour count (the flat vector<80 / textured≥80 split); transparent forces the
// cutout back-fit. The rationale for each constant lives in Resolve's comments above.
func ModeDefaultsFor(resolvedMode string, palette int, transparent bool) ModeDefaults {
	flat := PresetMode(resolvedMode) == "flat"
	d := ModeDefaults{
		// Alpha floor 0.30 for ALL organic content (was 0.40). Photo: monotone-better downward on
		// both photo bench imgs (smoother tonal ramps, all metrics agree). Anime: 0.30 vs 0.40
		// replicated across 5 bank imgs × 3 seeds = 6 wins / 4 ties / 0 losses (img_5 wins on every
		// seed, typically −5%); the seed-to-seed variance dwarfs finer tuning, and the eye shows
		// smoother iris/skin ramps with no crispness loss. A cel/painterly auto-split was probed
		// and REFUTED — no Analyze feature separates the preferences; it is one default, just lower.
		AlphaMin:    0.30,
		KindWeights: []float32{0.5, 0.4, 0.1}, // organic + textured flat: triangle-rich
		AspectMax:   6,
		PolishIters: 200,
		PolishTau1:  0.08,
		Boundary:    BoundaryDefault(resolvedMode),
		Backfit:     flat || transparent,
	}
	switch PresetMode(resolvedMode) {
	case "photo":
		d.WeightStr = 0.40
		// Shading pre-pass, photo only (owner decision 2026-07-19): claim coherent linear-ramp
		// regions (sky / metallic paint / soft studio backgrounds) as base-rect + gradient-word
		// stacks before the greedy. SAFE BY CONSTRUCTION on content without such ramps: the claim
		// gate requires the ramp to beat the flat cover by ≥30% exact-scored ΔSSE, else full
		// rollback — the whole anime bench passes through bit-identical (0 claims). Anime cel is
		// two-tone/flat by nature (OFF); flat/logo has no shading (OFF). See engine/shadepre.go.
		d.ShadePre = true
		// Smooth-region gradient base, photo too (owner decision 2026-07-20): large smooth zones
		// (sky / bokeh / studio backgrounds) are exactly its content, and it is gate-safe by
		// construction — every stack must contain an EARNING gradient layer over the hard base
		// (region-restricted exact ΔSSE) or it rolls back entirely. The weighted-term flag stays
		// off here: photo runs all perceptual λ terms at 0 (measured ΔE cost), so it would be inert.
		d.SmoothBase = true
		// Region-gated kinds for photo too (2026-07-20): the smooth-glow swap rides the kind gate,
		// and photo's soft backgrounds (bokeh/sky) are exactly where the translucent-facet
		// patchwork lives. Measured on img_10 photo @native: ΔE 3.25→3.16, p95 −3%, SSIM +0.008
		// with the swap; the gate itself is inert on structured zones (full pool at hard cells).
		d.RegionKinds = true
		// Same raised glow-swap pair as anime, and for the same reason (see the anime case): the
		// eval-kernel fix cut photo's glow density too (img_10 photo 63 vs the ~117 the old
		// mis-scoring produced). Measured on img_10 photo, seed 1: 0.30/0.90 is -4.3% SSE at 108
		// glows, while 0.30/0.80 is +1.0% at 99 — photo prefers the denser pair outright. NB one
		// image, one seed: anime carries the 3x2 replication, photo rides its direction.
		// REVERTED 2026-08-03 evening together with anime's — see the anime case.
		// LOO refit (see the anime case below for the measured rationale) — photo shares it.
		d.LooRefit = 2
	case "flat":
		d.WeightStr = 0
	default: // anime
		d.WeightStr = 0.15
		// False-edge additive polish term, anime only (λ·relu(|∇recon|−|∇target|), Sobel on luma —
		// the standout detector charged DURING the descent). GPU-measured at λ=0.004 across the bank
		// × seeds 1/7/13: img_22-class (cel, big flats — where polish used to be gate-discarded)
		// −14..20% weighted with ΔE/SSIM/banding all better; img_5 −0..3% with all metrics better;
		// img_24/img_25/img_12 tie/inert; eye: smoother skin/hair, line work intact. Photo pays a
		// consistent +1..2% weighted for the banding drop (OFF); flat is content-scattered (img_1
		// line-art likes 0.008 — manual flag), so both stay 0.
		// 2026-07-19 re-tune UNDER REGION WEIGHTING (the 0.004 was the global-λ-era compromise;
		// EAGLE was re-tuned for TermRegionWeight but FE was not): grid {0.004,0.012,0.03,0.06} ×
		// 3 imgs vs the artifact analyzer's false-edge total. 0.012 = strictly better on smooth
		// cel (img_10 FE −6%, banding+SSIM better, ΔE unchanged), parity-plus on img_5, the usual
		// spiky-content ΔE tax on img_24 (+0.09, the accepted family split). ≥0.03 buys more FE
		// but starts washing color (ΔE 3.14→3.30→3.51).
		d.FalseEdge = 0.012
		// SSIM additive polish term (λ·Σ(1−SSIM_8×8) on luma — local contrast/structure SSE
		// undercharges), anime only. GPU λ-grid {1e-4..0.02} × img_5/img_22/img_24 × seeds 1/2/3:
		// SSIM/banding improve monotonically with λ (band −10..16%) but ΔE drifts past ~0.006;
		// 0.006 = the balanced point (band −10..12%, SSIM +0.003..0.010, ΔE ≤ +0.06, term ≈2% SSE).
		// Eye at the 0.01 bracket: cel lash/lid lines crisper, pupil highlight cleaner, no colour
		// damage; painterly (img_24) a coin toss. Photo pays real colour (cat ΔE 2.87→3.08, +7%
		// weighted at 0.01) — OFF; flat thin evidence (one image) — OFF, manual -polish-ssim.
		d.SSIM = 0.006
		// EAGLE additive polish term (λ·Σ|HP(var₃(Scharr))| mismatch — hard-edge STRUCTURE the
		// magnitude-only FE relu can't see), anime only. GPU λ-grid {0.005/0.015/0.03} ×
		// img_5/img_24 × seeds 1/2: smooth cel wins both seeds at 0.015 (−1.0/−3.0% SSE with
		// SSIM/banding better; −8% at 0.005 on seed 2), spiky img_24 pays +1.3..1.8% SSE at
		// perceptual parity — the same content split FE/SSIM показали. λ=1e-3 from the paper is an
		// order too small here (term 0.1% of SSE); 0.015 ≈ 1% of SSE is the balance; ≥0.05
		// over-presses everything. Wall cost on GPU ≈ 0. Devices without the term (old DLL,
		// Vulkan pending its port) disable it instead of falling back to the CPU polish driver.
		// 2026-07-20: with the REGION-WEIGHTED terms (TermRegionWeight below) the λ compromise
		// moves — weighting by 1−HardEdgeMap zeroes the term on legitimate line-work, so smooth
		// zones can take 0.1 (the weighted grid's working point; 0.05 ≈ neutral). Measured with
		// SmoothBase as the combo: img_10 SSE parity + ΔE/p95/SSIM all better + visibly fewer
		// translucent rims in smooth zones (the Nexus-screenshot patchwork), img_5 −7% SSE with
		// every metric better, img_24 (spikes) pays +6% SSE — the family's known content split,
		// accepted by the owner for the smooth-content win.
		d.Eagle = 0.1
		d.TermRegionWeight = true
		// Region-gated kind selection, anime only (the generation-side fix for hard rects/tris
		// "standing out" of organic content — the owner's original complaint). Host-path A/B,
		// sRGB 3-channel map: img_5 −7.4/−0.9% across seeds with SSIM/banding better, img_22
		// −1.5%, img_12/img_25 ties, img_24 +1.7..5.1% SSE at perceptual PARITY (eye: spikes a
		// hair softer, shading cleaner — the perceptual intent). Per-cell gating runs inside the
		// CUDA generators (fp_set_kind_gate), wall cost ≈ 0; photo unmeasured (no photo bench
		// yet) and flat needs its rect-rich pool — both OFF.
		d.RegionKinds = true
		// Deep-smooth glow swap, raised from the engine default 0.10/0.80 (2026-08-03). The old
		// pair was tuned against a MIS-SCORING eval kernel: the warp kernel had no per-pixel-alpha
		// branch, so a glow candidate was scored as a SOLID ellipse — credited with full coverage,
		// winning steps it did not deserve, then composited with its real falloff. Fixing the
		// kernel halved the glow count (img_10 117 -> 64) and the ellipse rims the swap exists to
		// dissolve came back, which the owner caught by eye immediately. tau 0.30 buys that density
		// back deliberately and measures BETTER than the old accident: 3 images x 2 seeds vs the
		// 0.10 pair, 0.30/0.90 is -0.41% SSE mean (better on 4 of 6) at glow density 102-109 —
		// closest to the 117 the owner had approved — while 0.30/0.80 is the SSE optimum (-1.51%
		// mean, better on 5 of 6) at a lower density of 84-97. Density is what the eye reads here
		// and SSE is blind to it, so the eye-matching pair wins; -smooth-glow-tau/-prob override.
		// REVERTED 2026-08-03 evening to the engine pair (0.10/0.80) — the one the owner had actually
		// approved by eye. tau 0.30 reaches the glow swap into MODERATELY structured cells, and a glow
		// there is a half-transparent soft splat: the same failure mode as the size-conditioned swap
		// below, measured the same way (SSE + density counts, no full-frame eye check), shipped the
		// same day, and rejected by the owner on the same generation. Both stay reachable by flag.
		// The SIZE-conditioned glow swap (Options.BigGlowTau/-Prob) was measured here 2026-08-03 and
		// stays OFF — BUST. Every number said ship it: −3.9% SSE mean on anime, −5.0% on photo,
		// −6.2% at 600-900px, and the biggest false-edge cut of any variant. The owner's eye on a
		// full frame said the opposite and was right: swapping the big shapes for rimless glows
		// sprays half-transparent soft splats over the WHOLE image, which veils contrast, blotches
		// skin and smears line-work — while false-edge, a Sobel measure, rewards exactly that
		// (a soft radial blob has almost no gradient energy). Two lessons, both already in the
		// house style and both re-learned the hard way: judge on the FULL FRAME, never a crop, and
		// never let a proxy metric stand in for the eye. CLI-reachable via -big-glow-tau for lab work.
		// Lost-detail term, anime only (see lostdetail.go): charges structure the recon ERASED —
		// the mirror of FalseEdge, and the one artifact FE/EAGLE/SSE were all blind to (a rimless
		// glow over detail draws no edge and sits near the local mean). λ=0.2 measured across
		// img_23/img_10/img_5 × 2 seeds: better on 5 of 6 runs, img_10 −10%, img_5 inert (nothing
		// to recover there — it has ~no glows). λ=0.35 is unstable (img_10 seed 2 blows up) and
		// λ≥0.5 makes the polish reject every iterate and ship the greedy input unchanged.
		// PHOTO IS DELIBERATELY EXCLUDED: measured 57821 → 66154 (+14% worse) on img_10 photo —
		// photo runs every perceptual λ at 0 by design, and this term is no exception.
		// REVERTED 2026-08-03 evening, DEFAULT 0: λ=0.2 is an order above every other perceptual term
		// here (FE 0.012, EAGLE 0.1 region-weighted, SSIM 0.006) and was picked on "5 of 6 runs better
		// by SSE" alone — no full-frame eye check ever ran on it. It ships in the same build the owner
		// rejected for dull, blotchy colour. Reachable via -polish-lost-detail; re-tune it only against
		// full frames the owner has seen, one term at a time.
		// Smooth-region gradient base (see the photo case for the gate-safety rationale): the
		// other half of the anime combo above — claims replace the greedy's translucent facet
		// patchwork in large smooth zones with 2-4 jointly-colour-solved gradient stacks.
		d.SmoothBase = true
		// LOO refit (2026-07-20, the owner's "shapes are wasted" complaint measured and fixed):
		// after the polish, 17-25% of shapes are individually harmful-or-neutral in the FINAL
		// stack (greedy scores at placement; later shapes overpaint). Two exact-LOO prune→regrow→
		// re-polish rounds reclaim that budget: img_10 −10.5% SSE, img_5 −11.9%, img_24 −11.6%,
		// with ΔE/p95/SSIM ALL better on every image, both seeds. Rounds converge at 2 (round 3
		// prunes nothing); each round is gated end-to-end so it can never regress. Wall +30-50%
		// (the owner: quality over time). Flat keeps BackFit instead (unmeasured overlap).
		d.LooRefit = 2
		// Analytic per-candidate alpha (2026-07-19 night), anime only: the eval epilogue re-solves
		// the optimal color for a 6-point alpha grid over [alphaMin, 0.75] and keeps the ΔSSE-min
		// pair — alpha becomes exact instead of ~U(alphaMin,1), at zero wall cost (the accumulators
		// are alpha-independent). The 0.75 cap is essential: uncapped, the grid over-picks opaque
		// greedy-optimal claims that kill the soft layering the polish co-adapts (img_5 SSIM −0.02).
		// Capped: 7/9 SSE better (img_10 −1.7..−2.7%) across 3 imgs × 3 seeds, perceptual
		// parity-or-better. Photo measured flat (+0.6% SSE, parity) — stays off there.
		d.AnalyticAlpha = true
		// Shading pre-pass for anime too (2026-07-20 night, the owner's "gradients stand out"
		// complaint on ChatGPT-style art): claim coherent linear-ramp regions (lit walls, soft
		// vignettes) as base+gradient-word stacks before the greedy — the ramp detector was
		// photo-only while modern anime-tagged art is full of real ramps. Measured on img_9
		// (ramp-heavy): SSIM +0.011/+0.014 with ΔE −0.2 and banding better on BOTH seeds — the
		// biggest perceptual jump of the campaign (SSE pays up to +5%: the known smoothness split).
		// Cel stays safe by the ≥30% gate: img_24 bit-identical, img_5 slightly better, img_10
		// seed-noise. NB NOT combined with a hotter glow-swap (0.15/0.9) — measured anti-synergy.
		d.ShadePre = true
		// Merge consolidation inside the LOO rounds (owner's idea, 2026-07-20 night): collapse
		// near-duplicate pairs (same kind, near-same color, IoU≥0.55 — the translucent-stack
		// convergence greedy leaves in smooth cel) into one moment-fitted shape; the freed slots
		// regrow on the residual under the round's e2e gate. Measured: img_10 SSE −1.7/−3.0% with
		// false-edge −5% and SSIM +0.005 on BOTH seeds; img_5/img_24 parity (few mergeable pairs).
		d.MergeRefit = true
		// Saliency quota (built 2026-06-11, defaulted 2026-07-20 on the owner's "eyes break the
		// image" complaint): the final 15% of the budget places shapes ONLY inside the top-detail
		// cells, so eyes/faces can't be outbid by big soft regions. Measured: img_10 iris/pupil
		// visibly reassemble (the exact complaint) with unweighted SSE −1.4% and banding better
		// (ΔE +0.12 — budget honestly leaves the background); img_5 parity; img_24 (June) both
		// irises alive. Photo unmeasured — off there.
		d.SaliencyQuota = 0.15
		// Ramp-weight boost (the "gradients stand out" complaint, 2026-07-20):
		// up-weight the shape budget in smooth-gradient (ramp) cells so they get enough shapes to
		// render smoothly — edge-biased weighting starves gradients, leaving big standout facets.
		// metric.RampMap-gated so it self-limits on cel/line content. Measured: img_10 (ramp 0.148)
		// SSE −2.2% + SSIM +0.001 + FE −2% with the dress folds visibly smoother (eye: dress crop),
		// img_5 SSE −0.4% + SSIM +0.003 + banding better; 1.5 is the balanced point (2.5 over-pushes:
		// img_5/img_9 SSIM regress). img_9 (ramp ~0) is near-inert. See metric/gradient.go RampMap.
		d.RampWeight = 1.5
		// Ramp-aware hotter glow swap (Options.RampGlow) is NOT defaulted — measured 2026-07-20 as a
		// BUST. The idea: run the deep-smooth glow swap at tau 0.30 / prob 0.90 (vs the global 0.10 /
		// 0.80) only where RampMap flags a genuine gradient, to recover the global-tau-raise img_10 win
		// (SSE −4.7%, bokeh smoother) without its structured-content regression. It doesn't: the global
		// win came from glow-swapping MODERATE-hardness cells (hard 0.1-0.3 — dress folds), but RampMap
		// requires hard < 0.14, so ramp cells already glow at the global tau and the hot pair captures
		// ~none of the win. Measured img_10 SSE +0.01% (parity), img_24 −0.44%, img_9 +0.13% — noise.
		// Code kept + CLI-reachable (-ramp-glow); the device path is inert (byte-identical) when off.
	}
	if flat {
		d.AspectMax = 8
		d.PolishTau1 = 0.06
		// Auto-shape-count knee (flat/line-art only): large uniform regions (white bg) saturate fast,
		// after which the greedy wastes budget on imperceptible ghost facets ("квашня"). The floor lets
		// the knee trip on near-SOLVED flats (where the ÷currentErr rate blows up and never stops).
		// SELF-ADAPTIVE per image — validated on the bank: img_2 (sparse face on white) 3000→982 shapes
		// (−67%, SSIM ≥ base, LOWER banding); img_17 (dense brush-art) 3000→2354 (−22%, EYE-EQUAL at 3×
		// zoom — the −0.012 SSIM is below the perceptual threshold); img_18 (dense sketch) untouched (3000).
		// floor 0.003 (vs the more aggressive 0.005) is the eye-confirmed-safe value: still a big win on
		// sparse line-art, no visible loss on dense. OFF for anime/photo so genuine detail/eyes keep the
		// full budget. Later: a perceptual ΔE/JND gate (research #2) can make the cut fully surgical.
		d.KneeTol = 2e-4
		d.KneeFloor = 0.003
		if palette < 80 { // VECTOR logo (few colours): rect-rich + keep refining edges to 600
			d.KindWeights = []float32{0.8, 0.05, 0.15}
			d.PolishIters = 600
		} else { // TEXTURED flat: triangle-rich (kept) + converged by 300
			d.PolishIters = 300
		}
	}
	return d
}

// Economy auto-schedule thresholds (the low-budget global-search regime, validated on the bank). The
// co-adapted LIVE base lifts quality most at low-mid budgets; below ~80 the discrete anneal relocation wins;
// above ~1500 the gain is marginal vs the ~4x cost, so it's off. Off for flat (the knee already trims it).
const (
	economyAnnealMax   = 80   // budgets ≤ this use the basin-hopping anneal (tightest budget: discrete relocation beats co-adaptation)
	economyAnnealIters = 25   // anneal outer iterations at the tightest budgets
	economyLiveMax     = 1500 // budgets ≤ this (and > economyAnnealMax) use the two-phase LIVE base
	economyBaseCap     = 400  // LIVE base size cap (base ≈ budget/4, capped here)
	economyBatch       = 6    // LIVE batch size
)

// EconomyParams returns the AUTO low-budget global-search schedule for a content mode + shape budget — the
// economy "efficient base → detail budget" win. Returns engine.Options' LiveBatch / LiveBase / AnnealIters.
// Off (0,0,0) for flat/line-art (the knee handles those) and for budgets above economyLiveMax (marginal vs
// the ~4x cost). Single source of truth shared by the studio (Resolve) and the CLI so they can't drift.
func EconomyParams(resolvedMode string, shapes int) (liveBatch, liveBase, annealIters int) {
	if PresetMode(resolvedMode) == "flat" || shapes <= 0 {
		return 0, 0, 0
	}
	switch {
	case shapes <= economyAnnealMax:
		return 0, 0, economyAnnealIters
	case shapes <= economyLiveMax:
		base := shapes / 4
		if base > economyBaseCap {
			base = economyBaseCap
		}
		if base >= shapes {
			return 0, 0, 0
		}
		return economyBatch, base, 0
	default:
		return 0, 0, 0
	}
}

// ModeKnobDefaults returns the CONCRETE knob values a built-in content preset resolves to for the
// given content (palette colour count + whether the source is a cutout). It mirrors ModeDefaultsFor
// plus the alpha/kind/polish logic from resolveShapeParams, so the studio's expert controls can show
// the real numbers a preset uses instead of an opaque sentinel. Shapes/Seed stay caller-owned.
func ModeKnobDefaults(mode string, colors int, cutout bool) Choices {
	c := DefaultChoices()
	c.Mode = mode
	c.Quality = "quality" // the studio's built-in quality knee
	resolved := PresetMode(mode)
	if resolved == "gaussian" {
		return c
	}

	md := ModeDefaultsFor(resolved, colors, cutout)
	allowAlpha := resolved != "flat" && !cutout
	polish, weighted, compact := true, true, true
	random, mutated, sampleBudget, maxNI := PresetCounts(c.Quality)

	c.Alpha = &allowAlpha
	c.Polish = &polish
	c.Weighted = &weighted
	c.Compact = &compact
	c.Boundary = boolPtr(md.Boundary)
	c.Backfit = boolPtr(md.Backfit)
	c.Kinds = "ellipse,triangle,rectangle"
	c.KindWeights = formatKindWeights(md.KindWeights)
	c.Aspect = float64(md.AspectMax)
	c.WeightStrength = md.WeightStr
	c.AlphaMin = float64(md.AlphaMin)
	c.PolishIters = md.PolishIters
	c.PolishTau1 = md.PolishTau1
	c.Random, c.Mutated, c.SampleBudget, c.MaxNoImprove = random, mutated, sampleBudget, maxNI
	return c
}

func boolPtr(b bool) *bool { return &b }

// formatKindWeights renders per-kind weights as a CSV that ParseKindWeights round-trips.
func formatKindWeights(w []float32) string {
	if len(w) == 0 {
		return ""
	}
	parts := make([]string, len(w))
	for i, v := range w {
		parts[i] = strconv.FormatFloat(float64(v), 'g', -1, 32)
	}
	return strings.Join(parts, ",")
}

// PresetMode collapses any legacy mode name to one of the THREE manual presets. Empty/auto/unknown
// -> "anime", the best general-purpose preset (line-art and most gradients reconstruct well under it).
// Exported so the CLI shares the exact same 3-preset mapping as the studio (no divergence).
func PresetMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "photo", "realism":
		return "photo"
	case "flat", "logo", "line", "cutout":
		return "flat"
	case "lineart", "line-art":
		return "flat" // hybrid line-art: OPAQUE flat fill (a white background stays clean — no semi-transparent casts) + FDoG ink lines on top
	case "gaussian", "gauss", "smooth", "gradient":
		return "gaussian" // NICHE: soft-glow reconstruction for smooth/gradient content (engine.GenerateGaussian)
	case "pixel", "pixel-art", "pixelart":
		return "pixel" // EXACT pixel-art reproduction (internal/pixel rect decomposition — no engine)
	default: // "", "auto", "anime", "anime-ink", "hybrid", "shaded", "illustration", anything else
		return "anime" // hybrid anime-ink/hybrid: semi-transparent fill (alive eyes/gradients) + FDoG ink on top
	}
}

// IsHybridMode reports whether mode is a hybrid-family preset (geometrize fill + FDoG ink lines on top):
// "lineart" (opaque flat fill) or "anime-ink" (semi-transparent fill). The caller reserves InkBudget of
// the shapes for the ink and appends the lines after the fill; non-hybrid modes draw the fill only.
func IsHybridMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "lineart", "line-art", "anime-ink", "anime-hybrid", "hybrid":
		return true
	}
	return false
}

// DefaultInkRatio is a hybrid preset's starting Lines<->Fill split (fraction of budget for ink): line-art
// is line-led (the lines ARE the content), anime-ink is fill-led (colour dominates, fewer major contours).
// The studio's Artist slider seeds from this; 0 for non-hybrid modes.
func DefaultInkRatio(mode string) float64 {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "lineart", "line-art":
		return 0.40
	case "anime-ink", "anime-hybrid", "hybrid":
		return 0.20
	}
	return 0
}

// InkBudget splits a total shape budget into the FDoG ink count for a Lines<->Fill ratio, always leaving
// at least one fill shape. ratio is clamped to [0, MaxInkRatio]; the geometrize fill gets shapes-ink.
func InkBudget(ratio float64, shapes int) int {
	if ratio < 0 {
		ratio = 0
	}
	if ratio > MaxInkRatio {
		ratio = MaxInkRatio
	}
	ink := int(ratio*float64(shapes) + 0.5)
	if ink >= shapes {
		ink = shapes - 1
	}
	if ink < 0 {
		ink = 0
	}
	return ink
}

// PresetCounts returns the (random, mutated, sampleBudget, maxNoImprove) base for a quality
// preset name. The single source of truth shared by Resolve and the CLI. Unknown -> balanced.
func PresetCounts(name string) (random, mutated, sampleBudget, maxNI int) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "fast":
		return 400, 200, 0, 100
	case "max":
		return 10000, 2000, 16000, 500
	case "quality":
		return 50000, 5000, 32000, 2000
	case "ultra":
		return 200000, 12000, 1 << 30, 5000
	default: // balanced / unknown / ""
		return 1000, 1000, 0, 100
	}
}

// resolveGaussian builds the NICHE Gaussian-mode run config: reconstruct the image as `shapes` soft GLOW
// splats jointly trained by the polish (engine.GenerateGaussian), bypassing the greedy entirely. For
// smooth/gradient/painterly content (8x better than greedy on a gradient; loses on fine detail). The
// greedy-specific knobs (kinds/alpha/boundary/coarse/moment) are irrelevant, so this short-circuits Resolve.
// PolishOpts.Iters is the from-scratch training budget; uniform weight (no edge weighting for smooth content).
func resolveGaussian(prep imageio.Prepared, c Choices, w, h, shapes int) Resolved {
	iters := c.PolishIters
	if iters <= 0 {
		iters = gaussTrainIters(shapes)
	}
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1
	}
	opt := engine.Options{
		Width: w, Height: h, Background: prep.Background,
		StopAt:        shapes, // = the glow count
		Seed:          c.Seed,
		TransparentBG: prep.HasTransparency,
		Gaussian:      true,
		PolishOpts:    polishOpts(iters, 0, 0.08, false, 0, 0, 0, 0),
	}
	return Resolved{
		Options: opt,
		Weight:  weight,
		Grid:    128,
		Mode:    "gaussian",
		SS:      1,
		Summary: []string{
			fmt.Sprintf("Gaussian (niche): %d glow splats, %d train iters, linear-light=%v", shapes, iters, model.LinearLight),
			"smooth/gradient content — soft splats jointly trained on the GPU (no greedy, no densify)",
		},
	}
}

// gaussTrainIters scales the from-scratch training budget to the glow count (more glows = more params to
// converge), capped so a large budget stays interactive. ~1500 was ample for 600 glows; 3000 still gains
// a little past 3000 iters but the marginal return is small and each iter costs one GPU launch PER glow,
// so the cap trades a sliver of convergence for a big wall-time saving at the top end (3000 glows × 4000
// iters ≈ 24M launches ≈ minutes). The user's real speed knob is the budget (glow count) itself.
func gaussTrainIters(shapes int) int {
	it := 1000 + shapes
	if it > 3000 {
		it = 3000
	}
	return it
}

func polishOpts(iters int, tau0, tau1 float64, ste bool, feLambda, ssimLambda, eagleLambda, lostDetailLambda float64) engine.PolishOptions {
	o := engine.DefaultPolishOptions()
	if iters > 0 {
		o.Iters = iters
	}
	if tau0 > 0 {
		o.Tau0 = tau0
	}
	if tau1 > 0 {
		o.Tau1 = tau1
	}
	o.STE = ste
	o.FalseEdgeLambda = feLambda
	o.SSIMLambda = ssimLambda
	o.EagleLambda = eagleLambda
	o.LostDetailLambda = lostDetailLambda
	return o
}

// KindNames lists the supported shape primitives in canonical order. Shared by the UI kind picker;
// adding a primitive means extending this plus ParseKinds (and the engine/raster support).
var KindNames = []string{"ellipse", "triangle", "rectangle"}

// ParseKinds maps a CSV of kind names to ShapeKinds. No "line" — the FH6 catalog has none.
// Empty/garbage -> {ellipse}. Shared by Resolve and the CLI.
func ParseKinds(csv string) []model.ShapeKind {
	m := map[string]model.ShapeKind{
		"ellipse":   model.KindEllipse,
		"rectangle": model.KindRectangle,
		"triangle":  model.KindTriangle,
	}
	var out []model.ShapeKind
	for _, part := range strings.Split(csv, ",") {
		if k, ok := m[strings.TrimSpace(strings.ToLower(part))]; ok {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		out = []model.ShapeKind{model.KindEllipse}
	}
	return out
}

// ParseKindWeights parses a CSV of float weights (parallel to Kinds). Empty/malformed -> nil.
// Shared by Resolve and the CLI.
func ParseKindWeights(csv string) []float32 {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]float32, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil
		}
		out = append(out, float32(v))
	}
	return out
}
