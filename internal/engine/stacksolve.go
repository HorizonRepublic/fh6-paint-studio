package engine

import (
	"math"
	"sync"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// solveStack solves the RGB of every layer of a FIXED-geometry stack jointly. Sequential alpha
// compositing over the canvas is affine in the layer colours: with per-pixel opacities
// a_k(x) = A_k·cov_k(x) the composite is out(x) = φ₀(x)·canvas(x) + Σ_k φ_k(x)·c_k, where
// φ_k = a_k·Π_{j>k}(1−a_j) and φ₀ = Π(1−a_k). Minimising the weighted SSE over the stack's
// footprint is then a per-channel linear least-squares (one shared n×n normal matrix, three RHS) —
// the greedy per-layer colour solve is myopic for stacks (a base whose colour only makes sense
// UNDER the gradient scores positive alone and never gets placed). The alpha channel has no
// unknowns (out_A = φ₀·s_A + Σφ_k) and is accumulated straight into the returned ΔSSE.
//
// The normal-equation sums are sample-strided like the backend evaluators (colours are ratios —
// stride-invariant); the returned ΔSSE is then measured EXACTLY (full-res, clamped colours), so
// gates built on it match what Apply will do. ok=false on a degenerate stack (colinear coverages).
//
// sel (optional, len w*h) focuses the solve and the ΔSSE on the claiming region; out-of-region
// footprint pixels still participate at spillFrac of their weight (0 = ignore entirely). The greedy
// overpaints most of a claim's spill (claims sit deepest in the z-stack) but not all of it — an
// uncharged spill let a claim paint arbitrary colours outside its region (metamer stacks whose base
// only makes sense combined) and ship visible ghosts where the greedy ran out of budget.
// stackCacheCap bounds the per-call φ cache (in float64s, ~16 MB) so a full-resolution solve over a
// huge region falls back to recomputing instead of holding tens of megabytes per concurrent solve.
const stackCacheCap = 1 << 21

type stackScratch struct {
	idx  []int32
	phiC []float64
	phis []float64 // n entries per sample, flat
}

func (s *stackScratch) reset() {
	s.idx, s.phiC, s.phis = s.idx[:0], s.phiC[:0], s.phis[:0]
}

var stackScratchPool = sync.Pool{New: func() any { return new(stackScratch) }}

func solveStack(canvas, target, weight []float32, w, h int, layers []model.Candidate, sampleBudget int, sel []bool, spillFrac float64) ([]model.RGBA, float64, bool) {
	n := len(layers)
	if n == 0 {
		return nil, 0, false
	}
	xMin, yMin, xMax, yMax := w, h, -1, -1
	for _, l := range layers {
		x0, y0, x1, y1 := raster.BBox(l.Kind, l.P, w, h)
		if x0 < xMin {
			xMin = x0
		}
		if y0 < yMin {
			yMin = y0
		}
		if x1 > xMax {
			xMax = x1
		}
		if y1 > yMax {
			yMax = y1
		}
	}
	if xMax < xMin || yMax < yMin {
		return nil, 0, false
	}
	step := 1
	if sampleBudget > 0 {
		if area := (xMax - xMin + 1) * (yMax - yMin + 1); area > sampleBudget {
			if s := int(math.Sqrt(float64(area) / float64(sampleBudget))); s > 1 {
				step = s
			}
		}
	}

	covs := make([]float64, n)
	phi := make([]float64, n)
	prep := make([]raster.Prepared, n)
	alpha := make([]float64, n)
	for k, l := range layers {
		prep[k] = raster.Prep(l.Kind, l.P)
		alpha[k] = float64(l.Color.A)
	}
	phiAt := func(x, y int) (float64, bool) {
		covered := false
		for k := range prep {
			c := prep[k].Coverage(x, y) * alpha[k]
			covs[k] = c
			if c > 0 {
				covered = true
			}
		}
		suf := 1.0
		for k := n - 1; k >= 0; k-- {
			phi[k] = covs[k] * suf
			suf *= 1 - covs[k]
		}
		return suf, covered
	}

	// The ΔSSE pass below needs the same φ as the normal-equation pass, and each φ costs one
	// raster.Coverage per layer — the dominant cost of both. Keep them from the first pass instead
	// of recomputing (same values, same order, so the result is unchanged). Skipped when the
	// footprint is large enough that the cache would outweigh the saving.
	sc := stackScratchPool.Get().(*stackScratch)
	defer stackScratchPool.Put(sc)
	sc.reset()
	rows := (yMax-yMin)/step + 1
	cols := (xMax-xMin)/step + 1
	cache := rows*cols*n <= stackCacheCap

	m := make([][]float64, n)
	for i := range m {
		m[i] = make([]float64, n)
	}
	rhs := [3][]float64{make([]float64, n), make([]float64, n), make([]float64, n)}
	for y := yMin; y <= yMax; y += step {
		for x := xMin; x <= xMax; x += step {
			idx := y*w + x
			wgt := float64(weight[idx])
			if sel != nil && !sel[idx] {
				wgt *= spillFrac
			}
			if wgt <= 0 {
				continue
			}
			phiC, covered := phiAt(x, y)
			if !covered {
				continue
			}
			if cache {
				sc.idx = append(sc.idx, int32(idx))
				sc.phiC = append(sc.phiC, phiC)
				sc.phis = append(sc.phis, phi...)
			}
			p := idx * 4
			for i := 0; i < n; i++ {
				wi := wgt * phi[i]
				for j := i; j < n; j++ {
					m[i][j] += wi * phi[j]
				}
				for c := 0; c < 3; c++ {
					rhs[c][i] += wi * (float64(target[p+c]) - phiC*float64(canvas[p+c]))
				}
			}
		}
	}
	for i := 1; i < n; i++ {
		for j := 0; j < i; j++ {
			m[i][j] = m[j][i]
		}
	}
	solved, ok := solveSPD3(m, rhs, n)
	if !ok {
		return nil, 0, false
	}
	out := make([]model.RGBA, n)
	for k := range out {
		out[k] = model.RGBA{
			R: float32(clamp01f(solved[0][k])), G: float32(clamp01f(solved[1][k])),
			B: float32(clamp01f(solved[2][k])), A: layers[k].Color.A,
		}
	}

	// ΔSSE with the clamped colours; strided like the solve (scaled by step² below), exact at
	// sampleBudget<=0. Gates re-measure the accepted stack with an exact call before applying.
	var delta float64
	accum := func(idx int, wgt, phiC float64, ph []float64) {
		p := idx * 4
		var before, after float64
		for c := 0; c < 3; c++ {
			s, t := float64(canvas[p+c]), float64(target[p+c])
			o := phiC * s
			for k := 0; k < n; k++ {
				var cc float64
				switch c {
				case 0:
					cc = float64(out[k].R)
				case 1:
					cc = float64(out[k].G)
				default:
					cc = float64(out[k].B)
				}
				o += ph[k] * cc
			}
			before += (s - t) * (s - t)
			after += (o - t) * (o - t)
		}
		sA, tA := float64(canvas[p+3]), float64(target[p+3])
		oA := phiC * sA
		for k := 0; k < n; k++ {
			oA += ph[k]
		}
		before += (sA - tA) * (sA - tA)
		after += (oA - tA) * (oA - tA)
		delta += wgt * (after - before)
	}
	if cache {
		for s := range sc.idx {
			idx := int(sc.idx[s])
			wgt := float64(weight[idx])
			if sel != nil && !sel[idx] {
				wgt *= spillFrac
			}
			accum(idx, wgt, sc.phiC[s], sc.phis[s*n:s*n+n])
		}
	} else {
		for y := yMin; y <= yMax; y += step {
			for x := xMin; x <= xMax; x += step {
				idx := y*w + x
				wgt := float64(weight[idx])
				if sel != nil && !sel[idx] {
					wgt *= spillFrac
				}
				if wgt <= 0 {
					continue
				}
				phiC, covered := phiAt(x, y)
				if !covered {
					continue
				}
				accum(idx, wgt, phiC, phi)
			}
		}
	}
	return out, delta * float64(step*step), true
}

// solveSPD3 solves the shared symmetric system M·x = rhs_c for the three colour channels by
// Gaussian elimination with partial pivoting. ok=false when M is (near-)singular — colinear layer
// coverages make the colour split arbitrary and the stack candidate is discarded.
func solveSPD3(m [][]float64, rhs [3][]float64, n int) ([3][]float64, bool) {
	a := make([][]float64, n)
	var scale float64
	for i := range a {
		a[i] = append([]float64(nil), m[i]...)
		for _, v := range m[i] {
			if av := math.Abs(v); av > scale {
				scale = av
			}
		}
	}
	b := [3][]float64{
		append([]float64(nil), rhs[0]...),
		append([]float64(nil), rhs[1]...),
		append([]float64(nil), rhs[2]...),
	}
	if scale <= 0 {
		return b, false
	}
	eps := 1e-12 * scale
	for col := 0; col < n; col++ {
		piv := col
		for r := col + 1; r < n; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[piv][col]) {
				piv = r
			}
		}
		if math.Abs(a[piv][col]) < eps {
			return b, false
		}
		a[col], a[piv] = a[piv], a[col]
		for c := 0; c < 3; c++ {
			b[c][col], b[c][piv] = b[c][piv], b[c][col]
		}
		inv := 1 / a[col][col]
		for r := col + 1; r < n; r++ {
			f := a[r][col] * inv
			if f == 0 {
				continue
			}
			for k := col; k < n; k++ {
				a[r][k] -= f * a[col][k]
			}
			for c := 0; c < 3; c++ {
				b[c][r] -= f * b[c][col]
			}
		}
	}
	for col := n - 1; col >= 0; col-- {
		inv := 1 / a[col][col]
		for c := 0; c < 3; c++ {
			v := b[c][col]
			for k := col + 1; k < n; k++ {
				v -= a[col][k] * b[c][k]
			}
			b[c][col] = v * inv
		}
	}
	return b, true
}

