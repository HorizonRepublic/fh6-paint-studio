package engine

import (
	"math"
	"math/rand"
	"os"

	"fh6-paint-studio/internal/model"
)

// gridAnisoOn maps the error grid's cells to pixels through the covariance. The grid is
// gridSize×gridSize whatever the target's aspect (Vulkan.ErrorGrid), so on a 2000×1000
// image its cells are 2:1 and the isotropic remap below it ships every moment seed at the
// wrong aspect and the wrong angle — a 45° blob in grid space at 26.6° on the image.
//
// OFF by default because it MEASURED AS NOISE: n=27 at 1000 shapes / 2000px on a bank
// whose aspects run 1.08-1.78, mean +0.329%, 11/27 better, sd 2.853%, sign test p=0.442.
// The seed is refined downstream (device hill-climb, then geometry refine), and that
// absorbs the seed's aspect and angle error — so the correct fit buys nothing and moving
// the shipped output for it would only cost a baseline. FH6_GRIDASPECT=1 turns it on;
// the shim reads the same pin (ip[8]), and compseed.go shares it.
var gridAnisoOn = os.Getenv("FH6_GRIDASPECT") == "1"

// gridCellScale splits the cell -> pixel scale into the part applied to the COVARIANCE
// (pre) and the part applied to the fitted AXES (post). The pin path keeps the whole
// scale in post, which is where it used to be, so it stays bit-identical; multiplying a
// covariance by exactly 1 is exact, so the fix costs nothing when it is off.
func gridCellScale(sx, sy float32) (preX, preY, postX, postY float32) {
	if gridAnisoOn {
		return sx, sy, 1, 1
	}
	s := (sx + sy) / 2
	return 1, 1, s, s
}

// momentSemiAxis maps sqrt(eigenvalue) of the residual covariance to an ellipse
// semi-axis. For a UNIFORM filled ellipse of semi-axes (A,B), the second central
// moment along an axis equals A²/4, so sqrt(λ)=A/2 and 2·sqrt(λ)=A recovers the
// axis exactly — hence the factor 2.
const momentSemiAxis = 2.0

// momentEllipse fits the covariance (second-moment) ellipse of a weighted residual
// blob in closed form. w is a row-major NON-NEGATIVE weight field of size fw×fh
// (residual magnitude per cell). It returns the blob's centroid, the two semi-axes
// (rx = MAJOR, lying along thetaDeg; ry = minor) and the major-axis rotation in
// degrees, in the SAME coordinate space as w (the caller maps cell->pixel). ok=false
// when the total weight is ~0 (nothing to fit).
//
// This is the maximum-likelihood ellipse for the blob: the random candidate search is,
// in effect, brute-forcing toward exactly this shape, so it can be computed directly as
// a near-optimal seed instead of searched over tens of thousands of random candidates.
func momentEllipse(w []float32, fw, fh int) (cx, cy, rx, ry, thetaDeg float32, ok bool) {
	return momentEllipseAniso(w, fw, fh, 1, 1)
}

// momentEllipseAniso is momentEllipse for a window whose cell is sx×sy pixels: the
// centroid comes back in CELL coordinates (the caller owns the window offset) but the
// axes and the orientation come back in PIXEL space. Scaling the axes by an average of
// sx,sy is not the same fit — the covariance itself has to be mapped, S·Σ·Sᵀ, and
// re-decomposed, or an anisotropic cell reports both the wrong aspect and the wrong
// angle (a 45° blob in a 2:1 grid is 26.6° on the image).
func momentEllipseAniso(w []float32, fw, fh int, sx, sy float64) (cx, cy, rx, ry, thetaDeg float32, ok bool) {
	var m00, m10, m01 float64
	for y := 0; y < fh; y++ {
		row := y * fw
		for x := 0; x < fw; x++ {
			wv := float64(w[row+x])
			if wv <= 0 {
				continue
			}
			m00 += wv
			m10 += float64(x) * wv
			m01 += float64(y) * wv
		}
	}
	if m00 <= 1e-9 {
		return 0, 0, 0, 0, 0, false
	}
	mcx := m10 / m00
	mcy := m01 / m00

	// Second central moments (covariance of the weighted blob).
	var u20, u02, u11 float64
	for y := 0; y < fh; y++ {
		row := y * fw
		dy := float64(y) - mcy
		for x := 0; x < fw; x++ {
			wv := float64(w[row+x])
			if wv <= 0 {
				continue
			}
			dx := float64(x) - mcx
			u20 += wv * dx * dx
			u02 += wv * dy * dy
			u11 += wv * dx * dy
		}
	}
	u20 /= m00
	u02 /= m00
	u11 /= m00

	// Cell -> pixel: Σ_px = S·Σ_cell·Sᵀ with S = diag(sx,sy).
	u20 *= sx * sx
	u02 *= sy * sy
	u11 *= sx * sy

	// Eigenvalues of the symmetric covariance [[u20,u11],[u11,u02]].
	half := (u20 - u02) / 2
	d := math.Sqrt(math.Max(0, half*half+u11*u11))
	lmax := (u20+u02)/2 + d
	lmin := (u20+u02)/2 - d
	if lmin < 0 {
		lmin = 0
	}
	major := momentSemiAxis * math.Sqrt(lmax)
	minor := momentSemiAxis * math.Sqrt(lmin)
	// Orientation of the major eigenvector. The 0.5·atan2 form yields θ∈(-90,90];
	// a horizontal blob -> 0, vertical -> 90, +45 diagonal -> 45.
	theta := 0.5 * math.Atan2(2*u11, u20-u02) * 180 / math.Pi

	return float32(mcx), float32(mcy),
		float32(math.Max(major, 0.5)), float32(math.Max(minor, 0.5)),
		float32(theta), true
}

