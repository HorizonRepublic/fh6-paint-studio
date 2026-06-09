package raster

import (
	"math"

	"fh6-paint-studio/internal/model"
)

// Native FH6 radial-gradient primitives — KindGlow (word 0xE4) and KindDisk (word 0xE2). Unlike the
// hard primitives (binary inside/outside fill), a gradient contributes a PER-PIXEL alpha given by a
// baked radial falloff. These falloff curves were measured live in the FH6 editor (2026-06-03):
// inject the primitive at known scale/colour on the dark editor background -> screenshot after the
// foreground repaint -> radial luma profile -> convert sRGB->linear (FH6 composites in linear light) ->
// solve the per-radius alpha. The fits below reproduce that measurement; refine if richer data lands.
//
// t is the ELLIPTICAL normalised radius: t=0 at the centre, t=1 at the footprint edge (rx,ry are the
// alpha->0 radii). The effective coverage of a gradient shape is colour.A · falloff(t) (the editor's
// colour-alpha multiplies the mesh falloff — also measured: A=255->peak 0.89, A=128->peak 0.47).

// glowK and the normalisation reproduce the KindGlow profile: a truncated 2D gaussian that reaches 0
// at the footprint edge, with a measured peak of ~0.89 (a soft glow is never fully opaque even at its
// centre). exp(-glowK·t²) shifted/scaled so falloff(1)=0 and falloff(0)=glowPeak.
const (
	glowK    = 2.5
	glowPeak = 0.89
	diskCore = 0.40 // KindDisk is fully opaque (α=1) within this fraction of the footprint radius…
)

var (
	glowEdge = math.Exp(-glowK)     // gaussian value at t=1 (subtracted so the curve hits 0 at the edge)
	glowNorm = 1.0 / (1 - glowEdge) // renormalise so the shifted gaussian peaks at 1 before glowPeak
)

// FalloffGlow returns the soft-gaussian coverage (incl. the ~0.89 peak) at normalised radius t.
// t≤0 -> peak, t≥1 -> 0. This is the GaussianImage splat profile.
func FalloffGlow(t float64) float64 {
	if t <= 0 {
		return glowPeak
	}
	if t >= 1 {
		return 0
	}
	g := (math.Exp(-glowK*t*t) - glowEdge) * glowNorm
	return glowPeak * g
}

// FalloffDisk returns the feathered-disk coverage at normalised radius t: a fully opaque core out to
// diskCore, then a smoothstep rim falling to 0 at t=1. t≤0 -> 1, t≥1 -> 0.
func FalloffDisk(t float64) float64 {
	if t <= diskCore {
		return 1
	}
	if t >= 1 {
		return 0
	}
	u := (t - diskCore) / (1 - diskCore) // 0 at the core edge, 1 at the footprint edge
	return 1 - (3*u*u - 2*u*u*u)         // 1 - smoothstep(u)
}

// IsGradient reports whether a kind uses a per-pixel SOFT coverage (vs a binary fill): the radial
// gradients (KindGlow/KindDisk) and the captured mask words. Every soft-coverage call site keys off
// this (RenderFH6 isGrad, cpu evalGradient/applyGradient, polish coverage, engine opaqueShape). Mask
// geometry is still frozen — only KindGlow has a trainable analytic gradient (see optimizableGeo).
func IsGradient(kind model.ShapeKind) bool {
	return kind == model.KindGlow || kind == model.KindDisk || model.IsMask(kind)
}

