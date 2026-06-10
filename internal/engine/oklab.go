package engine

import "math"

// oklab.go — perceptual OKLab colour distance for the polish loss (PolishOptions.OKLab, default
// off; CLI -polish-oklab). Linear-RGB SSE under-prices hue/chroma errors, so a large shape with a
// slightly wrong colour in a smooth gradient survives the optimisation — the "standout" failure.
// OKLab (Ottosson 2020) makes the same mistake cost what the eye charges for it: a cube-root LMS
// transform, then Euclidean distance ≈ perceptual difference. The transform below maps from the
// LINEAR working space (model.LinearLight default); greedy scoring stays on plain SSE — only the
// polish loss/gradient changes, so candidate ranking is untouched (basin-shift guard).
//
// Loss per pixel: w·(ΔL² + Δa² + Δb² + Δα²). Alpha keeps the linear diff (OKLab has no alpha).
// Gradient: dLoss/d(rgb) = 2w·Jᵀ·ΔLab with J = M2·diag(cbrt′(lms))·M1, computed analytically.

// okLabEps floors the LMS channels before the cube root: cbrt′(x) = 1/(3·cbrt(x)²) diverges at
// black, and the optimiser only needs a finite (clamped) slope there.
const okLabEps = 1e-7

// linearToOKLab converts one linear-sRGB pixel to OKLab.
func linearToOKLab(r, g, b float64) (L, A, B float64) {
	l := 0.4122214708*r + 0.5363325363*g + 0.0514459929*b
	m := 0.2119034982*r + 0.6806995451*g + 0.1073969566*b
	s := 0.0883024619*r + 0.2817188376*g + 0.6299787005*b
	lc, mc, sc := math.Cbrt(l), math.Cbrt(m), math.Cbrt(s)
	L = 0.2104542553*lc + 0.7936177850*mc - 0.0040720468*sc
	A = 1.9779984951*lc - 2.4285922050*mc + 0.4505937099*sc
	B = 0.0259040371*lc + 0.7827717662*mc - 0.8086757660*sc
	return
}

// okLabPixelLoss returns w·(ΔL²+Δa²+Δb²+Δα²) for one render/target pixel pair.
func okLabPixelLoss(rr, rg, rb, ra, tr, tg, tb, ta, wt float64) float64 {
	rL, rA, rB := linearToOKLab(rr, rg, rb)
	tL, tA, tB := linearToOKLab(tr, tg, tb)
	dL, dA, dB, dAl := rL-tL, rA-tA, rB-tB, ra-ta
	return wt * (dL*dL + dA*dA + dB*dB + dAl*dAl)
}

// okLabPixelDC returns dLoss/d(r,g,b,α) of okLabPixelLoss w.r.t. the RENDER pixel: the backward
// seed (dcinit) for the OKLab polish. 2w·Jᵀ·ΔLab for the colour channels, 2w·Δα for alpha.
func okLabPixelDC(rr, rg, rb, ra, tr, tg, tb, ta, wt float64) (dr, dg, db, da float64) {
	l := 0.4122214708*rr + 0.5363325363*rg + 0.0514459929*rb
	m := 0.2119034982*rr + 0.6806995451*rg + 0.1073969566*rb
	s := 0.0883024619*rr + 0.2817188376*rg + 0.6299787005*rb
	lc, mc, sc := math.Cbrt(l), math.Cbrt(m), math.Cbrt(s)
	rL := 0.2104542553*lc + 0.7936177850*mc - 0.0040720468*sc
	rA := 1.9779984951*lc - 2.4285922050*mc + 0.4505937099*sc
	rB := 0.0259040371*lc + 0.7827717662*mc - 0.8086757660*sc
	tL, tA, tB := linearToOKLab(tr, tg, tb)
	dL, dA, dB := 2*wt*(rL-tL), 2*wt*(rA-tA), 2*wt*(rB-tB)
	// M2ᵀ: dLoss/d(lc,mc,sc)
	gl := dL*0.2104542553 + dA*1.9779984951 + dB*0.0259040371
	gm := dL*0.7936177850 - dA*2.4285922050 + dB*0.7827717662
	gs := -dL*0.0040720468 + dA*0.4505937099 - dB*0.8086757660
	// cbrt′ with the black clamp (matches the device kernels).
	gl /= 3 * cbrtSq(l)
	gm /= 3 * cbrtSq(m)
	gs /= 3 * cbrtSq(s)
	// M1ᵀ: dLoss/d(r,g,b)
	dr = gl*0.4122214708 + gm*0.2119034982 + gs*0.0883024619
	dg = gl*0.5363325363 + gm*0.6806995451 + gs*0.2817188376
	db = gl*0.0514459929 + gm*0.1073969566 + gs*0.6299787005
	da = 2 * wt * (ra - ta)
	return
}

// cbrtSq returns cbrt(max(x,ε))² — the denominator of the clamped cube-root derivative.
func cbrtSq(x float64) float64 {
	if x < okLabEps {
		x = okLabEps
	}
	c := math.Cbrt(x)
	return c * c
}
