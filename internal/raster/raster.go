package raster

import (
	"math"

	"fh6-paint-studio/internal/model"
)

const deg2rad = math.Pi / 180 // degrees -> radians conversion factor

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// EllipseBBox returns the clamped integer bounding box of a rotated ellipse.
// P = [cx, cy, rx, ry, thetaDeg, _].
func EllipseBBox(p [6]float32, w, h int) (xMin, yMin, xMax, yMax int) {
	cx, cy := float64(p[0]), float64(p[1])
	rx, ry := math.Max(1, float64(p[2])), math.Max(1, float64(p[3]))
	t := float64(p[4]) * deg2rad
	c, s := math.Cos(t), math.Sin(t)
	ex := math.Sqrt(rx*rx*c*c + ry*ry*s*s)
	ey := math.Sqrt(rx*rx*s*s + ry*ry*c*c)
	xMin = clampI(int(math.Floor(cx-ex-1)), 0, w-1)
	xMax = clampI(int(math.Ceil(cx+ex+1)), 0, w-1)
	yMin = clampI(int(math.Floor(cy-ey-1)), 0, h-1)
	yMax = clampI(int(math.Ceil(cy+ey+1)), 0, h-1)
	return
}

// EllipseInside reports whether pixel-center (x+0.5,y+0.5) lies within the ellipse.
func EllipseInside(p [6]float32, x, y int) bool {
	cx, cy := float64(p[0]), float64(p[1])
	rx, ry := math.Max(1, float64(p[2])), math.Max(1, float64(p[3]))
	t := float64(p[4]) * deg2rad
	c, s := math.Cos(t), math.Sin(t)
	dx := float64(x) + 0.5 - cx
	dy := float64(y) + 0.5 - cy
	xr := dx*c + dy*s
	yr := -dx*s + dy*c
	return xr*xr/(rx*rx)+yr*yr/(ry*ry) <= 1.0
}

// Inside dispatches the point-in-shape test by kind (pixel-center x+0.5,y+0.5).
func Inside(kind model.ShapeKind, p [6]float32, x, y int) bool {
	switch kind {
	case model.KindRectangle:
		return RectInside(p, x, y)
	case model.KindTriangle:
		return TriangleInside(p, x, y)
	case model.KindLine:
		return LineInside(p, x, y)
	default:
		if model.IsMask(kind) {
			// Soft coverage has no binary edge; treat ≥half-covered pixels as inside (membership only —
			// the real per-pixel alpha is in Coverage).
			if m := maskByKind(kind); m != nil {
				return maskCoverage(m, p, x, y) >= 0.5
			}
			return false
		}
		// Ellipse + the radial gradients (KindGlow/KindDisk) share the elliptical footprint; their
		// per-pixel falloff is in Coverage, but Inside (footprint membership) is the ellipse test.
		return EllipseInside(p, x, y)
	}
}

// BBox dispatches the clamped integer bounding box by kind.
func BBox(kind model.ShapeKind, p [6]float32, w, h int) (xMin, yMin, xMax, yMax int) {
	switch kind {
	case model.KindRectangle:
		return RectBBox(p, w, h)
	case model.KindTriangle:
		return TriangleBBox(p, w, h)
	case model.KindLine:
		return LineBBox(p, w, h)
	default:
		if model.IsMask(kind) {
			return maskBBox(p, w, h)
		}
		return EllipseBBox(p, w, h)
	}
}

// --- Rotated rectangle: P = [cx, cy, halfW, halfH, thetaDeg, _] ---

// RectBBox returns the clamped integer bounding box of a rotated rectangle.
func RectBBox(p [6]float32, w, h int) (xMin, yMin, xMax, yMax int) {
	cx, cy := float64(p[0]), float64(p[1])
	hw, hh := math.Max(0.5, float64(p[2])), math.Max(0.5, float64(p[3]))
	t := float64(p[4]) * deg2rad
	c, s := math.Cos(t), math.Sin(t)
	ex := math.Abs(hw*c) + math.Abs(hh*s)
	ey := math.Abs(hw*s) + math.Abs(hh*c)
	xMin = clampI(int(math.Floor(cx-ex-1)), 0, w-1)
	xMax = clampI(int(math.Ceil(cx+ex+1)), 0, w-1)
	yMin = clampI(int(math.Floor(cy-ey-1)), 0, h-1)
	yMax = clampI(int(math.Ceil(cy+ey+1)), 0, h-1)
	return
}

