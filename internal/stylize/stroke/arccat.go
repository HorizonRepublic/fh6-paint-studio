package stroke

import (
	"math"
	"sort"
	"sync"

	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"
)

// arcWord is a calibrated dictionary arc, measured ONCE from its coverage mask: everything needed to
// lay it along a contour. Coords are native-box units — a UV point (u,v) maps to
// ((u-0.5)*nativeW, (v-0.5)*nativeH), y-down to match the raster, the same q the renderer feeds through
// screen = pos + R(rot)·diag(Hx,Hy)·(u-0.5,v-0.5). a,b are the arc endpoints; mid is its bulge point;
// sweep is the swept angle (rad).
type arcWord struct {
	word             uint16
	kind             model.ShapeKind
	nativeW, nativeH float64
	a, b, mid        [2]float64
	sweep, radius    float64
	strokeHW         float64 // native half stroke width (coverage area / arc length / 2) — the placement scale multiplies it
}

// arcWordHex is the thin-stroke dictionary arcs usable as constant-width outlines. Partial arcs only
// (no full rings) so the chord placement never degenerates to a zero-length chord.
var arcWordHex = []uint16{
	0x0853, // gentlearc1 — shallow
	0x08b7, // arc-shallow
	0x089a, // arc-dome   — wide cap
	0x089b, // arc-90     — quarter, tapered tips
	0x08a7, // arc-180    — half band
	0x08b8, // openC      — open C (large sweep)
	0x08a6, // arc-J1     — J curve
	0x08ae, // arc-sweep  — long sweep
	0x0145, // arc-J2     — tall J curve
	0x08a4, // arc-90-thin  — quarter, fine stroke (fits the width cap on thin lines)
	0x08b4, // arc-90-mid
	0x08ac, // arc-90-wide
	0x08a3, // arc-180-thin — half, fine stroke
	0x08b3, // arc-180-mid
	0x08ab, // arc-180-wide
}

var (
	arcCatOnce sync.Once
	arcCat     []arcWord
)

// arcCatalog returns the measured thin-arc dictionary, sorted by sweep. Built lazily from the embedded
// mask bank (already registered with the model by then).
func arcCatalog() []arcWord {
	arcCatOnce.Do(func() {
		want := make(map[uint16]bool, len(arcWordHex))
		for _, w := range arcWordHex {
			want[w] = true
		}
		for _, e := range maskbank.All() {
			if !want[e.Word] {
				continue
			}
			if aw, ok := measureArc(e); ok {
				arcCat = append(arcCat, aw)
			}
		}
		sort.Slice(arcCat, func(i, j int) bool { return arcCat[i].sweep < arcCat[j].sweep })
	})
	return arcCat
}

// measureArc circle-fits the high-coverage ridge of a mask and reads off its angular span + endpoints.
func measureArc(e maskbank.Entry) (arcWord, bool) {
	var pts [][2]float64
	for y := 0; y < e.H; y++ {
		for x := 0; x < e.W; x++ {
			if e.Cov[y*e.W+x] < 0.5 {
				continue
			}
			u := (float64(x) + 0.5) / float64(e.W)
			v := (float64(y) + 0.5) / float64(e.H)
			pts = append(pts, [2]float64{(u - 0.5) * float64(e.NativeW), (v - 0.5) * float64(e.NativeH)})
		}
	}
	if len(pts) < 8 {
		return arcWord{}, false
	}
	cx, cy, r, ok := fitCircle(pts)
	if !ok || r <= 0 {
		return arcWord{}, false
	}
	angs := make([]float64, len(pts))
	for i, p := range pts {
		angs[i] = math.Atan2(p[1]-cy, p[0]-cx)
	}
	sort.Float64s(angs)
	maxGap, gapAt := -1.0, 0
	for i := range angs {
		d := angs[(i+1)%len(angs)] - angs[i]
		if i == len(angs)-1 {
			d += 2 * math.Pi
		}
		if d > maxGap {
			maxGap, gapAt = d, i
		}
	}
	a0 := angs[(gapAt+1)%len(angs)]
	a1 := angs[gapAt]
	if a1 < a0 {
		a1 += 2 * math.Pi
	}
	sweep := a1 - a0
	at := func(ang float64) [2]float64 { return [2]float64{cx + r*math.Cos(ang), cy + r*math.Sin(ang)} }
	// Native stroke half-width: covered area over arc length. The placement scale multiplies it,
	// so a big-scale fit renders a proportionally fatter (and softer — upscaled mask) line.
	var strokeHW float64
	if length := r * sweep; length > 1e-6 {
		pxArea := float64(e.NativeW) / float64(e.W) * float64(e.NativeH) / float64(e.H)
		strokeHW = float64(len(pts)) * pxArea / length / 2
	}
	return arcWord{
		word: e.Word, kind: e.Kind, nativeW: float64(e.NativeW), nativeH: float64(e.NativeH),
		a: at(a0), b: at(a1), mid: at(a0 + sweep/2), sweep: sweep, radius: r, strokeHW: strokeHW,
	}, true
}

// fitCircle is the algebraic (Kåsa) least-squares circle through pts. ok=false when the points are
// (near-)collinear — the normal matrix is singular, which the caller treats as "straight, not an arc".
func fitCircle(pts [][2]float64) (cx, cy, r float64, ok bool) {
	n := float64(len(pts))
	if n < 3 {
		return 0, 0, 0, false
	}
	var sx, sy, sxx, syy, sxy, sxz, syz, sz float64
	for _, p := range pts {
		x, y := p[0], p[1]
		z := x*x + y*y
		sx += x
		sy += y
		sxx += x * x
		syy += y * y
		sxy += x * y
		sxz += x * z
		syz += y * z
		sz += z
	}
	// Solve M·[A B C]ᵀ = rhs, fitting z = A·x + B·y + C; centre = (A/2,B/2), r² = C + (A²+B²)/4.
	m := [3][3]float64{{sxx, sxy, sx}, {sxy, syy, sy}, {sx, sy, n}}
	rhs := [3]float64{sxz, syz, sz}
	det := det3(m)
	scale := sxx + syy + 1
	if math.Abs(det) < 1e-9*scale*scale {
		return 0, 0, 0, false
	}
	A := det3(replaceCol(m, 0, rhs)) / det
	B := det3(replaceCol(m, 1, rhs)) / det
	C := det3(replaceCol(m, 2, rhs)) / det
	cx, cy = A/2, B/2
	rr := C + (A*A+B*B)/4
	if rr <= 0 {
		return 0, 0, 0, false
	}
	return cx, cy, math.Sqrt(rr), true
}

func det3(m [3][3]float64) float64 {
	return m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
		m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
		m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
}

func replaceCol(m [3][3]float64, c int, v [3]float64) [3][3]float64 {
	for r := 0; r < 3; r++ {
		m[r][c] = v[r]
	}
	return m
}
