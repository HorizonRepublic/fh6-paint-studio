package engine

import (
	"math"
	"math/rand"
	"os"
	"strconv"

	"fh6-paint-studio/internal/model"
)

// kindGate is the region-gated kind selection (Options.RegionKinds): hard is metric.HardEdgeMap of
// the target (per-pixel [0,1] "hard-edged structure"). A candidate centred at a pixel draws from the
// full kind pool with probability hard[idx] and is forced to the smooth kind (ellipse) otherwise —
// so rect/triangle rims can only appear where the target itself has line-work/wedges, and smooth
// shading is built from ellipses. This is the GENERATION-side fix for the standout-rect complaint:
// the post-hoc repair passes (standout, soft-swap) measurably cap out at ~1% of shapes, while the
// kinds A/B showed the ellipse-only win/loss is decided by exactly this local structure split.
type kindGate struct {
	hard []float32
	w, h int
	// ramp (Options.RampGlow) is metric.RampMap of the target: per-pixel [0,1] smooth-gradient
	// strength. Where ramp[idx] > rampGlowThresh the deep-smooth glow swap runs at the HOTTER
	// (tau, prob) pair, so glows fill genuine gradient zones (bokeh/shading) far more aggressively
	// without touching structured content. nil = the plain global glow swap everywhere.
	ramp []float32
	// tau/prob are the deep-smooth glow-swap pair for THIS run, resolved from the preset
	// (Options.SmoothGlowTau/-Prob) so each content mode can gate the swap differently; the
	// package defaults below apply when a caller leaves them unset.
	tau, prob float32
	// bigTau/bigProb are the SIZE-conditioned glow swap (Options.BigGlowTau/-Prob): an ellipse
	// whose sqrt(rx*ry) exceeds bigTau*min(w,h) becomes a glow with probability bigProb, whatever
	// the local hardness. Independent of hard (a gate may be nil and this still applies): the
	// hardness gate asks whether the REGION has structure, while the artifact this catches is a
	// property of the SHAPE — past a certain size an ellipse rim stops being a local edge and
	// becomes a long closed contour the eye traces as an oval, in smooth and textured zones alike.
	bigTau, bigProb float32
	bigAllKinds     bool
	bigKind         model.ShapeKind // what the swap emits: KindGlow (soft splat) or KindDisk (opaque core + soft rim)
}

// FH6_BIGGLOW="tau,prob" pins the size-conditioned swap for lab A/Bs and, with prob 0, DISABLES it
// — the only way to A/B the baked default through the studio, which has no flags. Mirrors
// FH6_SMOOTHGLOW; the env override wins over the preset, since a run that sets it means to pin.
var bigGlowTauEnv, bigGlowProbEnv, bigGlowPinned = func() (float64, float64, bool) {
	if s := os.Getenv("FH6_BIGGLOW"); s != "" {
		var t, p float64
		if n, _ := fmtSscan(s, &t, &p); n == 2 {
			return t, p, true
		}
	}
	return 0, 0, false
}()

// resolveBigGlow returns the run's (tau, prob) for the size-conditioned swap: the pinned env
// override, else the preset's values.
func resolveBigGlow(opt Options) (float64, float64) {
	if bigGlowPinned {
		return bigGlowTauEnv, bigGlowProbEnv
	}
	return opt.BigGlowTau, opt.BigGlowProb
}

// bigGlowKind resolves the swap's target primitive. Glow is the default: its falloff reaches zero,
// so it has no rim at all. Disk keeps an opaque core out to ~0.4R and only feathers the edge, so it
// covers like the ellipse it replaces (cheaper in SSE) while still drawing no step.
func bigGlowKind(opt Options) model.ShapeKind {
	if opt.BigGlowDisk {
		return model.KindDisk
	}
	return model.KindGlow
}

