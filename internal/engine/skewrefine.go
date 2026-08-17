package engine

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// Local geometry refine: after everything else has converged, walk each shape's geometry parameters
// one at a time and keep only the moves that lower the exact occlusion-aware local error.
//
// Two options share this machinery.
//
// Options.SkewRefine offers slot 5 — a SHEAR — to the kinds it gives a new silhouette:
//   - rectangle: a parallelogram, the one shape a shear genuinely creates.
//   - mask word: the coverage texture is resampled through the shear, so a gradient bar can shade
//     along one direction while its footprint faces another. Rotation alone locks those together.
//   - ellipse, glow, disk: NOTHING. Shearing an ellipse yields another rotated ellipse, so the
//     footprint is already reachable and the search would only re-find it.
//
// Options.GeomRefine walks every geometry parameter of every shape. The motive is a measurement, not
// a hunch: a one-dimensional line search over an ELLIPSE's shear recovered 0.5-3% of the error even
// though a sheared ellipse is a shape the polish could already reach. Nothing was gained but a
// direction to slide in, which means the polish leaves its shapes short of the local optimum and any
// spare degree of freedom cashes that in. A direct coordinate search collects it without pretending
// the extra parameter was the point.
//
// Why a post-pass rather than more parameters in the descent. Skew was tried as a sixth polish DOF
// and cleared the bar on only 4 of 7 pairs: inside the optimiser a shear trades against rotation and
// width, so it moves the whole basin and the run lands somewhere else — better on some images, worse
// on others. Accepting a move only when it lowers the error removes that coupling. A search-side
// change carries about 2.8% paired noise, which buries any small mean effect; a monotone pass has
// none of it, and its worst case is wasted time rather than a worse image.

var refineDebug = os.Getenv("FH6_REFINE_DEBUG") != ""

var refineTried, refineVetoed int64

// refineUnweighted scores trials WITHOUT the perceptual weight map. The weighting lifts dark pixels
// by up to 64x, so a pass that optimises it hard buys dark-region accuracy with bright-region
// accuracy — and the number this project ships on is the UNWEIGHTED in-game SSE. Measured on img_11:
// the weighted form cut the engine's own error 4.76% and made the in-game score 1.25% WORSE.
var refineUnweighted = os.Getenv("FH6_REFINE_UNWEIGHTED") == "1"

func srdbg(format string, a ...interface{}) {
	if refineDebug {
		fmt.Fprintf(os.Stderr, "[refine] "+format+"\n", a...)
	}
}

const (
	// refineSkewMax bounds the shear. Past this the parallelogram is a sliver that no longer
	// resembles the shape the rest of the stack settled around.
	refineSkewMax = 1.2
	// refineMinGain is the fraction of a shape's own local error a move must recover to be kept.
	// Without a floor the pass banks rounding-level "wins" that do not survive the re-render.
	refineMinGain = 0.002
	// refineStride caps the pixels sampled per shape, as looFitContrib does.
	refineStride = 4096
)

// refineScaleGain: scale each shape's gain by step² so the cross-shape conflict sort compares
// true pixel units. MEASURED 2026-08-16, n=27 paired: mean +0.72% WORSE, 22/27 (REJECT) — the
// "wrong" unscaled ordering is accidentally protective (it prioritises small, safe moves; large
// shapes' exact-at-judgment gains compose worse across overlapping accepts). Default stays the
// shipped unscaled sort; FH6_SKEW_SCALE=1 keeps the experiment reachable.
var refineScaleGain = os.Getenv("FH6_SKEW_SCALE") == "1"

const (
	refineTile = 64
	// refineSweeps is how many times the parameter list is walked. Parameters interact — moving a
	// centre changes the best width — but the second sweep finds far less than the first, and a third
	// costs another full pass for almost nothing.
	refineSweeps = 2
	// refineRounds caps the loop; refineRoundFloor ends it early. Only moves with disjoint windows may
	// commit together, so the rest wait for a later round — and because a shape that moves changes what
	// its neighbours want, each round opens new ground rather than merely mopping up. A fixed count was
	// the wrong shape for that: measured, the pass was still finding real gains at round 24, while on
	// other images it runs dry much sooner and the remaining rounds are pure wall. So the loop stops
	// when a round's accepted gain falls below a fraction of the FIRST round's, which spends rounds
	// only where they are still paying. FH6_REFINE_ROUNDS overrides the cap for an A/B.
	refineRounds     = 40
	refineRoundFloor = 0.05
	// refineShrink / refineMinStep drive the per-parameter pattern search: probe both directions,
	// follow a winner while it keeps paying, otherwise shrink. A grid would spend the same budget
	// sampling places the shape plainly does not want to be.
	refineShrink  = 0.4
	refineMinStep = 0.05
	// How far a single parameter may travel from where the polish left it. This is the leash that
	// makes the fixed scoring window sound, and it is also the right modelling choice: the pass exists
	// to finish a shape the optimiser left short, not to re-place it somewhere else. A shape that
	// wants to be far away is the greedy's business, not this pass's.
	// A move may not push paint onto ground that is already clean and that the shape was not on.
	// Monotone in the SUM is not enough: the sum will trade a new visible blemish for a diffuse gain,
	// and the owner caught exactly that — two pale smudges pushed out onto img_24's white background
	// in a run the metric called 4.58% better. Note what is NOT forbidden: a shape that VACATES clean
	// ground, or that changes a pixel already wrong. Those are honest relocations and the sum judges
	// them; forbidding any pixel from getting worse blocked even the unit test's recovery of a plainly
	// misplaced rectangle. refineCleanDev is how close to the target counts as clean, per channel.
	refineCleanDev = 0.03
	// ...and how much of it a move may take. A veto on a SINGLE intruding pixel was too absolute: when
	// a shape's angle differs from the region it is fitting, any growth sweeps a corner over clean
	// ground, and the unit test's plainly-misplaced rectangle stopped recovering at all. What separates
	// the artefact from the sliver is area — the smudges were hundreds of pixels each.
	refineIntrudeMax   = 64.0 // px of new paint on clean ground
	refineTravel       = 9.0  // px, for centres and vertices
	refineAngleTravel  = 12.0 // degrees
	refineExtentTravel = 0.20 // fraction of the current half-extent
)

