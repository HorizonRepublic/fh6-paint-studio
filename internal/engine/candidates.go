package engine

import (
	"math"
	"math/rand"
	"runtime"
	"sync"

	"fh6-paint-studio/internal/model"
)

// ErrorSampler turns a GPU/CPU error grid into a CDF for O(log n) importance sampling.
type ErrorSampler struct {
	gridW, gridH int
	imgW, imgH   int
	cdf          []float64
	total        float64
}

func NewErrorSampler(grid []float32, gridW, gridH, imgW, imgH int) *ErrorSampler {
	cdf := make([]float64, len(grid))
	var total float64
	for i, v := range grid {
		if v < 0 {
			v = 0
		}
		total += float64(v)
		cdf[i] = total
	}
	return &ErrorSampler{gridW, gridH, imgW, imgH, cdf, total}
}

func (s *ErrorSampler) Sample(rng *rand.Rand) (float32, float32) {
	if s.total <= 0 {
		return rng.Float32() * float32(s.imgW), rng.Float32() * float32(s.imgH)
	}
	u := rng.Float64() * s.total
	lo, hi := 0, len(s.cdf)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if s.cdf[mid] < u {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	gx, gy := lo%s.gridW, lo/s.gridW
	x0 := gx * s.imgW / s.gridW
	x1 := (gx + 1) * s.imgW / s.gridW
	y0 := gy * s.imgH / s.gridH
	y1 := (gy + 1) * s.imgH / s.gridH
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}
	return float32(x0) + rng.Float32()*float32(x1-x0), float32(y0) + rng.Float32()*float32(y1-y0)
}

func randRange(rng *rand.Rand, lo, hi float32) float32 { return lo + (hi-lo)*rng.Float32() }

// RandomShapes seeds candidates with error-biased centers; geometry is randomized
// per kind, color left zero (the backend computes the optimal color). When orient
// is non-nil (len w*h, degrees), elongated kinds (ellipse/rectangle) are seeded
// with their long axis along the local edge orientation instead of a random angle.
//
// weights (parallel to kinds, may be nil) biases the per-candidate kind choice.
// Ellipse-dominant weights (e.g. 0.8/0.1/0.1) avoid the faceted "low-poly" noise
// that a uniform mix of triangles/rectangles produces in smooth regions, while
// still letting straight-edged kinds win the score where they genuinely fit.
//
// allowAlpha lets candidates be semi-transparent: alpha ~U(alphaMin,1) instead of
// forced opaque. Many soft layers build smooth gradients/fur in far fewer shapes (and
// the livery editor natively supports 8-bit per-layer alpha). Opaque-only
// (allowAlpha=false) is kept for cutout images, where the reconstructed object must
// stay fully opaque.
func RandomShapes(rng *rand.Rand, w, h, count int, kinds []model.ShapeKind, weights []float32, s *ErrorSampler, progress float32, orient, coh []float32, aspectCap float32, allowAlpha bool, alphaMin, aspectMax float32, bc *boundaryCtx, kg *kindGate) []model.Candidate {
	if len(kinds) == 0 {
		kinds = []model.ShapeKind{model.KindEllipse}
	}
	kindCDF := buildKindCDF(kinds, weights)
	maxR := annealMaxR(w, h, progress)
	out := make([]model.Candidate, count)
	// Candidate generation is embarrassingly parallel. For large batches (deep
	// search, up to ~1M/shape) the single-threaded RNG loop would dominate
	// runtime, so fan out across cores. Determinism is preserved per (seed,
	// workers): we draw one sub-seed per worker from rng, so a given run is
	// reproducible (though the exact stream differs from the serial path).
	workers := runtime.NumCPU()
	if workers > 1 && count >= 4096 {
		seeds := make([]int64, workers)
		for i := range seeds {
			seeds[i] = rng.Int63()
		}
		chunk := (count + workers - 1) / workers
		var wg sync.WaitGroup
		for wk := 0; wk < workers; wk++ {
			lo := wk * chunk
			hi := lo + chunk
			if lo >= count {
				break
			}
			if hi > count {
				hi = count
			}
			wg.Add(1)
			go func(lo, hi int, seed int64) {
				defer wg.Done()
				r := rand.New(rand.NewSource(seed))
				for i := lo; i < hi; i++ {
					out[i] = genCandidate(r, w, h, kinds, kindCDF, s, maxR, orient, coh, aspectCap, allowAlpha, alphaMin, aspectMax, progress, bc, kg)
				}
			}(lo, hi, seeds[wk])
		}
		wg.Wait()
		return out
	}
	for i := 0; i < count; i++ {
		out[i] = genCandidate(rng, w, h, kinds, kindCDF, s, maxR, orient, coh, aspectCap, allowAlpha, alphaMin, aspectMax, progress, bc, kg)
	}
	return out
}

