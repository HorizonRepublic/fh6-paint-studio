package engine

import (
	"fmt"
	"math"
	"os"
	"sort"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// Soft-swap: post-polish standout REPAIR by substitution. The standout pass established (measured,
// FH6_STANDOUT_DEBUG on a live-polish run) that recolour/fade/remove repairs die on the global SSE
// gate: with polish converged, every shape's colour is near-optimal, so removing coverage always
// costs real error and 2-3 repairs exhaust the whole budget. Swapping the offending hard-edged
// shape for a soft/round one FITTED TO THE SAME FOOTPRINT (matched second moments, same colour,
// same z-position) keeps the coverage, so each repair is far cheaper under the same gate — the rim
// goes soft, the fill stays.

var softSwapDebug = os.Getenv("FH6_SOFTSWAP_DEBUG") != ""

func swdbg(format string, a ...interface{}) {
	if softSwapDebug {
		fmt.Fprintf(os.Stderr, "[softswap] "+format+"\n", a...)
	}
}

// momentEllipseOfShape fits the ellipse with the same area centroid and second moments as the
// shape's rendered coverage (numeric, kind-agnostic — soft kinds weight by their falloff). For a
// uniform ellipse the covariance eigenvalue is (semiaxis/2)², hence semiaxis = 2·√λ.
func momentEllipseOfShape(kind model.ShapeKind, p [6]float32, w, h int) (cx, cy, a, b, deg float64, ok bool) {
	x0, y0, x1, y1 := raster.BBox(kind, p, w, h)
	var m0, mx, my float64
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			c := raster.Coverage(kind, p, x, y)
			if c <= 0 {
				continue
			}
			m0 += c
			mx += c * (float64(x) + 0.5)
			my += c * (float64(y) + 0.5)
		}
	}
	if m0 < 4 { // degenerate sliver — nothing to fit
		return
	}
	cx, cy = mx/m0, my/m0
	var sxx, syy, sxy float64
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			c := raster.Coverage(kind, p, x, y)
			if c <= 0 {
				continue
			}
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			sxx += c * dx * dx
			syy += c * dy * dy
			sxy += c * dx * dy
		}
	}
	sxx, syy, sxy = sxx/m0, syy/m0, sxy/m0
	tr, det := sxx+syy, sxx*syy-sxy*sxy
	d := math.Sqrt(math.Max(0, tr*tr/4-det))
	l1, l2 := tr/2+d, tr/2-d
	if l1 <= 0 || l2 <= 0 {
		return
	}
	a, b = 2*math.Sqrt(l1), 2*math.Sqrt(l2)
	deg = 0.5 * math.Atan2(2*sxy, sxx-syy) / (math.Pi / 180)
	return cx, cy, a, b, deg, true
}

