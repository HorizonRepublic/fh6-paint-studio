package engine

import (
	"math"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// backgroundCanvas builds a w*h*4 RGBA buffer filled with the background color (opaque).
func backgroundCanvas(bg model.RGBA, w, h int) []float32 {
	c := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		c[i*4+0], c[i*4+1], c[i*4+2], c[i*4+3] = bg.R, bg.G, bg.B, 1
	}
	return c
}

// shapeToCandidate reconstructs a renderable Candidate from a serialized Shape.
func shapeToCandidate(s model.Shape) model.Candidate {
	c := model.Candidate{Kind: model.KindFromType(s.Type), P: model.ParamsFromShape(s)}
	if len(s.Color) >= 4 {
		// DecChan decodes the stored sRGB byte to the working space (linear in -linear mode,
		// sRGB otherwise) so the backend re-renders in the same space it scored. Alpha straight.
		c.Color = model.RGBA{
			R: model.DecChan(s.Color[0]), G: model.DecChan(s.Color[1]),
			B: model.DecChan(s.Color[2]), A: float32(s.Color[3]) / 255,
		}
	}
	return c
}

// recolorVisible repaints each opaque shape with the weighted-mean target color
// over the pixels it owns (is the topmost opaque layer for). Mutates shapes in
// place and returns it. Semi-transparent shapes are left untouched and do not
// claim ownership (their compositing is not a simple replace).
func recolorVisible(shapes []model.Shape, target, weight []float32, w, h int, varSkip float64) []model.Shape {
	if len(shapes) == 0 {
		return shapes
	}
	owner := make([]int32, w*h)
	for i := range owner {
		owner[i] = -1
	}
	for j := len(shapes) - 1; j >= 0; j-- {
		if !opaqueShape(shapes[j]) {
			continue
		}
		kind := model.KindFromType(shapes[j].Type)
		p := model.ParamsFromShape(shapes[j])
		xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				idx := y*w + x
				if owner[idx] == -1 && raster.Inside(kind, p, x, y) {
					owner[idx] = int32(j)
				}
			}
		}
	}
	n := len(shapes)
	sumW := make([]float64, n)
	sumR := make([]float64, n)
	sumG := make([]float64, n)
	sumB := make([]float64, n)
	sumR2 := make([]float64, n)
	sumG2 := make([]float64, n)
	sumB2 := make([]float64, n)
	for idx := 0; idx < w*h; idx++ {
		j := owner[idx]
		if j < 0 {
			continue
		}
		wt := float64(weight[idx])
		p := idx * 4
		tr, tg, tb := float64(target[p]), float64(target[p+1]), float64(target[p+2])
		sumW[j] += wt
		sumR[j] += wt * tr
		sumG[j] += wt * tg
		sumB[j] += wt * tb
		sumR2[j] += wt * tr * tr
		sumG2[j] += wt * tg * tg
		sumB2[j] += wt * tb * tb
	}
	for j := range shapes {
		if sumW[j] <= 0 || !opaqueShape(shapes[j]) {
			continue
		}
		inv := 1.0 / sumW[j]
		mr, mg, mb := sumR[j]*inv, sumG[j]*inv, sumB[j]*inv
		// Variance-aware skip: a shape whose OWNED target pixels span two colors (high
		// variance — e.g. a thin fur sliver straddling a red-mane / dark-spike boundary)
		// would be FLATTENED to a muddy mean by the weighted-mean repaint. Keep its existing
		// (greedy/polish-optimized) color instead so the boundary stays crisp. varSkip<=0 = off.
		if varSkip > 0 {
			variance := (sumR2[j]*inv - mr*mr) + (sumG2[j]*inv - mg*mg) + (sumB2[j]*inv - mb*mb)
			if variance > varSkip {
				continue
			}
		}
		shapes[j].Color[0] = model.EncByte(float32(mr))
		shapes[j].Color[1] = model.EncByte(float32(mg))
		shapes[j].Color[2] = model.EncByte(float32(mb))
	}
	return shapes
}

func opaqueShape(s model.Shape) bool {
	return len(s.Color) >= 4 && s.Color[3] >= 255
}

// pickBest evaluates a candidate batch and returns the lowest-score candidate
// (with the backend's optimal color merged in) and its RAW score. When penalty is
// non-nil, selection uses score+penalty(candidate) but the RAW score is returned
// for the accept threshold and error accounting — the penalty only biases WHICH
// shape wins, it must not pollute the accumulated error.
func pickBest(be backend.Backend, cands []model.Candidate, penalty func(model.Candidate) float32) (model.Candidate, float32) {
	res, _ := be.Evaluate(cands)
	if len(res) == 0 {
		return model.Candidate{}, math.MaxFloat32
	}
	bi := 0
	bestAdj := res[0].Score
	if penalty != nil {
		bestAdj += penalty(cands[0])
	}
	for i := 1; i < len(res); i++ {
		adj := res[i].Score
		if penalty != nil {
			adj += penalty(cands[i])
		}
		if adj < bestAdj {
			bi, bestAdj = i, adj
		}
	}
	best := cands[bi]
	best.Color = res[bi].Color
	return best, res[bi].Score
}

