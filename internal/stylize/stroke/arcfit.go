package stroke

import (
	"math"

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
			if s, ok := placeArc(P, i, j, col, cfg); ok {
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

// placeArc picks the dictionary arc whose sweep best matches the run i..j and solves the similarity
// (pos, rotation, uniform scale, mirror) that lands the word's arc endpoints on the run's endpoints,
// bowing the same way. Returns false if the run is too shallow or no arc is close enough.
func placeArc(P [][2]float64, i, j int, col []int, cfg Config) (model.Shape, bool) {
	sweep := runSweep(P, i, j)
	if sweep < cfg.MinSweep*deg2rad {
		return model.Shape{}, false
	}
	aw, ok := pickArc(arcCatalog(), sweep)
	if !ok {
		return model.Shape{}, false
	}
	Pi, Pj, mid := P[i], P[j], P[(i+j)/2]
	if math.Hypot(Pj[0]-Pi[0], Pj[1]-Pi[1]) < 2 { // near-closed run: no usable chord
		return model.Shape{}, false
	}
	cSign := crossSign(sub(Pj, Pi), sub(mid, Pi))
	for _, m := range [2]float64{1, -1} {
		A := [2]float64{m * aw.a[0], aw.a[1]}
		B := [2]float64{m * aw.b[0], aw.b[1]}
		Wm := [2]float64{m * aw.mid[0], aw.mid[1]}
		if crossSign(sub(B, A), sub(Wm, A)) != cSign {
			continue
		}
		dW, dS := sub(B, A), sub(Pj, Pi)
		lW := math.Hypot(dW[0], dW[1])
		if lW < 1e-6 {
			return model.Shape{}, false
		}
		s := math.Hypot(dS[0], dS[1]) / lW
		rot := math.Atan2(dS[1], dS[0]) - math.Atan2(dW[1], dW[0])
		c, sn := math.Cos(rot), math.Sin(rot)
		pos := [2]float64{Pi[0] - s*(c*A[0]-sn*A[1]), Pi[1] - s*(sn*A[0]+c*A[1])}
		return model.Shape{
			Type:  int(aw.word),
			Color: col,
			Data:  []float64{pos[0], pos[1], m * s * aw.nativeW, s * aw.nativeH, rot / deg2rad, 0},
		}, true
	}
	return model.Shape{}, false
}

// pickArc returns the catalog arc with sweep nearest the run's, within a generous tolerance.
func pickArc(cat []arcWord, sweep float64) (arcWord, bool) {
	best, bestd := -1, math.Inf(1)
	for idx := range cat {
		if d := math.Abs(cat[idx].sweep - sweep); d < bestd {
			bestd, best = d, idx
		}
	}
	if best < 0 || bestd > 70*deg2rad {
		return arcWord{}, false
	}
	return cat[best], true
}

// runSweep is the magnitude of the total turning along the run i..j (the swept angle of its arc).
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
