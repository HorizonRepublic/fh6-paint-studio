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
		kind := model.KindFromType(shapes[j].Type)
		p := model.ParamsFromShape(shapes[j])
		if !opaqueShape(shapes[j]) {
			// A per-pixel-alpha shape (mask word / glow / disk) CONTESTS what shows beneath it:
			// an opaque shape under a gradient must keep its solved colour (a claim stack's base
			// only makes sense UNDER its ramp) — a weighted-mean repaint through the gradient
			// flattens the composite. Block ownership on the gradient's footprint.
			if raster.IsGradient(kind) {
				xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
				for y := yMin; y <= yMax; y++ {
					for x := xMin; x <= xMax; x++ {
						idx := y*w + x
						if owner[idx] == -1 && raster.Coverage(kind, p, x, y) > 0.02 {
							owner[idx] = -2
						}
					}
				}
			}
			continue
		}
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

// opaqueShape reports whether s composites as a full opaque REPLACE — the assumption behind
// recolorVisible / shapeContributions / pruneToBudget (a shape "owns" the topmost pixels it covers).
// A gradient kind (glow/disk) is NEVER an opaque replace even at colour-alpha 255: it carries a
// per-pixel radial falloff, so it blends. Excluding it keeps those opaque-only passes from
// mis-owning / mis-recolouring a glow (e.g. the Gaussian all-glow mode, or any future pass that
// introduces glows). No effect on the hard kinds (IsGradient is false), so behaviour is byte-identical.
func opaqueShape(s model.Shape) bool {
	return len(s.Color) >= 4 && s.Color[3] >= 255 && !raster.IsGradient(model.KindFromType(s.Type))
}

// pickBest evaluates a candidate batch and returns the lowest-score candidate
// (with the backend's optimal color merged in) and its RAW score. When penalty is
// non-nil, selection uses score+penalty(candidate) but the RAW score is returned
// for the accept threshold and error accounting — the penalty only biases WHICH
// shape wins, it must not pollute the accumulated error.
func pickBest(be backend.Backend, cands []model.Candidate, penalty func(model.Candidate) float32) (model.Candidate, float32) {
	res, err := be.Evaluate(cands)
	if err != nil || len(res) == 0 {
		// A backend (device) fault returns garbage scores; treat the batch as "no improving
		// candidate" so the greedy skips it cleanly instead of accepting corrupt data.
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

// candidateArea returns the shape's geometric coverage area in px² (the same coverage the
// progressive-sampling Score extrapolates over via step²), per kind. Used by the low-contrast
// gate to turn a raw ΔSSE into a per-pixel "contrast" signal. O(1) closed forms; mask/unknown
// kinds return 0 so the gate is skipped for them (never gate a primitive we can't size).
func candidateArea(c model.Candidate, w, h int) float64 {
	switch c.Kind {
	case model.KindRectangle:
		hw := math.Max(0.5, float64(c.P[2]))
		hh := math.Max(0.5, float64(c.P[3]))
		return 4 * hw * hh
	case model.KindTriangle:
		x1, y1, x2, y2, x3, y3 := float64(c.P[0]), float64(c.P[1]), float64(c.P[2]), float64(c.P[3]), float64(c.P[4]), float64(c.P[5])
		return 0.5 * math.Abs((x2-x1)*(y3-y1)-(x3-x1)*(y2-y1))
	case model.KindLine:
		length := math.Hypot(float64(c.P[2]-c.P[0]), float64(c.P[3]-c.P[1]))
		hwid := math.Max(0.5, float64(c.P[4]))
		return length*2*hwid + math.Pi*hwid*hwid // capsule: rectangle + two end caps
	case model.KindEllipse, model.KindGlow, model.KindDisk:
		rx := math.Max(1, float64(c.P[2]))
		ry := math.Max(1, float64(c.P[3]))
		return math.Pi * rx * ry
	default:
		return 0 // masks / unknown — skip the gate
	}
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
//
// floor (ShapeKneeFloor) fixes the relative rate's near-zero blow-up: as curErr→0 the ÷curErr
// rate explodes and the knee never trips, so clean line-art / fully-solved flats keep burning
// budget on imperceptible shapes. Flooring the denominator at floor·initialErr pins it to a fixed
// baseline once the recon beats that fraction, so the same tol trips on near-solved content while
// detailed photos (curErr ≫ floor·init) are unaffected. floor<=0 = pure relative (legacy).
type kneeDetector struct {
	tol, floor   float64
	init         float64
	win, sustain int
	hist         []float64
	since        int // shape count when the rate first dropped below tol (-1 = above)
}

func newKneeDetector(tol, floor, initialErr float64) *kneeDetector {
	return &kneeDetector{tol: tol, floor: floor, init: initialErr, win: 100, sustain: 200, since: -1}
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
	denom := curErr
	if k.floor > 0 && k.init > 0 {
		if f := k.floor * k.init; f > denom {
			denom = f
		}
	}
	rate := (back - curErr) / (float64(k.win) * denom)
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
