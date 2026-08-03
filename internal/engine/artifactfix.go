package engine

// artifactfix.go — EXPERIMENTAL artifact-repair pass (Options.ArtifactFix, default off). The eye
// catches shapes that draw CONTRAST where the target is smooth — specks, rims, stray slivers —
// which SSE barely charges (a speck is a handful of pixels), so neither the greedy gate nor the
// LOO prune removes them. Detection: false-edge energy per pixel (relu(|∇render| − |∇target|),
// Sobel on working-space luma) attributed to the topmost visible shape; offenders rank by DENSITY
// (feSum/area — the speck signature). Repair: measured on img_10, single-shape point edits
// (delete/soften/glow-swap/LS-recolor) are FE-neutral-or-WORSE — the polished stack is co-adapted
// and every top shape masks structure beneath, so the pass follows the LOO-refit pattern instead:
// prune the offender batch, RE-POLISH so the survivors co-adapt around the removals, and gate the
// whole repair end-to-end on global false-edge + weighted error. Backend-agnostic host code.

import (
	"math"
	"os"
	"sort"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

const (
	afTopK     = 20    // offenders pruned per round
	afMinArea  = 25    // offender floor: owned pixels (skips sub-pixel noise)
	afMinFeSum = 25.0  // offender floor: owned false-edge energy
	afFeGate   = 0.97  // accept: global false-edge energy must drop below this fraction
	afSseGate  = 1.005 // accept: global weighted error may grow at most this factor
	afCoverEps = 0.02  // visible-ownership coverage threshold (matches recolorVisible)
)

// afDebug dumps gate numbers (lab-only; env FH6_AFDEBUG=1).
var afDebug = os.Getenv("FH6_AFDEBUG") == "1"

type artifactFixPass struct{}

func (artifactFixPass) enabled(opt Options) bool { return opt.ArtifactFix && opt.Polish }

func (artifactFixPass) apply(r *run) {
	r.setStatus("Refining (artifact repair)…")
	target := r.be.Target()
	w, h := r.w, r.h
	gtgt := afSobel(afLuma(target, w, h), w, h)

	render := func(shapes []model.Shape) []float32 {
		_ = r.be.Reset(r.initCanvas)
		for _, s := range shapes[1:] {
			_ = r.be.Apply(shapeToCandidate(s))
		}
		buf := make([]float32, w*h*4)
		_ = r.be.ReadCanvas(buf)
		return buf
	}

	canvas := render(r.shapes)
	fe := afFalseEdge(canvas, gtgt, w, h)
	owner := afOwnership(r.shapes, w, h)
	baseFe := afSum(fe)
	baseErr := r.finalErr

	type off struct {
		idx   int
		feSum float64
		area  int
	}
	sums := make([]off, len(r.shapes))
	for i := range sums {
		sums[i].idx = i
	}
	for i, o := range owner {
		if o <= 0 { // background (index 0) is not repairable
			continue
		}
		sums[o].feSum += fe[i]
		sums[o].area++
	}
	// Rank by false-edge DENSITY: the eye-catching speck is a small shape whose every pixel is
	// contrast the target doesn't have (a feSum ranking surfaces large soft shapes instead).
	offs := make([]off, 0, len(sums))
	for _, s := range sums[1:] {
		if s.area >= afMinArea && s.feSum >= afMinFeSum {
			offs = append(offs, s)
		}
	}
	sort.Slice(offs, func(a, b int) bool {
		return offs[a].feSum/float64(offs[a].area) > offs[b].feSum/float64(offs[b].area)
	})
	if len(offs) > afTopK {
		offs = offs[:afTopK]
	}
	if len(offs) == 0 {
		return
	}

	// Prune the batch, then re-polish: the co-adapted stack absorbs any single point edit
	// (measured — deleting one offender EXPOSES worse edges beneath), so the survivors must be
	// re-co-adapted around the removals before the repair is judged.
	drop := make(map[int]bool, len(offs))
	for _, o := range offs {
		drop[o.idx] = true
	}
	pruned := make([]model.Shape, 0, len(r.shapes)-len(offs))
	for i, s := range r.shapes {
		if !drop[i] {
			pruned = append(pruned, s)
		}
	}
	_ = render(pruned)
	grid, _, _, _ := r.be.ErrorGrid()
	newShapes, newErr := applyPolish(r.be, pruned, sumGrid(grid), r.initCanvas, r.opt, w, h, &r.tm)
	newFe := afSum(afFalseEdge(render(newShapes), gtgt, w, h))

	if newFe <= baseFe*afFeGate && newErr <= baseErr*afSseGate {
		r.shapes, r.finalErr = newShapes, newErr
		applog.Printf("artifact-fix: pruned %d offenders + re-polish ACCEPTED — false-edge %.0f -> %.0f (%.1f%%), err %.1f -> %.1f",
			len(offs), baseFe, newFe, 100*newFe/math.Max(baseFe, 1e-9), baseErr, newErr)
		return
	}
	// Rollback: restore the backend to the original stack.
	_ = render(r.shapes)
	if afDebug || newFe > baseFe {
		applog.Printf("artifact-fix: repair REJECTED (false-edge %.0f -> %.0f, err %.1f -> %.1f) — kept the original stack",
			baseFe, newFe, baseErr, newErr)
	}
}

// afOwnership returns the topmost visible shape index per pixel (−1 = none). Mirrors the
// recolorVisible convention: gradient/mask kinds own where coverage×alpha exceeds afCoverEps.
func afOwnership(shapes []model.Shape, w, h int) []int32 {
	owner := make([]int32, w*h)
	for i := range owner {
		owner[i] = -1
	}
	for si := 1; si < len(shapes); si++ {
		c := shapeToCandidate(shapes[si])
		grad := raster.IsGradient(c.Kind)
		xMin, yMin, xMax, yMax := raster.BBox(c.Kind, c.P, w, h)
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				if grad {
					if raster.Coverage(c.Kind, c.P, x, y)*float64(c.Color.A) <= afCoverEps {
						continue
					}
				} else if !raster.Inside(c.Kind, c.P, x, y) {
					continue
				}
				owner[y*w+x] = int32(si)
			}
		}
	}
	return owner
}