// RectInside reports whether the pixel-center lies within the rotated rectangle.
func RectInside(p [6]float32, x, y int) bool {
	cx, cy := float64(p[0]), float64(p[1])
	hw, hh := math.Max(0.5, float64(p[2])), math.Max(0.5, float64(p[3]))
	t := float64(p[4]) * deg2rad
	c, s := math.Cos(t), math.Sin(t)
	dx := float64(x) + 0.5 - cx
	dy := float64(y) + 0.5 - cy
	xr := dx*c + dy*s
	yr := -dx*s + dy*c
	return math.Abs(xr) <= hw && math.Abs(yr) <= hh
}

// --- Triangle: P = [x1, y1, x2, y2, x3, y3] ---

func triSign(ax, ay, bx, by, cx, cy float64) float64 {
	return (ax-cx)*(by-cy) - (bx-cx)*(ay-cy)
}

// TriangleBBox returns the clamped integer bounding box of a triangle.
func TriangleBBox(p [6]float32, w, h int) (xMin, yMin, xMax, yMax int) {
	x1, y1, x2, y2, x3, y3 := float64(p[0]), float64(p[1]), float64(p[2]), float64(p[3]), float64(p[4]), float64(p[5])
	minX := math.Min(x1, math.Min(x2, x3))
	maxX := math.Max(x1, math.Max(x2, x3))
	minY := math.Min(y1, math.Min(y2, y3))
	maxY := math.Max(y1, math.Max(y2, y3))
	xMin = clampI(int(math.Floor(minX)), 0, w-1)
	xMax = clampI(int(math.Ceil(maxX)), 0, w-1)
	yMin = clampI(int(math.Floor(minY)), 0, h-1)
	yMax = clampI(int(math.Ceil(maxY)), 0, h-1)
	return
}

// TriangleInside reports whether the pixel-center lies within the triangle
// (consistent winding via the half-plane sign test).
func TriangleInside(p [6]float32, x, y int) bool {
	px, py := float64(x)+0.5, float64(y)+0.5
	x1, y1, x2, y2, x3, y3 := float64(p[0]), float64(p[1]), float64(p[2]), float64(p[3]), float64(p[4]), float64(p[5])
	d1 := triSign(px, py, x1, y1, x2, y2)
	d2 := triSign(px, py, x2, y2, x3, y3)
	d3 := triSign(px, py, x3, y3, x1, y1)
	hasNeg := d1 < 0 || d2 < 0 || d3 < 0
	hasPos := d1 > 0 || d2 > 0 || d3 > 0
	return !(hasNeg && hasPos)
}

// --- Line / capsule: P = [x1, y1, x2, y2, halfWidth, _] ---

// LineBBox returns the clamped integer bounding box of a capsule (thick segment).
func LineBBox(p [6]float32, w, h int) (xMin, yMin, xMax, yMax int) {
	x1, y1, x2, y2 := float64(p[0]), float64(p[1]), float64(p[2]), float64(p[3])
	hwid := math.Max(0.5, float64(p[4]))
	xMin = clampI(int(math.Floor(math.Min(x1, x2)-hwid)), 0, w-1)
	xMax = clampI(int(math.Ceil(math.Max(x1, x2)+hwid)), 0, w-1)
	yMin = clampI(int(math.Floor(math.Min(y1, y2)-hwid)), 0, h-1)
	yMax = clampI(int(math.Ceil(math.Max(y1, y2)+hwid)), 0, h-1)
	return
}

// LineInside reports whether the pixel-center is within halfWidth of the segment.
func LineInside(p [6]float32, x, y int) bool {
	x1, y1, x2, y2 := float64(p[0]), float64(p[1]), float64(p[2]), float64(p[3])
	hwid := math.Max(0.5, float64(p[4]))
	px, py := float64(x)+0.5, float64(y)+0.5
	dx, dy := x2-x1, y2-y1
	l2 := dx*dx + dy*dy
	var t float64
	if l2 > 0 {
		t = ((px-x1)*dx + (py-y1)*dy) / l2
		if t < 0 {
			t = 0
		}
		if t > 1 {
			t = 1
		}
	}
	projX, projY := x1+t*dx, y1+t*dy
	ddx, ddy := px-projX, py-projY
	return ddx*ddx+ddy*ddy <= hwid*hwid
}
