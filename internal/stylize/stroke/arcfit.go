package stroke

import (
	"math"
	"sort"

	"fh6-paint-studio/internal/model"
)

const deg2rad = math.Pi / 180

// emitOutline lays one simplified open polyline (bbox-local, offset ox,oy) as a mix of calibrated
// dictionary arcs (smooth curved runs) and thin rects (the straight rest), appending to *out up to
// budget.
func emitOutline(loop [][2]float64, ox, oy int, halfW float64, col []int, cfg Config, out *[]model.Shape, budget int) {
	n := len(loop)
	if n < 2 {
		return
	}
	P := make([][2]float64, n) // screen-space copy for circle fitting (open polyline)
	for i := range loop {
		P[i] = [2]float64{float64(ox) + loop[i][0], float64(oy) + loop[i][1]}
	}
	for i := 0; i < n-1 && len(*out) < budget; {
		j := i + 1
		if cfg.Arcs {
			j = growArc(P, i, cfg)
		}
		if cfg.Arcs && j-i >= 2 {
			if s, ok := placeArc(P, i, j, halfW, col, cfg); ok {
				*out = append(*out, s)
				i = j
				continue
			}
		}
		if s := strokeRect(ox, oy, loop[i], loop[i+1], halfW, col); s != nil {
			*out = append(*out, *s)
		}
		i++
	}
}

// growArc extends a run from i while it keeps fitting one circle (low residual) and curves the same way.
// Returns the last index of the run (== i+1 for a non-arc).
func growArc(P [][2]float64, i int, cfg Config) int {
	n := len(P)
	j := i + 1
	sign := 0
	for k := i + 2; k < n; k++ {
		ts := crossSign(sub(P[k-1], P[k-2]), sub(P[k], P[k-1]))
		if sign == 0 {
			sign = ts
		} else if ts != 0 && ts != sign {
			break
		}
		cx, cy, r, ok := fitCircle(P[i : k+1])
		if !ok || r < 3 || r > 1e5 {
			break
		}
		if maxResidual(P[i:k+1], cx, cy, r) > cfg.ArcTol {
			break
		}
		j = k
	}
	return j
}

// placeArc picks the dictionary arc that best matches the run i..j in BOTH sweep and rendered
// stroke width, and solves the similarity (pos, rotation, uniform scale, mirror) that lands the
// word's arc endpoints on the run's endpoints, bowing the same way. The catalog carries several
// stroke weights per sweep, so candidates rank by sweep distance + |log(renderedHW/halfW)|; a hard
// cap rejects strokes visibly fatter than the run (the line-art "gradient lines" artifact) and a
// floor rejects strokes too faint to carry the requested ink (a thin gradient word at small scale
// under-draws) — both fall back to thin rects.
func placeArc(P [][2]float64, i, j int, halfW float64, col []int, cfg Config) (model.Shape, bool) {
	sweep := runSweep(P, i, j)
	if sweep < cfg.MinSweep*deg2rad {
		return model.Shape{}, false
	}
	Pi, Pj, mid := P[i], P[j], P[(i+j)/2]
	chord := math.Hypot(Pj[0]-Pi[0], Pj[1]-Pi[1])
	if chord < 2 { // near-closed run: no usable chord
		return model.Shape{}, false
	}
	cSign := crossSign(sub(Pj, Pi), sub(mid, Pi))
	type scored struct {
		aw    arcWord
		score float64
	}
	var cands []scored
	inSweep := 0
	for _, aw := range arcCatalog() {
		dSweep := math.Abs(aw.sweep - sweep)
		if dSweep > 70*deg2rad {
			continue
		}
		inSweep++
		lW := math.Hypot(aw.b[0]-aw.a[0], aw.b[1]-aw.a[1])
		if lW < 1e-6 {
			continue
		}
		// Rank by sweep distance + the rendered-width mismatch (the catalog carries thin/mid/wide
		// stroke weights per sweep). Under-width weighs heavier than over-width — an under-inked
		// gradient stamp washes out, slight over-ink still reads as a line. Geometry safety does not
		// live here: the bow check below rejects any candidate whose bulge misses the contour.
		score := dSweep
		if aw.strokeHW > 0 {
			w := chord / lW * aw.strokeHW
			if w > 2*halfW+0.25 || w < 0.45*halfW {
				continue
			}
			pen := math.Abs(math.Log(w / halfW))
			if w < halfW {
				pen *= 1.6
			}
			score += 0.35 * pen
		}
		cands = append(cands, scored{aw, score})
	}
	if arcStats != nil && len(cands) == 0 {
		if inSweep == 0 {
			arcStats.noSweep++
		} else {
			arcStats.noWidth++
		}
		arcStats.failed = append(arcStats.failed, [3]float64{sweep / deg2rad, halfW, chord})
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].score < cands[b].score })
	// Bow tolerance scales with the chord: a fixed-px limit kills the smooth hair-curve fits that look
	// better than rect chains, while a relative one still rejects the big soft "lens" a sweep-mismatched
	// arc paints beside a long near-straight run (the worst line-art artifact).
	midTol := math.Max(math.Max(1.5, halfW), 0.035*chord)
	if arcStats != nil {
		arcStats.runs++
		arcStats.sweepSum += sweep / deg2rad
		arcStats.chordSum += chord
		arcStats.hwSum += halfW
	}
	for _, sc := range cands {
		aw := sc.aw
		for _, m := range [2]float64{1, -1} {
			A := [2]float64{m * aw.a[0], aw.a[1]}
			B := [2]float64{m * aw.b[0], aw.b[1]}
			Wm := [2]float64{m * aw.mid[0], aw.mid[1]}
			if crossSign(sub(B, A), sub(Wm, A)) != cSign {
				continue
			}
			dW, dS := sub(B, A), sub(Pj, Pi)
			s := chord / math.Hypot(dW[0], dW[1])
			rot := math.Atan2(dS[1], dS[0]) - math.Atan2(dW[1], dW[0])
			c, sn := math.Cos(rot), math.Sin(rot)
			pos := [2]float64{Pi[0] - s*(c*A[0]-sn*A[1]), Pi[1] - s*(sn*A[0]+c*A[1])}
			// Geometry verification: every run vertex AND every segment midpoint must lie on the
			// placed word's circle (within midTol). Anything weaker gets fooled: the raw middle
			// vertex wobbles with tracer noise; a fitted-circle midpoint lies for corner-rounded
			// runs; and a vertices-only check passes a 3-vertex CORNER (the circle through 3 points
			// is exact and the pinned endpoints sit on it by construction) whose straight legs the
			// stamp then crosses with a soft "lens" — the worst line-art artifact. The polyline
			// midpoints catch that: on a true arc they ride the curve, on a corner they sit on the
			// straight legs far from the bulge.
			wcx, wcy, wr, wok := circum3(A, B, Wm)
			if !wok {
				continue
			}
			ccx := pos[0] + s*(c*wcx-sn*wcy)
			ccy := pos[1] + s*(sn*wcx+c*wcy)
			rr := s * wr
			devOK := true
			for k := i; k <= j && devOK; k++ {
				devOK = math.Abs(math.Hypot(P[k][0]-ccx, P[k][1]-ccy)-rr) <= midTol
				if devOK && k < j {
					sx, sy := (P[k][0]+P[k+1][0])/2, (P[k][1]+P[k+1][1])/2
					devOK = math.Abs(math.Hypot(sx-ccx, sy-ccy)-rr) <= midTol
				}
			}
			if !devOK {
				continue
			}
			if arcStats != nil {
				arcStats.placed++
			}
			return model.Shape{
				Type:  int(aw.word),
				Color: col,
				Data:  []float64{pos[0], pos[1], m * s * aw.nativeW, s * aw.nativeH, rot / deg2rad, 0},
			}, true
		}
	}
	if arcStats != nil && len(cands) > 0 {
		arcStats.noBow++
		arcStats.failed = append(arcStats.failed, [3]float64{sweep / deg2rad, halfW, chord})
	}
	return model.Shape{}, false
}