// bigGlowSwap rewrites a candidate as a rimless glow when it is large enough for its rim to read as
// a contour and it loses the coin toss. Ellipse and rectangle share the glow's parameterisation
// (centre, half-extents, theta) so the swap is a kind rewrite; a triangle has none, so it is
// re-emitted as the glow inscribed in its vertex box (0.7 of the half-extents ≈ the covered area).
// Mirrors the device generators (fp_set_big_glow); allKinds gates the rect/triangle half.
func (g *kindGate) bigGlowSwap(r *rand.Rand, c *model.Candidate) {
	if g == nil || g.bigProb <= 0 || g.bigTau <= 0 {
		return
	}
	minDim := float32(g.w)
	if g.h < g.w {
		minDim = float32(g.h)
	}
	thr := g.bigTau * minDim
	size, tri := float32(0), false
	switch c.Kind {
	case model.KindEllipse:
	case model.KindRectangle, model.KindTriangle:
		if !g.bigAllKinds {
			return
		}
		tri = c.Kind == model.KindTriangle
	default:
		return
	}
	var gcx, gcy, grx, gry float32
	if tri {
		x0, x1 := minF(minF(c.P[0], c.P[2]), c.P[4]), maxF(maxF(c.P[0], c.P[2]), c.P[4])
		y0, y1 := minF(minF(c.P[1], c.P[3]), c.P[5]), maxF(maxF(c.P[1], c.P[3]), c.P[5])
		grx, gry = maxF(2, 0.35*(x1-x0)), maxF(2, 0.35*(y1-y0))
		gcx, gcy = (c.P[0]+c.P[2]+c.P[4])/3, (c.P[1]+c.P[3]+c.P[5])/3
		size = float32(math.Sqrt(float64(grx * gry)))
	} else {
		size = float32(math.Sqrt(float64(c.P[2] * c.P[3])))
	}
	if size <= thr || r.Float32() >= g.bigProb {
		return
	}
	c.Kind = g.bigKind
	if c.Kind == 0 {
		c.Kind = model.KindGlow
	}
	if tri {
		c.P = [6]float32{gcx, gcy, grx, gry, 0, 0}
	}
}

// pick returns the kind for a candidate centred at (x, y).
func (g *kindGate) pick(r *rand.Rand, x, y float32, kinds []model.ShapeKind, cdf []float32) model.ShapeKind {
	if g == nil || g.hard == nil {
		return pickKind(r, kinds, cdf)
	}
	idx := int(y)*g.w + int(x)
	if idx < 0 || idx >= len(g.hard) {
		return pickKind(r, kinds, cdf)
	}
	if r.Float32() < g.hard[idx] {
		return pickKind(r, kinds, cdf)
	}
	// DEEP-smooth cells additionally swap a fraction of the forced ellipses for GLOWS: an ellipse
	// rim in a smooth zone is a luminance step the eye reads as patchwork even after the polish
	// terms press it; a glow's falloff reaches zero, so its rim does not exist by construction.
	// The old "gradient kinds in greedy" bust was about GLOBAL pool dilution (per-step myopia
	// everywhere); this is region-gated to cells with essentially no structure at all. In genuine
	// gradient zones (ramp[idx] > rampGlowThresh) the swap runs at the hotter (tau, prob) — mirrors
	// the device kernels' fp_set_ramp_glow branch; keeps the r.Float32() count identical.
	tau, prob := g.tau, g.prob
	if g.ramp != nil && idx < len(g.ramp) && g.ramp[idx] > rampGlowThresh {
		tau, prob = smoothGlowTauHot, smoothGlowProbHot
	}
	if g.hard[idx] < tau && r.Float32() < prob {
		return model.KindGlow
	}
	return model.KindEllipse
}

