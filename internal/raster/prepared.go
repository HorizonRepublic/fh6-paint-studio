package raster

import (
	"math"

	"fh6-paint-studio/internal/model"
)

// Prepared hoists the per-shape constants that Coverage and Inside otherwise recompute at every
// pixel — the rotation's sine and cosine above all. The host passes that dominate CPU time (the
// stack solve, the LOO refit) evaluate one shape across thousands of pixels, where a sincos per
// pixel is most of the work. The per-pixel expressions below are the same operations in the same
// order as the Coverage/Inside path, so the values are identical, not merely close.
type Prepared struct {
	kind   model.ShapeKind
	cx, cy float64
	rx, ry float64 // ellipse radii, or rect half-extents (clamped exactly as the plain path does)
	rx2    float64
	ry2    float64
	c, s   float64
	skew   float64
	mask   *maskTex
	// triangle vertices / line endpoints, kept in float64
	x1, y1, x2, y2, x3, y3 float64
	hwid                   float64
	l2                     float64
}

// Prep builds the per-pixel-invariant part of a shape. Unknown mask words yield a Prepared whose
// Coverage is 0 everywhere, matching Coverage's behaviour for a missing bank word.
func Prep(kind model.ShapeKind, p [6]float32) Prepared {
	pr := Prepared{kind: kind}
	switch kind {
	case model.KindTriangle:
		pr.x1, pr.y1 = float64(p[0]), float64(p[1])
		pr.x2, pr.y2 = float64(p[2]), float64(p[3])
		pr.x3, pr.y3 = float64(p[4]), float64(p[5])
	case model.KindLine:
		pr.x1, pr.y1 = float64(p[0]), float64(p[1])
		pr.x2, pr.y2 = float64(p[2]), float64(p[3])
		pr.hwid = math.Max(0.5, float64(p[4]))
		dx, dy := pr.x2-pr.x1, pr.y2-pr.y1
		pr.l2 = dx*dx + dy*dy
	case model.KindRectangle:
		pr.cx, pr.cy = float64(p[0]), float64(p[1])
		pr.rx, pr.ry = math.Max(0.5, float64(p[2])), math.Max(0.5, float64(p[3]))
		t := float64(p[4]) * deg2rad
		pr.c, pr.s = math.Cos(t), math.Sin(t)
		pr.skew = float64(p[5]) // editor-set shear; 0 for every generated shape
	default:
		if model.IsMask(kind) {
			pr.cx, pr.cy = float64(p[0]), float64(p[1])
			pr.rx, pr.ry = float64(p[2]), float64(p[3])
			t := float64(p[4]) * deg2rad
			pr.c, pr.s = math.Cos(t), math.Sin(t)
			pr.skew = float64(p[5])
			pr.mask = maskByKind(kind)
			break
		}
		pr.cx, pr.cy = float64(p[0]), float64(p[1])
		pr.rx, pr.ry = math.Max(1, float64(p[2])), math.Max(1, float64(p[3]))
		pr.rx2, pr.ry2 = pr.rx*pr.rx, pr.ry*pr.ry
		t := float64(p[4]) * deg2rad
		pr.c, pr.s = math.Cos(t), math.Sin(t)
		pr.skew = float64(p[5]) // editor-set shear for the ellipse + radial kinds; 0 when generated
	}
	return pr
}

// localS rotates the pixel centre into the shape frame and applies the inverse horizontal shear —
// the SAME K^{-1} the mask path uses (sx = kx - skew*ky). With skew 0 it is exactly local(), so every
// generated shape is bit-identical.
func (pr *Prepared) localS(x, y int) (float64, float64) {
	xr, yr := pr.local(x, y)
	return xr - pr.skew*yr, yr
}

// local rotates the pixel centre into the shape's frame.
func (pr *Prepared) local(x, y int) (float64, float64) {
	dx := float64(x) + 0.5 - pr.cx
	dy := float64(y) + 0.5 - pr.cy
	return dx*pr.c + dy*pr.s, -dx*pr.s + dy*pr.c
}

// Coverage is the prepared-shape equivalent of Coverage(kind, p, x, y).
func (pr *Prepared) Coverage(x, y int) float64 {
	switch pr.kind {
	case model.KindGlow:
		return FalloffGlow(pr.normRadius(x, y))
	case model.KindDisk:
		return FalloffDisk(pr.normRadius(x, y))
	default:
		if model.IsMask(pr.kind) {
			if pr.mask == nil || pr.rx == 0 || pr.ry == 0 {
				return 0
			}
			kx, ky := pr.local(x, y)
			sx := kx - pr.skew*ky
			return pr.mask.sampleUV(sx/pr.rx+0.5, ky/pr.ry+0.5)
		}
		if pr.Inside(x, y) {
			return 1
		}
		return 0
	}
}

// Inside is the prepared-shape equivalent of Inside(kind, p, x, y).
func (pr *Prepared) Inside(x, y int) bool {
	switch pr.kind {
	case model.KindRectangle:
		xr, yr := pr.localS(x, y)
		return math.Abs(xr) <= pr.rx && math.Abs(yr) <= pr.ry
	case model.KindTriangle:
		px, py := float64(x)+0.5, float64(y)+0.5
		d1 := triSign(px, py, pr.x1, pr.y1, pr.x2, pr.y2)
		d2 := triSign(px, py, pr.x2, pr.y2, pr.x3, pr.y3)
		d3 := triSign(px, py, pr.x3, pr.y3, pr.x1, pr.y1)
		hasNeg := d1 < 0 || d2 < 0 || d3 < 0
		hasPos := d1 > 0 || d2 > 0 || d3 > 0
		return !(hasNeg && hasPos)
	case model.KindLine:
		px, py := float64(x)+0.5, float64(y)+0.5
		dx, dy := pr.x2-pr.x1, pr.y2-pr.y1
		var t float64
		if pr.l2 > 0 {
			t = ((px-pr.x1)*dx + (py-pr.y1)*dy) / pr.l2
			if t < 0 {
				t = 0
			}
			if t > 1 {
				t = 1
			}
		}
		ddx, ddy := px-(pr.x1+t*dx), py-(pr.y1+t*dy)
		return ddx*ddx+ddy*ddy <= pr.hwid*pr.hwid
	default:
		if model.IsMask(pr.kind) {
			return pr.Coverage(x, y) >= 0.5
		}
		xr, yr := pr.localS(x, y)
		return xr*xr/pr.rx2+yr*yr/pr.ry2 <= 1.0
	}
}

// normRadius mirrors ellipseNormRadius for the gradient kinds.
func (pr *Prepared) normRadius(x, y int) float64 {
	xr, yr := pr.localS(x, y)
	return math.Sqrt(xr*xr/pr.rx2 + yr*yr/pr.ry2)
}
