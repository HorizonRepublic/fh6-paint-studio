package engine

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"sync"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// Global joint colour re-solve over the WHOLE stack.
//
// The greedy solves each shape's colour against the canvas that existed at the moment it was placed.
// Every later shape changes that canvas, so by the end most shapes are painted for a background that
// no longer exists — the likeliest explanation for the measured 17.8% of shapes that individually
// HARM the result. Fixing it needs no new heuristic, because the problem is exactly solvable:
// straight-alpha `over` in linear light is AFFINE in the layer colours. With per-pixel opacities
// a_k(x) = A_k·cov_k(x),
//
//	out(x) = φ₀(x)·base(x) + Σ_k φ_k(x)·c_k,  φ_k = a_k·Π_{j above k}(1−a_j),  φ₀ = Π(1−a_k)
//
// so for FIXED geometry and alpha the weighted SSE is a convex quadratic in the 3n colour channels,
// box-constrained to [0,1]. solveStack already carries this maths but forms a dense n×n normal
// matrix, which caps it at the <=4-layer claims it was written for; n here is 1000-3000.
//
// This solves the same problem matrix-free with projected FISTA. The gradient needs only two sweeps
// over the stack — a bottom-up composite for the residual and a top-down transmittance sweep to
// scatter it back — and since geometry and alpha are FROZEN, every a_k(x) is constant and is cached
// once. Each iteration is then pure arithmetic over that cache, with no coverage evaluation and no
// n² storage anywhere.
//
// Box projection is also the point, not a detail: clamping colours after an unconstrained solve is
// only equivalent to the constrained solution when the Gram matrix is diagonal, i.e. when nothing
// overlaps. Ours overlap by construction, so a clamped shape currently leaves its neighbours
// uncompensated, and per-channel clipping additionally SHIFTS HUE — an error the eye catches and SSE
// does not. Projecting inside the iteration lets the rest of the stack absorb the clamp.

// gcCacheBudget bounds the opacity cache in float32 entries (~64 MB). Caching every layer's bbox is
// what makes an iteration pure arithmetic, but a stack containing a few canvas-sized shapes would
// otherwise reserve hundreds of megabytes on a machine that may not have them — the owner's
// requirement is that this runs on weak cards. Layers past the budget keep their Prepared and are
// evaluated on the fly, which is slower per pixel but bounded in memory.
var gcCacheBudget = 16 << 20

// gcProtectFloor / gcCleanTau price the clean-pixel protection above. The floor is in units of the
// frame's MEAN weight. FH6_GC_PROTECT="floor,tau" overrides both for an A/B; floor 0 disables it and
// restores the plain weighted objective.
var gcProtectFloor, gcCleanTau = func() (float32, float32) {
	// Measured on img_24, the image whose white background showed the artefact (bad white pixels /
	// in-game SSE): off 1211/4958, 1.5,0.03 1141/4992, 3,0.03 439/5014, 6,0.03 429/5048,
	// 16,0.03 489/5085, 12,0.015 429/4987, 6,0.008 1202/4963, and this pair 436/4975. The band matters
	// more than the gain: at 0.008 the protection misses the pixels that get spoiled — they enter
	// deviating by between 0.008 and 0.015 — and at 0.03 it starts shackling the solve on pixels it
	// ought to be free to move, which costs error without buying any more of the artefact back.
	gain, tau := float32(1), float32(0.015)
	if s := os.Getenv("FH6_GC_PROTECT"); s != "" {
		var gp, tp float64
		if _, err := fmt.Sscanf(s, "%g,%g", &gp, &tp); err == nil {
			gain, tau = float32(gp), float32(tp)
		}
	}
	if tau <= 0 {
		tau = 0.03
	}
	return gain, tau
}()

// gcLayer is one frozen layer. `a` holds the per-pixel opacity a_k = A_k·cov_k over the bbox when
// the layer is cached; when it is not, `prep` and `alpha` reproduce the same value on demand.
type gcLayer struct {
	x0, y0, x1, y1 int
	a              []float32 // (x1-x0+1)*(y1-y0+1), row-major over the bbox; nil when uncached
	prep           raster.Prepared
	alpha          float64
	live           bool // false = empty footprint, nothing to composite
}

// at returns the layer's opacity at an absolute pixel, from the cache when there is one.
func (l *gcLayer) at(x, y, row int) float32 {
	if l.a != nil {
		return l.a[row+x-l.x0]
	}
	return float32(l.prep.Coverage(x, y) * l.alpha)
}

