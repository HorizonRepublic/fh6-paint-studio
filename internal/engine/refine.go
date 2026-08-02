package engine

import (
	"sort"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// shapeContributions computes, for each shape, how much weighted SSE it removes
// versus what would show if it were deleted (the next opaque shape below it, or
// the background). Uses a two-deep ownership buffer (topmost + second opaque
// shape per pixel), which is exact for non-overlapping coverage and a good
// estimate under overlap. shapes[0] (background) is never scored (always kept).
//
// transparent: cutout mode — pixels with no shape below fall back to "empty"
// (full target energy = the shape is critical), not a solid bg color.
func shapeContributions(shapes []model.Shape, target, weight []float32, w, h int, bg model.RGBA, transparent bool) []float64 {
	n := len(shapes)
	owner1 := make([]int32, w*h)
	owner2 := make([]int32, w*h)
	for i := range owner1 {
		owner1[i] = -1
		owner2[i] = -1
	}
	for j := n - 1; j >= 1; j-- {
		if !opaqueShape(shapes[j]) {
			continue
		}
		kind := model.KindFromType(shapes[j].Type)
		p := model.ParamsFromShape(shapes[j])
		xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				idx := y*w + x
				if owner1[idx] != -1 && owner2[idx] != -1 {
					continue
				}
				if !raster.Inside(kind, p, x, y) {
					continue
				}
				if owner1[idx] == -1 {
					owner1[idx] = int32(j)
				} else if owner2[idx] == -1 {
					owner2[idx] = int32(j)
				}
			}
		}
	}

	cr := make([]float64, n)
	cg := make([]float64, n)
	cb := make([]float64, n)
	for j := 0; j < n; j++ {
		if len(shapes[j].Color) >= 3 {
			cr[j] = float64(shapes[j].Color[0]) / 255
			cg[j] = float64(shapes[j].Color[1]) / 255
			cb[j] = float64(shapes[j].Color[2]) / 255
		}
	}
	bgr, bgg, bgb := float64(bg.R), float64(bg.G), float64(bg.B)

	contrib := make([]float64, n)
	for idx := 0; idx < w*h; idx++ {
		j := owner1[idx]
		if j < 1 {
			continue
		}
		p := idx * 4
		tr, tg, tb := float64(target[p]), float64(target[p+1]), float64(target[p+2])
		wt := float64(weight[idx])

		// Error with shape j present.
		dr, dg, db := tr-cr[j], tg-cg[j], tb-cb[j]
		sseWith := dr*dr + dg*dg + db*db

		// Error if j were removed: show owner2, or the fallback below.
		var rr, rg, rb float64
		if o2 := owner2[idx]; o2 >= 1 {
			rr, rg, rb = cr[o2], cg[o2], cb[o2]
		} else if transparent {
			rr, rg, rb = 0, 0, 0 // empty: full target energy
		} else {
			rr, rg, rb = bgr, bgg, bgb
		}
		dr, dg, db = tr-rr, tg-rg, tb-rb
		sseWithout := dr*dr + dg*dg + db*db

		contrib[j] += wt * (sseWithout - sseWith)
	}
	return contrib
}

// pruneToBudget keeps the background plus the up-to-`budget` highest-contribution shapes,
// preserving z-order. It keeps ALL placed shapes when they fit the budget — ONLY the budget cap
// drops any (the highest-contribution subset survives). It deliberately does NOT drop zero/
// negative contributors: contribution is computed PRE-polish, and the joint polish that runs
// afterwards makes those apparently-redundant shapes useful, so dropping them early hurts
// content the greedy over-places. Full occlusion is handled separately, earlier, by pruneOccluded.
func pruneToBudget(shapes []model.Shape, target, weight []float32, w, h, budget int, bg model.RGBA, transparent bool) []model.Shape {
	if len(shapes) <= 1 {
		return shapes
	}
	contrib := shapeContributions(shapes, target, weight, w, h, bg, transparent)

	type ranked struct {
		idx int
		c   float64
	}
	cand := make([]ranked, 0, len(shapes)-1)
	for j := 1; j < len(shapes); j++ {
		cand = append(cand, ranked{j, contrib[j]}) // keep all; only the budget cap below drops any
	}
	sort.Slice(cand, func(a, b int) bool { return cand[a].c > cand[b].c })
	if budget > 0 && len(cand) > budget {
		cand = cand[:budget]
	}

	keep := make([]bool, len(shapes))
	keep[0] = true
	for _, r := range cand {
		keep[r.idx] = true
	}
	out := make([]model.Shape, 0, len(cand)+1)
	for j := range shapes {
		if keep[j] {
			out = append(out, shapes[j])
		}
	}
	return out
}

// PruneToBudgetBlend prunes a shape list (shapes[0] = background, kept) to at most budget TOTAL
// shapes by the alpha-aware top-2-owner contribution rank, preserving z-order. Experiment API for
// the O&R over-provision→prune A/B (debug tooling); the in-run Overdraw path stays opaque-only.
func PruneToBudgetBlend(shapes []model.Shape, target, weight []float32, w, h, budget int, bg model.RGBA, transparent bool) []model.Shape {
	if len(shapes) <= 1 || budget <= 1 || len(shapes) <= budget {
		return shapes
	}
	contrib := shapeContributionsBlend(shapes, target, weight, w, h, bg, transparent)
	type ranked struct {
		idx int
		c   float64
	}
	cand := make([]ranked, 0, len(shapes)-1)
	for j := 1; j < len(shapes); j++ {
		cand = append(cand, ranked{j, contrib[j]})
	}
	sort.Slice(cand, func(a, b int) bool { return cand[a].c > cand[b].c })
	cand = cand[:budget-1]
	keep := make([]bool, len(shapes))
	keep[0] = true
	for _, r := range cand {
		keep[r.idx] = true
	}
	out := make([]model.Shape, 0, budget)
	for j := range shapes {
		if keep[j] {
			out = append(out, shapes[j])
		}
	}
	return out
}