// The pass is registered TWICE, before and after the global colour solve, and only one position is
// ever active. Position matters: moving geometry after the colours are solved leaves every moved
// shape wearing a colour fitted to where it used to be, while moving it before lets the solve adapt.
// Which one wins is a measurement, not an argument, so it is a switch.
type skewRefinePass struct{ early bool }

// The pass runs BEFORE the global colour solve by default. Measured: moving it there is better on
// every case tried and remarkably evenly (-0.675%, -0.732%, -0.857%, sd 0.09%) for +0.5% wall, and it
// is what turns photo from a loss into a win. The reason is plain once seen — a shape moved after the
// colours are solved keeps a colour fitted to where it used to be, while a shape moved before it
// gets one fitted to where it now is. FH6_REFINE_LATE=1 puts it back for an A/B.
var refineEarly = os.Getenv("FH6_REFINE_LATE") != "1"

// refineFloor / refineSweepsN: the two knobs that decide how much of this pass actually runs.
// The floor stops a round whose accepted gain falls under a fraction of the first round's, and
// the sweep count is how many times the parameter list is walked before a shape is committed.
// Both were fixed at values chosen to bound wall time — but this pass is the single largest CPU
// block of a run (measured: localRefine is 50% of the CPU samples, 16% of the wall), which means
// it is also where more spending has the most room to buy quality. FH6_REFINE_FLOOR and
// FH6_REFINE_SWEEPS make that measurable without a rebuild.
var refineFloor = func() float64 {
	if v := os.Getenv("FH6_REFINE_FLOOR"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f < 1 {
			return f
		}
	}
	return refineRoundFloor
}()

var refineSweepsN = func() int {
	if v := os.Getenv("FH6_REFINE_SWEEPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 8 {
			return n
		}
	}
	return refineSweeps
}()