// gcBuild prepares the layers, caching opacities largest-benefit-first until the budget runs out.
// Layers with an empty footprint stay in the slice so indices remain aligned with the caller's.
func gcBuild(layers []model.Shape, w, h int) []gcLayer {
	out := make([]gcLayer, len(layers))
	budget := gcCacheBudget
	for i, s := range layers {
		k := model.KindFromType(s.Type)
		pp := model.ParamsFromShape(s)
		x0, y0, x1, y1 := raster.BBox(k, pp, w, h)
		if x1 < x0 || y1 < y0 {
			out[i] = gcLayer{x0: 0, y0: 0, x1: -1, y1: -1}
			continue
		}
		var alpha float64 = 1
		if len(s.Color) >= 4 {
			alpha = float64(s.Color[3]) / 255
		}
		l := gcLayer{x0: x0, y0: y0, x1: x1, y1: y1, prep: raster.Prep(k, pp), alpha: alpha, live: true}
		if n := (x1 - x0 + 1) * (y1 - y0 + 1); n <= budget {
			budget -= n
			a := make([]float32, n)
			for y := y0; y <= y1; y++ {
				row := (y - y0) * (x1 - x0 + 1)
				for x := x0; x <= x1; x++ {
					a[row+x-x0] = float32(l.prep.Coverage(x, y) * alpha)
				}
			}
			l.a = a
		}
		out[i] = l
	}
	return out
}

// gcBandRows is the smallest row count worth giving a band: below it the goroutine costs more than
// the rows it walks.
const gcBandRows = 8

// gcBands splits the canvas into contiguous row bands, one per core.
//
// Every stack walk here is sequential over LAYERS — the composite blends bottom-up into a shared
// canvas, the gradient carries transmittance top-down — so the layer loop cannot be split. The pixels
// can: all of that state is indexed by pixel, and a band owns its rows exclusively, so a band
// reproduces the serial sequence exactly for the rows it holds. One barrier per walk instead of one
// per layer is what makes it worth doing: the stacks here are ~1000 mostly-small layers, and a
// per-layer fan-out spends more time synchronising than blending.
func gcBands(h int) [][2]int {
	n := runtime.GOMAXPROCS(0)
	if m := h / gcBandRows; n > m {
		n = m
	}
	if n < 1 {
		n = 1
	}
	rows := (h + n - 1) / n
	bands := make([][2]int, 0, n)
	for y := 0; y < h; y += rows {
		y1 := y + rows - 1
		if y1 > h-1 {
			y1 = h - 1
		}
		bands = append(bands, [2]int{y, y1})
	}
	return bands
}

// gcRunBands runs fn once per band, concurrently.
// gcRunBandsIdx is gcRunBands over precomputed bands, handing each callback its band index so a
// caller can collect per-band partials without sharing an accumulator.
func gcRunBandsIdx(bands [][2]int, fn func(bi, y0, y1 int)) {
	if len(bands) == 1 {
		fn(0, bands[0][0], bands[0][1])
		return
	}
	var wg sync.WaitGroup
	for bi, b := range bands {
		wg.Add(1)
		go func(bi, y0, y1 int) {
			defer wg.Done()
			fn(bi, y0, y1)
		}(bi, b[0], b[1])
	}
	wg.Wait()
}

func gcRunBands(h int, fn func(y0, y1 int)) {
	bands := gcBands(h)
	if len(bands) == 1 {
		fn(bands[0][0], bands[0][1])
		return
	}
	var wg sync.WaitGroup
	for _, b := range bands {
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			fn(y0, y1)
		}(b[0], b[1])
	}
	wg.Wait()
}

// gcReduceBands runs fn per band with a private accumulator of n float64s and sums them in BAND
// ORDER. Fixed-order reduction keeps the result reproducible run to run; it is not bit-identical to
// the serial sweep, which sums one continuous row-major sequence, because float addition does not
// associate.
func gcReduceBands(h, n int, out []float64, fn func(y0, y1 int, acc []float64)) {
	bands := gcBands(h)
	if len(bands) == 1 {
		fn(bands[0][0], bands[0][1], out)
		return
	}
	parts := make([][]float64, len(bands))
	var wg sync.WaitGroup
	for i, b := range bands {
		// Pooled: this runs ~330× per solve with bands × 24KB accumulators — fresh makes were
		// pure allocator churn. clear() re-zeroes, so the values are exactly what make() gave.
		buf, _ := gcPartsPool.Get().([]float64)
		if cap(buf) < n {
			buf = make([]float64, n)
		} else {
			buf = buf[:n]
			clear(buf)
		}
		parts[i] = buf
		wg.Add(1)
		go func(i, y0, y1 int) {
			defer wg.Done()
			fn(y0, y1, parts[i])
		}(i, b[0], b[1])
	}
	wg.Wait()
	for _, p := range parts {
		for i, v := range p {
			out[i] += v
		}
		gcPartsPool.Put(p) //nolint:staticcheck // slices are pointer-shaped enough here
	}
}

