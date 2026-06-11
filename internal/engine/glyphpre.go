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
	prepassTopK     = 3    // words tried per component
)

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

	claimed := 0
	for ci := range comps {
		if claimed >= prepassMaxClaim || len(r.shapes)-1 >= r.genTarget {
			break
		}
		c := &comps[ci]
		if c.area < prepassMinArea || float64(c.area) > prepassMaxFrac*float64(w*h) {
			continue
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
			rot := float64(mm.shift) * (360.0 / glyphBins)
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
			if compIoU(cand, labels, c, w, h) < prepassIoU {
				continue
			}
			res, err := r.be.Evaluate([]model.Candidate{cand})
			if err != nil || len(res) == 0 || res[0].Score >= 0 {
				continue
			}
			cand.Color = res[0].Color
			_ = r.be.Apply(cand)
			r.shapes = append(r.shapes, cand.ToShape(float64(res[0].Score)))
			claimed++
			break
		}
	}
	if claimed > 0 {
		r.grid, r.gw, r.gh, _ = r.be.ErrorGrid()
		r.sampler = NewErrorSampler(r.grid, r.gw, r.gh, r.w, r.h)
	}
	return claimed
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

// labelFlatComponents 4-connect labels near-uniform colour regions of the target (quantized linear
// RGB, opaque pixels only).
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
	var comps []flatComp
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
		id := int32(len(comps))
		comp := flatComp{id: id, x0: w, y0: h, x1: -1, y1: -1}
		var sx, sy float64
		stack = append(stack[:0], start)
		labels[start] = id
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := i%w, i/w
			comp.area++
			sx += float64(x) + 0.5
			sy += float64(y) + 0.5
			if x < comp.x0 {
				comp.x0 = x
			}
			if y < comp.y0 {
				comp.y0 = y
			}
			if x > comp.x1 {
				comp.x1 = x
			}
			if y > comp.y1 {
				comp.y1 = y
			}
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
		comp.cx = sx / float64(comp.area)
		comp.cy = sy / float64(comp.area)
		comps = append(comps, comp)
	}
	return labels, comps
}
