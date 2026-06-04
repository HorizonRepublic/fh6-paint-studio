package engine

import (
	"fmt"
	"math"
	"os"
	"sort"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/metric"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

var standoutDebug = os.Getenv("FH6_STANDOUT_DEBUG") != ""

func sdbg(format string, a ...interface{}) {
	if standoutDebug {
		fmt.Fprintf(os.Stderr, "[standout] "+format+"\n", a...)
	}
}

// Standout suppression — the PERCEPTUAL post-polish pass.
//
// The hard-loss metric (weighted SSE) is BLIND to a class of artifact: an individual
// circle/square whose OUTLINE is visible against a region the target renders smooth — a
// "standout". Its interior matches the target (so SSE is low) but its rim is a step the eye
// reads as a drawn shape that shouldn't be there. Because the area is thin, SSE barely sees
// it, so neither greedy nor polish removes it. This pass detects those rims directly (a
// gradient the recon has but the target lacks) and gently repairs them, GATED so it can never
// make the rendered error meaningfully worse.
//
// Method, all in the backend's working colour space (so recon vs target is apples-to-apples):
//  1. falseEdgeMap: relu(|grad recon| - |grad target|) per pixel — high where the recon draws
//     an edge the target doesn't have.
//  2. shapeStandoutSalience: rank shapes by the false-edge their footprint encloses (per unit
//     perimeter) to PRIORITISE which to try (bounds cost). The gate, not this ranking, decides.
//  3. Worst-standout-first, try a small REPAIR MENU on each: recolour toward the LOCAL TARGET
//     MEAN (kills a wrong-colour standout) and ALPHA-FADE the shape (50% / 25% / removal) —
//     which kills a RIGHT-colour shape whose rim still steps against a smooth target (the
//     dominant kind on a converged recon, where the colour is already near-optimal so recolour
//     is ~a no-op). Keep the repair that drops the shape's LOCAL false-edge most, subject to two
//     gates. The acceptance COMPOUNDS: each repair is judged against the running (already-
//     repaired) render, and the running error is bounded by finalErr*(1+opt.StandoutTol). So
//     independent per-shape costs cannot sum past the budget (a fixed independent gate let N
//     valid recolours sum past it); the budget is spent greedily on the highest-salience
//     standouts, and an error-LOWERING repair is free (it replenishes the budget).
//
// Opt-in (opt.StandoutTol > 0), default OFF. Validate by EYE: the metric will NOT show the
// win (that is the entire point) — the gate only guarantees it does not show a meaningful LOSS.
// On already-converged content the effect is subtle (only a handful of repairs fit the budget);
// it does more on content with many obvious standouts, since error-lowering repairs are free.

const (
	standoutMaxShapes    = 128  // cap on shapes tried per pass (cost bound; the salience rank picks the worst)
	standoutMinLocalDrop = 0.10 // a repair must cut the shape's local false-edge by >=10% to be eligible
	standoutSkipFrac     = 0.01 // early-out: if <1% of the recon's edge energy is "false", the recon is faithful — skip
)

// sobelMagAt returns the Sobel gradient magnitude of luma at (x,y) with edge clamping.
func sobelMagAt(luma []float32, w, h, x, y int) float32 {
	at := func(xx, yy int) float32 {
		if xx < 0 {
			xx = 0
		} else if xx >= w {
			xx = w - 1
		}
		if yy < 0 {
			yy = 0
		} else if yy >= h {
			yy = h - 1
		}
		return luma[yy*w+xx]
	}
	gx := (at(x+1, y-1) + 2*at(x+1, y) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x-1, y) + at(x-1, y+1))
	gy := (at(x-1, y+1) + 2*at(x, y+1) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x, y-1) + at(x+1, y-1))
	return float32(math.Hypot(float64(gx), float64(gy)))
}

// lumaOf fills dst (len w*h) with the Rec.601 luma of a working-space RGBA canvas.
func lumaOf(canvas []float32, w, h int, dst []float32) {
	for i := 0; i < w*h; i++ {
		p := i * 4
		dst[i] = metric.Luma(canvas[p], canvas[p+1], canvas[p+2])
	}
}