// refineRoundsOverride lets an A/B move the round count without a rebuild.
var refineRoundsOverride = func() int {
	if v := os.Getenv("FH6_REFINE_ROUNDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return refineRounds
}()

func (p skewRefinePass) enabled(opt Options) bool {
	return (opt.SkewRefine || opt.GeomRefine) && p.early == refineEarly
}

func (skewRefinePass) apply(r *run) {
	r.setStatus("Refining geometry…")
	before := r.finalErr
	shapes, changed := localRefine(r.be, r.shapes, r.be.Target(), r.be.Weight(), r.w, r.h, r.opt.GeomRefine)
	if changed == 0 {
		return
	}
	// End-to-end gate. Each shape was judged against the stack as it stood, so overlapping
	// acceptances are not guaranteed to compose; the re-render is the only honest arbiter.
	rerenderStack(r, shapes)
	g, _, _, err := r.be.ErrorGrid()
	if err != nil {
		rerenderStack(r, r.shapes)
		return
	}
	after := sumGrid(g)
	if after >= before {
		srdbg("rolled back: %d moves, error %.1f -> %.1f", changed, before, after)
		rerenderStack(r, r.shapes)
		return
	}
	srdbg("kept %d moves, error %.1f -> %.1f (%.3f%%)", changed, before, after, (after-before)/before*100)
	r.shapes, r.finalErr = shapes, after
}

func rerenderStack(r *run, shapes []model.Shape) {
	_ = r.be.Reset(r.initCanvas)
	applyShapes(r.be, shapes[1:])
}

// skewEligible reports whether a shear gives this kind a shape it cannot already reach.
func skewEligible(k model.ShapeKind) bool {
	return k == model.KindRectangle || model.IsMask(k)
}

// refineAxis is one searchable parameter: which slot, how far a first probe moves it, and the limits.
type refineAxis struct {
	slot int
	step float64
	lo   float64
	hi   float64
}

// refineAxes lists what may move for a kind. Steps are in the parameter's own units — pixels for
// positions and extents, degrees for the angle, a bare factor for the shear — and sized so the first
// probe is a visible move rather than a rounding difference.
func refineAxes(k model.ShapeKind, p [6]float32, geom bool) []refineAxis {
	// Every axis is BOUNDED around its current value. The bound is not a taste question: the search
	// scores each trial over one fixed window, so a shape allowed to walk out of that window would be
	// charged nothing for the coverage it took with it, and "leave the frame" would read as the best
	// move available. The window below is built from exactly these bounds.
	span := func(slot int, step, travel float64, lo, hi float64) refineAxis {
		v := float64(p[slot])
		l, hgh := v-travel, v+travel
		if l < lo {
			l = lo
		}
		if hgh > hi {
			hgh = hi
		}
		return refineAxis{slot, step, l, hgh}
	}
	if !geom {
		if skewEligible(k) {
			return []refineAxis{{5, 0.15, -refineSkewMax, refineSkewMax}}
		}
		return nil
	}
	switch k {
	case model.KindTriangle:
		// All six slots are vertex coordinates; there is no shear to add.
		out := make([]refineAxis, 0, 6)
		for s := 0; s < 6; s++ {
			out = append(out, span(s, 1.5, refineTravel, -1e9, 1e9))
		}
		return out
	case model.KindLine:
		return []refineAxis{
			span(0, 1.5, refineTravel, -1e9, 1e9), span(1, 1.5, refineTravel, -1e9, 1e9),
			span(2, 1.5, refineTravel, -1e9, 1e9), span(3, 1.5, refineTravel, -1e9, 1e9),
			span(4, 0.4, 0.4*refineTravel, 0.5, 1e9),
		}
	}
	// Box-framed kinds: centre, the two extents, the angle, and a shear where it means something.
	// Extent steps and travel scale with the shape, so a 400px background and a 6px speck each get a
	// probe worth making and a leash of its own size.
	ex := math.Max(0.6, 0.03*float64(p[2]))
	ey := math.Max(0.6, 0.03*float64(p[3]))
	lo := 0.5
	if k == model.KindEllipse || k == model.KindGlow || k == model.KindDisk {
		lo = 1.0
	}
	out := []refineAxis{
		span(0, 1.2, refineTravel, -1e9, 1e9), span(1, 1.2, refineTravel, -1e9, 1e9),
		span(2, ex, refineExtentTravel*float64(p[2])+2, lo, 1e9),
		span(3, ey, refineExtentTravel*float64(p[3])+2, lo, 1e9),
		span(4, 1.5, refineAngleTravel, -1e9, 1e9),
	}
	if skewEligible(k) {
		out = append(out, refineAxis{5, 0.15, -refineSkewMax, refineSkewMax})
	}
	return out
}

// localRefine searches every shape against the frozen stack and returns the updated shapes plus how
// many moved.
func localRefine(dev any, shapes []model.Shape, target, weight []float32, w, h int, geom bool) ([]model.Shape, int) {
	n := len(shapes)
	if n < 2 {
		return shapes, 0
	}
	type geomRec struct {
		kind model.ShapeKind
		p    [6]float32
		prep raster.Prepared
		col  [4]float32
		bbox [4]int
	}
	gs := make([]geomRec, n)
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
		gs[i] = geomRec{kind: k, p: p, prep: raster.Prep(k, p), col: col, bbox: [4]int{x0, y0, x1, y1}}
	}

	// Tile index over the stack, as looFitContrib builds it: the composite at a pixel only involves
	// shapes whose box reaches it, and without the index a background-sized shape re-tests the whole
	// stack at every sampled pixel. A movable shape is indexed under its SEARCH window, not its
	// current box — in a tile where it is not listed a trial would score against a stack the shape had
	// silently dropped out of, and the move would look free.
	tw, th := (w+refineTile-1)/refineTile, (h+refineTile-1)/refineTile
	tiles := make([][]int32, tw*th)
	windows := make([][4]int, n)
	committed := make([]bool, n)
	for j := range gs {
		windows[j] = searchWindow(gs[j].kind, gs[j].p, refineAxes(gs[j].kind, gs[j].p, geom), w, h)
	}
	rebuildTiles := func() {
		for i := range tiles {
			tiles[i] = tiles[i][:0]
		}
		for j := range gs {
			b := windows[j]
			if b[2] < b[0] || b[3] < b[1] {
				continue
			}
			for ty := b[1] / refineTile; ty <= b[3]/refineTile; ty++ {
				row := ty * tw
				for tx := b[0] / refineTile; tx <= b[2]/refineTile; tx++ {
					tiles[row+tx] = append(tiles[row+tx], int32(j))
				}
			}
		}
	}
	rebuildTiles()

	// Per-shape scoring context. Compositing "over" is AFFINE in whatever colour sits under the top
	// of the stack: applying the shapes above shape i to a colour X always yields T*X + C, where T is
	// the product of their transmittances and C their premultiplied accumulation. Neither depends on
	// shape i. So the stack is walked ONCE per shape to collect, per sampled pixel, the colour below i
	// and that (T, C) pair — after which a trial costs one blend per pixel instead of a walk through
	// every overlapping shape. With tens of trials per shape over a stack ten deep that is most of the
	// pass's cost removed, and it is exact rather than an approximation.
	type winCtx struct {
		nx, ny, step int
		b            [4]int
		below        []float32 // 3 per sample: the composite under shape i
		T            []float32 // 1 per sample
		C            []float32 // 3 per sample
		dev          []float32 // 1 per sample: the shipped geometry's own worst-channel miss
		cov          []float32 // 1 per sample: how much the shipped geometry itself covered
		tgt          []float32 // 3 per sample: the target, gathered once per shape
		wt           []float64 // 1 per sample: the resolved pixel weight, ditto
		eZero        []float64 // 1 per sample: the error term when the trial covers NOTHING here
		intruded     float64   // filled by trialErr: px of new paint this trial put on clean ground
	}
	build := func(ctx *winCtx, i int, b [4]int, step int) {
		nx := (b[2]-b[0])/step + 1
		ny := (b[3]-b[1])/step + 1
		need := nx * ny
		if cap(ctx.below) < need*3 {
			ctx.below = make([]float32, need*3)
			ctx.C = make([]float32, need*3)
			ctx.T = make([]float32, need)
		}
		if cap(ctx.dev) < need {
			ctx.dev = make([]float32, need)
			ctx.cov = make([]float32, need)
		}
		if cap(ctx.tgt) < need*3 {
			ctx.tgt = make([]float32, need*3)
			ctx.wt = make([]float64, need)
			ctx.eZero = make([]float64, need)
		}
		ctx.nx, ctx.ny, ctx.step, ctx.b = nx, ny, step, b
		ctx.below, ctx.C, ctx.T = ctx.below[:need*3], ctx.C[:need*3], ctx.T[:need]
		ctx.dev, ctx.cov = ctx.dev[:need], ctx.cov[:need]
		ctx.tgt, ctx.wt, ctx.eZero = ctx.tgt[:need*3], ctx.wt[:need], ctx.eZero[:need]
		si := 0
		for y := b[1]; y <= b[3]; y += step {
			trow := (y / refineTile) * tw
			for x := b[0]; x <= b[2]; x += step {
				// Gather the target and weight for this sample once. Every trial of this shape used
				// to re-read them through strided indices into two canvas-sized arrays — with
				// step>1 that is a fresh cache line per sample per trial, and it was 15-25% of the
				// pass. Same values, same order: the score is byte-identical.
				{
					idx := y*w + x
					q := idx * 4
					ctx.tgt[si*3+0], ctx.tgt[si*3+1], ctx.tgt[si*3+2] = target[q], target[q+1], target[q+2]
					if refineUnweighted {
						ctx.wt[si] = 1
					} else {
						ctx.wt[si] = float64(weight[idx])
					}
				}
				var br, bg, bb float32
				var cr, cg, cb float32
				tt := float32(1)
				for _, jj := range tiles[trow+x/refineTile] {
					if int(jj) == i {
						continue
					}
					g := &gs[jj]
					if x < g.bbox[0] || x > g.bbox[2] || y < g.bbox[1] || y > g.bbox[3] {
						continue
					}
					a := float32(g.prep.Coverage(x, y)) * g.col[3]
					if a <= 0 {
						continue
					}
					inv := 1 - a
					if int(jj) < i {
						br = br*inv + g.col[0]*a
						bg = bg*inv + g.col[1]*a
						bb = bb*inv + g.col[2]*a
					} else {
						cr = cr*inv + g.col[0]*a
						cg = cg*inv + g.col[1]*a
						cb = cb*inv + g.col[2]*a
						tt *= inv
					}
				}
				ctx.below[si*3+0], ctx.below[si*3+1], ctx.below[si*3+2] = br, bg, bb
				ctx.C[si*3+0], ctx.C[si*3+1], ctx.C[si*3+2] = cr, cg, cb
				ctx.T[si] = tt
				// The zero-coverage error term, precomputed with trialErr's exact arithmetic at
				// c==0 (a=0 leaves the blend at `below`, so f = T*below + C). A trial only pays
				// full evaluation inside its own bbox; every sample it cannot touch adds this
				// SAME value at the SAME position in the sum — byte-identical, most of the
				// window's Coverage() calls gone.
				{
					fr := tt*br + cr
					fg := tt*bg + cg
					fb := tt*bb + cb
					dr := float64(fr) - float64(ctx.tgt[si*3+0])
					dg := float64(fg) - float64(ctx.tgt[si*3+1])
					db := float64(fb) - float64(ctx.tgt[si*3+2])
					ctx.eZero[si] = ctx.wt[si] * (dr*dr + dg*dg + db*db)
				}
				si++
			}
		}
	}
	// trialErr scores one geometry for shape i against a prepared context. Alongside the sum it tracks
	// the largest RISE in any single pixel's miss, because the sum alone cannot see the thing the eye
	// catches first: a pass monotone in the total will happily trade a new visible blemish on clean
	// ground for a diffuse gain elsewhere. Measured — the owner spotted exactly that, two pale smudges
	// pushed onto img_24's white background, in a run the metric called 4.58% better.
	// bb is the TRIAL geometry's own bbox: outside it Coverage is exactly zero (every kind's
	// falloff has finite support inside raster.BBox), so those samples contribute ctx.eZero —
	// the identical value the full expression yields at c==0, added in the identical order.
	// The intrusion veto cannot fire at c==0 either, so skipping it there changes nothing.
	trialErr := func(ctx *winCtx, sub *raster.Prepared, alpha float32, col [4]float32, setDev bool, bb [4]int) float64 {
		var e float64
		ctx.intruded = 0
		area := float64(ctx.step * ctx.step) // each sample stands for this many pixels
		si := 0
		for y := ctx.b[1]; y <= ctx.b[3]; y += ctx.step {
			if y < bb[1] || y > bb[3] {
				for x := ctx.b[0]; x <= ctx.b[2]; x += ctx.step {
					e += ctx.eZero[si]
					si++
				}
				continue
			}
			for x := ctx.b[0]; x <= ctx.b[2]; x += ctx.step {
				if x < bb[0] || x > bb[2] {
					e += ctx.eZero[si]
					si++
					continue
				}
				c := float32(sub.Coverage(x, y))
				a := c * alpha
				inv := 1 - a
				xr := ctx.below[si*3+0]*inv + col[0]*a
				xg := ctx.below[si*3+1]*inv + col[1]*a
				xb := ctx.below[si*3+2]*inv + col[2]*a
				tt := ctx.T[si]
				fr := tt*xr + ctx.C[si*3+0]
				fg := tt*xg + ctx.C[si*3+1]
				fb := tt*xb + ctx.C[si*3+2]
				wt := ctx.wt[si]
				dr := float64(fr) - float64(ctx.tgt[si*3+0])
				dg := float64(fg) - float64(ctx.tgt[si*3+1])
				db := float64(fb) - float64(ctx.tgt[si*3+2])
				e += wt * (dr*dr + dg*dg + db*db)
				if setDev {
					// dev is only consumed here, on the once-per-shape context build; computing it on
					// every trial pixel was 12% of the whole run's CPU (math.Max never inlines).
					ctx.dev[si] = float32(math.Max(math.Abs(dr), math.Max(math.Abs(dg), math.Abs(db))))
					ctx.cov[si] = c
				} else if c > ctx.cov[si]+0.02 && ctx.dev[si] < refineCleanDev {
					// New ground that the picture ALREADY gets right. Whether the shape looks harmless
					// there right now is not the test: it wears the colour it had, and the global solve
					// runs after this pass and will re-price it. That is exactly how the two smudges got
					// onto img_24's white background — a shape crept out carrying a near-white colour,
					// cost nothing at trial time, and was handed a grey by the solve afterwards.
					ctx.intruded += area
				}
				si++
			}
		}
		return e
	}

	moved := make([][6]float32, n)
	touched := make([]bool, n)
	gain := make([]float64, n) // local error recovered, used to order the conflict filter below

	// The work list starts as every eligible shape and shrinks to the ones a round blocked.
	pending := make([]int, 0, n)
	for i := 1; i < n; i++ {
		if len(refineAxes(gs[i].kind, gs[i].p, geom)) > 0 {
			pending = append(pending, i)
		}
	}
	eligible := len(pending)
	changed := 0
	firstGain := 0.0

	// The device path. This pass was the largest CPU block of a run — 50% of the samples, 16% of the
	// wall, already goroutine-parallel — so the answer was never more cores. One workgroup per shape
	// runs the whole coordinate descent on the GPU (refine.comp); the host keeps the kinds the
	// shader does not frame (triangles, lines, bank words) and the conflict filter, which is a
	// sequential decision over the finished gains and belongs here.
	//
	// It is NOT bit-identical: the device sums the window through a tree where the host sums it
	// serially in float64, so a marginal accept can flip. Every accept is still gated on lowering
	// the shape's exact local error, so the pass can only pick a different subset of improvements.
	// The stack, flattened once: the shader reads it as plain arrays and it does not change while
	// the rounds run (only the shapes the filter commits do, and those are re-flattened below).
	flatP := make([]float32, n*6)
	flatCol := make([]float32, n*4)
	flatKind := make([]int32, n)
	flatBox := make([]int32, n*4)
	reflatten := func() {
		for i := range gs {
			copy(flatP[i*6:], gs[i].p[:])
			copy(flatCol[i*4:], gs[i].col[:])
			flatKind[i] = int32(gs[i].kind)
			for k := 0; k < 4; k++ {
				flatBox[i*4+k] = int32(gs[i].bbox[k])
			}
		}
	}
	reflatten()
	// The tile index as CSR. `tiles` is rebuilt per round as a slice of slices; the shader wants
	// one offset array and one flat index array.
	tileOff := make([]int32, len(tiles)+1)
	var tileIdx []int32
	flattenTiles := func() {
		tileIdx = tileIdx[:0]
		for t, lst := range tiles {
			tileOff[t] = int32(len(tileIdx))
			tileIdx = append(tileIdx, lst...)
		}
		tileOff[len(tiles)] = int32(len(tileIdx))
	}
	flattenTiles()

	devRefine := deviceRefiner(dev)
	gpuRound := func(pending []int) []int {
		if devRefine == nil {
			return pending
		}
		reflatten()
		flattenTiles()
		jobShape := make([]int32, 0, len(pending))
		jobWin := make([]int32, 0, len(pending)*6)
		jobAxes := make([]float32, 0, len(pending)*24)
		jobNAx := make([]int32, 0, len(pending))
		slot := make([]int, 0, len(pending)) // job -> shape index, for writing the results back
		host := pending[:0:0]
		maxNeed := 0
		for _, i := range pending {
			g := &gs[i]
			axes := refineAxes(g.kind, g.p, geom)
			b := windows[i]
			if !refineOnDevice(g.kind) || len(axes) == 0 || len(axes) > 6 || g.col[3] <= 0 ||
				b[2] < b[0] || b[3] < b[1] {
				host = append(host, i)
				continue
			}
			area := (b[2] - b[0] + 1) * (b[3] - b[1] + 1)
			step := 1
			if area > refineStride {
				if sN := int(math.Sqrt(float64(area) / float64(refineStride))); sN > 1 {
					step = sN
				}
			}
			nx := (b[2]-b[0])/step + 1
			ny := (b[3]-b[1])/step + 1
			need := nx * ny
			// Tiny windows stay on the host. A workgroup that cannot fill its own lanes spends more
			// on the reduction tree than on the arithmetic, and the host loop over a couple of hundred
			// samples is genuinely quick — measured, offloading everything was 2.3x SLOWER than not.
			if need < refineDevMinSamples {
				host = append(host, i)
				continue
			}
			if need > maxNeed {
				maxNeed = need
			}
			slot = append(slot, i)
			jobShape = append(jobShape, int32(i))
			jobWin = append(jobWin, int32(b[0]), int32(b[1]), int32(b[2]), int32(b[3]), int32(step), int32(need))
			jobNAx = append(jobNAx, int32(len(axes)))
			for k := 0; k < 6; k++ {
				if k < len(axes) {
					a := axes[k]
					jobAxes = append(jobAxes, float32(a.slot), float32(a.step), float32(a.lo), float32(a.hi))
				} else {
					jobAxes = append(jobAxes, 0, 0, 0, 0) // step 0 = the shader skips the slot
				}
			}
		}
		if len(slot) == 0 {
			return host
		}
		// The context slab is njobs * ctxCap * 14 floats and it is the whole memory cost of the
		// pass, so the round is handed over in chunks small enough that a 4GB card is never asked
		// for something it does not have.
		perJob := maxNeed * 14 * 4
		chunk := len(slot)
		if perJob > 0 {
			if c := refineDevBudget / perJob; c < chunk {
				chunk = maxInt(1, c)
			}
		}
		outP := make([]float32, chunk*6)
		outGain := make([]float32, chunk)
		for off := 0; off < len(slot); off += chunk {
			cnt := minInt(chunk, len(slot)-off)
			jb := &vulkanRefineJobs{
				n: len(gs), shapeP: flatP, shapeCol: flatCol, shapeKind: flatKind, shapeBox: flatBox,
				tileOff: tileOff, tileIdx: tileIdx, tw: tw, th: th, tile: refineTile,
				jobShape: jobShape[off : off+cnt], jobWin: jobWin[off*6 : (off+cnt)*6],
				jobAxes: jobAxes[off*24 : (off+cnt)*24], jobNAx: jobNAx[off : off+cnt],
				ctxCap: maxNeed, sweeps: refineSweepsN, unweighted: refineUnweighted,
				minGain: refineMinGain, intrudeMax: refineIntrudeMax, cleanDev: refineCleanDev,
				shrink: refineShrink, minStepFrac: refineMinStep,
			}
			if devRefine(jb, outP[:cnt*6], outGain[:cnt]) < 0 {
				srdbg("device refine refused — falling back to the host for the rest of the run")
				devRefine = nil
				return pending
			}
			for j := 0; j < cnt; j++ {
				if outGain[j] <= 0 {
					continue
				}
				i := slot[off+j]
				var np [6]float32
				copy(np[:], outP[j*6:j*6+6])
				sc := 1.0
				if refineScaleGain {
					st := float64(jobWin[(off+j)*6+4])
					sc = st * st
				}
				moved[i], touched[i], gain[i] = np, true, float64(outGain[j])*sc
			}
		}
		return host
	}

	for round := 0; round < refineRoundsOverride && len(pending) > 0; round++ {
		for _, i := range pending {
			touched[i], gain[i] = false, 0
		}
		hostPending := gpuRound(pending)
		var wg sync.WaitGroup
		jobs := make(chan int, runtime.NumCPU()*2)
		for wk := 0; wk < runtime.NumCPU(); wk++ {
			wg.Add(1)
			go func() {
				var ctx winCtx // reused across this worker's shapes
				defer wg.Done()
				for i := range jobs {
					g := &gs[i]
					axes := refineAxes(g.kind, g.p, geom)
					if len(axes) == 0 || g.col[3] <= 0 {
						continue
					}
					b := windows[i]
					if b[2] < b[0] || b[3] < b[1] {
						continue
					}
					area := (b[2] - b[0] + 1) * (b[3] - b[1] + 1)
					step := 1
					if area > refineStride {
						if sN := int(math.Sqrt(float64(area) / float64(refineStride))); sN > 1 {
							step = sN
						}
					}
					build(&ctx, i, b, step)
					cur := g.p
					score := func(p [6]float32) float64 {
						pr := raster.Prep(g.kind, p)
						bx0, by0, bx1, by1 := raster.BBox(g.kind, p, w, h)
						e := trialErr(&ctx, &pr, g.col[3], g.col, false, [4]int{bx0, by0, bx1, by1})
						if refineDebug {
							atomic.AddInt64(&refineTried, 1)
						}
						if ctx.intruded > refineIntrudeMax {
							if refineDebug {
								atomic.AddInt64(&refineVetoed, 1)
							}
							return math.Inf(1) // a new blemish is not a trade the sum may make
						}
						return e
					}
					basePrep := raster.Prep(g.kind, cur)
					base := trialErr(&ctx, &basePrep, g.col[3], g.col, true, b) // full window: this pass fills dev/cov
					if base <= 0 {
						continue
					}
					best := base
					for sweep := 0; sweep < refineSweepsN; sweep++ {
						improvedAny := false
						for _, ax := range axes {
							v, e2, ok := searchAxis(score, cur, ax, best)
							if ok {
								cur[ax.slot] = float32(v)
								best = e2
								improvedAny = true
							}
						}
						if !improvedAny {
							break
						}
					}
					if base-best >= refineMinGain*base {
						// Scale to TRUE pixel units (one strided sample stands for step² pixels;
						// looFitContrib does the same). Without it the cross-shape conflict sort
						// deflated large shapes' gains up to step²× against small ones, so they
						// systematically lost the disjointness race — and roundGain (the round
						// floor's signal) summed mismatched units. The accept test above is a
						// RATIO of same-window sums, so it is unaffected either way.
						// FH6_SKEW_SCALE=0 pins the old unscaled ordering for A/Bs.
						sc := 1.0
						if refineScaleGain {
							sc = float64(step * step)
						}
						moved[i], touched[i], gain[i] = cur, true, (base-best)*sc
					}
				}
			}()
		}
		for _, i := range hostPending {
			jobs <- i
		}
		close(jobs)
		wg.Wait()

		// Conflict filter. Every shape was judged against the stack as it stood, which is exact for a
		// move nothing else overlaps and merely optimistic for one sharing pixels with another accepted
		// move: two shapes can each improve the composite alone and worsen it together. Measured on
		// img_10, committing all 606 improvements raised the global error and the whole pass rolled
		// back. Best gain first, dropping any later move whose window touches an accepted one, keeps
		// the guarantee — disjoint windows compose exactly.
		order := make([]int, 0, len(pending))
		for _, i := range pending {
			if touched[i] {
				order = append(order, i)
			}
		}
		sort.Slice(order, func(a, b int) bool { return gain[order[a]] > gain[order[b]] })
		// Overlap is tested against the accepted windows THEMSELVES, not against claimed tiles. Tiles
		// are 64px, so a ten-pixel shape used to reserve a whole tile and block everything near it;
		// the round then committed a handful of moves and the rest waited for a later round, which is
		// why the pass kept finding more at 24 rounds and had still not converged. Exact rectangles
		// commit far more per round for the same guarantee.
		var taken [][4]int
		var blocked []int
		accepted := 0
		roundGain := 0.0
		for _, i := range order {
			b := windows[i]
			free := true
			for _, t := range taken {
				if b[0] <= t[2] && t[0] <= b[2] && b[1] <= t[3] && t[1] <= b[3] {
					free = false
					break
				}
			}
			if !free {
				blocked = append(blocked, i)
				continue
			}
			taken = append(taken, b)
			roundGain += gain[i]
			// Commit into the live stack so the next round scores against reality.
			gs[i].p = moved[i]
			gs[i].prep = raster.Prep(gs[i].kind, moved[i])
			x0, y0, x1, y1 := raster.BBox(gs[i].kind, moved[i], w, h)
			gs[i].bbox = [4]int{x0, y0, x1, y1}
			committed[i] = true
			accepted++
		}
		changed += accepted
		if accepted == 0 {
			break
		}
		if round == 0 {
			firstGain = roundGain
		} else if roundGain < refineFloor*firstGain {
			srdbg("round %d earned %.1f, under %.0f%% of the first round's %.1f — stopping",
				round, roundGain, refineFloor*100, firstGain)
			pending = blocked
			break
		}
		// Windows and the tile index follow the committed geometry.
		for _, i := range order {
			windows[i] = searchWindow(gs[i].kind, gs[i].p, refineAxes(gs[i].kind, gs[i].p, geom), w, h)
		}
		rebuildTiles()
		pending = blocked
	}

	out := make([]model.Shape, n)
	copy(out, shapes)
	for i := 1; i < n; i++ {
		if !committed[i] {
			continue
		}
		c := model.Candidate{Kind: gs[i].kind, P: gs[i].p, Color: model.RGBA{
			R: gs[i].col[0], G: gs[i].col[1], B: gs[i].col[2], A: gs[i].col[3]}}
		s := c.ToShape(shapes[i].Score)
		s.Color = shapes[i].Color // keep the exported colour bytes byte-for-byte
		out[i] = s
	}
	srdbg("geom=%v eligible=%d changed=%d trials=%d vetoed-for-intrusion=%d", geom, eligible, changed, atomic.LoadInt64(&refineTried), atomic.LoadInt64(&refineVetoed))
	return out, changed
}