// softSwapStandouts replaces the worst standout rect/triangle shapes with a soft-edged shape of the
// same footprint. Candidate ranking, the local false-edge accept criterion and the cumulative error
// gate all mirror suppressStandouts; the repair differs: a moment-matched ellipse / feathered disk /
// glow trial menu instead of recolour/fade/remove.
func softSwapStandouts(be backend.Backend, shapes []model.Shape, finalErr float64, initCanvas []float32, opt Options, w, h int) ([]model.Shape, float64) {
	tol := opt.SoftSwapTol
	if tol <= 0 || len(shapes) <= 2 || finalErr <= 0 {
		return shapes, finalErr
	}
	target := be.Target()
	weight := be.Weight()
	recon := make([]float32, w*h*4)
	if be.ReadCanvas(recon) != nil {
		return shapes, finalErr
	}
	fe, f0, gtotal := falseEdgeMap(recon, target, w, h)
	ratio := 0.0
	if gtotal > 0 {
		ratio = f0 / gtotal
	}
	swdbg("F0=%.1f Gtotal=%.1f ratio=%.4f (skip<%.3f) finalErr=%.1f n=%d", f0, gtotal, ratio, standoutSkipFrac, finalErr, len(shapes))
	// The skip gate asks whether the WHOLE frame carries enough false edge to be worth a pass. Rim
	// aiming asks a per-shape question instead, and the offenders it finds are a few percent of the
	// stack — a frame can be clean on average and still show them.
	if gtotal <= 0 || (!opt.RimAim && ratio < standoutSkipFrac) {
		return shapes, finalErr
	}
	targetLuma := make([]float32, w*h)
	curLuma := make([]float32, w*h)
	trialLuma := make([]float32, w*h)
	lumaOf(target, w, h, targetLuma)
	lumaOf(recon, w, h, curLuma)

	// Aim. The original ranking scores the false-edge mass INSIDE a shape and only ever considers
	// rectangles and triangles; the rim artefact is a property of the BOUNDARY and sits mostly on
	// ellipses, which that ordering cannot see at all (rimsalience.go).
	sal := shapeStandoutSalience(shapes, fe, w, h)
	if opt.RimAim {
		sal = shapeRimDebt(shapes, curLuma, targetLuma, w, h)
	}
	order := make([]int, 0, len(shapes)-1)
	for j := 1; j < len(shapes); j++ {
		k := model.KindFromType(shapes[j].Type)
		eligible := k == model.KindRectangle || k == model.KindTriangle
		if opt.RimAim {
			eligible = eligible || k == model.KindEllipse
		}
		if eligible && sal[j] > 0 {
			order = append(order, j)
		}
	}
	if len(order) == 0 {
		return shapes, finalErr
	}
	sort.Slice(order, func(a, b int) bool { return sal[order[a]] > sal[order[b]] })
	if len(order) > standoutMaxShapes {
		order = order[:standoutMaxShapes]
	}

	work := cloneShapes(shapes)
	gateErr := finalErr * (1 + tol)
	curErr := finalErr
	swapped := 0
	var dbgFeZero, dbgNoFit, dbgGated int
	var dbgBestDropSeen float64

	for _, j := range order {
		kind := model.KindFromType(shapes[j].Type)
		p := model.ParamsFromShape(shapes[j])
		xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
		bx0, by0 := maxInt(0, xMin-2), maxInt(0, yMin-2)
		bx1, by1 := minInt(w-1, xMax+2), minInt(h-1, yMax+2)
		feBefore := localFalseEdge(curLuma, targetLuma, w, h, bx0, by0, bx1, by1)
		if feBefore <= 0 {
			dbgFeZero++
			continue
		}
		cx, cy, a, b, deg, ok := momentEllipseOfShape(kind, p, w, h)
		if !ok {
			dbgNoFit++
			continue
		}

		orig := work[j]
		color := append([]int(nil), orig.Color...)
		mk := func(typ int, scale float64) model.Shape {
			return model.Shape{Type: typ, Data: []float64{cx, cy, math.Max(1, a*scale), math.Max(1, b*scale), deg}, Color: color, Score: orig.Score}
		}
		// The soft footprints reach zero coverage at their edge, so they need to overshoot the
		// moment ellipse to keep the fill: disk is opaque to 0.40·r, glow is a gaussian. Each
		// geometry is also tried recoloured to its OWN footprint's target mean — the original
		// colour was optimal for the rect/triangle footprint, not the replacement's.
		menu := []model.Shape{
			mk(model.TypeRotatedEllipse, 1),
			mk(model.TypeGradDisk, 1.15),
			mk(model.TypeGradGlow, 1.5),
		}
		alpha := 255
		if len(color) >= 4 {
			alpha = color[3]
		}
		for _, g := range [...]model.Shape{menu[0], menu[1], menu[2]} {
			if mr, mg, mb, mok := localMeanColorTarget(g, target, weight, w, h); mok {
				re := g
				re.Color = []int{mr, mg, mb, alpha}
				menu = append(menu, re)
			}
		}
		bestDrop := standoutMinLocalDrop
		bestErr := curErr
		best := -1
		for i := range menu {
			work[j] = menu[i]
			gErr := renderExcept(be, initCanvas, work, -1)
			work[j] = orig
			if gErr > gateErr {
				dbgGated++
				continue
			}
			_ = be.ReadCanvas(recon)
			lumaOf(recon, w, h, trialLuma)
			feAfter := localFalseEdge(trialLuma, targetLuma, w, h, bx0, by0, bx1, by1)
			drop := (feBefore - feAfter) / feBefore
			if drop > dbgBestDropSeen {
				dbgBestDropSeen = drop
			}
			if drop > bestDrop {
				bestDrop, bestErr, best = drop, gErr, i
			}
		}
		if best >= 0 {
			work[j] = menu[best]
			curErr = bestErr
			swapped++
			_ = renderExcept(be, initCanvas, work, -1)
			_ = be.ReadCanvas(recon)
			lumaOf(recon, w, h, curLuma)
		} else {
			_ = renderExcept(be, initCanvas, work, -1) // the last trial left the backend dirty
		}
	}

	swdbg("loop done: cand=%d feZero=%d noFit=%d gated=%d bestDropSeen=%.3f (need>%.2f) swapped=%d curErr=%.1f gate=%.1f",
		len(order), dbgFeZero, dbgNoFit, dbgGated, dbgBestDropSeen, standoutMinLocalDrop, swapped, curErr, gateErr)
	if swapped == 0 {
		_ = renderExcept(be, initCanvas, shapes, -1)
		return shapes, finalErr
	}
	keptErr := renderExcept(be, initCanvas, work, -1)
	swdbg("APPLY: swapped=%d keptErr=%.1f (finalErr=%.1f, %+.2f%%)", swapped, keptErr, finalErr, 100*(keptErr-finalErr)/finalErr)
	return work, keptErr
}

