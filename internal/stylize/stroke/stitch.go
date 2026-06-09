package stroke

import (
	"math"
	"sort"
)

// stitchThroughJunctions merges skeleton branches that meet at a node into longer strokes by
// good-continuation: where branch-ends share a node, the two whose OUTWARD tangents are most nearly
// opposite (the straightest path through the crossing) are linked, so a stroke runs THROUGH the junction
// instead of being cut at it (Noris et al. 2013, "Topology-Driven Vectorization of Clean Line Drawings";
// the junction-continuation principle). Ends with no near-straight partner terminate, so sharp corners
// (eyelash tips, hair) stay split. Turns the fragmented per-branch skeleton (thousands of short pieces)
// into few long coherent polylines. maxTurnDeg = how far from straight a through-link may bend (smaller =
// only near-straight crossings bridge). Deterministic.
func stitchThroughJunctions(branches [][][2]float64, maxTurnDeg float64) [][][2]float64 {
	n := len(branches)
	if n == 0 {
		return branches
	}
	minDot := math.Cos((180 - maxTurnDeg) * math.Pi / 180) // pair requires out·out ≤ minDot (≈ -1 = straight)

	type end struct{ br, side int } // side: 0=head, 1=tail
	ends := make([]end, 0, 2*n)
	endOf := make([][2]int, n) // br -> [headEndIdx, tailEndIdx]; [-1,-1] for a degenerate branch
	for i, br := range branches {
		if len(br) < 2 {
			endOf[i] = [2]int{-1, -1}
			continue
		}
		endOf[i] = [2]int{len(ends), len(ends) + 1}
		ends = append(ends, end{i, 0}, end{i, 1})
	}
	posOf := func(e end) [2]float64 {
		br := branches[e.br]
		if e.side == 0 {
			return br[0]
		}
		return br[len(br)-1]
	}
	// outDir = unit tangent at the end, pointing OUT of the branch (interior → end).
	outDir := func(e end) [2]float64 {
		br := branches[e.br]
		m := len(br)
		k := 4
		if k > m-1 {
			k = m - 1
		}
		var ex, ey, ix, iy float64
		if e.side == 0 {
			ex, ey, ix, iy = br[0][0], br[0][1], br[k][0], br[k][1]
		} else {
			ex, ey, ix, iy = br[m-1][0], br[m-1][1], br[m-1-k][0], br[m-1-k][1]
		}
		dx, dy := ex-ix, ey-iy
		if l := math.Hypot(dx, dy); l > 1e-9 {
			dx, dy = dx/l, dy/l
		}
		return [2]float64{dx, dy}
	}
	pix := func(p [2]float64) [2]int { return [2]int{int(math.Round(p[0])), int(math.Round(p[1]))} }

	byNode := map[[2]int][]int{}
	outs := make([][2]float64, len(ends))
	for ei := range ends {
		k := pix(posOf(ends[ei]))
		byNode[k] = append(byNode[k], ei)
		outs[ei] = outDir(ends[ei])
	}
	nodes := make([][2]int, 0, len(byNode))
	for k := range byNode {
		nodes = append(nodes, k)
	}
	sort.Slice(nodes, func(a, b int) bool {
		if nodes[a][0] != nodes[b][0] {
			return nodes[a][0] < nodes[b][0]
		}
		return nodes[a][1] < nodes[b][1]
	})

	partner := make([]int, len(ends))
	for i := range partner {
		partner[i] = -1
	}
	for _, k := range nodes {
		es := byNode[k]
		if len(es) < 2 {
			continue
		}
		type cand struct {
			a, b int
			dot  float64
		}
		var cs []cand
		for a := 0; a < len(es); a++ {
			for b := a + 1; b < len(es); b++ {
				if ends[es[a]].br == ends[es[b]].br {
					continue // don't fuse a branch's two ends to itself
				}
				d := outs[es[a]][0]*outs[es[b]][0] + outs[es[a]][1]*outs[es[b]][1]
				if d <= minDot {
					cs = append(cs, cand{es[a], es[b], d})
				}
			}
		}
		sort.Slice(cs, func(i, j int) bool { // most-opposite first, then stable by index
			if cs[i].dot != cs[j].dot {
				return cs[i].dot < cs[j].dot
			}
			if cs[i].a != cs[j].a {
				return cs[i].a < cs[j].a
			}
			return cs[i].b < cs[j].b
		})
		for _, c := range cs {
			if partner[c.a] == -1 && partner[c.b] == -1 {
				partner[c.a] = c.b
				partner[c.b] = c.a
			}
		}
	}

	used := make([]bool, n)
	var result [][][2]float64
	trace := func(startEi int) {
		var poly [][2]float64
		cur := ends[startEi]
		first := true
		for {
			if endOf[cur.br][0] < 0 || used[cur.br] {
				break
			}
			used[cur.br] = true
			br := branches[cur.br]
			var exitEi int
			if cur.side == 0 { // entered at head → travel head→tail
				for k := 0; k < len(br); k++ {
					if first || k > 0 { // drop the shared node point when chaining
						poly = append(poly, br[k])
					}
				}
				exitEi = endOf[cur.br][1]
			} else { // entered at tail → travel tail→head
				for k := len(br) - 1; k >= 0; k-- {
					if first || k < len(br)-1 {
						poly = append(poly, br[k])
					}
				}
				exitEi = endOf[cur.br][0]
			}
			first = false
			pe := partner[exitEi]
			if pe == -1 {
				break
			}
			cur = ends[pe]
		}
		if len(poly) >= 2 {
			result = append(result, poly)
		}
	}
	for ei := range ends { // start from terminals (unpaired ends) for maximal strokes
		if partner[ei] == -1 && !used[ends[ei].br] {
			trace(ei)
		}
	}
	for i := 0; i < n; i++ { // any branch left over sits in a pure cycle
		if endOf[i][0] >= 0 && !used[i] {
			trace(endOf[i][0])
		}
	}
	return result
}

// dropShortPolys removes polylines with fewer than minLen points (post-stitch spur cleanup). At 1px
// skeleton resolution point-count ≈ pixel length, matching traceSkeleton's original minLen semantics.
func dropShortPolys(polys [][][2]float64, minLen int) [][][2]float64 {
	out := polys[:0]
	for _, p := range polys {
		if len(p) >= minLen {
			out = append(out, p)
		}
	}
	return out
}
