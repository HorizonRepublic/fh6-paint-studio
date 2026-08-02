package engine

import (
	"fmt"
	"math"
	"os"
	"sort"

	"fh6-paint-studio/internal/metric"
	"fh6-paint-studio/internal/model"
)

// Smooth-region gradient base (Options.SmoothBase): claim LARGE smooth regions of the target BEFORE
// the greedy with a minimal stack — an opaque base + a few gradient primitives (the bank's linear
// ramp word, glow/disk radial falloffs, arc-band words) — all colours solved JOINTLY per stack
// (stacksolve.go). One stack of 2-4 huge shapes replaces the hundreds of mid-sized translucent
// facets the greedy spends on smooth shading, whose rims ARE the patchwork artifact (SSE is blind
// to them); the freed budget goes to genuine detail. Regions come from the same HardEdgeMap that
// gates kinds (regionkinds.go): low hard-structure cells, BFS-grouped with colour continuity.
// Anti-region-fill gate: a stack must contain at least one EARNING gradient layer — flat covers
// are never pre-placed (the region-fill lesson); every layer must carry its weight or the whole
// stack rolls back.

const (
	smoothCell      = 16
	smoothTau       = 0.10 // cell mean HardEdgeMap below this = smooth
	smoothColStep   = 0.22 // max linear-RGB distance between neighbour cell means. Loose on purpose: a legit 0.5-amplitude ramp steps ~0.18 per 16px cell; STEP edges between distinct objects live in non-smooth cells (HardEdgeMap) and never join anyway.
	smoothAlphaMin  = 0.7  // cell opaque-pixel fraction below this = cutout territory, not claimable
	smoothMinCells  = 24   // ≥ ~6k px at 16px cells — only LARGE regions are worth a stack
	smoothMaxClaim  = 10   // stacks per run (≤ 40 shapes of the 3000 budget)
	smoothMaxSplit  = 2    // rejected-region split recursion: a huge drifting region (a whole background) rarely fits ONE 4-layer stack — halving along the long axis lets each part claim its own
	smoothMaxLayers = 4    // shapes per stack (base + up to 3 gradient layers)
	smoothLayerGain = 0.10 // each layer must cut ≥ this fraction of the region's REMAINING error. Gates on the residual, not on the previous layer's earn: a base rect earns huge just covering the bg mismatch, which would starve the gentle-gradient layers the whole pass exists for (SSE undervalues them — the SSE↔eye divergence lesson).
	smoothSoftGain  = 0.08 // the gradient layers together must cut ≥ this fraction of the post-hard-base residual, else the claim is a flat fill the greedy handles (anti-region-fill)
	smoothPad       = 8.0  // px of placement overshoot past the region extents
	smoothSpill     = 0.5  // weight fraction charged for footprint pixels OUTSIDE the region: the greedy overpaints most of a claim's spill but not all, and an uncharged spill lets the joint solve pick metamer colours (a green base under a grey arc) that ship as ghosts wherever the budget runs out
	smoothBudget    = 1 << 18
)

var smoothDebug = os.Getenv("FH6_SMOOTH_DEBUG") != ""

func smdbg(format string, a ...interface{}) {
	if smoothDebug {
		fmt.Fprintf(os.Stderr, "[smoothbase] "+format+"\n", a...)
	}
}

// smoothWords are the bank's usable gradient-fill words (gradwords survey): 2204 = linear Gouraud
// ramp; 2202/2219/2220 = arc bands for curved shading. Missing words just drop out of the menu.
var smoothWords = []uint16{2204, 2202, 2219, 2220}

type smoothRegion struct {
	cells    []int
	px       int
	cx, cy   float64 // pixel centroid
	hu, hv   float64 // half extents along the principal axes
	deg      float64 // principal orientation
	gdeg     float64 // mean luma-gradient direction (ramp axis)
	hgu, hgv float64 // half extents along the gradient axes (ramp-word frame)
}