// maskFrameFit places a mask word so its ACTIVE coverage box (raster.MaskActiveUV — bank words
// carry transparent margins inside their unit square) covers exactly the requested frame
// cx±halfW, cy±halfH rotated by deg. ok=false for words without usable coverage.
func maskFrameFit(kind model.ShapeKind, cx, cy, halfW, halfH, deg float64) (model.Candidate, bool) {
	return maskShearFit(kind, cx, cy, halfW, halfH, deg, 0)
}

// maskShearFit is maskFrameFit with a shear. The requested frame is read in the word's own
// (sx, sy) coordinates, which the placement then shears by k and rotates by deg — so the covered
// screen region is a parallelogram, and a caller wanting to cover a box of half-extents (hu, hv) at
// this angle must ask for halfW = hu + |k|·hv.
//
// Shearing moves the active box's centre by K·(sxc, syc) before the rotation; miss that and the word
// lands off by the width of its own transparent margin.
func maskShearFit(kind model.ShapeKind, cx, cy, halfW, halfH, deg, skew float64) (model.Candidate, bool) {
	u0, v0, u1, v1, ok := raster.MaskActiveUV(kind)
	if !ok || u1-u0 <= 0.01 || v1-v0 <= 0.01 {
		return model.Candidate{}, false
	}
	hx := 2 * halfW / (u1 - u0)
	hy := 2 * halfH / (v1 - v0)
	sxc := ((u0+u1)/2-0.5)*hx + skew*((v0+v1)/2-0.5)*hy
	syc := ((v0+v1)/2 - 0.5) * hy
	th := deg * math.Pi / 180
	c, s := math.Cos(th), math.Sin(th)
	return model.Candidate{Kind: kind, Color: model.RGBA{A: 1},
		P: [6]float32{float32(cx - (sxc*c - syc*s)), float32(cy - (sxc*s + syc*c)),
			float32(hx), float32(hy), float32(deg), float32(skew)}}, true
}

func clamp01f(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