// searchAxis pattern-searches one parameter: probe both ways, follow a winning direction while it
// keeps paying, and otherwise shrink the step until it stops being worth a probe.
func searchAxis(score func([6]float32) float64, p [6]float32, ax refineAxis, best float64) (float64, float64, bool) {
	start := float64(p[ax.slot])
	cur, curE := start, best
	stepUnit := ax.step
	if stepUnit <= 0 {
		return 0, best, false
	}
	minStep := refineMinStep * ax.step
	trial := p
	try := func(v float64) (float64, bool) {
		if v < ax.lo || v > ax.hi {
			return 0, false
		}
		trial[ax.slot] = float32(v)
		return score(trial), true
	}
	for step := stepUnit; step >= minStep; step *= refineShrink {
		dir := 0.0
		for _, d := range [2]float64{1, -1} {
			if e, ok := try(cur + d*step); ok && e < curE {
				curE, cur, dir = e, cur+d*step, d
				break
			}
		}
		// Keep walking a direction that paid, doubling as long as it does. A shape that wants to be
		// somewhere else entirely gets there without a probe per pixel of the way.
		for dir != 0 {
			s2 := step * 2
			e, ok := try(cur + dir*s2)
			if !ok || e >= curE {
				break
			}
			curE, cur = e, cur+dir*s2
			step = s2
		}
	}
	if cur == start {
		return start, best, false
	}
	return cur, curE, true
}