// momentSeedFromGrid fits the residual-covariance ellipse in a window of the coarse
// error grid around pixel (px,py) and returns a near-optimal ellipse seed in PIXEL
// coordinates. The window half-size follows radiusPx (≈ the anneal max radius) so big
// early shapes fit a wide blob and late detail a tight one. ok=false when the window
// carries ~no error. The grid is gridSize×gridSize whatever the image aspect, so its
// cells are as anisotropic as the target and the covariance is mapped to pixels before
// it is decomposed.
func momentSeedFromGrid(grid []float32, gw, gh, imgW, imgH int, px, py, radiusPx float32) (cx, cy, rx, ry, theta float32, ok bool) {
	if gw <= 0 || gh <= 0 || len(grid) < gw*gh {
		return 0, 0, 0, 0, 0, false
	}
	sx := float32(imgW) / float32(gw)
	sy := float32(imgH) / float32(gh)
	gcx := int(px / sx)
	gcy := int(py / sy)
	rcx := int(radiusPx/sx) + 1
	rcy := int(radiusPx/sy) + 1
	gx0, gy0 := gcx-rcx, gcy-rcy
	gx1, gy1 := gcx+rcx, gcy+rcy
	if gx0 < 0 {
		gx0 = 0
	}
	if gy0 < 0 {
		gy0 = 0
	}
	if gx1 > gw-1 {
		gx1 = gw - 1
	}
	if gy1 > gh-1 {
		gy1 = gh - 1
	}
	ww, wh := gx1-gx0+1, gy1-gy0+1
	if ww < 1 || wh < 1 {
		return 0, 0, 0, 0, 0, false
	}
	win := make([]float32, ww*wh)
	for y := 0; y < wh; y++ {
		src := (gy0+y)*gw + gx0
		dst := y * ww
		for x := 0; x < ww; x++ {
			v := grid[src+x]
			if v < 0 {
				v = 0
			}
			win[dst+x] = v
		}
	}
	px, py, qx, qy := gridCellScale(sx, sy)
	lcx, lcy, lrx, lry, lth, fok := momentEllipseAniso(win, ww, wh, float64(px), float64(py))
	if !fok {
		return 0, 0, 0, 0, 0, false
	}
	return (float32(gx0) + lcx + 0.5) * sx, (float32(gy0) + lcy + 0.5) * sy,
		lrx * qx, lry * qy, lth, true
}

// momentPool builds the small candidate pool for one greedy step from the moment seed:
// the exact covariance ellipse (candidate 0) plus count-1 kind-weighted candidates
// LOCALISED to the seed's centre, orientation, size and aspect — replacing the blind
// error-sampled random batch of tens of thousands. Far fewer candidates suffice because
// the seed already localises the search to the residual blob instead of brute-forcing
// toward it; the hill-climb mutate then refines the winner. maxR caps the seed/locals.
func momentPool(rng *rand.Rand, cx, cy, rx, ry, theta, maxR float32, kinds []model.ShapeKind,
	kindCDF []float32, count int, w, h float32, allowAlpha bool, alphaMin float32, kg *kindGate) []model.Candidate {
	if count < 1 {
		count = 1
	}
	major, minor := maxF(rx, ry), minF(rx, ry)
	aspect := float32(1)
	if minor > 0.5 {
		aspect = clampF(major/minor, 1, 8)
	}
	localR := minF(maxR, maxF(4, major*1.4))
	out := make([]model.Candidate, 0, count)
	// candidate 0: the exact moment ellipse (opaque covers the blob; the pool and the
	// hill-climb mutate explore transparency around it).
	seed := model.Candidate{Kind: model.KindEllipse, Color: model.RGBA{A: 1}}
	seed.P = [6]float32{cx, cy, clampF(rx, 2, maxR), clampF(ry, 2, maxR), theta, 0}
	out = append(out, seed)
	// Direct gradient seed: the moment fit IS a 2D gaussian (centroid + covariance = exactly a
	// KindGlow's centre/axes), so when the soft-glow kind is enabled, seed it at the fitted ellipse —
	// the natural moment->GaussianImage hand-off. The hill-climb then refines it like any candidate.
	if containsKind(kinds, model.KindGlow) {
		g := seed
		g.Kind = model.KindGlow
		out = append(out, g)
	}
	jit := localR * 0.5 // explore a neighbourhood of the seed centre, not a single point
	for i := 1; i < count; i++ {
		alpha := float32(1)
		if allowAlpha {
			alpha = randRange(rng, alphaMin, 1)
		}
		th := theta + randRange(rng, -15, 15)
		jx := clampF(cx+randRange(rng, -jit, jit), 0, w-1)
		jy := clampF(cy+randRange(rng, -jit, jit), 0, h-1)
		c := randomShapeOfKind(rng, kg.pick(rng, jx, jy, kinds, kindCDF), jx, jy, localR, w, h, th, alpha, aspect)
		kg.bigGlowSwap(rng, &c)
		out = append(out, c)
	}
	return out
}
