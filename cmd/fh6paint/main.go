package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/metric"
	"fh6-paint-studio/internal/model"
	presetpkg "fh6-paint-studio/internal/preset"
)

func main() {
	in := flag.String("input", "", "input image (png/jpeg)")
	out := flag.String("output", "out/geometry.json", "output geometry json")
	preview := flag.String("preview", "", "optional preview PNG path")
	shapes := flag.Int("shapes", 1000, "shape budget (<=3000); higher = more detail")
	maxRes := flag.Int("max-res", 1100, "max image side in px (IDENTICAL to the studio's loadImage default)")
	autocrop := flag.Bool("autocrop", true, "auto-crop uniform/empty margins to the content bbox BEFORE downscale, so the content fills the render (more detail+shapes per feature). No-op on full-bleed images (guarded). Off: -autocrop=false.")
	region := flag.String("region", "", "reconstruct only a sub-region as its OWN decal: fx,fy,fw,fh (fractions of the source, 0..1). The crop is taken at FULL source resolution THEN scaled to -max-res, so the region fills the render — a face crop gets far more shapes/pixels than the same region inside the whole-image budget. The path to crisp detail: the detail's angular size in the decal sets its crispness, so a face inside a whole-scene decal is small and soft. Output coords are in the crop's own space (a standalone decal).")
	gridSize := flag.Int("grid", 48, "error-grid resolution (NxN cells) for importance sampling; finer (96-160) targets shapes onto small high-contrast features (thin contours, text) instead of spreading them — helps fine detail at the cost of a slightly more scattered coarse stage")
	randomSamples := flag.Int("random", 1000, "random candidates per shape")
	mutated := flag.Int("mutated", 1000, "hill-climb mutation budget per shape")
	seed := flag.Int64("seed", 1, "RNG seed")
	kindsCSV := flag.String("kinds", "ellipse,triangle,rectangle", "comma-separated shape kinds: ellipse,rectangle,triangle")
	kindWeightsCSV := flag.String("kind-weights", "0.5,0.4,0.1", "weights parallel to -kinds for the per-candidate kind pick. Organic default is TRIANGLE-RICH: triangles cheaply hit sharp fur/hair/wedge features and co-adapt under joint polish (which refines triangle geometry). Flat/logo auto-overrides to rect-rich (straight edges fragment under triangles). Set '' or mismatched length for uniform.")
	weighted := flag.Bool("weighted", true, "use edge-weighted perceptual error")
	weightStrength := flag.Float64("weight-strength", 1.0, "blend the edge-weight map toward uniform: w'=(1-s)+s*w. 1=full edge weighting, 0=uniform. Lower strength improves both image-space SSE and SSIM, since full edge weighting over-fits contours at the cost of flat-region fidelity.")
	preset := flag.String("preset", "quality", "quality preset: fast|balanced|max|quality|ultra (sets -random/-mutated/-sample-budget/-max-no-improve unless given). Default 'quality' = the high-quality knee, identical to the studio.")
	overdraw := flag.Float64("overdraw", 1.0, "generate -shapes*overdraw, keep the most useful -shapes (>1 trades time for a small quality-per-shape gain; opaque-only — ignored with -alpha)")
	alpha := flag.Bool("alpha", true, "allow semi-transparent shapes (alpha<1) on opaque images — many soft layers build smooth gradients/fur in fewer shapes (auto-OFF for cutouts so the object stays opaque)")
	sampleBudget := flag.Int("sample-budget", 0, "progressive-sampling pixel cap per shape (0=preset/backend default 4000; higher=big early shapes scored sharper; ultra uses ~full-res)")
	maxNoImprove := flag.Int("max-no-improve", 0, "consecutive non-improving shapes before early-stop (0=preset/default 100; high=fill the FULL shape budget)")
	recolorVar := flag.Float64("recolor-var", 0, "skip the weighted-mean recolor for shapes whose owned target pixels have color variance > this (keeps crisp color on boundary-straddling fur/contour slivers instead of a muddy mean). 0=off. ~0.02-0.05 for organic content.")
	detailStrength := flag.Float64("detail-strength", 0, "detail-weighted sampling: late in the run, bias candidate centres toward high-detail TARGET regions (faces/linework) by scaling the sampling grid x(1+s*detail). 0=off. ~0.35 suits organic content. Reduces face softness + smooth-region faceting on organic/anime/photo; ~no effect on flat.")
	detailStart := flag.Float64("detail-start", 0.6, "progress fraction at which detail-weighted sampling engages (needs -detail-strength>0). Earlier=stronger detail focus, less coarse-base coverage.")
	boundary := flag.Bool("boundary", false, "boundary-aware radius (OPT-IN): cap each candidate's size by its centre's distance to the nearest target boundary (luma edge / cutout silhouette), ramped past -boundary-start, so shapes can't balloon ACROSS edges. A win on smooth photo/anime CHARACTER content (smoother gradients, less translucent veil), but it regresses on text/dense-detail/flat (the all-edge cap fragments fills), so it stays opt-in. Use it on character/photo liveries.")
	boundaryPad := flag.Float64("boundary-pad", 16, "with -boundary: px a shape may still reach past a boundary (larger=looser cap, smaller=tighter to edges).")
	boundaryStart := flag.Float64("boundary-start", 0.42, "with -boundary: progress at which the radius cap engages (ramps to full by progress 1). Earlier=tighter silhouettes sooner but constrains the coarse base.")
	canvasPad := flag.Float64("canvas-pad", 0.04, "canvas-edge clamp: shrink any ellipse/rect whose rotated bbox extends past the image rectangle by more than canvas-pad*min(w,h) px on a side. Stops shapes ballooning OUTSIDE the image (visible in-game, clipped in the preview) + saves budget. 0=off (legacy). ~0.04 keeps a small edge bleed; a small value like 0.002 clamps tight (≈no bleed). Helps opaque/busy content most.")
	padTransparent := flag.Float64("pad-transparent", 0, "keep shapes inside the image: generate against a transparent surround of this fraction of the long side (~0.1) so the overhang/spill penalty bounds every shape to the content rectangle (no shape balloons past the edge). The geometry + preview are mapped back to the original size afterwards (no frame). 0=off. The robust fix for the 'shapes outside the image' artefact on full-bleed content.")
	standout := flag.Float64("standout", 0, "post-polish PERCEPTUAL standout suppression: detect shapes whose rim draws an edge the TARGET lacks (a visible circle/square the SSE metric is blind to) and recolour-to-local-mean or remove them, gated so the GLOBAL error rises at most this fraction. 0=off. ~0.005 = conservative. The metric will NOT show the win — judge by eye; the gate only bounds the loss.")
	alphaMinFlag := flag.Float64("alpha-min", -1, "lower bound for candidate alpha when -alpha is on (candidates draw alpha~U(alpha-min,1)). -1 = content-mode default (photo 0.3, anime 0.55). HIGHER = crisper/more-opaque shapes (less soft muddying of detail like eyes/lips), at the cost of needing more shapes for smooth gradients; test 0.7-0.85 on detailed faces.")
	shapeTol := flag.Float64("shape-tol", 0, "auto-shape-count: stop placing shapes when the relative marginal improvement rate r=ΔErr/(window·currentErr) per shape stays below this (0=off, fill -shapes budget). Adapts the count per image: saturated flat/logo stop early (~175-400), detailed photo/anime/cartoon fill the budget. -shapes is the ceiling. Recommended auto value: 0.0002 (conservative; trims only genuinely-saturated content). 0.0005 = aggressive/draft.")
	compact := flag.Bool("compact", true, "bias the per-shape pick toward compact shapes (cleaner coarse stage)")
	mode := flag.String("mode", "anime", "content PRESET (3 manual): anime | photo | flat. Legacy names (logo/line/illustration/cutout/auto) collapse to one of the 3 via preset.PresetMode. anime/photo = semi-transparent + triangle-rich + STE; flat = opaque + rect/triangle by palette + boundary + backfit. Transparency is auto-detected (forces opaque). Explicit flags override.")
	weightV2 := flag.Bool("weight-v2", false, "force the richer dilated ink-aware saliency map (WeightMapV2) on/off; default is auto (on for flat/logo/line/cutout, off for photo/anime). V2 protects 1-2px black contours from being smeared by flat-fill shapes.")
	preprocess := flag.String("preprocess", "auto", "target preprocessing: none|luma_bands. luma_bands = edge-weighted luminance banding (cleans contours/flat fills for the generator). auto = none (kept as a manual opt-in for noisy sources).")
	posterize := flag.Int("posterize", 0, "quantize each target RGB channel to N levels before fitting (0=off; ~32-96 for flat/logo to snap broad color regions to exact constants). Applied after -preprocess.")
	aspect := flag.Float64("aspect", -1, "max aspect ratio for ellipse/rect candidates: minor=major/U(1,aspect) makes thin slivers along the edge orientation (traces sharp contours). -1=auto (flat 8, organic 6). <=1 = round axes.")
	ssaa := flag.Int("ss", 1, "preview supersampling factor (1=off): render the output shapes at ss× then box-downsample for ANTI-ALIASED edges. Our raster uses hard binary coverage, so contours are 1px steps while the source images have soft ~1-2px ramps — that mismatch is where nearly all residual image-space error sits. ss=3-4 closes it. Affects the preview/comparison render only (the game rasterizes the geometry itself).")
	gpuSearch := flag.Bool("gpu-search", true, "CUDA build only: run each shape's random-candidate phase on-device (generate+score+argmin in one launch) — the throughput unlock for high candidate volume. Ignored by the CPU backend (host path).")
	// -moment-seed = PURE fast generation (moment-seeding, no hybrid handoff). Kept as a CLI flag for
	// experiments / new-preset tuning even though the studio no longer exposes it standalone: the studio's
	// "Fast generation" toggle now maps to the HYBRID (moment base + random detail, -moment-detail-start
	// 0.55), which superseded pure-fast for everyday use. Pure fast stays reachable here for A/B.
	momentSeed := flag.Bool("moment-seed", false, "moment-seeding (PURE fast, no hybrid): replace the blind random candidate batch with a closed-form covariance-ellipse seed fitted from the residual grid + a small localised refine pool (-moment-refine). Far fewer candidates per shape -> large eval speedup. Add -moment-detail-start 0.55 for the HYBRID (the studio's Fast generation). Kept for experiments/preset tuning.")
	momentRefine := flag.Int("moment-refine", 2048, "with -moment-seed: candidate-pool size per shape (the seeds + localised kind-weighted refinements, scored via the normal eval path + hill-climb mutate). 2048 is the quality-neutral knee (~-33% eval vs the 50k search); ~512 is faster (~-40%) for a small quality cost.")
	momentCenters := flag.Int("moment-centers", 16, "with -moment-seed: number of error-sampled SEED CENTRES per shape (the -moment-refine budget is split across them). 1 = single fit (anchors to one centre, loses to random); ~16 spreads the budget to restore multi-location exploration at the same candidate cost.")
	momentDetailStart := flag.Float64("moment-detail-start", 0, "with -moment-seed: HYBRID schedule — past this progress (0..1) hand the per-shape search off from the moment pool to the blind random brute force, which finds the sharp SMALL detail shapes the 2nd-moment blob fit never proposes (the late shapes are cheap, so it buys crispness for little time). 0 = off (moment all the way). ~0.6-0.7 = fast smooth base + sharp random detail.")
	coarseSearch := flag.Bool("coarse-search", true, "CUDA build only: coarse-to-fine search — score the candidate batch at a CHEAP pixel cap (-coarse-budget) to filter, then re-score only the -coarse-k survivors at the full -sample-budget and pick from those. The winner is full-budget scored (quality-neutral — unlike a uniform -sample-budget cut, which mis-picks on low-res noise), while the bulk pays only the coarse cost. The dominant eval-speed lever at the quality preset (roughly halves eval wall-time). Auto-disabled below ~33k candidates (n>4*-coarse-k). -coarse-search=false for the exhaustive single-pass.")
	coarseBudget := flag.Int("coarse-budget", 3000, "with -coarse-search: pixel cap for the cheap coarse filter pass (lower = faster bulk; must stay high enough that the true winner is its partition's coarse-min). 3000 is the floor: it selects the same survivors as a larger filter, while going below it starts missing winners.")
	coarseK := flag.Int("coarse-k", 8192, "with -coarse-search: number of coarse survivors re-scored at the FULL budget (higher = the true winner is more reliably included -> closer to baseline quality, at a small extra re-eval cost; the bulk stays cheap).")
	coarseFP16 := flag.Bool("coarse-fp16", true, "with -coarse-search: run the coarse FILTER pass in FP16/half2 (halves the ALU-bound per-pixel work; the FP32 re-eval still picks+scores the winner). Quality stays within the coarse-to-fine ranking band. -coarse-fp16=false restores FP32 filtering (exact-but-slower ranking).")
	warpEval := flag.Bool("warp-eval", false, "CUDA build only: warp-per-candidate eval kernel (opt-in). Slower than the default block-per-candidate kernel — large early shapes dominate runtime and want 128 threads/candidate, not 32. Kept for reference.")
	polish := flag.Bool("polish", false, "joint differentiable polish pass after greedy (refines all shapes together; slower, gated so it never regresses)")
	gaussian := flag.Bool("gaussian", false, "NICHE MODE: reconstruct the image as -shapes soft GLOW splats jointly trained by the polish (no greedy, no densify) — engine.GenerateGaussian. For SMOOTH / gradient / painterly content only (8x better than greedy on a gradient; loses on fine detail). -polish-iters sets the training budget (0 = auto-scaled to the glow count). Output glows are native FH6 KindGlow primitives.")
	polishIters := flag.Int("polish-iters", 200, "polish gradient-descent iterations. Default 200 organic / 300 flat (set below) — the perceptual knee: past it the result is indistinguishable by eye even though the hard-loss metric keeps inching down, so the extra iters are wasted wall-time (polish is 60-85% of a run). Raise for max metric fidelity.")
	polishTau0 := flag.Float64("polish-tau0", 2.0, "polish initial edge softness (px); higher = coarser early")
	polishTau1 := flag.Float64("polish-tau1", 0.08, "polish final edge softness (px); lower = sharper, smaller soft->hard snap gap. DEFAULT is content-adaptive (set below unless given): flat/cutout 0.06, organic 0.08. ~0.06-0.08 is the sweet spot across content; the gradient vanishes below ~0.05.")
	polishSTE := flag.Bool("polish-ste", false, "polish straight-through estimator: HARD-coverage forward composite (optimizes the EXACT shipped hard render, closing the soft->hard snap gap) with the soft surrogate gradient for geometry. Biggest win on flat/vector content where the snap gap is largest. Default off (soft polish).")
	polishEarly := flag.Bool("polish-early", true, "early-stop the polish loop on diminishing returns (a late-phase check adds <2% of the total hard-loss gain so far, 3x); the best-hard point is still shipped, so this only drops a genuinely-wasteful tail. Inert at the tuned iters (polish is still productive there); trims when iters are raised. -polish-early=false runs the full -polish-iters.")
	backfit := flag.Bool("backfit", false, "back-fitting: remove the lowest-contribution shapes and RE-GREEDY them against the completed-canvas residual (breaks the greedy plateau — each shape was optimal WHEN placed, but later shapes changed the canvas). Gated END-TO-END: polish(greedy) vs polish(backfit(greedy)), keep the winner, so it NEVER regresses. AUTO-ON for flat/logo/line + cutout (where the greedy plateau bites hardest); opt-in elsewhere since it costs ~one extra polish for a smaller gain.")
	backfitPasses := flag.Int("backfit-passes", 2, "number of back-fitting passes (each removes+regrows -backfit-frac of the shapes); passes stop early once one stops improving")
	backfitFrac := flag.Float64("backfit-frac", 0.1, "fraction of shapes removed+regrown per back-fitting pass (0.1 = the weakest 10%)")
	scoreJSON := flag.String("score-json", "", "comparison mode: render an existing geometry JSON through our backend, score it (unweighted SSE + per-pixel) vs -input, save -preview, then exit. Set -max-res to the JSON's canvas size for alignment.")
	polishJSON := flag.String("polish-json", "", "polish-harness mode: load a saved greedy geometry JSON, run ONLY the gated joint polish on it (current -polish-* opts) against -input, save polished -output + -preview, then exit. The greedy input is FIXED, so any final-error delta is purely the polish change — an isolated harness for tuning polish (faster than a full run). Set -max-res to the JSON canvas size.")
	imgVs := flag.String("img-vs", "", "image-space comparison: compare the -input PNG against this PNG pixel-for-pixel (must be same size), report SSE + per-pixel, save a difference heatmap to -preview, then exit. Convention-free (no rendering).")
	imgBlur := flag.Int("img-blur", 0, "with -img-vs: box-blur the compared (-img-vs) image by this radius before diffing — tests the anti-aliasing hypothesis (does softening our hard edges match an AA target?).")
	linear := flag.Bool("linear", true, "composite in LINEAR light — the space the editor renders in (gamma ~2.2). DEFAULT-ON (matches the studio): the engine optimises the linear composite so the in-game result matches the target and semi-transparent shapes stop 'popping'; output colours are sRGB-encoded; opaque content is unaffected. Use -linear=false for an sRGB comparison. Measure with -fh6-score.")
	fh6Score := flag.String("fh6-score", "", "comparison mode: render a geometry JSON the way the GAME composites it (LINEAR light) and report SSE vs -input (sRGB), then exit. Measures real in-game fidelity (the semi-transparent pop). Set -max-res to the JSON canvas size; -ss for AA; saves the in-game render to -preview.")
	flag.Parse()

	model.LinearLight = *linear
	applyPreset(*preset, randomSamples, mutated, sampleBudget, maxNoImprove)

	logPath := applog.Init("fh6paint.log")
	defer applog.Close()
	defer applog.Recover()

	// In-game hard ceiling: a livery group accepts at most 3000 shape layers. A
	// bumper panel is ~1000, a full side or roof is ~3000 — each panel is its own
	// budget, so quality-per-shape matters most at the lower counts.
	const fh6MaxShapes = 3000
	if *shapes > fh6MaxShapes {
		applog.Printf("WARNING: -shapes %d exceeds FH6 ceiling; clamping to %d", *shapes, fh6MaxShapes)
		*shapes = fh6MaxShapes
	}
	if *shapes < 1 {
		*shapes = 1
	}

	if *in == "" {
		fmt.Fprintln(os.Stderr, "usage: fh6paint -input IMG [-output JSON] [-preview PNG] [-shapes N] [-kinds ellipse,triangle,...]")
		os.Exit(2)
	}
	applog.Printf("fh6paint start: input=%s shapes=%d maxRes=%d kinds=%s weighted=%v seed=%d log=%s",
		*in, *shapes, *maxRes, *kindsCSV, *weighted, *seed, logPath)
	applog.Printf("preset=%s random=%d mutated=%d sampleBudget=%d maxNoImprove=%d alpha=%v compact=%v overdraw=%.2f",
		*preset, *randomSamples, *mutated, *sampleBudget, *maxNoImprove, *alpha, *compact, *overdraw)

	// Image-space comparison: diff two finished PNGs (native res, no rendering) against each
	// other and exit. Convention-free — renderer / type-id / ellipse-convention differences
	// can't distort it, so it's the honest way to compare two reconstructions of the same target.
	if *imgVs != "" {
		compareImages(*in, *imgVs, *preview, *imgBlur)
		return
	}

	// In-game fidelity check: render a geometry JSON in LINEAR light (how the editor composites) and
	// score it against the sRGB target — quantifies the semi-transparent "pop" and whether -linear fixes it.
	if *fh6Score != "" {
		scoreFH6(*fh6Score, *in, *maxRes, *ssaa, *preview)
		return
	}

	var prep *imageio.Prepared
	if *region != "" {
		fx, fy, fw, fh, perr := parseRegion(*region)
		must(perr)
		p, rect, lerr := imageio.LoadRegion(*in, *maxRes, fx, fy, fw, fh)
		must(lerr)
		prep = p
		applog.Printf("region crop: source rect [%d,%d %dx%d] -> %dx%d render (standalone decal)",
			rect.Min.X, rect.Min.Y, rect.Dx(), rect.Dy(), prep.W, prep.H)
	} else if *autocrop {
		p, rect, lerr := imageio.LoadAutoCropped(*in, *maxRes)
		must(lerr)
		prep = p
		applog.Printf("auto-crop: content rect [%d,%d %dx%d] (no-op when it equals the source)", rect.Min.X, rect.Min.Y, rect.Dx(), rect.Dy())
	} else {
		p, lerr := imageio.Load(*in, *maxRes)
		must(lerr)
		prep = p
	}
	applog.Printf("loaded %dx%d (bg=%.3f,%.3f,%.3f)", prep.W, prep.H, prep.Background.R, prep.Background.G, prep.Background.B)
	// Keep-shapes-inside: wrap the target in a transparent surround so the spill penalty bounds every
	// shape to the content rectangle. Records the border + original dims to map the result back below.
	padPx, origW, origH := 0, prep.W, prep.H
	if *padTransparent > 0 {
		prep, padPx = imageio.PadTransparent(prep, *padTransparent)
		applog.Printf("pad-transparent: %.2f → %dx%d surround (spill-penalty bounds shapes inside the image)", *padTransparent, prep.W, prep.H)
	}
	// cutout = a real source cutout, not just the keep-inside margin. The spill penalty (TransparentBG)
	// still fires on the margin either way; the tuning below only switches to cutout mode for a real one.
	cutout := prep.HasTransparency && !prep.PaddedOpaque

	// Resolve the content MODE FIRST — it drives three things: alpha (flat/line-art ->
	// OPAQUE crisp edges; photo/anime -> semi-transparent smooth gradient build-up), the
	// kind mix (flat -> ellipse-dominant thin strokes that trace contours), AND the
	// saliency map (flat/cutout -> the richer ink-aware WeightMapV2). Cutouts force opaque.
	// Explicit flags always win.
	userSet := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { userSet[f.Name] = true })
	cs := metric.ContentClass(prep.Pixels, prep.W, prep.H) // cs.Colors feeds the flat vector/textured split
	// Content preset is USER-EXPLICIT, shared 1:1 with the studio via presetpkg.PresetMode (3 manual
	// presets). "auto"/""/unknown -> anime.
	resolvedMode := presetpkg.PresetMode(*mode)
	flatMode := resolvedMode == "flat"
	// All per-mode constants come from presetpkg.ModeDefaultsFor — the SINGLE source of truth shared
	// with the studio (preset.Resolve), so the CLI and GUI can never drift. The CLI keeps its
	// flag/userSet override plumbing; only the constant VALUES are sourced from md.
	md := presetpkg.ModeDefaultsFor(resolvedMode, cs.Colors, cutout)

	// Boundary-aware radius default (md.Boundary: on anime/character, off flat/photo). -boundary overrides.
	if !userSet["boundary"] {
		*boundary = md.Boundary
	}
	// Alpha + kind mix (md): organic = semi-transparent, alphaMin 0.40, triangle-rich; flat = OPAQUE,
	// rect-rich for VECTOR logos (few colours), triangle-rich for TEXTURED flat.
	allowAlpha := !flatMode
	alphaMin := md.AlphaMin
	kindWeights := presetpkg.ParseKindWeights(*kindWeightsCSV)
	if flatMode && !userSet["kind-weights"] && cs.Colors < 80 {
		kindWeights = md.KindWeights // vector logo: rect-rich (avoids the faint stray triangle)
	}
	if cutout {
		allowAlpha = false // cutout object must stay opaque
	}
	if userSet["alpha"] { // explicit -alpha overrides the mode's on/off
		allowAlpha = *alpha && !cutout
	}
	if userSet["alpha-min"] && *alphaMinFlag >= 0 && *alphaMinFlag <= 1 {
		alphaMin = float32(*alphaMinFlag) // explicit crispness override (GPU SearchRandom honours it as a scalar)
	}

	// Weight-strength default: the full edge weight (1.0) over-fits contours and hurts both
	// image-space SSE and SSIM, so soften it per content — flat/vector -> uniform (0), anime ->
	// light (0.15), photo -> mild (0.40). Explicit -weight-strength overrides. (The blend is
	// applied to the weight map below.)
	if !userSet["weight-strength"] {
		*weightStrength = md.WeightStr // anime 0.15 / photo 0.40 / flat 0 (the only anime≠photo knob)
	}

	// Aspect bias (thin elongated slivers laid along the local edge orientation): the BIGGEST
	// lever for fine CONTOURS (fur spikes, hair, outlines). Slivers trace sharp edges in few
	// shapes; round axes leave fur/hair blobby (rounded spikes, lost separations). flat 8
	// (sharpest), organic 6; round axes (0) only if forced off. Explicit -aspect wins.
	aspectMax := md.AspectMax // flat 8, organic 6
	if userSet["aspect"] {
		aspectMax = float32(*aspect)
	}

	// Joint polish auto-on for ALL content. With the sharpened final tau the soft->hard snap gap is
	// small enough that polish helps FLAT/vector art too, and on smooth content it's the big lever.
	// It's GPU-fast and strictly gated (never regresses), so always-on is safe; explicit -polish=false
	// opts out (e.g. for speed — flat polish at 3000 shapes is ~half the wall time).
	if !userSet["polish"] {
		*polish = true
	}
	// STE (hard-coverage forward polish) improves every content type, because it optimises the EXACT
	// hard render the editor ships rather than a soft-blurred surrogate. Always on unless overridden.
	if !userSet["polish-ste"] {
		*polishSTE = true
	}
	// recolor-var 0.03 keeps crisp colour on contour slivers instead of replacing it with a muddy mean.
	if !userSet["recolor-var"] {
		*recolorVar = 0.03
	}
	// Back-fitting auto-on for flat/logo/line + CUTOUT, where the greedy plateau bites hardest (large
	// flat regions): it re-spends the weakest shapes against the completed-canvas residual. Gated
	// end-to-end (polish(greedy) vs polish(backfit(greedy)), keep the winner) so it NEVER regresses; on
	// flat/cutout content polish is the cheaper pass so the gate's 2nd polish costs little. Detailed/photo
	// content benefits less and pays a dearer 2nd polish, so it stays opt-in (-backfit) there.
	if !userSet["backfit"] && md.Backfit {
		*backfit = true
	}
	// Polish-iters knee for flat content SPLITS by palette (the organic default is 200, set on the flag):
	// a few-colour hard-edge VECTOR logo keeps refining its crisp edges to 600, while a many-colour
	// TEXTURED cartoon/text-illust is converged by 300. Past the knee the hard-loss metric keeps dropping
	// but the eye doesn't (a vector-logo edge is the one exception). -polish-iters overrides.
	if !userSet["polish-iters"] && flatMode {
		*polishIters = md.PolishIters // vector-flat 600 / textured-flat 300 (organic 200 = the flag default)
	}
	// Polish FINAL TAU (tau1): lower tau1 sharpens the soft render's final edges, shrinking the
	// soft->hard snap gap so polish optimizes nearer the SHIPPED hard render. ~0.06-0.08 is the sweet
	// spot across content; the gradient starts to vanish below ~0.05. Flat/cutout wants 0.06, organic
	// is most stable near 0.08. Content-adaptive, mirroring the polish-iters split. -polish-tau1 overrides.
	if !userSet["polish-tau1"] {
		*polishTau1 = md.PolishTau1 // flat 0.06 / organic 0.08
	}

	// Target preprocessing (flat/vector content): clean the image the generator fits so shapes lock
	// onto crisp contours + exact fills instead of compression/AA noise. luma_bands = edge-weighted
	// luminance banding; posterize = color quantization. Applied BEFORE saliency + backend so both see
	// the cleaned target. Banding makes the target differ from the original, which usually costs more
	// than the cleaner-contour benefit buys on already-clean sources, so auto = none — both are kept as
	// manual opt-ins for noisy sources where they might help.
	preMode := strings.ToLower(*preprocess)
	if preMode == "auto" {
		preMode = "none"
	}
	if preMode == "luma_bands" {
		prep.Pixels = metric.LumaBands(prep.Pixels, prep.W, prep.H)
	}
	if *posterize >= 2 {
		prep.Pixels = metric.Posterize(prep.Pixels, prep.W, prep.H, *posterize)
	}

	// Saliency map: flat/line-art/cutout default to the richer WeightMapV2 (absolute,
	// 3x3-dilated, ink-aware) for crisp contours; smooth content keeps the Sobel WeightMap.
	// -weight-v2 forces the choice. Scale-invariant => no backend/CUDA change.
	useV2 := flatMode || cutout
	if userSet["weight-v2"] {
		useV2 = *weightV2
	}
	var weight []float32
	if *weighted {
		if useV2 {
			weight = metric.WeightMapV2(prep.Pixels, prep.W, prep.H)
		} else {
			weight = metric.WeightMap(prep.Pixels, prep.W, prep.H)
		}
	}
	// Weight-strength blend toward uniform (w'=(1-s)+s*w). The edge weight over-fits
	// contours; softening it improves both image-space SSE and SSIM (metric-probe). s=1
	// keeps legacy behavior, s=0 = uniform (== -weighted=false). Applies to both greedy and
	// polish (one weight buffer feeds the whole pipeline).
	if weight != nil && *weightStrength < 1 {
		s := float32(math.Max(0, math.Min(1, *weightStrength)))
		for i := range weight {
			weight[i] = (1 - s) + s*weight[i]
		}
	}
	// -linear PERCEPTUAL WEIGHT: composite in linear (correct, no in-game pop) but minimise a
	// PERCEPTUAL error, by weighting each pixel's linear-SSE by (d sRGB/d linear)². The sRGB EOTF
	// is steep in darks / flat in brights, so this up-weights shadow detail and down-weights bright
	// regions exactly as perception does — making the analytic weighted-mean optimal colour solve
	// for the sRGB-displayed result while the blend stays linear. Without it, plain linear-SSE
	// biases colours bright and smooths detail.
	if model.LinearLight {
		if weight == nil {
			weight = make([]float32, prep.W*prep.H)
			for i := range weight {
				weight[i] = 1
			}
		}
		wp := make([]float32, prep.W*prep.H)
		var sum float64
		for i := 0; i < prep.W*prep.H; i++ {
			y := 0.2126*prep.Pixels[i*4] + 0.7152*prep.Pixels[i*4+1] + 0.0722*prep.Pixels[i*4+2]
			if y < 0.02 {
				y = 0.02 // clamp the dark blow-up of the sRGB derivative
			}
			d := 0.4396 * math.Pow(float64(y), -0.5833) // d/dlin of 1.055*lin^(1/2.4)-0.055
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
	applog.Printf("content mode: %s (alpha=%v alphaMin=%.2f aspectMax=%.1f weightV2=%v wstr=%.2f preprocess=%s posterize=%d polish=%v | flat=%.2f ramp=%.2f edge=%.2f palette=%d)",
		resolvedMode, allowAlpha, alphaMin, aspectMax, *weighted && useV2, *weightStrength, preMode, *posterize, *polish, cs.FlatFrac, cs.RampFrac, cs.EdgeFrac, cs.Colors)

	be, beName, err := newBackend(prep.Pixels, weight, prep.W, prep.H, *gridSize)
	must(err)
	defer be.Close()
	if we, ok := be.(interface{ SetWarpEval(bool) }); ok {
		we.SetWarpEval(*warpEval)
	}
	applog.Printf("backend=%s", beName)
	if prep.HasTransparency {
		applog.Printf("transparent background detected — keeping background empty (cutout mode)")
	}

	// Comparison mode: render an external geometry JSON through our renderer and score it against
	// -input, then exit (compare another output 1:1 on the SAME renderer + metric).
	// Shape coords are absolute px at the JSON's resolution, so -max-res must match (we warn).
	if *scoreJSON != "" {
		scoreGeometryJSON(be, *scoreJSON, prep.Pixels, prep.W, prep.H, *preview)
		return
	}

	// POLISH-HARNESS mode: run only the gated polish on a saved greedy JSON (FIXED input),
	// so a polish change is perfectly isolated from the greedy basin. Uses the backend's
	// already-built target/weight (same content-mode weight map as a full run).
	if *polishJSON != "" {
		polishGeometryJSON(be, *polishJSON, prep, engine.Options{
			Width: prep.W, Height: prep.H, Background: prep.Background, TransparentBG: prep.HasTransparency,
			RecolorVarSkip: *recolorVar,
			Polish:         true,
			PolishOpts:     polishOpts(*polishIters, *polishTau0, *polishTau1, *polishSTE, *polishEarly),
		}, *out, *preview, *ssaa)
		return
	}
	start := time.Now()
	if *gaussian {
		// NICHE Gaussian mode: bypass the greedy entirely (see engine.GenerateGaussian). -polish-iters
		// is the from-scratch training budget; 0 auto-scales to the glow count.
		gIters := *polishIters
		if gIters <= 0 {
			if gIters = 1000 + *shapes; gIters > 3000 {
				gIters = 3000
			}
		}
		res := engine.GenerateGaussian(be, engine.Options{
			Width: prep.W, Height: prep.H, Background: prep.Background,
			StopAt: *shapes, Seed: *seed, TransparentBG: prep.HasTransparency,
			Gaussian:   true,
			PolishOpts: polishOpts(gIters, *polishTau0, *polishTau1, false, *polishEarly),
		})
		applog.Printf("gaussian: %d glows, error %.1f -> %.1f in %.1fs",
			len(res.Shapes)-1, res.InitialError, res.FinalError, time.Since(start).Seconds())
		gOutW, gOutH := prep.W, prep.H
		if padPx > 0 {
			res.Shapes = imageio.TranslateShapes(res.Shapes, -float64(padPx), -float64(padPx))
			gOutW, gOutH = origW, origH
		}
		must(ensureDir(*out))
		must(imageio.WriteGeometry(*out, model.Geometry{Shapes: res.Shapes}))
		if *preview != "" {
			must(ensureDir(*preview))
			canvas := imageio.RenderFH6(res.Shapes, prep.HasTransparency, gOutW, gOutH, *ssaa)
			must(imageio.SavePreview(*preview, canvas, gOutW, gOutH))
		}
		applog.Printf("wrote %s", *out)
		return
	}
	o := engine.Options{
		Width: prep.W, Height: prep.H, Background: prep.Background,
		StopAt: *shapes, RandomSamples: *randomSamples, MutatedSamples: *mutated, Seed: *seed,
		Kinds:               presetpkg.ParseKinds(*kindsCSV),
		KindWeights:         kindWeights,
		TransparentBG:       prep.HasTransparency,
		Overdraw:            float32(*overdraw),
		AllowAlpha:          allowAlpha,
		AlphaMin:            alphaMin,
		AspectMax:           aspectMax,
		MaxNoImprove:        *maxNoImprove,
		ShapeKneeTol:        *shapeTol,
		RecolorVarSkip:      *recolorVar,
		SampleBudget:        *sampleBudget,
		DetailStrength:      float32(*detailStrength),
		DetailSamplingStart: float32(*detailStart),
		BoundaryRadius:      *boundary,
		BoundaryPadding:     float32(*boundaryPad),
		BoundaryStart:       float32(*boundaryStart),
		CanvasPad:           float32(*canvasPad),
		StandoutTol:         *standout,
		// Compact-shape bias is SSE-neutral on opaque content but mildly HURTS cutouts (it
		// early-stops short of the budget — forcing small shapes fights the large flat fills
		// a cutout's object needs). So apply it only to opaque images.
		CompactPenalty:    *compact && !cutout,
		OnDeviceSearch:    *gpuSearch,
		MomentSeed:        *momentSeed,
		MomentRefine:      *momentRefine,
		MomentSeeds:       *momentCenters,
		MomentDetailStart: float32(*momentDetailStart),
		CoarseSearch:      *coarseSearch,
		CoarseBudget:      *coarseBudget,
		CoarseK:           *coarseK,
		CoarseFP16:        *coarseFP16,
		Polish:            *polish,
		PolishOpts:        polishOpts(*polishIters, *polishTau0, *polishTau1, *polishSTE, *polishEarly),
		BackFit:           *backfit,
		BackFitPasses:     *backfitPasses,
		BackFitFrac:       *backfitFrac,
		Progress: func(n int, e float64) {
			if n%25 == 0 {
				applog.Printf("  progress: %d/%d shapes, error %.1f (%.1fs)", n, *shapes, e, time.Since(start).Seconds())
			}
		},
	}
	res := engine.Run(be, o)
	applog.Printf("done: %d shapes, error %.1f -> %.1f in %.1fs",
		len(res.Shapes)-1, res.InitialError, res.FinalError, time.Since(start).Seconds())
	logTimings(res.Timings)

	// Map a transparent-surround run back to the original size: the shapes are all inside the content
	// rectangle, so shifting them by -padPx yields a clean origin-0 reconstruction at the original dims.
	outW, outH := prep.W, prep.H
	if padPx > 0 {
		res.Shapes = imageio.TranslateShapes(res.Shapes, -float64(padPx), -float64(padPx))
		outW, outH = origW, origH
		applog.Printf("pad-transparent: mapped %d shapes back to %dx%d (un-padded, frame-free)", len(res.Shapes)-1, outW, outH)
	}

	must(ensureDir(*out))
	must(imageio.WriteGeometry(*out, model.Geometry{Shapes: res.Shapes}))
	if *preview != "" {
		must(ensureDir(*preview))
		// WYSIWYG preview: render the way the GAME composites — LINEAR light — so semi-transparent
		// shapes show their TRUE in-game appearance instead of an sRGB-blend preview that under-states
		// the "pop". For opaque content this equals a plain preview (no blending). ss>1 supersamples
		// for anti-aliased edges.
		canvas := imageio.RenderFH6(res.Shapes, prep.HasTransparency, outW, outH, *ssaa)
		must(imageio.SavePreview(*preview, canvas, outW, outH))
		applog.Printf("wrote WYSIWYG preview %s (ssaa=%d, linear-light composite)", *preview, *ssaa)
	}
	applog.Printf("wrote %s", *out)
}
