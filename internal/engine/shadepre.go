package engine

import (
	"fmt"
	"math"
	"os"
	"sort"

	"fh6-paint-studio/internal/model"
)

// Shading pre-pass (Options.ShadePrepass): claim LINEAR-RAMP regions of the target as a two-shape
// stack BEFORE the greedy — an opaque base rect + the bank's linear-gradient word on top. In linear
// light the stack composites to an exact linear interpolation between the two solved colours along
// the ramp axis, so one claim replaces the many translucent facets greedy spends approximating
// smooth shading (the SGLIVE finding, using the in-game gradient vocabulary no external tool has).
// Distinct from the region-fill bust: only regions with a COHERENT NON-ZERO gradient are claimed
// (never flat fills), and the ramp must beat the best flat cover by a margin, exact-scored by the
// backend. The claims are colour-only under polish (mask geometry frozen), like glyph claims.

const (
	shadeWord      = 2204 // the bank's smooth linear alpha ramp (a: 1→0 along mask +x)
	shadeCell      = 16
	shadeMagLo     = 0.0025 // Sobel-luma per-cell mean magnitude of a genuine shading ramp…
	shadeMagHi     = 0.3    // …up to (beyond = an edge, not shading)
	shadeCoherence = 0.75   // |mean ∇| / mean|∇| within a cell (real shading carries dither/JPEG noise)
	shadePairCoh   = 0.8    // direction agreement between grouped neighbour cells
	shadeEdgeMax   = 0.05   // cells with more line-work pixels than this belong to the greedy
	shadeEdgeTau   = 0.35
	shadeMinCells  = 6   // ≥ ~1.5k px regions
	shadeMaxClaim  = 6   // stack pairs per run (12 shapes)
	shadeGainFrac  = 0.3 // the ramp must carry ≥ this fraction of the stack's joint ΔSSE
	shadePad       = 6.0 // px of placement overshoot past the region extents

	shadeSolveBudget = 1 << 18 // sample budget of the strided joint colour solves
	shadePairEconomy = 1.3     // base+ramp must earn ≥ this × the ramp alone to spend the extra shape
)

var shadeDebug = os.Getenv("FH6_SHADE_DEBUG") != ""

func shdbg(format string, a ...interface{}) {
	if shadeDebug {
		fmt.Fprintf(os.Stderr, "[shadepre] "+format+"\n", a...)
	}
}

type shadeCellT struct {
	gx, gy, mag float64 // mean gradient vector + mean magnitude
	edge        float64 // line-work pixel fraction
	group       int32
}