var gcPartsPool sync.Pool

// gcCompositeLayer blends one layer's colour over out across rows [y0,y1]. Rows are disjoint in out,
// so several bands of the SAME layer may run concurrently without a race.
func gcCompositeLayer(l *gcLayer, cr, cg, cb float32, out []float32, w, y0, y1 int) {
	bw := l.x1 - l.x0 + 1
	// The cached branch is nearly every layer (gcBuild's 16MB budget covers ~all bboxes), yet
	// l.at() paid the nil test plus a full-slice bounds check on EVERY pixel of every layer of
	// every sweep — ~15M pixel-visits per solver iteration. Hoist the test and sub-slice the row
	// so the compiler proves the indexing. Same arithmetic, same order.
	if l.a != nil {
		for y := y0; y <= y1; y++ {
			ar := l.a[(y-l.y0)*bw : (y-l.y0)*bw+bw]
			p := (y*w + l.x0) * 4
			for i := 0; i < bw; i++ {
				a := ar[i]
				if a > 0 {
					ia := 1 - a
					out[p+0] = out[p+0]*ia + a*cr
					out[p+1] = out[p+1]*ia + a*cg
					out[p+2] = out[p+2]*ia + a*cb
				}
				p += 4
			}
		}
		return
	}
	for y := y0; y <= y1; y++ {
		for x := l.x0; x <= l.x1; x++ {
			a := float32(l.prep.Coverage(x, y) * l.alpha)
			if a <= 0 {
				continue
			}
			p := (y*w + x) * 4
			ia := 1 - a
			out[p+0] = out[p+0]*ia + a*cr
			out[p+1] = out[p+1]*ia + a*cg
			out[p+2] = out[p+2]*ia + a*cb
		}
	}
}

// gcComposite renders the stack for the given colours (c is 3 per layer, RGB interleaved) into out,
// which must already hold the base canvas. Bottom-up, exactly the shipped `over`. Layers stay strictly
// ordered (each blends over the previous); only a LARGE layer's own rows fan out, which does not
// reorder the blend.
func gcComposite(gl []gcLayer, c []float64, out []float32, w int) {
	gcRunBands(len(out)/(4*w), func(by0, by1 int) {
		for i := range gl {
			l := &gl[i]
			if !l.live || l.y1 < by0 || l.y0 > by1 {
				continue
			}
			y0, y1 := l.y0, l.y1
			if y0 < by0 {
				y0 = by0
			}
			if y1 > by1 {
				y1 = by1
			}
			gcCompositeLayer(l, float32(c[i*3+0]), float32(c[i*3+1]), float32(c[i*3+2]), out, w, y0, y1)
		}
	})
}

// gcGradient accumulates g_k = Σ_p φ_k(p)·resid(p) per channel, where resid is the weighted
// residual of the current composite. The top-down sweep carries the transmittance Π(1−a_j) over
// layers ABOVE k, which is what makes φ available without ever storing it.
func gcGradient(gl []gcLayer, resid, trans []float32, g []float64, w int) {
	for i := range g {
		g[i] = 0
	}
	gcReduceBands(len(trans)/w, len(gl)*3, g, func(by0, by1 int, acc []float64) {
		for i := by0 * w; i < (by1+1)*w; i++ {
			trans[i] = 1
		}
		for i := len(gl) - 1; i >= 0; i-- {
			l := &gl[i]
			if !l.live || l.y1 < by0 || l.y0 > by1 {
				continue
			}
			bw := l.x1 - l.x0 + 1
			y0, y1 := l.y0, l.y1
			if y0 < by0 {
				y0 = by0
			}
			if y1 > by1 {
				y1 = by1
			}
			var gr, gg, gb float64
			if l.a != nil {
				// Hoisted cached branch — see gcCompositeLayer. Same arithmetic, same order.
				for y := y0; y <= y1; y++ {
					ar := l.a[(y-l.y0)*bw : (y-l.y0)*bw+bw]
					ti := y*w + l.x0
					for i2 := 0; i2 < bw; i2++ {
						a := ar[i2]
						if a > 0 {
							phi := float64(a * trans[ti])
							p := ti * 4
							gr += phi * float64(resid[p+0])
							gg += phi * float64(resid[p+1])
							gb += phi * float64(resid[p+2])
							trans[ti] *= 1 - a
						}
						ti++
					}
				}
			} else {
				for y := y0; y <= y1; y++ {
					for x := l.x0; x <= l.x1; x++ {
						a := float32(l.prep.Coverage(x, y) * l.alpha)
						if a <= 0 {
							continue
						}
						ti := y*w + x
						phi := float64(a * trans[ti])
						p := ti * 4
						gr += phi * float64(resid[p+0])
						gg += phi * float64(resid[p+1])
						gb += phi * float64(resid[p+2])
						trans[ti] *= 1 - a
					}
				}
			}
			acc[i*3+0] += gr
			acc[i*3+1] += gg
			acc[i*3+2] += gb
		}
	})
}