// falseEdgeMap returns the per-pixel false-edge map fe = relu(|grad recon| - |grad target|),
// its total energy F0 = sum(fe), and the recon's total gradient energy Gtotal (for the
// faithfulness early-out). All gradients are Sobel magnitude on luma in the working space.
func falseEdgeMap(recon, target []float32, w, h int) (fe []float32, f0, gtotal float64) {
	rl := make([]float32, w*h)
	tl := make([]float32, w*h)
	lumaOf(recon, w, h, rl)
	lumaOf(target, w, h, tl)
	fe = make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			gR := sobelMagAt(rl, w, h, x, y)
			gT := sobelMagAt(tl, w, h, x, y)
			gtotal += float64(gR)
			if d := gR - gT; d > 0 {
				fe[i] = d
				f0 += float64(d)
			}
		}
	}
	return fe, f0, gtotal
}

// shapeStandoutSalience ranks every shape (1..n-1; background 0 is never scored) by the
// false-edge energy enclosed by its footprint divided by an estimate of its perimeter — so a
// big "standout circle" (large area, thin rim) and a small one compare fairly. A clean shape
// (no false rim) sums to ~0. This only PRIORITISES which shapes the gated repair tries.
func shapeStandoutSalience(shapes []model.Shape, fe []float32, w, h int) []float64 {
	n := len(shapes)
	sal := make([]float64, n)
	for j := 1; j < n; j++ {
		kind := model.KindFromType(shapes[j].Type)
		p := model.ParamsFromShape(shapes[j])
		xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
		if xMax < xMin || yMax < yMin {
			continue
		}
		var sum float64
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				if raster.Inside(kind, p, x, y) {
					sum += float64(fe[y*w+x])
				}
			}
		}
		perim := 2*float64((xMax-xMin)+(yMax-yMin)) + 1
		sal[j] = sum / perim
	}
	return sal
}

// localMeanColorTarget returns the weighted-mean TARGET colour (0..255 bytes, working-space
// encoded) over the pixels shape s covers, and ok=false if its footprint carries no weight.
// Repainting a standout to this colour makes it match its (smooth) surroundings so its rim
// disappears — the gentle repair, tried before removal.
func localMeanColorTarget(s model.Shape, target, weight []float32, w, h int) (r, g, b int, ok bool) {
	kind := model.KindFromType(s.Type)
	p := model.ParamsFromShape(s)
	xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
	var sw, sr, sg, sb float64
	for y := yMin; y <= yMax; y++ {
		for x := xMin; x <= xMax; x++ {
			if !raster.Inside(kind, p, x, y) {
				continue
			}
			idx := y*w + x
			wt := float64(weight[idx])
			q := idx * 4
			sw += wt
			sr += wt * float64(target[q])
			sg += wt * float64(target[q+1])
			sb += wt * float64(target[q+2])
		}
	}
	if sw <= 0 {
		return 0, 0, 0, false
	}
	inv := 1.0 / sw
	return model.EncByte(float32(sr * inv)), model.EncByte(float32(sg * inv)), model.EncByte(float32(sb * inv)), true
}

// localFalseEdge sums relu(|grad recon| - |grad target|) over the bbox [x0..x1]×[y0..y1]
// using precomputed luma planes (Sobel reads ±1 neighbours via clamping).
func localFalseEdge(reconLuma, targetLuma []float32, w, h, x0, y0, x1, y1 int) float64 {
	var s float64
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			gR := sobelMagAt(reconLuma, w, h, x, y)
			gT := sobelMagAt(targetLuma, w, h, x, y)
			if d := gR - gT; d > 0 {
				s += float64(d)
			}
		}
	}
	return s
}

