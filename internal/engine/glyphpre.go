package engine

import (
	"math"
	"sort"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// Glyph pre-pass (Options.GlyphPrepass, opt-in): claim dictionary-shaped features on the TARGET
// before the greedy runs — the way the ink pass claims lines. The per-step glyph competition
// measured as an architectural dead end (a free-rotation primitive wins every single-step argmin),
// but a whole feature that IS a dictionary silhouette is exactly one word: flat-colour connected
// components of the full-resolution target are signature-matched against the bank, verified by
// strict IoU between the placed word's coverage and the component, and the survivors are applied as
// the FIRST shapes. The greedy then builds the rest on the true residual.

const (
	prepassMinArea  = 200  // px² — ignore specks
	prepassMaxFrac  = 0.25 // of the canvas — ignore background-scale components
	prepassIoU      = 0.80 // placed word vs component, hard shape-truth gate
	prepassMaxClaim = 60   // shapes the pre-pass may spend (budget safety)
	prepassTopK     = 5    // words tried per component (the IoU gate rejects wrong matches)

	prepassFringeMax = 50   // px² — components this small are AA fringe, absorbed by colour
	prepassMergeTol  = 0.05 // sqrt-linear mean distance for merging bin-split neighbours
	prepassAbsorbTol = 0.12 // sqrt-linear pixel-vs-mean cap for fringe absorption
	prepassRefineIoU = 0.45 // initial IoU below this is a wrong word — skip refinement
)

// prepassStatsT is debug-only demand instrumentation for the pre-pass (enabled by tests).
type prepassStatsT struct {
	comps, window, thin, evalFail, claimed int
	iouBest                                []float64    // best IoU over topK per unclaimed window comp
	detail                                 []PrepassRec // per window comp
}

// PrepassRec is one window component's outcome (debug hooks only).
type PrepassRec struct {
	Area, X0, Y0, X1, Y1 int
	IoU                  float64
	Word                 uint16
	Claimed              bool
}

var prepassStats *prepassStatsT

// GlyphPrepassDemandStart / GlyphPrepassDemandReport are temporary debug hooks for measuring
// where the pre-pass loses claims (labeling vs matching vs the IoU gate).
func GlyphPrepassDemandStart() { prepassStats = &prepassStatsT{} }

func GlyphPrepassDemandReport() (comps, window, thin, evalFail, claimed int, iouBest []float64, detail []PrepassRec) {
	st := prepassStats
	prepassStats = nil
	if st == nil {
		return
	}
	return st.comps, st.window, st.thin, st.evalFail, st.claimed, st.iouBest, st.detail
}

// glyphPrepass labels flat-colour components, matches each against the dictionary and applies the
// IoU-verified winners. Returns the number of shapes claimed.
func (r *run) glyphPrepass() int {
	words := glyphBank()
	if len(words) == 0 {
		return 0
	}
	target := r.be.Target()
	w, h := r.w, r.h
	labels, comps := labelFlatComponents(target, w, h)
	if len(comps) == 0 {
		return 0
	}
	// biggest first: large features benefit most from a single-word claim
	sort.Slice(comps, func(a, b int) bool { return comps[a].area > comps[b].area })

	if prepassStats != nil {
		prepassStats.comps = len(comps)
	}
	claimed := 0
	for ci := range comps {
		if claimed >= prepassMaxClaim || len(r.shapes)-1 >= r.genTarget {
			break
		}
		c := &comps[ci]
		if c.area < prepassMinArea || float64(c.area) > prepassMaxFrac*float64(w*h) {
			continue
		}
		if prepassStats != nil {
			prepassStats.window++
		}
		// full-resolution signature of the component
		var acc sigAcc
		for y := c.y0; y <= c.y1; y++ {
			for x := c.x0; x <= c.x1; x++ {
				if labels[y*w+x] == c.id {
					acc.add(float64(x)+0.5-c.cx, float64(y)+0.5-c.cy, 1)
				}
			}
		}
		blob, ok := acc.sig()
		if !ok || blob.rms < 4 {
			if prepassStats != nil {
				prepassStats.thin++
			}
			continue
		}
		type m struct {
			wi, shift, mirror int
			d                 float64
		}
		best := make([]m, 0, len(words))
		for wi := range words {
			mm := m{wi: wi, d: math.Inf(1)}
			for mir := 0; mir < 2; mir++ {
				sg := &words[wi].sig[mir]
				for sh := 0; sh < glyphBins; sh++ {
					if d := sigDist(&blob, sg, sh); d < mm.d {
						mm.d, mm.shift, mm.mirror = d, sh, mir
					}
				}
			}
			best = append(best, mm)
		}
		sort.Slice(best, func(a, b int) bool { return best[a].d < best[b].d })

		bestIoU, won := 0.0, false
		var wonKind model.ShapeKind
		for k := 0; k < prepassTopK && k < len(best); k++ {
			mm := best[k]
			gw := &words[mm.wi]
			s := blob.rms / gw.sig[mm.mirror].rms
			hx, hy := s*gw.nativeW, s*gw.nativeH
			if hx < 4 || hy < 4 || hx > 2.5*float64(w) || hy > 2.5*float64(h) {
				continue
			}
			mir, ccx := 1.0, gw.cx
			if mm.mirror == 1 {
				mir, ccx = -1, -gw.cx
			}
			// sigDist matches blob bin i against word bin i+shift, so a blob rotated by θ
			// is found at shift = -θ·bins/360: the placement rotation negates the shift
			rot := float64((glyphBins-mm.shift)%glyphBins) * (360.0 / glyphBins)
			rad := rot * math.Pi / 180
			co, sn := math.Cos(rad), math.Sin(rad)
			ox, oy := s*ccx, s*gw.cy
			cand := model.Candidate{
				Kind:  gw.kind,
				Color: model.RGBA{A: 1},
				P: [6]float32{
					float32(c.cx - (co*ox - sn*oy)), float32(c.cy - (sn*ox + co*oy)),
					float32(mir * hx), float32(hy), float32(rot), 0,
				},
			}
			iou := compIoU(cand, labels, c, w, h)
			if iou >= prepassRefineIoU {
				cand, iou = refinePlacement(cand, iou, labels, c, w, h)
			}
			if iou > bestIoU {
				bestIoU = iou
			}
			if iou < prepassIoU {
				continue
			}
			res, err := r.be.Evaluate([]model.Candidate{cand})
			if err != nil || len(res) == 0 || res[0].Score >= 0 {
				if prepassStats != nil {
					prepassStats.evalFail++
				}
				continue
			}
			cand.Color = res[0].Color
			_ = r.be.Apply(cand)
			r.shapes = append(r.shapes, cand.ToShape(float64(res[0].Score)))
			claimed++
			won = true
			wonKind = gw.kind
			break
		}
		if prepassStats != nil {
			if won {
				prepassStats.claimed++
			} else {
				prepassStats.iouBest = append(prepassStats.iouBest, bestIoU)
			}
			prepassStats.detail = append(prepassStats.detail, PrepassRec{
				Area: c.area, X0: c.x0, Y0: c.y0, X1: c.x1, Y1: c.y1,
				IoU: bestIoU, Word: uint16(wonKind), Claimed: won,
			})
		}
	}
	if claimed > 0 {
		r.grid, r.gw, r.gh, _ = r.be.ErrorGrid()
		r.sampler = NewErrorSampler(r.grid, r.gw, r.gh, r.w, r.h)
	}
	return claimed
}

// refinePlacement polishes a near-miss placement by coordinate descent on the IoU itself:
// the initial guess is coarse by construction (rotation = 15° signature bin, scale = rms ratio,
// position = centroid), so a correct word often lands at IoU 0.6-0.75 purely from placement
// error. Two rounds over rotation, isotropic scale and position; each axis keeps its argmax.
// A wrong word stays below the gate — IoU rewards whole-silhouette agreement only.
func refinePlacement(cand model.Candidate, iou float64, labels []int32, c *flatComp, w, h int) (model.Candidate, float64) {
	best, cur := iou, cand
	for round := 0; round < 2; round++ {
		step := float32(2.0) // degrees
		if round == 1 {
			step = 1
		}
		for _, d := range [6]float32{-3, -2, -1, 1, 2, 3} {
			t := cur
			t.P[4] += d * step
			if v := compIoU(t, labels, c, w, h); v > best {
				best, cur = v, t
			}
		}
		for _, s := range [4]float32{0.94, 0.97, 1.03, 1.06} {
			t := cur
			t.P[2] *= s
			t.P[3] *= s
			if v := compIoU(t, labels, c, w, h); v > best {
				best, cur = v, t
			}
		}
		// words stretch legally in-game; the signature scale assumed the native aspect
		for _, s := range [4]float32{0.92, 0.96, 1.04, 1.08} {
			t := cur
			t.P[2] *= s
			t.P[3] /= s
			if v := compIoU(t, labels, c, w, h); v > best {
				best, cur = v, t
			}
		}
		px := float32(1.0)
		if round == 0 {
			px = 2
		}
		for _, d := range [4][2]float32{{-px, 0}, {px, 0}, {0, -px}, {0, px}} {
			t := cur
			t.P[0] += d[0]
			t.P[1] += d[1]
			if v := compIoU(t, labels, c, w, h); v > best {
				best, cur = v, t
			}
		}
	}
	return cur, best
}

// compIoU samples the component bbox (padded) and compares the placed word's coverage against the
// component mask — the shape-truth gate.
func compIoU(cand model.Candidate, labels []int32, c *flatComp, w, h int) float64 {
	pad := 4
	x0, y0 := c.x0-pad, c.y0-pad
	x1, y1 := c.x1+pad, c.y1+pad
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > w-1 {
		x1 = w - 1
	}
	if y1 > h-1 {
		y1 = h - 1
	}
	step := 1
	for (x1-x0+1)/step*(y1-y0+1)/step > 20000 {
		step++
	}
	var inter, union int
	for y := y0; y <= y1; y += step {
		for x := x0; x <= x1; x += step {
			in := labels[y*w+x] == c.id
			cov := raster.Coverage(cand.Kind, cand.P, x, y) >= 0.5
			if in && cov {
				inter++
			}
			if in || cov {
				union++
			}
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

type flatComp struct {
	id             int32
	area           int
	x0, y0, x1, y1 int
	cx, cy         float64
}

// labelFlatComponents labels near-uniform colour regions of the target in four passes:
// exact quantized-key cores (4-connected, opaque pixels only), then a union-find merge of
// adjacent cores whose mean colours are within prepassMergeTol (quantization splits a flat
// feature whose colour sits on a bin boundary), then absorption of AA-fringe specks into the
// nearest-by-colour adjacent core (anti-aliasing otherwise erodes the silhouette by ~1px —
// a direct IoU tax on small features), and finally a dense relabel with fresh stats.
// Colour distances are in sqrt-linear space so dark tones are not crushed by linear light.
func labelFlatComponents(target []float32, w, h int) ([]int32, []flatComp) {
	key := func(i int) uint32 {
		if target[i*4+3] < 0.5 {
			return 0xFFFFFFFF
		}
		q := func(v float32) uint32 {
			k := uint32(v * 14)
			if k > 13 {
				k = 13
			}
			return k
		}
		return q(target[i*4])<<8 | q(target[i*4+1])<<4 | q(target[i*4+2])
	}
	labels := make([]int32, w*h)
	for i := range labels {
		labels[i] = -1
	}
	type core struct {
		area       int
		sr, sg, sb float64 // sqrt-linear colour sums
	}
	var cores []core
	stack := make([]int, 0, 1024)
	for start := 0; start < w*h; start++ {
		if labels[start] != -1 {
			continue
		}
		k := key(start)
		if k == 0xFFFFFFFF {
			labels[start] = -2
			continue
		}
		id := int32(len(cores))
		var c core
		stack = append(stack[:0], start)
		labels[start] = id
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x := i % w
			c.area++
			c.sr += math.Sqrt(float64(target[i*4]))
			c.sg += math.Sqrt(float64(target[i*4+1]))
			c.sb += math.Sqrt(float64(target[i*4+2]))
			for _, n := range [4]int{i - 1, i + 1, i - w, i + w} {
				if n < 0 || n >= w*h {
					continue
				}
				if (n == i-1 && x == 0) || (n == i+1 && x == w-1) {
					continue
				}
				if labels[n] == -1 && key(n) == k {
					labels[n] = id
					stack = append(stack, n)
				}
			}
		}
		cores = append(cores, c)
	}

	// union-find merge of bin-split neighbours (substantial cores only — fringe blends would
	// chain a feature into its background)
	parent := make([]int32, len(cores))
	for i := range parent {
		parent[i] = int32(i)
	}
	var find func(int32) int32
	find = func(i int32) int32 {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	meanDist := func(a, b *core) float64 {
		ia, ib := 1/float64(a.area), 1/float64(b.area)
		dr := a.sr*ia - b.sr*ib
		dg := a.sg*ia - b.sg*ib
		db := a.sb*ia - b.sb*ib
		return dr*dr + dg*dg + db*db
	}
	tryMerge := func(i, n int) {
		la, lb := labels[i], labels[n]
		if la < 0 || lb < 0 {
			return
		}
		ra, rb := find(la), find(lb)
		if ra == rb {
			return
		}
		ca, cb := &cores[ra], &cores[rb]
		if ca.area < prepassFringeMax || cb.area < prepassFringeMax {
			return
		}
		if meanDist(ca, cb) >= prepassMergeTol*prepassMergeTol {
			return
		}
		parent[rb] = ra
		ca.area += cb.area
		ca.sr += cb.sr
		ca.sg += cb.sg
		ca.sb += cb.sb
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if x+1 < w {
				tryMerge(i, i+1)
			}
			if y+1 < h {
				tryMerge(i, i+w)
			}
		}
	}
	for i := range labels {
		if labels[i] >= 0 {
			labels[i] = find(labels[i])
		}
	}

	// absorb AA fringe: pixels of tiny comps adopt the nearest-by-colour adjacent large comp,
	// in synchronous waves (fringe is 1-2px wide)
	for wave := 0; wave < 4; wave++ {
		type move struct {
			i  int
			to int32
		}
		var moves []move
		for i := 0; i < w*h; i++ {
			l := labels[i]
			if l < 0 || cores[l].area >= prepassFringeMax {
				continue
			}
			pr := math.Sqrt(float64(target[i*4]))
			pg := math.Sqrt(float64(target[i*4+1]))
			pb := math.Sqrt(float64(target[i*4+2]))
			bestD, bestL := prepassAbsorbTol*prepassAbsorbTol, int32(-1)
			x := i % w
			for _, n := range [4]int{i - 1, i + 1, i - w, i + w} {
				if n < 0 || n >= w*h {
					continue
				}
				if (n == i-1 && x == 0) || (n == i+1 && x == w-1) {
					continue
				}
				ln := labels[n]
				if ln < 0 || cores[ln].area < prepassFringeMax || ln == l {
					continue
				}
				c := &cores[ln]
				inv := 1 / float64(c.area)
				dr, dg, db := pr-c.sr*inv, pg-c.sg*inv, pb-c.sb*inv
				if d := dr*dr + dg*dg + db*db; d < bestD {
					bestD, bestL = d, ln
				}
			}
			if bestL >= 0 {
				moves = append(moves, move{i, bestL})
			}
		}
		if len(moves) == 0 {
			break
		}
		for _, m := range moves {
			old := labels[m.i]
			pr := math.Sqrt(float64(target[m.i*4]))
			pg := math.Sqrt(float64(target[m.i*4+1]))
			pb := math.Sqrt(float64(target[m.i*4+2]))
			cores[old].area--
			cores[old].sr -= pr
			cores[old].sg -= pg
			cores[old].sb -= pb
			labels[m.i] = m.to
			cores[m.to].area++
			cores[m.to].sr += pr
			cores[m.to].sg += pg
			cores[m.to].sb += pb
		}
	}

	// dense relabel + fresh stats
	remap := make(map[int32]int32, 64)
	var comps []flatComp
	for i := 0; i < w*h; i++ {
		l := labels[i]
		if l < 0 {
			continue
		}
		id, ok := remap[l]
		if !ok {
			id = int32(len(comps))
			remap[l] = id
			comps = append(comps, flatComp{id: id, x0: w, y0: h, x1: -1, y1: -1})
		}
		labels[i] = id
		c := &comps[id]
		x, y := i%w, i/w
		c.area++
		c.cx += float64(x) + 0.5
		c.cy += float64(y) + 0.5
		if x < c.x0 {
			c.x0 = x
		}
		if y < c.y0 {
			c.y0 = y
		}
		if x > c.x1 {
			c.x1 = x
		}
		if y > c.y1 {
			c.y1 = y
		}
	}
	for i := range comps {
		comps[i].cx /= float64(comps[i].area)
		comps[i].cy /= float64(comps[i].area)
	}
	return labels, comps
}
