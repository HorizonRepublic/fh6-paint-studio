package engine

import (
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
	tau, prob := smoothGlowTau, smoothGlowProb
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
// FH6_SMOOTHGLOW ("tau,prob") overrides for lab A/Bs; prob 0 disables.
var smoothGlowTau, smoothGlowProb = func() (float32, float32) {
	tau, prob := float32(0.1), float32(0.8)
	if s := os.Getenv("FH6_SMOOTHGLOW"); s != "" {
		var t, p float64
		if n, _ := fmtSscan(s, &t, &p); n == 2 {
			tau, prob = float32(t), float32(p)
		}
	}
	return tau, prob
}()

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