// smoothBase segments large smooth regions and claims each with a jointly-solved minimal stack.
// Returns the number of claimed stacks.
func (r *run) smoothBase() int {
	w, h := r.w, r.h
	target := r.be.Target()
	hard := []float32(nil)
	if r.kindGate != nil {
		hard = r.kindGate.hard
	} else {
		hard = metric.HardEdgeMap(target, w, h)
	}

	cw, ch := (w+smoothCell-1)/smoothCell, (h+smoothCell-1)/smoothCell
	nCells := cw * ch
	cellHard := make([]float64, nCells)
	cellR := make([]float64, nCells)
	cellG := make([]float64, nCells)
	cellB := make([]float64, nCells)
	cellA := make([]float64, nCells)
	cellN := make([]float64, nCells)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := (y/smoothCell)*cw + x/smoothCell
			i := y*w + x
			p := i * 4
			cellHard[c] += float64(hard[i])
			cellR[c] += float64(target[p])
			cellG[c] += float64(target[p+1])
			cellB[c] += float64(target[p+2])
			if target[p+3] >= 0.5 {
				cellA[c]++
			}
			cellN[c]++
		}
	}
	smooth := make([]bool, nCells)
	for c := 0; c < nCells; c++ {
		if cellN[c] <= 0 {
			continue
		}
		inv := 1 / cellN[c]
		cellHard[c] *= inv
		cellR[c] *= inv
		cellG[c] *= inv
		cellB[c] *= inv
		cellA[c] *= inv
		smooth[c] = cellHard[c] < smoothTau && cellA[c] >= smoothAlphaMin
	}

	// BFS-group smooth cells; neighbours join only when their mean colours are continuous, so one
	// region never bridges two different flat objects across a soft boundary.
	group := make([]int32, nCells)
	for i := range group {
		group[i] = -1
	}
	var regs []smoothRegion
	for c0 := 0; c0 < nCells; c0++ {
		if group[c0] >= 0 || !smooth[c0] {
			continue
		}
		id := int32(len(regs))
		group[c0] = id
		queue := []int{c0}
		var cells []int
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			cells = append(cells, cur)
			cx, cy := cur%cw, cur/cw
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := cx+d[0], cy+d[1]
				if nx < 0 || ny < 0 || nx >= cw || ny >= ch {
					continue
				}
				ni := ny*cw + nx
				if group[ni] >= 0 || !smooth[ni] {
					continue
				}
				dr := cellR[cur] - cellR[ni]
				dg := cellG[cur] - cellG[ni]
				db := cellB[cur] - cellB[ni]
				if dr*dr+dg*dg+db*db > smoothColStep*smoothColStep {
					continue
				}
				group[ni] = id
				queue = append(queue, ni)
			}
		}
		regs = append(regs, smoothRegion{cells: cells})
	}
	if len(regs) == 0 {
		return 0
	}

	// Pixel-level frame per region: centroid + covariance -> principal axes; extents by projection.
	lum := make([]float32, w*h)
	lumaOf(target, w, h, lum)
	for gi := range regs {
		regs[gi].computeFrame(cw, w, h, lum)
	}
	sort.Slice(regs, func(a, b int) bool { return regs[a].px > regs[b].px })

	canvasBuf := make([]float32, w*h*4)
	if r.be.ReadCanvas(canvasBuf) != nil {
		return 0
	}
	weight := r.be.Weight()

	// Worklist: a rejected region splits along its long axis (up to smoothMaxSplit deep) — a whole
	// drifting background rarely fits ONE stack, but its halves often do. Children go to the tail.
	type smoothWork struct {
		rg    smoothRegion
		depth int
	}
	queue := make([]smoothWork, 0, len(regs)*2)
	for _, rg := range regs {
		queue = append(queue, smoothWork{rg, 0})
	}
	claimed := 0
	for qi := 0; qi < len(queue); qi++ {
		rg := queue[qi].rg
		depth := queue[qi].depth
		if claimed >= smoothMaxClaim || len(r.shapes)-1+smoothMaxLayers > r.genTarget {
			break
		}
		if len(rg.cells) < smoothMinCells {
			continue // children of a split can be small — keep scanning the queue
		}
		sel := buildCellSel(rg.cells, cw, w, h)
		stack, delta, soft := r.smoothClaimStack(canvasBuf, weight, &rg, sel)
		if stack == nil {
			why := "no earning stack"
			if depth < smoothMaxSplit && len(rg.cells) >= 2*smoothMinCells {
				a, b := rg.split(cw)
				a.computeFrame(cw, w, h, lum)
				b.computeFrame(cw, w, h, lum)
				queue = append(queue, smoothWork{a, depth + 1}, smoothWork{b, depth + 1})
				smdbg("region %d px=%d SPLIT (%s) -> %d + %d cells", qi, rg.px, why, len(a.cells), len(b.cells))
			} else {
				smdbg("region %d px=%d REJECT (%s)", qi, rg.px, why)
			}
			continue
		}
		for _, l := range stack {
			_ = r.be.Apply(l)
			r.shapes = append(r.shapes, l.ToShape(delta/float64(len(stack))))
		}
		if r.be.ReadCanvas(canvasBuf) != nil {
			return claimed
		}
		claimed++
		smdbg("region %d px=%d CLAIM layers=%d Δ=%.1f soft=%.1f frame c=(%.0f,%.0f) ext=(%.0f,%.0f) θ=%.0f° ∇θ=%.0f°",
			qi, rg.px, len(stack), delta, soft, rg.cx, rg.cy, rg.hu, rg.hv, rg.deg, rg.gdeg)
	}
	smdbg("done: regions=%d claimed=%d stacks", len(regs), claimed)
	return claimed
}

