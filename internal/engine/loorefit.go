package engine

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"

	"fh6-paint-studio/internal/applog"
)

// LOO refit (Options.LooRefit rounds): after the polish, measure every shape's TRUE leave-one-out
// fit contribution — the weighted-SSE change from removing the shape while keeping everything else
// (occlusion-aware: exactly what the shape still does in the SHIPPED stack, not what it scored when
// placed) — then prune the shapes that HURT or barely help, regrow the freed budget against the
// residual (backFit's machinery), re-polish, and gate end-to-end. Measured motivation (img_10
// @3000 polished): 17.8% of shapes are individually harmful-or-neutral and the weakest half carries
// <7% of the positive fit — greedy scores at placement time, later shapes overpaint, polish can
// only fade, and the budget the owner picks is silently wasted.

const (
	looStride   = 4096 // sampled pixels per shape for the LOO measure
	looTinyFrac = 0.02 // also prune positive-but-tiny shapes: fit below this fraction of the median positive fit
)

// looPruneCap / looMinRegrow decide how far a round may go and when the loop gives up. They are
// pinned here rather than hard-coded because an exact leave-one-out pass over a FINISHED 3000-shape
// run still finds 10.8% of shapes hurting the fit, and the rounds converge (identical output at 6,
// 8 and 10 rounds) well before that residue is gone -- so the question of whether a bolder cap
// reaches it is worth an A/B. FH6_LOO_PRUNE ("cap,minRegrow") overrides both.
var looPruneCap, looMinRegrow = func() (float64, int) {
	cap_, min_ := 0.25, 8
	if v := os.Getenv("FH6_LOO_PRUNE"); v != "" {
		var c float64
		var m int
		if n, err := fmt.Sscanf(v, "%f,%d", &c, &m); n == 2 && err == nil && c > 0 && c <= 1 && m >= 1 {
			cap_, min_ = c, m
		}
	}
	return cap_, min_
}()

type looRefitPass struct{}

func (looRefitPass) enabled(opt Options) bool { return opt.LooRefit > 0 && opt.Polish }

// looWarmFrac / looWarmTau0 tune the LOO re-polish as a WARM start. The initial polish runs a full
// soft→hard tau anneal over the configured Iters to escape the greedy plateau; the LOO re-polish
// starts from an ALREADY-polished stack where only ~5% of shapes changed (prune+merge+regrow), so
// that soft exploration re-softens converged shapes only to re-sharpen them — wasted. A sharper Tau0
// (1.0 vs the 2.0 default) and half the iters re-settle the changed shapes at ~55% of the cost.
// Measured 2026-07-20 (img_10/img_24 @3000): re-polish 29s→16-17s (~1.7×), the rendered (gate) error
// is EQUAL-or-better, unweighted SSE wobbles ±0.5% in a content-dependent direction (img_10 +0.4%,
// img_24 −0.2%) — perceptually indistinguishable by eye on both smooth and structured content, and
// the round's end-to-end gate still guarantees no regression vs no-LOO. FH6_LOO_WARM ("frac,tau0")
// overrides for A/Bs; frac<=0 or >=1 disables (full re-polish).
var looWarmFrac, looWarmTau0 = func() (float64, float64) {
	frac, tau0 := 0.5, 1.0 // warm-start defaults (measured; see above)
	if s := os.Getenv("FH6_LOO_WARM"); s != "" {
		var f, t float64
		if n, _ := fmtSscan(s, &f, &t); n == 2 {
			frac, tau0 = f, t
		}
	}
	return frac, tau0
}()

// warmRepolishOpts returns r.opt with the polish tuned for the LOO re-polish's warm start (see
// looWarmFrac). Unchanged when warm-start is disabled.
func (r *run) warmRepolishOpts() Options {
	opt := r.opt
	if looWarmFrac > 0 && looWarmFrac < 1 {
		po := opt.PolishOpts
		po.Iters = maxInt(30, int(float64(po.Iters)*looWarmFrac))
		if looWarmTau0 > 0 {
			po.Tau0 = looWarmTau0
		}
		opt.PolishOpts = po
	}
	return opt
}