// smoothGlowTau/-Prob gate the glow swap to essentially-structureless cells. Defaults (0.1, 0.8)
// measured 2026-07-20 on img_10 @native: the grey-bokeh bg and dress patchwork visibly dissolve
// (eye-img10-glow.png) at metric parity, replicated on img_24 (all metrics better) and img_10
// photo; img_5 (bright, few deep-smooth cells) pays noise-level ΔE +0.06. NB the banding metric
// reads WORSE with glows (0.44→0.51) while the eye reads dramatically smoother — judge by eye.
// FH6_SMOOTHGLOW ("tau,prob") overrides for lab A/Bs; prob 0 disables. The env override wins over
// the preset — it is the lab knob, and a run that sets it means to pin the pair.
var smoothGlowTau, smoothGlowProb, smoothGlowPinned = func() (float32, float32, bool) {
	tau, prob := float32(0.1), float32(0.8)
	if s := os.Getenv("FH6_SMOOTHGLOW"); s != "" {
		var t, p float64
		if n, _ := fmtSscan(s, &t, &p); n == 2 {
			return float32(t), float32(p), true
		}
	}
	return tau, prob, false
}()

// resolveSmoothGlow picks the run's glow-swap pair: the pinned env override, else the preset's
// values, else the package defaults (a hand-built Options — debug tools, tests — leaves them 0).
func resolveSmoothGlow(optTau, optProb float64) (float32, float32) {
	if smoothGlowPinned {
		return smoothGlowTau, smoothGlowProb
	}
	tau, prob := smoothGlowTau, smoothGlowProb
	if optTau > 0 {
		tau = float32(optTau)
	}
	if optProb > 0 {
		prob = float32(optProb)
	}
	return tau, prob
}

// rampGlowThresh / smoothGlowTauHot / smoothGlowProbHot gate the RAMP-AWARE hotter glow swap
// (Options.RampGlow, kindGate.ramp = metric.RampMap): where a cell reads as a genuine smooth
// gradient (ramp > thresh) the glow swap runs at tau 0.30 / prob 0.90 instead of the global
// 0.10 / 0.80. BUST (opt-in only, not defaulted — measured 2026-07-20): the target was the
// img_10 win from a GLOBAL tau-raise (SSE −4.7%, bokeh smoother), which came from glow-swapping
// MODERATE-hardness cells (hard 0.1-0.3), but RampMap requires hard < 0.14 so ramp cells already
// glow at the global tau — the hot pair captures ~none of it (img_10 SSE +0.01% parity, img_24
// −0.44%, img_9 +0.13% — noise). NB RampMap values are small (img_10 max 0.42, mean 0.025, ~5%
// of pixels > 0.12); thresh 0.12 is where they live. FH6_RAMPGLOW ("thresh,tau,prob") tunes.
var rampGlowThresh, smoothGlowTauHot, smoothGlowProbHot = func() (float32, float32, float32) {
	thresh, tau, prob := float32(0.12), float32(0.30), float32(0.90)
	if s := os.Getenv("FH6_RAMPGLOW"); s != "" {
		var t, u, p float64
		if n, _ := fmtSscan3(s, &t, &u, &p); n == 3 {
			thresh, tau, prob = float32(t), float32(u), float32(p)
		}
	}
	return thresh, tau, prob
}()

// fmtSscan3 parses "a,b,c" into three float64s (returns the count parsed).
func fmtSscan3(s string, a, b, c *float64) (int, error) {
	i := -1
	for j := 0; j < len(s); j++ {
		if s[j] == ',' {
			i = j
			break
		}
	}
	if i < 0 {
		return 0, nil
	}
	va, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, err
	}
	n, err := fmtSscan(s[i+1:], b, c)
	if err != nil {
		return 1 + n, err
	}
	*a = va
	return 1 + n, nil
}

// fmtSscan parses "a,b" (strconv keeps the fmt import out of this hot file).
func fmtSscan(s string, a, b *float64) (int, error) {
	i := -1
	for j := 0; j < len(s); j++ {
		if s[j] == ',' {
			i = j
			break
		}
	}
	if i < 0 {
		return 0, nil
	}
	va, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, err
	}
	vb, err := strconv.ParseFloat(s[i+1:], 64)
	if err != nil {
		return 1, err
	}
	*a, *b = va, vb
	return 2, nil
}