// renderExcept resets the backend to initCanvas and applies shapes[1:], optionally skipping
// one index (skip<0 = none) — the building block for a removal trial and the final re-render.
func renderExcept(be backend.Backend, initCanvas []float32, shapes []model.Shape, skip int) float64 {
	_ = be.Reset(initCanvas)
	for j := 1; j < len(shapes); j++ {
		if j == skip {
			continue
		}
		_ = be.Apply(shapeToCandidate(shapes[j]))
	}
	g, _, _, _ := be.ErrorGrid()
	return sumGrid(g)
}

// suppressStandouts is the gated perceptual repair pass (see file header). It leaves the
// backend rendering the RETURNED shapes and returns their hard-rendered error. opt.StandoutTol
// is the maximum fractional GLOBAL error rise a repair may cost; 0 disables the pass.
func suppressStandouts(be backend.Backend, shapes []model.Shape, finalErr float64, initCanvas []float32, opt Options, w, h int) ([]model.Shape, float64) {
	tol := opt.StandoutTol
	if tol <= 0 || len(shapes) <= 2 || finalErr <= 0 {
		return shapes, finalErr
	}
	target := be.Target()
	weight := be.Weight()

	// 1. Read the current recon and build the false-edge map.
	recon := make([]float32, w*h*4)
	if be.ReadCanvas(recon) != nil {
		return shapes, finalErr
	}
	fe, f0, gtotal := falseEdgeMap(recon, target, w, h)
	ratio := 0.0
	if gtotal > 0 {
		ratio = f0 / gtotal
	}
	sdbg("F0=%.1f Gtotal=%.1f ratio=%.4f (skip<%.3f) finalErr=%.1f n=%d", f0, gtotal, ratio, standoutSkipFrac, finalErr, len(shapes))
	if gtotal <= 0 || ratio < standoutSkipFrac {
		sdbg("EARLY-OUT: recon faithful (ratio %.4f < %.3f)", ratio, standoutSkipFrac)
		return shapes, finalErr // recon already faithful — nothing visibly stands out
	}
	reconLuma0 := make([]float32, w*h)
	targetLuma := make([]float32, w*h)
	lumaOf(recon, w, h, reconLuma0)
	lumaOf(target, w, h, targetLuma)

	// 2. Rank shapes by standout salience; take the worst up to the cost cap.
	sal := shapeStandoutSalience(shapes, fe, w, h)
	order := make([]int, 0, len(shapes)-1)
	for j := 1; j < len(shapes); j++ {
		if sal[j] > 0 {
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
	if standoutDebug && len(order) > 0 {
		top := order[0]
		sdbg("candidates=%d topSalience=%.4f (shape %d) medSalience=%.4f", len(order), sal[top], top, sal[order[len(order)/2]])
	}

	// 3. COMPOUNDING per-shape repair with a cumulative error budget. Repairs are tried worst-
	// standout-first; each is measured against the CURRENT (already-repaired) render, and the
	// running error is gated against finalErr*(1+tol). So independent per-shape costs can't sum
	// past the gate (the bug a fixed independent gate hit: 7 valid recolours summed to +1%):
	// the budget is consumed greedily by the highest-salience standouts and the pass STOPS once
	// the cumulative rise would breach the gate. The final error is <= gate by construction.
	work := cloneShapes(shapes) // mutated in place as repairs are accepted (removed = alpha forced to 0)
	removed := make([]bool, len(shapes))
	trialLuma := make([]float32, w*h)
	curLuma := make([]float32, w*h)
	copy(curLuma, reconLuma0) // running recon luma (refreshed after each accepted repair)
	gateErr := finalErr * (1 + tol)
	curErr := finalErr
	applied := 0
	var dbgFeZero, dbgGated int
	var dbgBestDropSeen float64

	for _, j := range order {
		kind := model.KindFromType(shapes[j].Type)
		p := model.ParamsFromShape(shapes[j])
		xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
		// Expand the bbox by 2 px so the rim's gradient (which reads neighbours) is captured.
		bx0, by0 := maxInt(0, xMin-2), maxInt(0, yMin-2)
		bx1, by1 := minInt(w-1, xMax+2), minInt(h-1, yMax+2)
		feBefore := localFalseEdge(curLuma, targetLuma, w, h, bx0, by0, bx1, by1)
		if feBefore <= 0 {
			dbgFeZero++
			continue
		}

		orig := work[j]
		bestDrop := standoutMinLocalDrop
		var bestColor []int
		bestErr := curErr
		have := false
		// consider renders `work` (with work[j] already set to the trial colour) and, if it stays
		// within the gate AND drops this shape's local false-edge most so far, records it.
		consider := func(color []int) {
			gErr := renderExcept(be, initCanvas, work, -1)
			if gErr > gateErr {
				dbgGated++
				return
			}
			_ = be.ReadCanvas(recon)
			lumaOf(recon, w, h, trialLuma)
			feAfter := localFalseEdge(trialLuma, targetLuma, w, h, bx0, by0, bx1, by1)
			drop := (feBefore - feAfter) / feBefore
			if drop > dbgBestDropSeen {
				dbgBestDropSeen = drop
			}
			if drop > bestDrop {
				bestDrop, bestColor, bestErr, have = drop, color, gErr, true
			}
		}

		r0, g0, b0, alpha := 0, 0, 0, 255
		if len(orig.Color) >= 3 {
			r0, g0, b0 = orig.Color[0], orig.Color[1], orig.Color[2]
		}
		if len(orig.Color) >= 4 {
			alpha = orig.Color[3]
		}
		// Build the repair menu — each is a full RGBA the shape could take. consider() keeps
		// whichever drops this shape's local false-edge most within the cumulative error gate.
		// Two complementary repair families:
		//   - RECOLOUR toward the local target mean (alpha kept): kills a wrong-colour standout.
		//   - ALPHA-FADE (colour kept), down to removal: kills a RIGHT-colour shape whose rim
		//     still steps against a smooth target (the dominant kind on a converged recon, which
		//     recolour can't touch because the shape's colour is already near-optimal).
		var menu [][]int
		if mr, mg, mb, ok := localMeanColorTarget(shapes[j], target, weight, w, h); ok {
			menu = append(menu, []int{mr, mg, mb, alpha})
		}
		menu = append(menu,
			[]int{r0, g0, b0, alpha / 2},       // fade to 50%
			[]int{r0, g0, b0, (alpha + 1) / 4}, // fade to 25%
			[]int{r0, g0, b0, 0},               // remove
		)
		for _, c := range menu {
			work[j].Color = c
			consider(c)
			work[j] = orig
		}

		if have {
			work[j].Color = bestColor
			if bestColor[3] == 0 {
				removed[j] = true
			}
			curErr = bestErr
			applied++
			// Finalise: re-render the accepted state and refresh the running recon luma.
			_ = renderExcept(be, initCanvas, work, -1)
			_ = be.ReadCanvas(recon)
			lumaOf(recon, w, h, curLuma)
		} else {
			// No repair accepted — restore the backend to `work` (the last trial left it dirty).
			work[j] = orig
			_ = renderExcept(be, initCanvas, work, -1)
		}
	}

	sdbg("loop done: feZero=%d gated=%d bestDropSeen=%.3f (need>%.2f) applied=%d curErr=%.1f gate=%.1f",
		dbgFeZero, dbgGated, dbgBestDropSeen, standoutMinLocalDrop, applied, curErr, gateErr)
	if applied == 0 {
		_ = renderExcept(be, initCanvas, shapes, -1) // restore the original render
		return shapes, finalErr
	}

	// Drop the removed shapes; re-render the kept set for the exact returned error.
	kept := make([]model.Shape, 0, len(work))
	kept = append(kept, work[0])
	for j := 1; j < len(work); j++ {
		if !removed[j] {
			kept = append(kept, work[j])
		}
	}
	keptErr := renderExcept(be, initCanvas, kept, -1)
	sdbg("APPLY: applied=%d removed=%d kept=%d keptErr=%.1f (finalErr=%.1f, +%.2f%%)",
		applied, len(shapes)-len(kept), len(kept), keptErr, finalErr, 100*(keptErr-finalErr)/finalErr)
	return kept, keptErr
}