// genCandidate produces one error-biased, kind-weighted, orientation-seeded
// candidate (color left zero; the backend solves the optimal color).
func genCandidate(r *rand.Rand, w, h int, kinds []model.ShapeKind, kindCDF []float32, s *ErrorSampler, maxR float32, orient, coh []float32, aspectCap float32, allowAlpha bool, alphaMin, aspectMax, progress float32, bc *boundaryCtx, kg *kindGate) model.Candidate {
	x, y := s.Sample(r)
	x = clampF(x, 0, float32(w-1))
	y = clampF(y, 0, float32(h-1))
	// Boundary-aware radius: cap this candidate's size by its centre's distance to the
	// nearest target boundary, so it can't balloon across an edge (ramps in past start).
	if bc != nil && bc.dist != nil {
		if idx := int(y)*w + int(x); idx >= 0 && idx < len(bc.dist) {
			maxR = boundaryRadiusCap(maxR, bc.dist[idx], bc.padding, progress, bc.start)
		}
	}
	theta := r.Float32() * 360
	if orient != nil {
		idx := int(y)*w + int(x)
		if idx >= 0 && idx < len(orient) {
			theta = orient[idx] + randRange(r, -20, 20) // seed along edge, small jitter
			// Coherence prior: where the structure tensor is confident the jitter narrows and the
			// candidate is drawn elongated ALONG that direction; where it is not, the angle means
			// nothing, so the jitter opens up and the shape stays round. Seeding orientation without
			// this treats a flat region's noise angle as if it were an edge.
			if coh != nil && aspectCap > 1 && idx < len(coh) {
				k := coh[idx]
				theta = orient[idx] + randRange(r, -20, 20)*(1-0.75*k)
				// REPLACES the preset's global aspect rather than widening it. The global value is
				// applied everywhere today, including flat regions where the seeding angle is noise,
				// so taking a maximum here would leave that exact case untouched and make the prior
				// a no-op wherever the preset is already permissive.
				aspectMax = 1 + k*(aspectCap-1)
			}
		}
	}
	alpha := float32(1)
	if allowAlpha {
		alpha = randRange(r, alphaMin, 1)
	}
	c := randomShapeOfKind(r, kg.pick(r, x, y, kinds, kindCDF), x, y, maxR, float32(w), float32(h), theta, alpha, aspectMax)
	kg.bigGlowSwap(r, &c)
	return c
}

// buildKindCDF returns a cumulative-weight table over kinds for weighted random
// selection. nil/mismatched/non-positive weights fall back to a uniform CDF.
func buildKindCDF(kinds []model.ShapeKind, weights []float32) []float32 {
	cdf := make([]float32, len(kinds))
	var sum float32
	useW := len(weights) == len(kinds)
	for i := range kinds {
		w := float32(1)
		if useW {
			if weights[i] < 0 {
				w = 0
			} else {
				w = weights[i]
			}
		}
		sum += w
		cdf[i] = sum
	}
	if sum <= 0 { // all-zero weights: revert to uniform
		for i := range cdf {
			cdf[i] = float32(i + 1)
		}
	}
	return cdf
}

// containsKind reports whether k is in the kind set.
func containsKind(kinds []model.ShapeKind, k model.ShapeKind) bool {
	for _, kk := range kinds {
		if kk == k {
			return true
		}
	}
	return false
}