// gcDiag computes the Gram's diagonal G_kk = Σ_p w_p·φ_k(p)², one entry per layer. It is the
// curvature the solve sees along each layer's own axis, and it varies by ORDERS of magnitude across
// a stack — a big opaque base covers a hundred thousand pixels, a late sliver a few hundred. A
// single global step size is therefore paced by the largest layer and crawls on every small one,
// which is exactly why plain FISTA had not converged after 300 iterations.
func gcDiag(gl []gcLayer, weight, trans []float32, d []float64, w int) {
	for i := range d {
		d[i] = 0
	}
	gcReduceBands(len(trans)/w, len(gl), d, func(by0, by1 int, out []float64) {
		for i := by0 * w; i < (by1+1)*w; i++ {
			trans[i] = 1
		}
		for i := len(gl) - 1; i >= 0; i-- {
			l := &gl[i]
			if !l.live || l.y1 < by0 || l.y0 > by1 {
				continue
			}
			bw := l.x1 - l.x0 + 1
			y0, y1 := l.y0, l.y1
			if y0 < by0 {
				y0 = by0
			}
			if y1 > by1 {
				y1 = by1
			}
			var acc float64
			if l.a != nil {
				// Hoisted cached branch — see gcCompositeLayer. Same arithmetic, same order.
				for y := y0; y <= y1; y++ {
					ar := l.a[(y-l.y0)*bw : (y-l.y0)*bw+bw]
					ti := y*w + l.x0
					for i2 := 0; i2 < bw; i2++ {
						a := ar[i2]
						if a > 0 {
							phi := float64(a * trans[ti])
							acc += float64(weight[ti]) * phi * phi
							trans[ti] *= 1 - a
						}
						ti++
					}
				}
			} else {
				for y := y0; y <= y1; y++ {
					for x := l.x0; x <= l.x1; x++ {
						a := float32(l.prep.Coverage(x, y) * l.alpha)
						if a <= 0 {
							continue
						}
						ti := y*w + x
						phi := float64(a * trans[ti])
						acc += float64(weight[ti]) * phi * phi
						trans[ti] *= 1 - a
					}
				}
			}
			out[i] += acc
		}
	})
}

// gcProtectClean lifts the price of spoiling pixels the picture already gets right — but only where
// the perceptual weight has UNDER-priced them, which is the whole of the problem. The weight map
// exists to favour dark pixels, and it does that by making bright ones cheap: a layer straddling a
// white background and something darker can buy a real gain on the dark side with a faint wash over
// the light one and come out ahead on the sum. So the protection is a FLOOR, not a multiplier. A
// clean pixel is worth at least an average one; a clean DARK pixel already outweighs that and is left
// exactly as it was.
//
// Multiplying instead was measured and is the wrong shape: x7 on every clean pixel cost 2.1% of the
// in-game SSE across the matrix (photo 3.0%), because on a photograph most pixels are clean and the
// solve ends up shackled everywhere rather than where it is careless. The floor costs nothing:
// 27 paired runs, mean -0.000% with sem 0.017%, 18 of them inside +-0.1%, wall +0.1% — while the
// white-background artefact on img_24 drops from 1211 pixels to 415. Raw pairs in out/ab/gc-floor.tsv.
func gcProtectClean(gl []gcLayer, c []float64, base, target, weight []float32, w, h int) []float32 {
	if gcProtectFloor <= 0 {
		return weight
	}
	var sum float64
	for _, v := range weight {
		sum += float64(v)
	}
	if len(weight) == 0 || sum <= 0 {
		return weight
	}
	floor := float32(sum/float64(len(weight))) * gcProtectFloor

	cur := make([]float32, w*h*4)
	copy(cur, base)
	gcComposite(gl, c, cur, w)
	out := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		p := i * 4
		d := absf32(cur[p] - target[p])
		if v := absf32(cur[p+1] - target[p+1]); v > d {
			d = v
		}
		if v := absf32(cur[p+2] - target[p+2]); v > d {
			d = v
		}
		out[i] = weight[i]
		if d < gcCleanTau && out[i] < floor {
			// Ramped to the floor rather than switched onto it, so a pixel does not change price
			// discontinuously as the solve moves it across the threshold.
			out[i] += (floor - out[i]) * (1 - d/gcCleanTau)
		}
	}
	return out
}