// GaussianCovGrad returns the KindGlow coverage at pixel-centre (x+0.5,y+0.5) AND its analytic gradient
// wrt the 5 ellipse params [cx,cy,rx,ry,thetaDeg] (slot 5 = 0). The glow is a truncated 2D gaussian
// cov = glowPeak·glowNorm·(exp(-glowK·u) − glowEdge), with u = (xr/rx)²+(yr/ry)² the squared elliptical
// radius in the rotated frame — SMOOTH everywhere inside the footprint (unlike a hard SDF's discontinuous
// edge), which is what makes a gaussian splat trainable by gradient descent (the basis of GaussianImage).
// Outside the footprint (u≥1) coverage and gradient are 0. KindDisk has a flat opaque core (zero geometry
// gradient there), so it is returned coverage-only — glows are the trainable gaussian primitive.
func GaussianCovGrad(kind model.ShapeKind, p [6]float32, x, y int) (cov float64, g [6]float64) {
	if kind != model.KindGlow {
		return Coverage(kind, p, x, y), g // disk/other: coverage only, geometry frozen
	}
	cx, cy := float64(p[0]), float64(p[1])
	rx, ry := math.Max(1, float64(p[2])), math.Max(1, float64(p[3]))
	th := float64(p[4]) * deg2rad
	c, s := math.Cos(th), math.Sin(th)
	dx := float64(x) + 0.5 - cx
	dy := float64(y) + 0.5 - cy
	xr := dx*c + dy*s
	yr := -dx*s + dy*c
	u := xr*xr/(rx*rx) + yr*yr/(ry*ry)
	if u >= 1 {
		return 0, g
	}
	cov = glowPeak * glowNorm * (math.Exp(-glowK*u) - glowEdge)
	dcov := glowPeak * glowNorm * (-glowK * math.Exp(-glowK*u)) // dcov/du
	dudxr := 2 * xr / (rx * rx)
	dudyr := 2 * yr / (ry * ry)
	g[0] = dcov * (dudxr*(-c) + dudyr*(s))  // d/dcx
	g[1] = dcov * (dudxr*(-s) + dudyr*(-c)) // d/dcy
	if float64(p[2]) > 1 {                  // respect the max(1,·) clamp: no gradient below the floor
		g[2] = dcov * (-2 * xr * xr / (rx * rx * rx)) // d/drx
	}
	if float64(p[3]) > 1 {
		g[3] = dcov * (-2 * yr * yr / (ry * ry * ry)) // d/dry
	}
	g[4] = dcov * (2 * deg2rad * xr * yr * (1/(rx*rx) - 1/(ry*ry))) // d/dthetaDeg
	return cov, g
}

// ellipseNormRadius returns the elliptical normalised radius t of pixel-centre (x+0.5,y+0.5) in the
// shape's rotated frame: t=1 on the ellipse boundary. P = [cx, cy, rx, ry, thetaDeg, _].
func ellipseNormRadius(p [6]float32, x, y int) float64 {
	cx, cy := float64(p[0]), float64(p[1])
	rx, ry := math.Max(1, float64(p[2])), math.Max(1, float64(p[3]))
	t := float64(p[4]) * deg2rad
	c, s := math.Cos(t), math.Sin(t)
	dx := float64(x) + 0.5 - cx
	dy := float64(y) + 0.5 - cy
	xr := dx*c + dy*s
	yr := -dx*s + dy*c
	return math.Sqrt(xr*xr/(rx*rx) + yr*yr/(ry*ry))
}

// Coverage returns the per-pixel coverage (0..1) of a shape at pixel (x,y): the baked radial falloff
// for gradient kinds, or a binary 1/0 fill for the hard kinds. Multiply by colour.A for the effective
// composited alpha. This is the single source of truth mirrored by the CUDA eval kernel and RenderFH6.
func Coverage(kind model.ShapeKind, p [6]float32, x, y int) float64 {
	switch kind {
	case model.KindGlow:
		return FalloffGlow(ellipseNormRadius(p, x, y))
	case model.KindDisk:
		return FalloffDisk(ellipseNormRadius(p, x, y))
	default:
		if model.IsMask(kind) {
			if m := maskByKind(kind); m != nil {
				return maskCoverage(m, p, x, y)
			}
			return 0
		}
		if Inside(kind, p, x, y) {
			return 1
		}
		return 0
	}
}