// pickKind draws a kind index from the cumulative table.
func pickKind(rng *rand.Rand, kinds []model.ShapeKind, cdf []float32) model.ShapeKind {
	if len(kinds) == 1 {
		return kinds[0]
	}
	u := rng.Float32() * cdf[len(cdf)-1]
	for i, c := range cdf {
		if u < c {
			return kinds[i]
		}
	}
	return kinds[len(kinds)-1]
}

// aspectMax (>1) biases ellipse/rectangle generation toward THIN, ELONGATED shapes:
// the minor axis is drawn as major/U(1,aspectMax), so the shape becomes a sliver whose
// MAJOR axis lies along theta (the orientation-seeded edge direction). Slivers laid along
// the edge trace sharp contours in few shapes (a typical sliver is ~8:1). With aspectMax<=1
// the axes are independent (round-ish), better for smooth/photo content where slivers add
// faceting noise.
func randomShapeOfKind(rng *rand.Rand, kind model.ShapeKind, cx, cy, maxR, w, h, theta, alpha, aspectMax float32) model.Candidate {
	c := model.Candidate{Kind: kind, Color: model.RGBA{A: alpha}}
	switch kind {
	case model.KindRectangle:
		hw := randRange(rng, 1, maxR)
		hh := randRange(rng, 1, maxR)
		if aspectMax > 1 {
			hh = maxF(0.5, hw/randRange(rng, 1, aspectMax))
		}
		c.P = [6]float32{cx, cy, hw, hh, theta, 0}
	case model.KindTriangle:
		r := randRange(rng, 4, maxR)
		c.P = [6]float32{
			clampF(cx+randRange(rng, -r, r), 0, w-1), clampF(cy+randRange(rng, -r, r), 0, h-1),
			clampF(cx+randRange(rng, -r, r), 0, w-1), clampF(cy+randRange(rng, -r, r), 0, h-1),
			clampF(cx+randRange(rng, -r, r), 0, w-1), clampF(cy+randRange(rng, -r, r), 0, h-1),
		}
	case model.KindLine:
		r := randRange(rng, 4, maxR)
		c.P = [6]float32{
			clampF(cx+randRange(rng, -r, r), 0, w-1), clampF(cy+randRange(rng, -r, r), 0, h-1),
			clampF(cx+randRange(rng, -r, r), 0, w-1), clampF(cy+randRange(rng, -r, r), 0, h-1),
			randRange(rng, 1, maxF(1, maxR*0.25)), 0,
		}
	default: // ellipse
		rx := randRange(rng, 2, maxR)
		ry := randRange(rng, 2, maxR)
		if aspectMax > 1 {
			ry = maxF(1, rx/randRange(rng, 1, aspectMax))
		}
		c.P = [6]float32{cx, cy, rx, ry, theta, 0}
	}
	return c
}

// RandomEllipses is a Segment-1 compatibility wrapper for ellipse-only generation.
func RandomEllipses(rng *rand.Rand, w, h, count int, s *ErrorSampler, progress float32) []model.Candidate {
	return RandomShapes(rng, w, h, count, []model.ShapeKind{model.KindEllipse}, nil, s, progress, nil, nil, 0, false, 1, 0, nil, nil)
}

