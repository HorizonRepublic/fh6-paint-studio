package engine

import (
	"math"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// Rim aiming for the soft-swap pass (Options.RimAim).
//
// The repair itself already exists and works — softswap replaces a hard-edged shape with a soft one
// on the same footprint, so the fill stays and the rim goes. What it lacked was aim. It ranked
// candidates by the false-edge mass INSIDE a shape and only ever looked at rectangles and triangles,
// while the defect the owner names ("circles that stick out") is a property of a shape's BOUNDARY and,
// measured, lives mostly on ellipses: of the fifty worst offenders on a test frame, thirty-seven were
// ellipses, which the old ordering could not even see.
//
// What the measure counts is the edge a shape draws that the picture does not have: gradient of the
// reconstruction in excess of the target's, summed along the shape's outline, and only where the
// TARGET is smooth. Both restrictions are load-bearing. Without the smooth restriction the test finds
// almost nothing, because a reconstruction simplifies and its gradient sits below the target's nearly
// everywhere. Without restricting to the outline, the score becomes an area measure that big shapes
// win by being big.
//
// It is deliberately not a blur detector, and that distinction was verified rather than assumed:
// softening thirty-eight RANDOMLY chosen shapes made this score WORSE (+4.5%) while improving SSE
// more than the aimed swap did. Blur alone does not satisfy it — which is the trap the size-keyed
// glow swap fell into, where every metric approved a frame the owner's eye rejected as veiled.
const (
	rimSmoothTau  = 0.016 // target gradient (linear luma per pixel) below this counts as smooth surroundings
	rimSmoothFrac = 0.5   // an outline must run at least this far through smooth target to be judged at all
)

// shapeRimDebt scores every shape by the false edge its own outline lays on smooth ground. Index 0
// (the background) is never scored. Shapes whose outline mostly follows real structure score 0 — that
// is the shape doing its job, not an artefact.
func shapeRimDebt(shapes []model.Shape, reconLuma, targetLuma []float32, w, h int) []float64 {
	debt := make([]float64, len(shapes))
	if w < 3 || h < 3 {
		return debt
	}
	gr := gradMag(reconLuma, w, h)
	gt := gradMag(targetLuma, w, h)
	for j := 1; j < len(shapes); j++ {
		kind := model.KindFromType(shapes[j].Type)
		p := model.ParamsFromShape(shapes[j])
		xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
		if xMax < xMin || yMax < yMin {
			continue
		}
		var sum float64
		var onEdge, onSmooth int
		for y := maxInt(1, yMin); y <= minInt(h-2, yMax); y++ {
			for x := maxInt(1, xMin); x <= minInt(w-2, xMax); x++ {
				if !isOutline(kind, p, x, y) {
					continue
				}
				onEdge++
				i := y*w + x
				if gt[i] >= rimSmoothTau {
					continue
				}
				onSmooth++
				if d := gr[i] - gt[i]; d > 0 {
					sum += float64(d)
				}
			}
		}
		if onEdge == 0 || float64(onSmooth) < rimSmoothFrac*float64(onEdge) {
			continue
		}
		debt[j] = sum
	}
	return debt
}

// isOutline reports whether (x,y) lies on the shape's boundary — inside it, with at least one
// 4-neighbour outside. Asking the rasteriser keeps this exact for every kind, including the mask
// words, without a per-kind outline formula to drift out of sync with how the shape actually renders.
func isOutline(kind model.ShapeKind, p [6]float32, x, y int) bool {
	if !raster.Inside(kind, p, x, y) {
		return false
	}
	return !raster.Inside(kind, p, x-1, y) || !raster.Inside(kind, p, x+1, y) ||
		!raster.Inside(kind, p, x, y-1) || !raster.Inside(kind, p, x, y+1)
}

// gradMag is the central-difference gradient magnitude of a luma plane, border pixels left at zero.
func gradMag(luma []float32, w, h int) []float32 {
	g := make([]float32, w*h)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*w + x
			dx := float64(luma[i+1] - luma[i-1])
			dy := float64(luma[i+w] - luma[i-w])
			g[i] = float32(0.5 * math.Hypot(dx, dy))
		}
	}
	return g
}