// shadePrepass detects coherent linear-ramp regions and claims them as base-rect + ramp-word
// stacks. Returns the number of claimed stacks.
func (r *run) shadePrepass() int {
	kind, ok := model.MaskKind(shadeWord)
	if !ok {
		return 0
	}
	target := r.be.Target()
	w, h := r.w, r.h
	lum := make([]float32, w*h)
	lumaOf(target, w, h, lum)

	cw, ch := (w+shadeCell-1)/shadeCell, (h+shadeCell-1)/shadeCell
	cells := make([]shadeCellT, cw*ch)
	for i := range cells {
		cells[i].group = -1
	}
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			gx, gy := sobelAtFast(lum, w, x, y)
			m := math.Hypot(gx, gy)
			c := &cells[(y/shadeCell)*cw+x/shadeCell]
			c.gx += gx
			c.gy += gy
			c.mag += m
			if m > shadeEdgeTau {
				c.edge++
			}
		}
	}
	n := float64(shadeCell * shadeCell)
	ramp := func(c *shadeCellT) bool {
		mm := c.mag / n
		if mm < shadeMagLo || mm > shadeMagHi {
			return false
		}
		if c.edge/n > shadeEdgeMax {
			return false
		}
		return math.Hypot(c.gx, c.gy) >= shadeCoherence*c.mag
	}

	// BFS-group ramp cells whose mean directions agree.
	groups := 0
	for ci := range cells {
		if cells[ci].group >= 0 || !ramp(&cells[ci]) {
			continue
		}
		id := int32(groups)
		groups++
		queue := []int{ci}
		cells[ci].group = id
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			cx, cy := cur%cw, cur/cw
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := cx+d[0], cy+d[1]
				if nx < 0 || ny < 0 || nx >= cw || ny >= ch {
					continue
				}
				ni := ny*cw + nx
				nc := &cells[ni]
				if nc.group >= 0 || !ramp(nc) {
					continue
				}
				a, b := &cells[cur], nc
				dot := a.gx*b.gx + a.gy*b.gy
				if dot < shadePairCoh*math.Hypot(a.gx, a.gy)*math.Hypot(b.gx, b.gy) {
					continue
				}
				nc.group = id
				queue = append(queue, ni)
			}
		}
	}
	if groups == 0 {
		return 0
	}
	type region struct {
		cells          []int
		gx, gy         float64
		cx, cy, hx, hy float64
	}
	regs := make([]region, groups)
	for ci := range cells {
		if g := cells[ci].group; g >= 0 {
			regs[g].cells = append(regs[g].cells, ci)
			regs[g].gx += cells[ci].gx
			regs[g].gy += cells[ci].gy
		}
	}
	sort.Slice(regs, func(a, b int) bool { return len(regs[a].cells) > len(regs[b].cells) })

	canvasBuf := make([]float32, w*h*4)
	if r.be.ReadCanvas(canvasBuf) != nil {
		return 0
	}
	weight := r.be.Weight()

	claimed := 0
	for gi := range regs {
		rg := &regs[gi]
		if claimed >= shadeMaxClaim || len(r.shapes)-1 >= r.genTarget-2 {
			break
		}
		if len(rg.cells) < shadeMinCells {
			shdbg("stop at region %d: %d cells < %d (largest remaining)", gi, len(rg.cells), shadeMinCells)
			break // sorted by size — the rest are smaller
		}
		// Placement frame: ramp axis = mean gradient direction; extents = cell-centre projections.
		ang := math.Atan2(rg.gy, rg.gx)
		ca, sa := math.Cos(ang), math.Sin(ang)
		var sx, sy float64
		for _, ci := range rg.cells {
			sx += (float64(ci%cw) + 0.5) * shadeCell
			sy += (float64(ci/cw) + 0.5) * shadeCell
		}
		rg.cx, rg.cy = sx/float64(len(rg.cells)), sy/float64(len(rg.cells))
		var maxU, maxV float64
		for _, ci := range rg.cells {
			dx := (float64(ci%cw)+0.5)*shadeCell - rg.cx
			dy := (float64(ci/cw)+0.5)*shadeCell - rg.cy
			if u := math.Abs(dx*ca + dy*sa); u > maxU {
				maxU = u
			}
			if v := math.Abs(-dx*sa + dy*ca); v > maxV {
				maxV = v
			}
		}
		rg.hx = maxU + shadeCell/2 + shadePad
		rg.hy = maxV + shadeCell/2 + shadePad

		deg := ang * 180 / math.Pi
		// The pair's colours are solved JOINTLY (stacksolve.go): sequentially the base colour only
		// makes sense UNDER the ramp and scores positive alone, so the greedy solve never places it.
		// Economy rule: the base is worth its shape only when the pair earns ≥30% more than the
		// ramp alone (over a canvas already at the region's mean, the canvas itself is the second
		// interpolation endpoint).
		frame := [6]float32{float32(rg.cx), float32(rg.cy), float32(rg.hx), float32(rg.hy), float32(deg), 0}
		base := model.Candidate{Kind: model.KindRectangle, Color: model.RGBA{A: 1}, P: frame}
		// The word is placed by its ACTIVE coverage box (maskFrameFit — 2204 keeps a transparent
		// margin in its uv square). The ramp is directional: solve both orientations, keep the better.
		var colsP, colsW []model.RGBA
		dPair, dWord := math.Inf(1), math.Inf(1)
		var okP, okW bool
		var wordP, wordW model.Candidate
		for _, a2 := range [2]float64{deg, deg + 180} {
			wc, wok := maskFrameFit(kind, rg.cx, rg.cy, rg.hx, rg.hy, a2)
			if !wok {
				break
			}
			if cp, dp, ok := solveStack(canvasBuf, target, weight, w, h, []model.Candidate{base, wc}, shadeSolveBudget, nil, 0); ok && dp < dPair {
				colsP, dPair, okP, wordP = cp, dp, true, wc
			}
			if cw, dw, ok := solveStack(canvasBuf, target, weight, w, h, []model.Candidate{wc}, shadeSolveBudget, nil, 0); ok && dw < dWord {
				colsW, dWord, okW, wordW = cw, dw, true, wc
			}
		}
		usePair := okP && dPair < 0 && (!okW || dPair <= shadePairEconomy*dWord)
		if usePair {
			// Anti-region-fill: the ramp must carry ≥ frac of the stack's earn, else this is a flat
			// cover the greedy handles without pre-placement.
			_, dBase, okB := solveStack(canvasBuf, target, weight, w, h, []model.Candidate{base}, shadeSolveBudget, nil, 0)
			if !okB {
				dBase = 0
			}
			if dPair-math.Min(dBase, 0) > shadeGainFrac*dPair {
				usePair = false
			}
		}
		var stack []model.Candidate
		switch {
		case usePair:
			base.Color, wordP.Color = colsP[0], colsP[1]
			stack = []model.Candidate{base, wordP}
		case okW && dWord < 0:
			wordW.Color = colsW[0]
			stack = []model.Candidate{wordW}
		default:
			shdbg("region %d cells=%d REJECT ramp (pair=%.1f word=%.1f)", gi, len(rg.cells), dPair, dWord)
			continue
		}
		// Exact re-measure at full res with the final colours — the strided solve is an estimate.
		colsX, dExact, okX := solveStack(canvasBuf, target, weight, w, h, stack, 0, nil, 0)
		if !okX || dExact >= 0 {
			shdbg("region %d cells=%d REJECT ramp (exact=%.1f)", gi, len(rg.cells), dExact)
			continue
		}
		for k := range stack {
			stack[k].Color = colsX[k]
			_ = r.be.Apply(stack[k])
			r.shapes = append(r.shapes, stack[k].ToShape(dExact/float64(len(stack))))
		}
		if r.be.ReadCanvas(canvasBuf) != nil {
			return claimed
		}
		claimed++
		shdbg("region %d cells=%d CLAIM stack (layers=%d pair=%.1f word=%.1f exact=%.1f ang=%.0f° c=(%.0f,%.0f) ext=(%.0f,%.0f))",
			gi, len(rg.cells), len(stack), dPair, dWord, dExact, deg, rg.cx, rg.cy, rg.hx, rg.hy)
	}
	shdbg("done: groups=%d claimed=%d stacks", groups, claimed)
	return claimed
}