// MutateShape perturbs the geometry of a base candidate per kind (color is
// recomputed by the backend on each evaluation). When allowAlpha is set, the
// shape's alpha is also perturbed within [alphaMin,1] — the hill climb then tunes
// transparency alongside geometry (semi-transparent layers for smooth gradients).
func MutateShape(rng *rand.Rand, base model.Candidate, count int, w, h, moveStep, radiusStep float32, allowAlpha bool, alphaMin float32) []model.Candidate {
	out := make([]model.Candidate, 0, count)
	for i := 0; i < count; i++ {
		c := base
		if model.IsMask(base.Kind) {
			// Mask stamp [cx,cy,Hx,Hy,rot,skew]: extents are SIGNED (negative Hx = mirror) — jitter
			// their magnitude, keep the sign and the skew.
			c.P[0] = clampF(c.P[0]+randRange(rng, -moveStep, moveStep), 0, w-1)
			c.P[1] = clampF(c.P[1]+randRange(rng, -moveStep, moveStep), 0, h-1)
			for _, j := range [2]int{2, 3} {
				mag := c.P[j]
				if mag < 0 {
					mag = -mag
				}
				mag = maxF(2, mag+randRange(rng, -radiusStep, radiusStep))
				if c.P[j] < 0 {
					mag = -mag
				}
				c.P[j] = mag
			}
			c.P[4] += randRange(rng, -30, 30)
			if allowAlpha {
				c.Color.A = clampF(c.Color.A+randRange(rng, -0.1, 0.1), alphaMin, 1)
			}
			out = append(out, c)
			continue
		}
		switch base.Kind {
		case model.KindTriangle:
			for j := 0; j < 6; j += 2 {
				c.P[j] = clampF(c.P[j]+randRange(rng, -moveStep, moveStep), 0, w-1)
				c.P[j+1] = clampF(c.P[j+1]+randRange(rng, -moveStep, moveStep), 0, h-1)
			}
		case model.KindLine:
			c.P[0] = clampF(c.P[0]+randRange(rng, -moveStep, moveStep), 0, w-1)
			c.P[1] = clampF(c.P[1]+randRange(rng, -moveStep, moveStep), 0, h-1)
			c.P[2] = clampF(c.P[2]+randRange(rng, -moveStep, moveStep), 0, w-1)
			c.P[3] = clampF(c.P[3]+randRange(rng, -moveStep, moveStep), 0, h-1)
			c.P[4] = maxF(1, c.P[4]+randRange(rng, -radiusStep, radiusStep))
		default: // ellipse or rectangle: center + two extents + rotation
			c.P[0] = clampF(c.P[0]+randRange(rng, -moveStep, moveStep), 0, w-1)
			c.P[1] = clampF(c.P[1]+randRange(rng, -moveStep, moveStep), 0, h-1)
			c.P[2] = maxF(1, c.P[2]+randRange(rng, -radiusStep, radiusStep))
			c.P[3] = maxF(1, c.P[3]+randRange(rng, -radiusStep, radiusStep))
			c.P[4] += randRange(rng, -30, 30)
		}
		if allowAlpha {
			c.Color.A = clampF(c.Color.A+randRange(rng, -0.1, 0.1), alphaMin, 1)
		}
		out = append(out, c)
	}
	return out
}

// boundaryCtx carries the boundary-aware radius parameters into candidate generation.
// A nil *boundaryCtx disables the cap. dist is the per-pixel distance-to-boundary field
// (metric.BoundaryDistance, len w*h, in pixels); padding is how far past a boundary a
// shape may still reach; start is the progress at which the cap begins to engage.
type boundaryCtx struct {
	dist    []float32
	padding float32
	start   float32
}

// boundaryRadiusCap returns the maximum candidate radius allowed at a center whose
// distance to the nearest target boundary is `dist`, so a shape near an edge can't
// balloon ACROSS it. The cap RAMPS IN past `start` progress:
// before start it returns maxR unchanged; at progress 1 it returns min(maxR, dist+padding);
// between, it lerps maxR->cap so the coarse base stage still lays large covering shapes and
// only late detail is constrained. Identical formula on the host and device paths.
func boundaryRadiusCap(maxR, dist, padding, progress, start float32) float32 {
	mix := boundaryMix(progress, start)
	if mix <= 0 {
		return maxR
	}
	lim := dist + padding
	if lim >= maxR {
		return maxR
	}
	return maxR + (lim-maxR)*mix // lerp loose->tight as the run finishes
}

// boundaryMix is the boundary-cap ramp factor: 0 before `start` progress (no cap),
// rising linearly to 1 at progress 1. Shared by the host cap (boundaryRadiusCap) and the
// device generator (passed as boundMix to fp_search_random) so both apply the SAME lerp.
func boundaryMix(progress, start float32) float32 {
	if progress < start || start >= 1 {
		return 0
	}
	m := (progress - start) / (1 - start)
	if m > 1 {
		m = 1
	}
	return m
}

