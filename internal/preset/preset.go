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
	"strconv"
	"strings"

	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/metric"
	"fh6-paint-studio/internal/model"
)

// MaxShapes is the FH6 per-group layer ceiling; Resolve clamps Shapes to it.
const MaxShapes = 3000

// Choices is the high-level configuration the UI exposes. Curated fields drive the
// common path; the advanced fields default to the quality preset / content mode unless
// set. Tri-state toggles use *bool (nil = mode default). Float "auto" sentinels: Aspect
// and WeightStrength use -1; PolishTau0/Tau1 use 0; Overdraw uses 0/1 for off.

type Choices struct {
	Shapes   int    // budget; clamped to [1, MaxShapes]
	Mode     string // anime|photo|flat (3 manual presets; legacy names collapse via presetMode; "" -> anime)
	Quality  string // fast|balanced|max|quality|ultra ("" -> balanced)
	Alpha    *bool  // nil = mode default; set = override (forced off for cutouts)
	Seed     int64
	Polish   *bool // nil = on
	Backfit  *bool // nil = mode default (auto for flat/logo/line/cutout)
	Boundary *bool // nil = off (opt-in). boundary-aware radius — best on smooth photo/anime characters (smoother gradients, less veil overshoot); regresses on text/flat, so not auto-defaulted
	SS       int   // preview supersample factor (UI-side; carried through Resolved.SS)

	// Advanced (zero = preset/mode default, except Aspect/WeightStrength = -1)
	Random, Mutated, SampleBudget, MaxNoImprove int
	Grid                                        int
	Kinds                                       string // CSV; "" = ellipse,triangle,rectangle
	KindWeights                                 string // CSV parallel to Kinds; "" = mode default
	Aspect                                      float64
	WeightStrength                              float64
	PolishIters                                 int
	PolishTau0                                  float64
	PolishTau1                                  float64
	PolishSTE                                   bool
	Weighted                                    *bool // nil = true
	Compact                                     *bool // nil = true (engine still skips it for cutouts)
	Overdraw                                    float64
}

// DefaultChoices returns the GUI's starting configuration (matches the CLI flag defaults).
func DefaultChoices() Choices {
	return Choices{
		Shapes: 1000, Mode: "anime", Quality: "balanced", Seed: 1, SS: 1,
		Grid: 48, Aspect: -1, WeightStrength: -1, Overdraw: 1,
	}
}

// Resolved is everything a run needs: the engine options, the per-pixel weight map and
// grid size for the backend constructor, the concrete (auto-resolved) mode, the preview
// supersample factor, and a human-readable settings summary for the log.
type Resolved struct {
	Options engine.Options
	Weight  []float32
	Grid    int
	Mode    string
	SS      int
	Summary []string
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
	flatMode := resolved == "flat"
	transparent := prep.HasTransparency && !prep.PaddedOpaque // padded-opaque keeps content tuning; spill penalty still fires

	// All benchmark-hardwired per-mode constants come from ModeDefaultsFor (the single source of truth
	// shared with the CLI). The override logic (explicit Choices fields) stays here.
	md := ModeDefaultsFor(resolved, cs.Colors, transparent)

	sp := resolveShapeParams(md, c, flatMode, transparent)

	weight := buildWeightMap(prep, w, h, c, flatMode || transparent, sp.wstr)

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
		PolishOpts:        polishOpts(sp.iters, c.PolishTau0, sp.tau1, sp.ste),
		BackFit:           sp.backfit,
		BackFitPasses:     2,
		BackFitFrac:       0.1,
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

	return Resolved{Options: opt, Weight: weight, Grid: grid, Mode: resolved, SS: ss, Summary: summary}
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
	allowAlpha  bool
	alphaMin    float32
	kindsCSV    string
	kindWeights []float32
	wstr        float64 // edge-weight blend toward uniform; also drives the saliency map
	aspectMax   float32
	polish      bool
	boundaryOn  bool
	backfit     bool
	iters       int
	tau1        float64
	ste         bool
}

// resolveShapeParams layers the explicit Choices overrides on top of the mode defaults. The
// per-mode rationale for each base value lives in ModeDefaults / ModeDefaultsFor; the notes here
// cover the override semantics (which sentinel means "unset").
func resolveShapeParams(md ModeDefaults, c Choices, flatMode, transparent bool) shapeParams {
	sp := shapeParams{
		alphaMin:    md.AlphaMin,
		kindWeights: md.KindWeights,
		wstr:        md.WeightStr,
		aspectMax:   md.AspectMax,
		boundaryOn:  md.Boundary,
		backfit:     md.Backfit,
		iters:       md.PolishIters,
		tau1:        md.PolishTau1,
		ste:         true, // STE is always on; the studio toggle is removed
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
	_ = c.PolishSTE // STE is always on; the studio toggle is removed

	return sp
}

// buildWeightMap produces the per-pixel saliency weight. It optionally builds an edge-saliency map
// (richer dilated V2 for flat/cutout, Sobel for smooth content), blends it toward uniform by
// weight-strength, then — under linear-light output — multiplies in the perceptual sRGB-derivative
// weight. Returns nil only when saliency weighting is off AND linear-light is off (the engine then
// treats the run as uniform).
func buildWeightMap(prep imageio.Prepared, w, h int, c Choices, useV2 bool, wstr float64) []float32 {
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
		wp := make([]float32, w*h)
		var sum float64
		for i := 0; i < w*h; i++ {
			y := 0.2126*prep.Pixels[i*4] + 0.7152*prep.Pixels[i*4+1] + 0.0722*prep.Pixels[i*4+2]
			yd := float64(y)
			if yd < 0.02 {
				yd = 0.02 // clamp the dark blow-up of the sRGB derivative
			}
			d := 0.4396 * math.Pow(yd, -0.5833) // d/dlin of 1.055*lin^(1/2.4)-0.055
			f := float32(d * d)
			if f > 16 {
				f = 16
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
	Boundary    bool      // boundary-aware radius
	Backfit     bool      // post-polish back-fitting
}

// ModeDefaultsFor returns the tuned defaults for a resolved preset (anime|photo|flat). palette is the
// metric.ContentClass colour count (the flat vector<80 / textured≥80 split); transparent forces the
// cutout back-fit. The rationale for each constant lives in Resolve's comments above.
func ModeDefaultsFor(resolvedMode string, palette int, transparent bool) ModeDefaults {
	flat := PresetMode(resolvedMode) == "flat"
	d := ModeDefaults{
		AlphaMin:    0.40,
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
	case "flat":
		d.WeightStr = 0
	default: // anime
		d.WeightStr = 0.15
	}
	if flat {
		d.AspectMax = 8
		d.PolishTau1 = 0.06
		if palette < 80 { // VECTOR logo (few colours): rect-rich + keep refining edges to 600
			d.KindWeights = []float32{0.8, 0.05, 0.15}
			d.PolishIters = 600
		} else { // TEXTURED flat: triangle-rich (kept) + converged by 300
			d.PolishIters = 300
		}
	}
	return d
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
	case "gaussian", "gauss", "smooth", "gradient":
		return "gaussian" // NICHE: soft-glow reconstruction for smooth/gradient content (engine.GenerateGaussian)
	default: // "", "auto", "anime", "shaded", "illustration", anything else
		return "anime"
	}
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

// polishOpts builds engine.PolishOptions from defaults with non-zero overrides applied.
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
		PolishOpts:    polishOpts(iters, 0, 0.08, false),
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

func polishOpts(iters int, tau0, tau1 float64, ste bool) engine.PolishOptions {
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
	return o
}

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