func afLuma(pix []float32, w, h int) []float64 {
	out := make([]float64, w*h)
	for i := 0; i < w*h; i++ {
		out[i] = 0.2126*float64(pix[i*4]) + 0.7152*float64(pix[i*4+1]) + 0.0722*float64(pix[i*4+2])
	}
	return out
}

func afSobel(l []float64, w, h int) []float64 {
	out := make([]float64, w*h)
	at := func(x, y int) float64 {
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x >= w {
			x = w - 1
		}
		if y >= h {
			y = h - 1
		}
		return l[y*w+x]
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			gx := -at(x-1, y-1) - 2*at(x-1, y) - at(x-1, y+1) + at(x+1, y-1) + 2*at(x+1, y) + at(x+1, y+1)
			gy := -at(x-1, y-1) - 2*at(x, y-1) - at(x+1, y-1) + at(x-1, y+1) + 2*at(x, y+1) + at(x+1, y+1)
			out[y*w+x] = math.Hypot(gx, gy)
		}
	}
	return out
}

// afFalseEdge computes relu(|∇render|−|∇target|) over the full frame (gtgt precomputed).
func afFalseEdge(canvas []float32, gtgt []float64, w, h int) []float64 {
	gr := afSobel(afLuma(canvas, w, h), w, h)
	for i := range gr {
		if d := gr[i] - gtgt[i]; d > 0 {
			gr[i] = d
		} else {
			gr[i] = 0
		}
	}
	return gr
}

func afSum(m []float64) float64 {
	var s float64
	for _, v := range m {
		s += v
	}
	return s
}