// searchWindow returns a box covering the shape at every position the search can reach, so all trials
// are scored over one identical pixel set and the comparison between them is honest.
// It unions the bounding box over the corner cases of the axis bounds, so no reachable trial can put
// coverage outside it — an under-sized window would charge a trial nothing for the paint it moved out
// of view, and the search would chase exactly that.
func searchWindow(kind model.ShapeKind, p [6]float32, axes []refineAxis, w, h int) [4]int {
	x0, y0, x1, y1 := raster.BBox(kind, p, w, h)
	b := [4]int{x0, y0, x1, y1}
	if len(axes) == 0 {
		return b
	}
	// Positional slots widen the box by their travel; the rest are swept through their extremes, one
	// at a time, on top of the already-widened extents. Sweeping every COMBINATION would be exact but
	// exponential, and taking the extents at maximum first makes the remaining sweeps conservative.
	var move float64
	q := p
	for _, ax := range axes {
		switch {
		case ax.slot == 4 && kind != model.KindTriangle && kind != model.KindLine:
			// angle: handled by the sweep below
		case ax.slot == 5 && skewEligible(kind):
			// shear: handled by the sweep below
		case (ax.slot == 2 || ax.slot == 3) && kind != model.KindTriangle:
			q[ax.slot] = float32(math.Max(float64(q[ax.slot]), ax.hi))
		default:
			move = math.Max(move, math.Max(math.Abs(ax.hi-float64(p[ax.slot])), math.Abs(float64(p[ax.slot])-ax.lo)))
		}
	}
	sweep := func(r [6]float32) {
		ax0, ay0, ax1, ay1 := raster.BBox(kind, r, w, h)
		b[0] = mini(b[0], ax0)
		b[1] = mini(b[1], ay0)
		b[2] = maxi(b[2], ax1)
		b[3] = maxi(b[3], ay1)
	}
	angles := []float32{q[4]}
	skews := []float32{q[5]}
	for _, ax := range axes {
		if ax.slot == 4 && kind != model.KindTriangle && kind != model.KindLine {
			angles = append(angles, float32(ax.lo), float32(ax.hi))
		}
		if ax.slot == 5 && skewEligible(kind) {
			skews = append(skews, float32(ax.lo), float32(ax.hi))
		}
	}
	for _, a := range angles {
		for _, s := range skews {
			r := q
			r[4], r[5] = a, s
			sweep(r)
		}
	}
	ip := int(math.Ceil(move)) + 2
	b[0] = maxi(0, b[0]-ip)
	b[1] = maxi(0, b[1]-ip)
	b[2] = mini(w-1, b[2]+ip)
	b[3] = mini(h-1, b[3]+ip)
	return b
}
