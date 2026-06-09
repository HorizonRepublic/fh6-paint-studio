package raster

import (
	"math"

	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"
)

// maskTexByKind resolves a mask ShapeKind to its coverage texture. Populated at init from the embedded
// bank (which has already registered each word with the model at KindMaskBase+i).
var maskTexByKind map[model.ShapeKind]*maskTex

func init() {
	maskTexByKind = make(map[model.ShapeKind]*maskTex, len(maskbank.All()))
	for _, e := range maskbank.All() {
		maskTexByKind[e.Kind] = &maskTex{w: e.W, h: e.H, cov: e.Cov}
	}
}

func maskByKind(kind model.ShapeKind) *maskTex { return maskTexByKind[kind] }

// maskTex is a coverage texture sampled in UV space [0,1]² (v=0 = top row, matching the captured
// PNG and the engine's y-down raster). cov is row-major w*h, values 0..1. It backs the KindMask
// primitive: an FH6 dictionary word's silhouette, calibrated live (see docs/research/lineart).
type maskTex struct {
	w, h int
	cov  []float32
}

// sampleUV returns the bilinearly-interpolated coverage at (u,v). Outside the unit square -> 0;
// inside, neighbours are clamped to the edge (the captured masks fade to ~0 at their own border).
func (m *maskTex) sampleUV(u, v float64) float64 {
	if u < 0 || u > 1 || v < 0 || v > 1 {
		return 0
	}
	tx := u*float64(m.w) - 0.5
	ty := v*float64(m.h) - 0.5
	x0 := int(math.Floor(tx))
	y0 := int(math.Floor(ty))
	fx := tx - float64(x0)
	fy := ty - float64(y0)
	x1 := clampI(x0+1, 0, m.w-1)
	y1 := clampI(y0+1, 0, m.h-1)
	x0 = clampI(x0, 0, m.w-1)
	y0 = clampI(y0, 0, m.h-1)
	c00 := float64(m.cov[y0*m.w+x0])
	c10 := float64(m.cov[y0*m.w+x1])
	c01 := float64(m.cov[y1*m.w+x0])
	c11 := float64(m.cov[y1*m.w+x1])
	return (1-fx)*(1-fy)*c00 + fx*(1-fy)*c10 + (1-fx)*fy*c01 + fx*fy*c11
}

// maskCoverage returns the KindMask coverage at pixel-centre (x+0.5,y+0.5) for the affine
// P = [cx, cy, Hx, Hy, rotDeg, skew], where Hx,Hy are the full screen extents in px. It inverts the
// forward placement screen = pos + R(rot)·K(skew)·diag(Hx,Hy)·n (n∈[-0.5,0.5]²) to recover the
// native UV, then bilinearly samples the mask. Anisotropy (Hx≠Hy) and skew warp the silhouette
// exactly as the game warps its mesh, so the thickness artefacts reproduce for free.
func maskCoverage(m *maskTex, p [6]float32, x, y int) float64 {
	hx, hy := float64(p[2]), float64(p[3])
	if hx == 0 || hy == 0 {
		return 0
	}
	th := float64(p[4]) * deg2rad
	skew := float64(p[5])
	c, s := math.Cos(th), math.Sin(th)
	dx := float64(x) + 0.5 - float64(p[0])
	dy := float64(y) + 0.5 - float64(p[1])
	kx := dx*c + dy*s // R(-rot)·d — local frame, same convention as EllipseInside
	ky := -dx*s + dy*c
	sx := kx - skew*ky // K⁻¹ = [[1,-skew],[0,1]] (inverse horizontal shear)
	return m.sampleUV(sx/hx+0.5, ky/hy+0.5)
}

// maskBBox returns the clamped integer bounding box of a mask placed by P=[cx,cy,Hx,Hy,rotDeg,skew]:
// the footprint box corners (n=±0.5, i.e. ±Hx/2,±Hy/2) pushed forward through K(skew) then R(rot).
func maskBBox(p [6]float32, w, h int) (xMin, yMin, xMax, yMax int) {
	cx, cy := float64(p[0]), float64(p[1])
	hx, hy := float64(p[2])/2, float64(p[3])/2
	th := float64(p[4]) * deg2rad
	skew := float64(p[5])
	c, s := math.Cos(th), math.Sin(th)
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, ex := range []float64{-hx, hx} {
		for _, ey := range []float64{-hy, hy} {
			kx := ex + skew*ey // K(skew)
			px := cx + kx*c - ey*s
			py := cy + kx*s + ey*c
			minX, maxX = math.Min(minX, px), math.Max(maxX, px)
			minY, maxY = math.Min(minY, py), math.Max(maxY, py)
		}
	}
	xMin = clampI(int(math.Floor(minX-1)), 0, w-1)
	xMax = clampI(int(math.Ceil(maxX+1)), 0, w-1)
	yMin = clampI(int(math.Floor(minY-1)), 0, h-1)
	yMax = clampI(int(math.Ceil(maxY+1)), 0, h-1)
	return
}