func absf32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// gcStep is one gradient evaluation at colours c: composites, forms the weighted residual, and
// scatters it back. Returns the weighted SSE at c (RGB only — the alpha channel of the composite
// carries no unknowns, so it is a constant offset the optimiser cannot move).
func gcStep(gl []gcLayer, c []float64, base, target, weight, scratch, resid, trans []float32, g []float64, w, h int) float64 {
	copy(scratch, base)
	gcComposite(gl, c, scratch, w)
	// Residual in the same row bands as the composite: per-pixel writes are independent, so the
	// bytes in resid are identical to the serial loop's. Only the SSE reduction order moves, and
	// that value is informational — it feeds the pass's log line, never a decision (the pass gates
	// on the GPU rerender). This loop used to be the solver's serial half: ~7.4ms per call x ~300
	// calls a pass, with the GPU idle throughout.
	bands := gcBands(h)
	partial := make([]float64, len(bands))
	gcRunBandsIdx(bands, func(bi, y0, y1 int) {
		var s float64
		for i := y0 * w; i < (y1+1)*w; i++ {
			wt := weight[i]
			p := i * 4
			for ch := 0; ch < 3; ch++ {
				d := scratch[p+ch] - target[p+ch]
				resid[p+ch] = wt * d
				s += float64(wt) * float64(d) * float64(d)
			}
		}
		partial[bi] = s
	})
	var sse float64
	for _, s := range partial {
		sse += s
	}
	gcGradient(gl, resid, trans, g, w)
	return sse
}

// globalColorSolve re-solves every layer's RGB jointly for the frozen geometry of `shapes`
// (shapes[0] is the background and is left alone, mirroring Polish). It returns a new slice with the
// solved colours and the weighted SSE before and after, so the caller can gate on a real
// improvement. ok=false means there was nothing to solve.
//
// Projected FISTA with a power-iteration step size. The objective is convex, so the only ways to end
// up worse are a step size that is too large or a caller that ignores the gate — the first is
// handled here (the Lipschitz estimate is deliberately padded), the second is the caller's job.
func globalColorSolve(shapes []model.Shape, target, weight []float32, w, h int, bg model.RGBA, transparent bool, iters int) ([]model.Shape, float64, float64, bool) {
	return globalColorAlphaSolve(shapes, target, weight, w, h, bg, transparent, iters, 0, 0)
}