// falseEdgeRatio measures the fraction of the CURRENT canvas's edge energy the target lacks —
// the global "how much stands out" score the pre-polish gate compares.
func falseEdgeRatio(be backend.Backend, w, h int) float64 {
	recon := make([]float32, w*h*4)
	if be.ReadCanvas(recon) != nil {
		return 0
	}
	_, f0, gtot := falseEdgeMap(recon, be.Target(), w, h)
	if gtot <= 0 {
		return 0
	}
	return f0 / gtot
}

// softSwapPrePolish is the PRE-polish variant: swap on the greedy result, then let the joint polish
// co-adapt every shape around the substitutions (colours re-solve, glow geometry trains), instead of
// swapping on the converged optimum where every substitution costs irreducible SSE. Measured on the
// post-polish variant: the cumulative gate starves at 4-7 swaps (three repair-menu generations hit
// the same wall) — the redistribution has to come from the polish. Gated end-to-end like back-fit:
// polish(greedy) vs polish(swap(greedy)); the swapped branch must land within tol on SSE AND lower
// the global false-edge ratio, else the baseline ships.
func softSwapPrePolish(r *run) {
	w, h := r.w, r.h
	baseShapes, baseErr := applyPolish(r.be, cloneShapes(r.shapes), r.finalErr, r.initCanvas, r.opt, w, h, &r.tm)
	baseRatio := falseEdgeRatio(r.be, w, h)
	_ = renderExcept(r.be, r.initCanvas, r.shapes, -1) // swap ranks/trials against the GREEDY render, not branch A's
	swCand, swErr := softSwapStandouts(r.be, cloneShapes(r.shapes), r.finalErr, r.initCanvas, r.opt, w, h)
	swShapes, swPolErr := applyPolish(r.be, swCand, swErr, r.initCanvas, r.opt, w, h, &r.tm)
	swRatio := falseEdgeRatio(r.be, w, h)
	swdbg("pre-polish gate: base=%.1f feRatio=%.4f | swapped=%.1f feRatio=%.4f (SSE tol %.3f)",
		baseErr, baseRatio, swPolErr, swRatio, r.opt.SoftSwapTol)
	if swPolErr <= baseErr*(1+r.opt.SoftSwapTol) && swRatio < baseRatio {
		r.shapes, r.finalErr = swShapes, swPolErr
		return
	}
	r.shapes, r.finalErr = baseShapes, baseErr
	_ = renderExcept(r.be, r.initCanvas, r.shapes, -1)
}
