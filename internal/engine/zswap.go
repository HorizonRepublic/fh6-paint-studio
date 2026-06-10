package engine

import (
	"sort"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// Z-order local swap — opt-in EXPERIMENT pass (ZSwapTrials > 0), after the polish trio, before
// the standout pass. Greedy fixes each shape's depth at placement time; on opaque content the
// stack order decides which shape owns every contested pixel, and a later small shape can sit
// UNDER an earlier large one only by being placed later — greedy never revisits that choice.
// This pass tries swapping z-adjacent pairs whose bounding boxes overlap, ranked by the current
// error under the contested region, accepting only swaps that LOWER the hard-rendered error
// (the pass invariant). Each trial is a full re-render through the backend — exact but costly —
// so ZSwapTrials caps the attempts; the ranking spends them where the residual is worst.
//
// Re-measure context: an earlier incarnation was error-neutral under alpha blending (a swap of
// two translucent shapes barely moves the composite); the opaque/mono pipeline landed later and
// is the regime where occlusion order genuinely matters. Verdict belongs to the flat/mono bench.

const zswapMinCells = 1 // skip pairs whose bbox intersection covers no error-grid cell

type zswapPass struct{}

func (zswapPass) enabled(opt Options) bool { return opt.ZSwapTrials > 0 }

func (zswapPass) apply(r *run) {
	r.setStatus("Reordering layers…")
	r.shapes, r.finalErr = zOrderSwap(r.be, r.shapes, r.finalErr, r.initCanvas, r.opt, r.w, r.h)
}

type zswapCand struct {
	j     int
	local float64
}

func zOrderSwap(be backend.Backend, shapes []model.Shape, finalErr float64, initCanvas []float32, opt Options, w, h int) ([]model.Shape, float64) {
	n := len(shapes)
	if opt.ZSwapTrials <= 0 || n < 3 {
		return shapes, finalErr
	}
	grid, gw, gh, err := be.ErrorGrid()
	if err != nil || gw <= 0 || gh <= 0 {
		return shapes, finalErr
	}

	type box struct{ x0, y0, x1, y1 int }
	boxes := make([]box, n)
	for j := 1; j < n; j++ {
		kind := model.KindFromType(shapes[j].Type)
		x0, y0, x1, y1 := raster.BBox(kind, model.ParamsFromShape(shapes[j]), w, h)
		boxes[j] = box{x0, y0, x1, y1}
	}

	cellW := float64(w) / float64(gw)
	cellH := float64(h) / float64(gh)
	cands := make([]zswapCand, 0, n)
	for j := 1; j < n-1; j++ {
		a, b := boxes[j], boxes[j+1]
		ix0, iy0 := maxInt(a.x0, b.x0), maxInt(a.y0, b.y0)
		ix1, iy1 := minInt(a.x1, b.x1), minInt(a.y1, b.y1)
		if ix0 > ix1 || iy0 > iy1 {
			continue
		}
		gx0, gy0 := int(float64(ix0)/cellW), int(float64(iy0)/cellH)
		gx1, gy1 := int(float64(ix1)/cellW), int(float64(iy1)/cellH)
		gx1, gy1 = minInt(gx1, gw-1), minInt(gy1, gh-1)
		var local float64
		cells := 0
		for gy := gy0; gy <= gy1; gy++ {
			for gx := gx0; gx <= gx1; gx++ {
				local += float64(grid[gy*gw+gx])
				cells++
			}
		}
		if cells < zswapMinCells || local <= 0 {
			continue
		}
		cands = append(cands, zswapCand{j: j, local: local})
	}
	if len(cands) == 0 {
		return shapes, finalErr
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].local > cands[b].local })
	if len(cands) > opt.ZSwapTrials {
		cands = cands[:opt.ZSwapTrials]
	}

	in0 := finalErr
	best := finalErr
	accepted := 0
	for _, c := range cands {
		j := c.j
		shapes[j], shapes[j+1] = shapes[j+1], shapes[j]
		if e := renderExcept(be, initCanvas, shapes, -1); e < best {
			best = e
			accepted++
		} else {
			shapes[j], shapes[j+1] = shapes[j+1], shapes[j]
		}
	}
	finalErr = renderExcept(be, initCanvas, shapes, -1)
	sdbg("zswap: %d/%d swaps accepted, err %.1f -> %.1f", accepted, len(cands), in0, finalErr)
	return shapes, finalErr
}