// computeFrame fills the region's pixel-level placement frame: centroid + covariance principal
// axes with projection extents, plus the mean-gradient axis frame for the directional ramp words.
func (rg *smoothRegion) computeFrame(cw, w, h int, lum []float32) {
	var n, sx, sy float64
	for _, ci := range rg.cells {
		x0, y0 := (ci%cw)*smoothCell, (ci/cw)*smoothCell
		for y := y0; y < y0+smoothCell && y < h; y++ {
			for x := x0; x < x0+smoothCell && x < w; x++ {
				n++
				sx += float64(x)
				sy += float64(y)
			}
		}
	}
	rg.px = int(n)
	if n <= 0 {
		return
	}
	rg.cx, rg.cy = sx/n, sy/n
	var cxx, cxy, cyy, ggx, ggy float64
	for _, ci := range rg.cells {
		x0, y0 := (ci%cw)*smoothCell, (ci/cw)*smoothCell
		for y := y0; y < y0+smoothCell && y < h; y++ {
			for x := x0; x < x0+smoothCell && x < w; x++ {
				dx, dy := float64(x)-rg.cx, float64(y)-rg.cy
				cxx += dx * dx
				cxy += dx * dy
				cyy += dy * dy
				if x >= 1 && x < w-1 && y >= 1 && y < h-1 {
					gx, gy := sobelAtFast(lum, w, x, y)
					ggx += gx
					ggy += gy
				}
			}
		}
	}
	cxx /= n
	cxy /= n
	cyy /= n
	th := 0.5 * math.Atan2(2*cxy, cxx-cyy)
	ca, sa := math.Cos(th), math.Sin(th)
	var maxU, maxV float64
	for _, ci := range rg.cells {
		dx := (float64(ci%cw)+0.5)*smoothCell - rg.cx
		dy := (float64(ci/cw)+0.5)*smoothCell - rg.cy
		if u := math.Abs(dx*ca + dy*sa); u > maxU {
			maxU = u
		}
		if v := math.Abs(-dx*sa + dy*ca); v > maxV {
			maxV = v
		}
	}
	rg.deg = th * 180 / math.Pi
	rg.hu = maxU + smoothCell/2 + smoothPad
	rg.hv = maxV + smoothCell/2 + smoothPad
	gth := math.Atan2(ggy, ggx)
	rg.gdeg = gth * 180 / math.Pi
	gc, gs := math.Cos(gth), math.Sin(gth)
	var maxGU, maxGV float64
	for _, ci := range rg.cells {
		dx := (float64(ci%cw)+0.5)*smoothCell - rg.cx
		dy := (float64(ci/cw)+0.5)*smoothCell - rg.cy
		if u := math.Abs(dx*gc + dy*gs); u > maxGU {
			maxGU = u
		}
		if v := math.Abs(-dx*gs + dy*gc); v > maxGV {
			maxGV = v
		}
	}
	rg.hgu = maxGU + smoothCell/2 + smoothPad
	rg.hgv = maxGV + smoothCell/2 + smoothPad
}