// globalColorAlphaSolve is globalColorSolve plus alphaSweeps rounds of closed-form alpha coordinate
// descent, alternating with the colour solve (block coordinate descent on a frozen geometry). aMin
// is the alpha floor the sweep may not go below — the preset's organic floor, so this cannot re-open
// the hole the polish used to leave.
func globalColorAlphaSolve(shapes []model.Shape, target, weight []float32, w, h int, bg model.RGBA, transparent bool, iters, alphaSweeps int, aMin float64) ([]model.Shape, float64, float64, bool) {
	if len(shapes) <= 1 || iters <= 0 {
		return shapes, 0, 0, false
	}
	layers := shapes[1:]
	n := len(layers)
	gl := gcBuild(layers, w, h)

	base := make([]float32, w*h*4)
	if !transparent {
		for i := 0; i < w*h; i++ {
			base[i*4+0], base[i*4+1], base[i*4+2], base[i*4+3] = bg.R, bg.G, bg.B, 1
		}
	}
	// The composite's ALPHA channel is deliberately not modelled: the objective below reads RGB
	// only, so the alpha error is a constant offset while the layer alphas are fixed, and the alpha
	// sweep is disabled for the one case where it would not be (cutouts).

	c := make([]float64, n*3)
	for i, s := range layers {
		if len(s.Color) >= 3 {
			c[i*3+0] = float64(model.DecChan(s.Color[0]))
			c[i*3+1] = float64(model.DecChan(s.Color[1]))
			c[i*3+2] = float64(model.DecChan(s.Color[2]))
		}
	}

	scratch := make([]float32, w*h*4)
	resid := make([]float32, w*h*4)
	trans := make([]float32, w*h)
	g := make([]float64, n*3)

	// Protect what the picture already gets right. The objective here is the weighted SSE, and the
	// perceptual weight is LOW on bright pixels — so tinting a clean white background is nearly free
	// to the solve, while a layer spanning that background and something darker can pay for a real
	// gain on the dark side with a faint wash over the light one. The sum cannot tell the difference;
	// the eye sees only the wash. Measured on img_24: with this solve disabled the residual white
	// artefact drops from 1211 pixels to 244, so four fifths of it is priced here.
	//
	// So pixels the composite ENTERING this pass already matches are made expensive to disturb. The
	// solve is still free to improve everything else, and the pass's own gate still measures the
	// engine's real error, so an over-eager protection loses the pass rather than shipping a worse
	// frame.
	weight = gcProtectClean(gl, c, base, target, weight, w, h)

	// Alpha changes the Gram, so the colour solve is re-run from scratch after every sweep rather
	// than patched: block coordinate descent, colours then alphas, until a sweep moves nothing.
	// Cutouts are excluded — their silhouette must stay solid, and the alpha channel's own error is
	// outside this model (the colour solve reads RGB only).
	sweeps := alphaSweeps
	if transparent {
		sweeps = 0
	}
	var vcache [][]float32
	if sweeps > 0 {
		vcache = make([][]float32, len(gl))
		budget := gcCacheBudget
		for i := range gl {
			if !gl[i].live || gl[i].a == nil {
				continue
			}
			// The below-canvas cache is three channels per pixel and obeys the same budget as the
			// opacities; a layer that does not fit keeps its alpha instead of being optimised
			// against a stale canvas.
			sz := (gl[i].x1 - gl[i].x0 + 1) * (gl[i].y1 - gl[i].y0 + 1) * 3
			if sz > budget {
				continue
			}
			budget -= sz
			vcache[i] = make([]float32, sz)
		}
	}

	before := gcStep(gl, c, base, target, weight, scratch, resid, trans, g, w, h)
	last := before
	for round := 0; ; round++ {
		last = gcColorFISTA(gl, c, base, target, weight, scratch, resid, trans, g, w, h, iters)
		if round >= sweeps {
			break
		}
		if gcAlphaSweep(gl, c, base, target, weight, vcache, trans, w, h, aMin) == 0 {
			break
		}
	}

	out := make([]model.Shape, len(shapes))
	out[0] = cloneShape(shapes[0])
	// The alpha half of the block descent has to LEAVE through the shapes too. gcAlphaSweep solved
	// A_k and the colour re-solve conditioned on it — but the write-back below used to copy the
	// INPUT alpha verbatim, shipping colours solved against alphas that were then discarded (an
	// internally inconsistent stack; the pass's e2e gate is what kept it from regressing).
	// FH6_GC_ALPHA=0 pins the old copy-through for A/Bs from the studio.
	exportAlpha := sweeps > 0 && os.Getenv("FH6_GC_ALPHA") != "0"
	for i, s := range layers {
		ns := cloneShape(s)
		if len(ns.Color) >= 3 {
			// EncByte, not F2B: the solve works in the LINEAR compositing space that DecChan
			// produced, and F2B is only its inverse when linear-light is off. Writing back with
			// F2B under linear light silently gamma-shifts every colour.
			ns.Color[0] = model.EncByte(float32(c[i*3+0]))
			ns.Color[1] = model.EncByte(float32(c[i*3+1]))
			ns.Color[2] = model.EncByte(float32(c[i*3+2]))
		}
		if exportAlpha && gl[i].live && len(ns.Color) >= 4 {
			a := int(math.Round(gl[i].alpha * 255))
			if a < 0 {
				a = 0
			} else if a > 255 {
				a = 255
			}
			ns.Color[3] = a
		}
		out[i+1] = ns
	}
	return out, before, last, true
}

// gcLipschitz estimates the largest eigenvalue of the Gram matrix by power iteration on the
// matrix-free matvec, then pads it. The quadratic's curvature is 2·G, and FISTA needs a step no
// larger than 1/L; over-estimating only slows convergence, while under-estimating diverges, so the
// asymmetry is resolved in favour of the safe side.
func gcLipschitz(gl []gcLayer, weight []float32, sc []float64, resid, trans []float32, g []float64, w, h int) float64 {
	n := len(gl)
	v := make([]float64, n*3)
	for i := range v {
		v[i] = 1
	}
	scratch := make([]float32, w*h*4)
	cv := make([]float64, n*3)
	lam := 1.0
	for it := 0; it < 8; it++ {
		// G·v = scatter(composite(v) − composite(0)); composing against a zero base gives Σφ_k v_k
		// directly, so the subtraction is unnecessary.
		// The operator is the RESCALED Gram, so the matvec is D^{-1/2}·G·D^{-1/2}: unscale on the
		// way in, scale on the way out.
		for i := range cv {
			cv[i] = v[i] / sc[i/3]
		}
		clear(scratch) // was a copy() from a 64MB always-zero twin allocated per call
		gcComposite(gl, cv, scratch, w)
		for i := 0; i < w*h; i++ {
			// The Gram is WEIGHTED, so the power iteration must be too: the perceptual weight
			// reaches its cap of 16-64 in shadows, and estimating L on the unweighted operator
			// would understate it by that factor and make the step size diverge.
			wt := weight[i]
			p := i * 4
			resid[p+0], resid[p+1], resid[p+2] = wt*scratch[p+0], wt*scratch[p+1], wt*scratch[p+2]
		}
		gcGradient(gl, resid, trans, g, w)
		for i := range g {
			g[i] /= sc[i/3]
		}
		var norm float64
		for _, x := range g {
			norm += x * x
		}
		norm = math.Sqrt(norm)
		if norm <= 0 {
			return 1
		}
		lam = norm
		for i := range v {
			v[i] = g[i] / norm
		}
	}
	return 2.2 * lam // the factor 2 of the quadratic, plus 10% headroom on the estimate
}