// detailBias ramps the detail-sampling strength from 0 at `start` progress to the full
// `strength` at progress 1. It returns 0 when disabled
// (strength<=0), before `start`, or when start>=1 — so the bias engages only LATE, after
// the coarse base is laid, and grows as the run finishes on fine detail.
func detailBias(progress, start, strength float32) float32 {
	if strength <= 0 || progress < start || start >= 1 {
		return 0
	}
	t := (progress - start) / (1 - start)
	if t > 1 {
		t = 1
	}
	return strength * t
}

// blendDetailGrid returns the error grid scaled per-cell by (1 + s*detail): importance
// sampling then favours detailed cells THAT STILL CARRY ERROR (a fully-solved cell stays
// at 0, so we never dump shapes on already-perfect detail). It only steers WHERE candidate
// centres land — the per-candidate ΔSSE scoring and error accounting are untouched — so the
// optimum is unchanged, only the search's spatial focus shifts. Returns err unchanged when
// s<=0 or the detail map size mismatches.
func blendDetailGrid(err, detail []float32, s float32) []float32 {
	if s <= 0 || len(detail) != len(err) {
		return err
	}
	out := make([]float32, len(err))
	for i, e := range err {
		if e < 0 {
			e = 0
		}
		out[i] = e * (1 + s*detail[i])
	}
	return out
}

func clampF(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
func maxF(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
func minF(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

// clampCandidatesToCanvas shrinks each ellipse/rect candidate's half-extents so its rotated bounding
// box stays within the canvas, plus a margin of padFrac*min(w,h) px on each side. Centers are already
// clamped in-canvas (genCandidate), but the RADII are not, so a shape near an edge can balloon far
// outside it — invisible in the W×H preview (the buffer clips), but drawn in full in-game where there
// is no clip. Applied to the host batch BEFORE pickBest, so selection sees the in-canvas geometry.
// padFrac<=0 is a no-op (disabled). Triangles/lines have vertex-clamped geometry, so they're skipped.
func clampCandidatesToCanvas(cands []model.Candidate, w, h, padFrac float32) {
	if padFrac <= 0 {
		return
	}
	pad := padFrac * minF(w, h)
	for i := range cands {
		c := &cands[i]
		switch c.Kind {
		case model.KindEllipse, model.KindRectangle, model.KindGlow, model.KindDisk:
			// elliptical/rect footprint with P[2],P[3] half-extents — uniformly shrink to fit
		default:
			continue // triangles/lines are vertex-clamped already
		}
		c.P[2], c.P[3] = clampExtents(c.P[0], c.P[1], c.P[2], c.P[3], c.P[4], w, h, pad)
	}
}

// clampExtents returns half-extents (a,b = rx,ry or hw,hh) scaled down uniformly so the rotated bbox
// of a shape centred at (cx,cy), angle thetaDeg, fits within [-pad, w+pad] x [-pad, h+pad]. Uniform
// scaling preserves the shape's aspect and angle. Returns the input unchanged when it already fits.
func clampExtents(cx, cy, a, b, thetaDeg, w, h, pad float32) (float32, float32) {
	th := float64(thetaDeg) * math.Pi / 180
	cs, sn := float32(math.Cos(th)), float32(math.Sin(th))
	hx := float32(math.Hypot(float64(a*cs), float64(b*sn))) // rotated bbox half-width
	hy := float32(math.Hypot(float64(a*sn), float64(b*cs))) // rotated bbox half-height
	allowX := maxF(1, minF(cx, w-cx)+pad)
	allowY := maxF(1, minF(cy, h-cy)+pad)
	scale := float32(1)
	if hx > allowX {
		scale = minF(scale, allowX/hx)
	}
	if hy > allowY {
		scale = minF(scale, allowY/hy)
	}
	if scale < 1 {
		return a * scale, b * scale
	}
	return a, b
}