// compactPenalty biases the per-shape pick away from large shapes. The first few
// accepted shapes (the coarse base layers) are penalized HARD (span²) so the engine
// does not slap down a giant blob that later shapes must repaint over; once the base
// composition exists, the penalty is mild (linear). span is the shape's half-extent
// in px. Selection-only (see pickBest).
func compactPenalty(c model.Candidate, shapeCount, w, h int) float32 {
	xMin, yMin, xMax, yMax := raster.BBox(c.Kind, c.P, w, h)
	dw, dh := xMax-xMin, yMax-yMin
	span := dw
	if dh > span {
		span = dh
	}
	hs := float32(span) * 0.5 // half-extent ~ radius scale
	if hs < 96 {
		return 0
	}
	if shapeCount < 8 {
		return hs * hs * 0.1
	}
	return hs * 0.05
}

func planHillClimb(budget int) (rounds, perRound int) {
	if budget <= 0 {
		return 0, 0
	}
	// Fewer, WIDER hill-climb rounds (budget/128 ≈ 39 rounds of ~128 candidates for the
	// default 5000 mutate budget) beat many narrow rounds on both quality and speed. Wider
	// per-round breadth finds better local steps, ~39 sequential steps still refine enough,
	// and halving the round count halves the host round-trips per shape — the mutate phase is
	// bound by host round-trip latency, not GPU compute. div=256 (≈19 rounds) is too coarse,
	// so 128 is the sweet spot.
	rounds = budget / 128
	if rounds < 1 {
		rounds = 1
	}
	if rounds > 128 {
		rounds = 128
	}
	perRound = budget / rounds
	if perRound < 1 {
		perRound = 1
	}
	return
}

// pruneOccluded removes shapes that contribute no visible pixels because later
// opaque shapes fully cover them. Kind-aware via the raster dispatch. The
// background (index 0) is always kept. Removing hidden shapes does not change
// the rendered canvas (they were invisible) but trims wasted layers under the
// game's ~3000-layer cap.
func pruneOccluded(shapes []model.Shape, w, h int) []model.Shape {
	if len(shapes) <= 1 {
		return shapes
	}
	cov := make([]bool, w*h)
	keep := make([]bool, len(shapes))
	keep[0] = true
	for j := len(shapes) - 1; j >= 1; j-- {
		s := shapes[j]
		kind := model.KindFromType(s.Type)
		p := model.ParamsFromShape(s)
		xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
		visible := false
		for y := yMin; y <= yMax && !visible; y++ {
			for x := xMin; x <= xMax; x++ {
				if raster.Inside(kind, p, x, y) && !cov[y*w+x] {
					visible = true
					break
				}
			}
		}
		keep[j] = visible
		opaque := len(s.Color) >= 4 && s.Color[3] >= 255
		if visible && opaque {
			for y := yMin; y <= yMax; y++ {
				for x := xMin; x <= xMax; x++ {
					if raster.Inside(kind, p, x, y) {
						cov[y*w+x] = true
					}
				}
			}
		}
	}
	out := make([]model.Shape, 0, len(shapes))
	for j, k := range keep {
		if k {
			out = append(out, shapes[j])
		}
	}
	return out
}

// kneeDetector implements the auto-shape-count stop rule: it tracks the total error
// after each accepted shape and reports when the per-shape RELATIVE marginal improvement
// — rate = (errBack - errNow) / (window · errNow), i.e. how much each shape reduces error
// relative to what REMAINS — has stayed below tol for `sustain` shapes. The relative
// (÷ current error) formulation normalizes the wildly different error scales across content
// (logo err ~114k vs fox ~689), so one tolerance adapts per image: genuinely-saturated
// flat/logo content stops at ~175-400 shapes, detailed photo/anime/cartoon fill the budget.
// tol 2e-4 = conservative (trims only saturated content), 5e-4 = aggressive/draft.
// tol<=0 disables (fill the StopAt budget).
type kneeDetector struct {
	tol          float64
	win, sustain int
	hist         []float64
	since        int // shape count when the rate first dropped below tol (-1 = above)
}

func newKneeDetector(tol float64) *kneeDetector {
	return &kneeDetector{tol: tol, win: 100, sustain: 200, since: -1}
}

// push records the current total error after placing a shape and reports whether the
// knee (sustained diminishing returns) has been reached.
func (k *kneeDetector) push(curErr float64) bool {
	if k.tol <= 0 {
		return false
	}
	k.hist = append(k.hist, curErr)
	n := len(k.hist)
	if n <= k.win || curErr <= 0 {
		return false
	}
	back := k.hist[n-1-k.win]
	rate := (back - curErr) / (float64(k.win) * curErr)
	if rate < k.tol {
		if k.since < 0 {
			k.since = n
		} else if n-k.since >= k.sustain {
			return true
		}
	} else {
		k.since = -1
	}
	return false
}

func sumGrid(g []float32) float64 {
	var s float64
	for _, v := range g {
		s += float64(v)
	}
	return s
}
func seed(s int64) int64 {
	if s != 0 {
		return s
	}
	return 1
}