// circum3 is the circle through three points (the word arc's endpoints + bulge); ok=false when
// near-collinear.
func circum3(a, b, c [2]float64) (cx, cy, r float64, ok bool) {
	d := 2 * (a[0]*(b[1]-c[1]) + b[0]*(c[1]-a[1]) + c[0]*(a[1]-b[1]))
	if math.Abs(d) < 1e-9 {
		return 0, 0, 0, false
	}
	a2 := a[0]*a[0] + a[1]*a[1]
	b2 := b[0]*b[0] + b[1]*b[1]
	c2 := c[0]*c[0] + c[1]*c[1]
	cx = (a2*(b[1]-c[1]) + b2*(c[1]-a[1]) + c2*(a[1]-b[1])) / d
	cy = (a2*(c[0]-b[0]) + b2*(a[0]-c[0]) + c2*(b[0]-a[0])) / d
	return cx, cy, math.Hypot(a[0]-cx, a[1]-cy), true
}

// arcStatsT is debug-only demand instrumentation for the arc matcher (enabled by tests).
type arcStatsT struct {
	runs, placed, noSweep, noWidth, noBow int
	sweepSum, chordSum, hwSum             float64
	failed                                [][3]float64 // sweep°, halfW, chord of unplaced curved runs
}

var arcStats *arcStatsT

// runSweep is the swept central angle of the run i..j about its fitted circle (the true arc angle).
// Falls back to interior turning if the points are collinear.
func runSweep(P [][2]float64, i, j int) float64 {
	cx, cy, _, ok := fitCircle(P[i : j+1])
	if !ok {
		var sum float64
		for k := i + 1; k < j; k++ {
			sum += turnAngle(P[k-1], P[k], P[k+1])
		}
		return math.Abs(sum)
	}
	var sum, prev float64
	prev = math.Atan2(P[i][1]-cy, P[i][0]-cx)
	for k := i + 1; k <= j; k++ {
		a := math.Atan2(P[k][1]-cy, P[k][0]-cx)
		d := a - prev
		for d > math.Pi {
			d -= 2 * math.Pi
		}
		for d < -math.Pi {
			d += 2 * math.Pi
		}
		sum += d
		prev = a
	}
	return math.Abs(sum)
}

func maxResidual(pts [][2]float64, cx, cy, r float64) float64 {
	max := 0.0
	for _, p := range pts {
		if d := math.Abs(math.Hypot(p[0]-cx, p[1]-cy) - r); d > max {
			max = d
		}
	}
	return max
}

func turnAngle(a, b, c [2]float64) float64 {
	u, v := sub(b, a), sub(c, b)
	return math.Atan2(u[0]*v[1]-u[1]*v[0], u[0]*v[0]+u[1]*v[1])
}

func crossSign(u, v [2]float64) int {
	cr := u[0]*v[1] - u[1]*v[0]
	switch {
	case cr > 1e-9:
		return 1
	case cr < -1e-9:
		return -1
	}
	return 0
}

func sub(a, b [2]float64) [2]float64 { return [2]float64{a[0] - b[0], a[1] - b[1]} }