// Alpha is the other half of a layer's contribution, and the polish only approximates it: Adam on a
// soft-coverage surrogate. But for FIXED geometry and fixed colours the composite is AFFINE in one
// layer's alpha as well, so its optimum has a closed form.
//
// Split the stack at layer k. Everything above contributes a term that does not involve a_k;
// everything below (and the base) composites into V_k; the transmittance of the layers above is T_k:
//
//	out(x) = above(x) + T_k(x)·[ a_k(x)·c_k + (1−a_k(x))·V_k(x) ],  a_k = A_k·cov_k
//
// which is affine in A_k with slope s(x) = T_k(x)·cov_k(x)·(c_k − V_k(x)). One weighted least-squares
// per layer then gives the optimal A_k directly, clamped to the preset's organic floor — the floor
// the polish used to dismantle, so this must not re-open that hole.
//
// Sweeping TOP-DOWN keeps T_k incremental; V_k is the one thing that cannot be, so it is cached per
// layer under the same budget as the opacities. A layer whose cache did not fit keeps its alpha
// rather than being optimised against a stale V.

// gcAlphaSweep updates every layer's alpha in place (writing through to the cached opacities) and
// returns the number of layers it moved. c holds the current colours, out the current composite.
func gcAlphaSweep(gl []gcLayer, c []float64, base, target, weight []float32, vcache [][]float32,
	trans []float32, w, h int, aMin float64) int {
	_ = h
	for i := range trans {
		trans[i] = 1
	}
	// V for every layer, bottom-up: V_k is the composite of the base and everything below k.
	cur := make([]float32, w*h*4)
	copy(cur, base)
	for i := range gl {
		l := &gl[i]
		if !l.live {
			continue
		}
		bw := l.x1 - l.x0 + 1
		if vcache[i] != nil {
			for y := l.y0; y <= l.y1; y++ {
				row := (y - l.y0) * bw
				for x := l.x0; x <= l.x1; x++ {
					p := (y*w + x) * 4
					d := (row + x - l.x0) * 3
					vcache[i][d+0], vcache[i][d+1], vcache[i][d+2] = cur[p+0], cur[p+1], cur[p+2]
				}
			}
		}
		cr, cg, cb := float32(c[i*3+0]), float32(c[i*3+1]), float32(c[i*3+2])
		for y := l.y0; y <= l.y1; y++ {
			row := (y - l.y0) * bw
			for x := l.x0; x <= l.x1; x++ {
				a := l.at(x, y, row)
				if a <= 0 {
					continue
				}
				p := (y*w + x) * 4
				ia := 1 - a
				cur[p+0] = cur[p+0]*ia + a*cr
				cur[p+1] = cur[p+1]*ia + a*cg
				cur[p+2] = cur[p+2]*ia + a*cb
			}
		}
	}

	moved := 0
	for i := len(gl) - 1; i >= 0; i-- {
		l := &gl[i]
		if !l.live {
			continue
		}
		bw := l.x1 - l.x0 + 1
		v := vcache[i]
		if v == nil || l.a == nil || l.alpha <= 0 {
			// Cannot optimise this layer, but its transmittance still has to be carried down.
			for y := l.y0; y <= l.y1; y++ {
				row := (y - l.y0) * bw
				for x := l.x0; x <= l.x1; x++ {
					if a := l.at(x, y, row); a > 0 {
						trans[y*w+x] *= 1 - a
					}
				}
			}
			continue
		}
		cr, cg, cb := c[i*3+0], c[i*3+1], c[i*3+2]
		var num, den float64
		for y := l.y0; y <= l.y1; y++ {
			row := (y - l.y0) * bw
			for x := l.x0; x <= l.x1; x++ {
				cov := float64(l.a[row+x-l.x0]) / l.alpha // the geometry alone, alpha divided out
				if cov <= 0 {
					continue
				}
				ti := y*w + x
				t := float64(trans[ti])
				if t <= 0 {
					continue
				}
				wt := float64(weight[ti])
				if wt <= 0 {
					continue
				}
				p, d := ti*4, (row+x-l.x0)*3
				// Residual at A_k = 0: the composite with this layer fully transparent. Everything
				// above is `out − T·[a·c + (1−a)·V]`, which cancels to the same expression whether or
				// not the layer is present, so it is reconstructed from V rather than stored.
				aCur := float64(l.a[row+x-l.x0])
				for ch, cc := range [3]float64{cr, cg, cb} {
					vv := float64(v[d+ch])
					sl := t * cov * (cc - vv) // d(out)/d(A_k)
					if sl == 0 {
						continue
					}
					// The composite with this layer fully transparent, reconstructed from the full
					// composite rather than stored: out = r0 + T·a·(c − V).
					r0 := float64(cur[p+ch]) - t*aCur*(cc-vv)
					num += wt * sl * (float64(target[p+ch]) - r0)
					den += wt * sl * sl
				}
			}
		}
		if den > 0 {
			na := math.Min(1, math.Max(aMin, num/den))
			if math.Abs(na-l.alpha) > 1e-6 {
				sc := float32(na / l.alpha)
				// Carry the update into `cur`. r0 is reconstructed as cur − T·a·(c−V), and T is the
				// transmittance of the layers ABOVE with their UPDATED alphas — so cur has to be
				// updated too or the two describe different stacks: built once from the entry
				// alphas, it made r0 evaluate to A_old + T_old·V + (T_old−T_new)·a·(c−V) instead of
				// A_new + T_new·V, and every layer below the first one that moved was solved
				// against a composite that no longer existed. The change is confined to this
				// layer's bbox and is exact: cur += T·Δa·(c − V).
				for y := l.y0; y <= l.y1; y++ {
					row := (y - l.y0) * bw
					for x := l.x0; x <= l.x1; x++ {
						d := (row + x - l.x0)
						aOld := l.a[d]
						if aOld <= 0 {
							continue
						}
						ti := y*w + x
						t := trans[ti]
						if t <= 0 {
							continue
						}
						da := t * aOld * (sc - 1)
						p, d3 := ti*4, d*3
						cur[p+0] += da * (float32(cr) - v[d3+0])
						cur[p+1] += da * (float32(cg) - v[d3+1])
						cur[p+2] += da * (float32(cb) - v[d3+2])
					}
				}
				for j := range l.a {
					l.a[j] *= sc
				}
				l.alpha = na
				moved++
			}
		}
		for y := l.y0; y <= l.y1; y++ {
			row := (y - l.y0) * bw
			for x := l.x0; x <= l.x1; x++ {
				if a := l.at(x, y, row); a > 0 {
					trans[y*w+x] *= 1 - a
				}
			}
		}
	}
	return moved
}