func (looRefitPass) apply(r *run) {
	r.setStatus("LOO refit…")
	env := r.newBackfitEnv()
	target, weight := r.be.Target(), r.be.Weight()
	for round := 0; round < r.opt.LooRefit; round++ {
		if r.opt.Cancel != nil && r.opt.Cancel() {
			break // Stop pressed: the stack as of the last accepted round is a complete result
		}
		// Progress inside the phase is per-round: base..base+span. Without this the re-polish below
		// reports its own iterations through the run-wide hook and drives the PHASE counter to 100%
		// in round 0 — the bar then sits frozen for the remaining rounds (the counter is forward-only).
		base := float64(round) / float64(r.opt.LooRefit)
		span := 1.0 / float64(r.opt.LooRefit)
		r.eta.setFrac(base)
		r.tm.LooRounds++
		tRound := time.Now()
		errBefore := r.finalErr
		// Colours first, then the ranking. looFitContrib asks what each shape CONTRIBUTES, but a
		// shape still carrying the colour the greedy fitted against a canvas later shapes have
		// overwritten looks harmful for a reason that has nothing to do with redundancy — so the
		// prune would be ranking staleness. Re-solving first makes the ranking measure the thing it
		// is named after. Gated like everything else: a solve that does not lower the hard render is
		// discarded and the round proceeds on the original stack.
		var gcDur time.Duration
		if r.opt.GlobalColorInLoo && r.opt.GlobalColorIters > 0 {
			t0 := time.Now()
			if cand, _, _, ok := globalColorSolve(r.shapes, target, weight, r.w, r.h,
				r.opt.Background, r.opt.TransparentBG, r.opt.GlobalColorIters); ok {
				if e := rerender(r.be, r.initCanvas, cand); e+1e-9 < r.finalErr {
					r.shapes, r.finalErr = cand, e
				} else {
					rerender(r.be, r.initCanvas, r.shapes)
				}
			}
			gcDur = time.Since(t0)
			r.tm.GlobalColor += gcDur
		}
		tRank := time.Now()
		fit := looFitContrib(r.shapes, target, weight, r.w, r.h)
		rankDur := time.Since(tRank)
		drop := looSelectPrune(fit)
		kept := make([]model.Shape, 0, len(r.shapes))
		for j := range r.shapes {
			if j == 0 || !drop[j] {
				kept = append(kept, cloneShape(r.shapes[j]))
			}
		}
		// Merge consolidation (Options.MergeRefit): near-duplicate pairs collapse into one fitted
		// shape — a second slot source beside the LOO prune; the shared regrow/re-polish/gate below
		// restores quality for both (see mergerefit.go).
		merged := 0
		if r.opt.MergeRefit {
			t0 := time.Now()
			kept, merged = mergePairs(kept, r.w, r.h)
			r.tm.MergeRefit += time.Since(t0)
			applog.Printf("loo round %d: pruned %d, merged %d pairs", round, len(r.shapes)-len(kept)+merged, merged)
		}
		if len(drop)+merged < looMinRegrow {
			break
		}
		// Re-render the survivors, regrow the freed budget against the residual (backFit's loop).
		tRegrow := time.Now()
		_ = r.be.Reset(r.initCanvas)
		applyShapes(r.be, kept[1:])
		targetCount := len(r.shapes)
		grid, gw, gh, _ := r.be.ErrorGrid()
		sampler := NewErrorSampler(grid, gw, gh, r.w, r.h)
		regrown := 0
		for len(kept) < targetCount {
			progress := float32(len(kept)-1) / float32(targetCount-1)
			best, bestScore := env.searchOne(sampler, grid, gw, gh, len(kept)-1, progress)
			if bestScore >= -1e-7 {
				break
			}
			_ = r.be.Apply(best)
			kept = append(kept, best.ToShape(float64(bestScore)))
			regrown++
			grid, gw, gh, _ = r.be.ErrorGrid()
			sampler = NewErrorSampler(grid, gw, gh, r.w, r.h)
		}
		regrowDur := time.Since(tRegrow)
		r.eta.setFrac(base + 0.2*span)
		// Re-polish the refitted set and gate END-TO-END on the rendered error. Its iteration
		// progress is rescaled into this round's remaining window — the run-wide hook would report
		// it as 0..100% of the whole phase.
		popt := r.warmRepolishOpts()
		popt.PolishOpts.OnProgress = func(iter, total int) {
			if total > 0 {
				r.eta.setFrac(base + span*(0.2+0.8*float64(iter)/float64(total)))
			}
		}
		tPol := time.Now()
		// The device canvas ALREADY holds exactly `kept` (the Reset+Apply loop above plus one
		// Apply per regrown shape), and `grid` is current for it on every regrow-loop exit path —
		// a rerender() here was a full Reset + n fenced Applies purely to recompute this sum.
		preErr := sumGrid(grid)
		candShapes, candErr := applyPolish(r.be, kept, preErr, r.initCanvas, popt, r.w, r.h, &r.tm)
		applog.Printf("loo round %d walls: gcolor %.1fs rank %.1fs regrow %.1fs(%d shapes) repolish %.1fs total %.1fs err %.1f -> %.1f",
			round, gcDur.Seconds(), rankDur.Seconds(), regrowDur.Seconds(), regrown, time.Since(tPol).Seconds(), time.Since(tRound).Seconds(),
			errBefore, math.Min(candErr, r.finalErr))
		if candErr+1e-9 < r.finalErr {
			r.shapes, r.finalErr = candShapes, candErr
		} else {
			rerender(r.be, r.initCanvas, r.shapes)
			break
		}
	}
}