// split partitions the region's cells across its LONGER principal axis through the centroid.
func (rg *smoothRegion) split(cw int) (smoothRegion, smoothRegion) {
	th := rg.deg * math.Pi / 180
	ax, ay := math.Cos(th), math.Sin(th)
	if rg.hv > rg.hu {
		ax, ay = -ay, ax // cut across the longer extent
	}
	var a, b smoothRegion
	for _, ci := range rg.cells {
		dx := (float64(ci%cw)+0.5)*smoothCell - rg.cx
		dy := (float64(ci/cw)+0.5)*smoothCell - rg.cy
		if dx*ax+dy*ay > 0 {
			a.cells = append(a.cells, ci)
		} else {
			b.cells = append(b.cells, ci)
		}
	}
	return a, b
}

// buildCellSel rasterizes cell membership into a per-pixel selection mask for the region-local solve.
func buildCellSel(cells []int, cw, w, h int) []bool {
	sel := make([]bool, w*h)
	for _, ci := range cells {
		x0, y0 := (ci%cw)*smoothCell, (ci/cw)*smoothCell
		for y := y0; y < y0+smoothCell && y < h; y++ {
			row := y * w
			for x := x0; x < x0+smoothCell && x < w; x++ {
				sel[row+x] = true
			}
		}
	}
	return sel
}