// gcColorFISTA runs the Jacobi-rescaled projected FISTA for the colours of the CURRENT alphas,
// overwriting c, and returns the weighted SSE it reached. Split out of the solver so an alpha sweep
// can be followed by a fresh colour solve: alpha changes the Gram, so its diagonal and the step size
// must be recomputed rather than carried over.
func gcColorFISTA(gl []gcLayer, c []float64, base, target, weight, scratch, resid, trans []float32,
	g []float64, w, h, iters int) float64 {
	n := len(gl)
	d := make([]float64, n)
	gcDiag(gl, weight, trans, d, w)
	sc := make([]float64, n)
	for i := range sc {
		if d[i] > 0 {
			sc[i] = math.Sqrt(d[i])
		} else {
			sc[i] = 1 // a layer nothing sees; its colour is arbitrary, keep it finite
		}
	}
	step := 1 / gcLipschitz(gl, weight, sc, resid, trans, g, w, h)

	z := make([]float64, n*3)
	for i := range z {
		z[i] = c[i] * sc[i/3]
	}
	y := append([]float64(nil), z...)
	prev := append([]float64(nil), z...)
	cy := make([]float64, n*3)
	tk := 1.0
	for it := 0; it < iters; it++ {
		for i := range cy {
			cy[i] = y[i] / sc[i/3]
		}
		gcStep(gl, cy, base, target, weight, scratch, resid, trans, g, w, h)
		for i := range z {
			k := i / 3
			v := y[i] - 2*step*g[i]/sc[k] // the SSE's gradient carries the factor 2
			z[i] = math.Min(sc[k], math.Max(0, v))
		}
		tn := (1 + math.Sqrt(1+4*tk*tk)) / 2
		mom := (tk - 1) / tn
		for i := range z {
			y[i] = z[i] + mom*(z[i]-prev[i])
		}
		copy(prev, z)
		tk = tn
	}
	for i := range c {
		c[i] = z[i] / sc[i/3]
	}
	return gcStep(gl, c, base, target, weight, scratch, resid, trans, g, w, h)
}