// looSelectPrune picks the prune set: every strictly-harmful shape plus the positive-but-tiny tail,
// capped at looPruneCap of the stack (worst first — LOO deltas interact, so huge sweeps lie).
func looSelectPrune(fit []float64) map[int]bool {
	n := len(fit)
	pos := make([]float64, 0, n)
	for j := 1; j < n; j++ {
		if fit[j] > 0 {
			pos = append(pos, fit[j])
		}
	}
	tiny := 0.0
	if len(pos) > 0 {
		sort.Float64s(pos)
		tiny = looTinyFrac * pos[len(pos)/2]
	}
	type ranked struct {
		idx int
		f   float64
	}
	var cand []ranked
	for j := 1; j < n; j++ {
		if fit[j] <= tiny {
			cand = append(cand, ranked{j, fit[j]})
		}
	}
	sort.Slice(cand, func(a, b int) bool { return cand[a].f < cand[b].f })
	cap := int(looPruneCap * float64(n-1))
	if len(cand) > cap {
		cand = cand[:cap]
	}
	drop := make(map[int]bool, len(cand))
	for _, c := range cand {
		drop[c.idx] = true
	}
	return drop
}

// looFitContrib returns, per shape, the weighted-SSE improvement the shape still delivers in the
// final stack: SSE(without shape, vs target) − SSE(with, vs target) over its bbox, sample-strided.
// Positive = helps; ≤0 = the image is better off without it. Parallel across shapes.
func looFitContrib(shapes []model.Shape, target, weight []float32, w, h int) []float64 {
	n := len(shapes)
	type geom struct {
		kind model.ShapeKind
		prep raster.Prepared // per-shape constants hoisted out of the per-pixel loops below
		col  [4]float32
		bbox [4]int
	}
	gs := make([]geom, n)
	for i, s := range shapes {
		k := model.KindFromType(s.Type)
		p := model.ParamsFromShape(s)
		var col [4]float32
		if len(s.Color) >= 4 {
			col = [4]float32{model.DecChan(s.Color[0]), model.DecChan(s.Color[1]), model.DecChan(s.Color[2]), float32(s.Color[3]) / 255}
		} else {
			col[3] = 1
		}
		x0, y0, x1, y1 := raster.BBox(k, p, w, h)
		gs[i] = geom{kind: k, prep: raster.Prep(k, p), col: col, bbox: [4]int{x0, y0, x1, y1}}
	}
	// Tile index over the stack. The composite at a pixel only involves shapes whose bbox contains
	// that pixel, but the per-shape overlap list is everything intersecting the WHOLE bbox — for a
	// background-sized shape that is the entire stack, re-tested at every sampled pixel. Bucketing
	// by tile keeps the inner loop at the handful that actually reach the pixel; the composited set
	// and its z-order are unchanged (tile lists are built in shape order), so the result is identical.
	const looTile = 64
	tw, th := (w+looTile-1)/looTile, (h+looTile-1)/looTile
	tiles := make([][]int32, tw*th)
	for j := range gs {
		b := gs[j].bbox
		if b[2] < b[0] || b[3] < b[1] {
			continue
		}
		for ty := b[1] / looTile; ty <= b[3]/looTile; ty++ {
			row := ty * tw
			for tx := b[0] / looTile; tx <= b[2]/looTile; tx++ {
				tiles[row+tx] = append(tiles[row+tx], int32(j))
			}
		}
	}
	fit := make([]float64, n)
	var wg sync.WaitGroup
	jobs := make(chan int, runtime.NumCPU()*2)
	for wk := 0; wk < runtime.NumCPU(); wk++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				b := gs[i].bbox
				if b[2] < b[0] || b[3] < b[1] || gs[i].col[3] <= 0 {
					continue
				}
				area := (b[2] - b[0] + 1) * (b[3] - b[1] + 1)
				step := 1
				if area > looStride {
					if s := int(math.Sqrt(float64(area) / float64(looStride))); s > 1 {
						step = s
					}
				}
				var d float64
				for y := b[1]; y <= b[3]; y += step {
					trow := (y / looTile) * tw
					for x := b[0]; x <= b[2]; x += step {
						// Where shape i itself has no coverage, removing it changes nothing (the
						// with/without composites process the identical shape set), so without−full = 0
						// exactly — skip the whole overlap composite. A big cut for shapes whose footprint
						// is much smaller than their bbox (triangles, rotated/thin ellipses); provably
						// quality-neutral (the skipped pixels contribute 0 to the LOO delta).
						if gs[i].prep.Coverage(x, y) <= 0 {
							continue
						}
						var fr, fg, fb, wr, wgc, wb float32
						for _, jj := range tiles[trow+x/looTile] {
							g := &gs[jj]
							if x < g.bbox[0] || x > g.bbox[2] || y < g.bbox[1] || y > g.bbox[3] {
								continue
							}
							a := float32(g.prep.Coverage(x, y)) * g.col[3]
							if a <= 0 {
								continue
							}
							inv := 1 - a
							fr = fr*inv + g.col[0]*a
							fg = fg*inv + g.col[1]*a
							fb = fb*inv + g.col[2]*a
							if int(jj) != i {
								wr = wr*inv + g.col[0]*a
								wgc = wgc*inv + g.col[1]*a
								wb = wb*inv + g.col[2]*a
							}
						}
						idx := y*w + x
						p := idx * 4
						wt := float64(weight[idx])
						tr, tg, tb := float64(target[p]), float64(target[p+1]), float64(target[p+2])
						full := (float64(fr)-tr)*(float64(fr)-tr) + (float64(fg)-tg)*(float64(fg)-tg) + (float64(fb)-tb)*(float64(fb)-tb)
						without := (float64(wr)-tr)*(float64(wr)-tr) + (float64(wgc)-tg)*(float64(wgc)-tg) + (float64(wb)-tb)*(float64(wb)-tb)
						d += wt * (without - full)
					}
				}
				fit[i] = d * float64(step*step)
			}
		}()
	}
	for i := 1; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return fit
}
