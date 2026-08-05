package engine

import (
	"math"

	"fh6-paint-studio/internal/model"
)

// Residual connected-component seeding (LIVE, arXiv 2206.04655 — the largest single win in their
// ablation, and the one mechanism from that literature that transfers to an OCCLUDING greedy
// unchanged, because a candidate here only JOINS the pool and still has to win the same exact score).
//
// Our moment seeding fits a covariance ellipse inside a LOCAL WINDOW around a sampled centre. A
// window has no relationship to the picture: it happily straddles two unrelated regions and returns
// the blob that fits their union, which is a shape the target never contained. A connected component
// of the residual is a region the image itself delimits, so the ellipse fitted to it is a shape the
// picture is actually asking for.
//
// The seeds are ADDITIVE — they are scored by the same backend evaluator against the same residual
// and only replace the search's own answer if they beat it, so the pass cannot lose to itself. What
// it can lose is wall time, which is why the pool is small and only built every few shapes.

// compSeedThresh is the fraction of the grid's MEAN cell error above which a cell counts as part of
// a component. Below the mean is background chatter; a threshold on the mean rather than an absolute
// value keeps the rule scale-free as the residual shrinks through the run.
const compSeedThresh = 1.5

// compSeeds fits one candidate per connected component of the residual grid, largest components
// first, and returns at most n of them. maxR caps the radii the same way the anneal caps the rest of
// the search, so a component spanning the whole canvas cannot propose a canvas-sized shape late in
// the run.
func compSeeds(grid []float32, gw, gh, imgW, imgH int, maxR float32, n int, kinds []model.ShapeKind,
	kindCDF []float32, alpha float32) []model.Candidate {
	if n <= 0 || gw <= 0 || gh <= 0 || len(grid) < gw*gh {
		return nil
	}
	var sum float64
	for i := 0; i < gw*gh; i++ {
		sum += float64(grid[i])
	}
	if sum <= 0 {
		return nil
	}
	thresh := float32(compSeedThresh * sum / float64(gw*gh))

	// Flood fill on 4-neighbours. The grid is small (a few thousand cells), so an explicit stack
	// costs nothing and keeps the recursion depth off the goroutine stack.
	label := make([]int32, gw*gh)
	for i := range label {
		label[i] = -1
	}
	type comp struct {
		mass                  float64
		sx, sy, sxx, syy, sxy float64
		cells                 int
	}
	var comps []comp
	stack := make([]int32, 0, 256)
	for start := 0; start < gw*gh; start++ {
		if label[start] >= 0 || grid[start] < thresh {
			continue
		}
		id := int32(len(comps))
		var c comp
		label[start] = id
		stack = append(stack[:0], int32(start))
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			gx, gy := int(p)%gw, int(p)/gw
			m := float64(grid[p])
			c.mass += m
			c.cells++
			fx, fy := float64(gx)+0.5, float64(gy)+0.5
			c.sx += m * fx
			c.sy += m * fy
			c.sxx += m * fx * fx
			c.syy += m * fy * fy
			c.sxy += m * fx * fy
			for _, q := range [4]int{int(p) - 1, int(p) + 1, int(p) - gw, int(p) + gw} {
				if q < 0 || q >= gw*gh || label[q] >= 0 || grid[q] < thresh {
					continue
				}
				// The horizontal neighbours must not wrap across a row edge.
				if (q == int(p)-1 && gx == 0) || (q == int(p)+1 && gx == gw-1) {
					continue
				}
				label[q] = id
				stack = append(stack, int32(q))
			}
		}
		comps = append(comps, c)
	}
	if len(comps) == 0 {
		return nil
	}
	// Largest mass first: the biggest unexplained region is where a shape buys the most.
	order := make([]int, len(comps))
	for i := range order {
		order[i] = i
	}
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && comps[order[j]].mass > comps[order[j-1]].mass; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}

	sx := float32(imgW) / float32(gw)
	sy := float32(imgH) / float32(gh)
	out := make([]model.Candidate, 0, n)
	for _, ci := range order {
		if len(out) >= n {
			break
		}
		c := comps[ci]
		if c.cells < 2 || c.mass <= 0 {
			continue
		}
		mx, my := c.sx/c.mass, c.sy/c.mass
		vxx := c.sxx/c.mass - mx*mx
		vyy := c.syy/c.mass - my*my
		vxy := c.sxy/c.mass - mx*my
		if vxx < 0 {
			vxx = 0
		}
		if vyy < 0 {
			vyy = 0
		}
		// Principal axes of the component's error mass. Two standard deviations covers it without
		// the tail that a full extent would include.
		tr, det := vxx+vyy, vxx*vyy-vxy*vxy
		disc := math.Sqrt(math.Max(0, tr*tr/4-det))
		l1, l2 := tr/2+disc, tr/2-disc
		if l2 < 0 {
			l2 = 0
		}
		theta := 0.5 * math.Atan2(2*vxy, vxx-vyy) * 180 / math.Pi
		rx := float32(2*math.Sqrt(l1)) * sx
		ry := float32(2*math.Sqrt(l2)) * sy
		if rx < 1 {
			rx = 1
		}
		if ry < 1 {
			ry = 1
		}
		if rx > maxR {
			rx = maxR
		}
		if ry > maxR {
			ry = maxR
		}
		cand := model.Candidate{
			Kind:  compSeedKind(kinds, kindCDF),
			P:     [6]float32{float32(mx) * sx, float32(my) * sy, rx, ry, float32(theta), 0},
			Color: model.RGBA{A: alpha},
		}
		if cand.Kind == model.KindTriangle {
			// A triangle has no centre/radius form; inscribe one in the same ellipse so the seed
			// still describes the component rather than a random wedge near it.
			cx, cy := cand.P[0], cand.P[1]
			cand.P = [6]float32{cx, cy - ry, cx - rx, cy + ry, cx + rx, cy + ry}
		}
		out = append(out, cand)
	}
	return out
}

// compSeedKind picks the seed's kind from the run's pool. Ellipse is the fallback: a component's
// second moment describes an ellipse, and forcing a different primitive onto it would test the
// primitive rather than the seeding.
func compSeedKind(kinds []model.ShapeKind, kindCDF []float32) model.ShapeKind {
	for _, k := range kinds {
		if k == model.KindEllipse {
			return model.KindEllipse
		}
	}
	if len(kinds) > 0 {
		return kinds[0]
	}
	return model.KindEllipse
}