// smoothClaimStack greedily grows the best jointly-solved stack for a region: every round re-solves
// ALL colours with each menu candidate appended and keeps the argmin, accepting only rounds that
// deepen the stack's ΔSSE by ≥ smoothLayerFrac. Returns the final candidates (colours solved),
// the stack's exact ΔSSE, and the share of it carried by the gradient layers.
func (r *run) smoothClaimStack(canvas, weight []float32, rg *smoothRegion, sel []bool) ([]model.Candidate, float64, float64) {
	target := r.be.Target()
	w, h := r.w, r.h
	menu := r.smoothMenu(rg)
	if len(menu) == 0 {
		return nil, 0, 0
	}

	before := regionSSE(canvas, target, weight, sel)
	if before <= 0 {
		return nil, 0, 0
	}

	var stack []model.Candidate
	bestDelta := 0.0
	used := make([]bool, len(menu))
	for len(stack) < smoothMaxLayers {
		bestI := -1
		var bestCols []model.RGBA
		roundBest := bestDelta
		residual := before + bestDelta
		for i, cand := range menu {
			if used[i] {
				continue
			}
			trial := append(append([]model.Candidate(nil), stack...), cand)
			cols, d, ok := solveStack(canvas, target, weight, w, h, trial, smoothBudget, sel, smoothSpill)
			if !ok || d >= roundBest {
				continue
			}
			// The layer must cut a material fraction of the region's REMAINING error.
			if d-bestDelta > -smoothLayerGain*residual {
				continue
			}
			roundBest, bestI, bestCols = d, i, cols
		}
		if bestI < 0 {
			break
		}
		used[bestI] = true
		stack = append(stack, menu[bestI])
		for k := range stack {
			stack[k].Color = bestCols[k]
		}
		bestDelta = roundBest
	}
	if len(stack) == 0 || bestDelta >= 0 {
		smdbg("  dry: px=%d before=%.1f", rg.px, before)
		return nil, 0, 0
	}

	// Exact full-res re-measure with the final geometry; keep the exact colours.
	cols, dExact, ok := solveStack(canvas, target, weight, w, h, stack, 0, sel, smoothSpill)
	if !ok || dExact >= 0 {
		return nil, 0, 0
	}
	for k := range stack {
		stack[k].Color = cols[k]
	}
	// Anti-region-fill: the gradient layers must cut a material fraction of what the hard base
	// leaves behind — a hard-only claim is a flat fill the greedy handles without pre-placement.
	var hardOnly []model.Candidate
	for _, l := range stack {
		if !isSoftKind(l.Kind) {
			hardOnly = append(hardOnly, l)
		}
	}
	if len(hardOnly) == len(stack) {
		smdbg("  hard-only stack: px=%d Δ=%.1f", rg.px, dExact)
		return nil, 0, 0
	}
	soft := dExact
	if len(hardOnly) > 0 {
		dHard := 0.0
		if _, dh, okH := solveStack(canvas, target, weight, w, h, hardOnly, smoothBudget, sel, smoothSpill); okH && dh < 0 {
			dHard = dh
		}
		soft = dExact - dHard
		if soft > -smoothSoftGain*(before+dHard) {
			smdbg("  soft too weak: px=%d Δ=%.1f hard=%.1f soft=%.1f residual=%.1f", rg.px, dExact, dHard, soft, before+dHard)
			return nil, 0, 0
		}
	}
	return stack, dExact, soft
}

// regionSSE returns the weighted SSE between canvas and target over the selected pixels.
func regionSSE(canvas, target, weight []float32, sel []bool) float64 {
	var s float64
	for i, in := range sel {
		if !in {
			continue
		}
		wgt := float64(weight[i])
		if wgt <= 0 {
			continue
		}
		p := i * 4
		for c := 0; c < 4; c++ {
			d := float64(canvas[p+c] - target[p+c])
			s += wgt * d * d
		}
	}
	return s
}

// smoothMenu builds the region's candidate layers: opaque hard bases on the moment frame plus
// gradient primitives — the linear ramp word along ± the gradient axis, arc bands, and the native
// radial falloffs. All geometry is fixed; only colours are solved.
func (r *run) smoothMenu(rg *smoothRegion) []model.Candidate {
	var menu []model.Candidate
	frame := func(k model.ShapeKind, sx, sy float64) model.Candidate {
		return model.Candidate{Kind: k, Color: model.RGBA{A: 1},
			P: [6]float32{float32(rg.cx), float32(rg.cy), float32(rg.hu * sx), float32(rg.hv * sy), float32(rg.deg), 0}}
	}
	menu = append(menu,
		frame(model.KindRectangle, 1, 1),
		frame(model.KindEllipse, 1.2, 1.2),
		frame(model.KindGlow, 1.5, 1.5),
		frame(model.KindDisk, 1.15, 1.15),
	)
	if r.glyphs {
		for _, word := range smoothWords {
			kind, ok := model.MaskKind(word)
			if !ok {
				continue
			}
			// The word's u axis (ramp direction) runs along the region's mean gradient; the frame
			// extents are the region's projections onto that axis. Both orientations offered.
			for _, a2 := range [2]float64{rg.gdeg, rg.gdeg + 180} {
				if wc, wok := maskFrameFit(kind, rg.cx, rg.cy, rg.hgu, rg.hgv, a2); wok {
					menu = append(menu, wc)
				}
			}
		}
	}
	return menu
}

func isSoftKind(k model.ShapeKind) bool {
	return k == model.KindGlow || k == model.KindDisk || model.IsMask(k)
}
