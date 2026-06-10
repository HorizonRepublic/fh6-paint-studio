package engine

import (
	"math"
	"os"
	"time"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// polishDebug (FH6_POLISH_DEBUG=1) traces the best-hard trajectory during the refinement loop.
var polishDebug = os.Getenv("FH6_POLISH_DEBUG") != ""

// Joint differentiable "polish" pass — breaks the greedy plateau that pure greedy
// placement cannot. After greedy placement, ALL shapes are refined together by
// gradient descent on a SOFT-rasterized render vs the target, so shapes co-adapt
// (faceted fur merges into smooth coverage; the smeared face sharpens). The soft
// coverage cov = sigmoid(-SDF/tau) makes the hard inside-test differentiable;
// tau anneals soft->sharp (coarse-to-fine) and we SNAP back to the exact hard
// ellipse/rect/triangle the editor needs. Optimised in sRGB (the editor's display
// space). Ellipse geometry+color+alpha are refined; rect/triangle composite with
// their hard coverage (fixed geometry) but still get color+alpha refined.

const polishDeg2Rad = math.Pi / 180

// polishEarlyMinProgress gates the plateau early-stop to the LATE phase of the tau anneal.
// The shipped HARD loss is non-monotonic: under the high-tau soft start it sits ABOVE the
// greedy baseline (soft exploration), and the real gains only land once tau sharpens. So a
// "no improvement" run early on is NOT convergence — counting it would stop polish right
// before the payoff. Only past this progress fraction does the hard loss genuinely converge,
// making a sub-margin plateau a real signal to stop.
const polishEarlyMinProgress = 0.6

// Fine-exploit phase knobs (shared by Polish and PolishWithBackend — see the phase comment in
// PolishWithBackend): a short low-LR descent from the best point seen, at a fixed near-final tau.
const (
	polishFineLRScale = 0.1 // fraction of the main-loop learning rates
	polishFineTauMin  = 0.2 // gradient-surrogate softness floor (STE keeps the forward hard regardless)
	polishFineCheck   = 5   // hard-render best-tracking cadence (iters)
)

// polishFineIters sizes the fine-exploit phase from the main-loop budget.
func polishFineIters(iters int) int { return maxInt(30, iters/2) }

// PolishOptions configures the joint refinement. Zero value -> DefaultPolishOptions.
type PolishOptions struct {
	Iters    int
	Tau0     float64 // initial edge softness (px)
	Tau1     float64 // final edge softness (px)
	LRPos    float64 // Adam lr for cx,cy (px/step)
	LRRad    float64 // Adam lr for rx,ry
	LRAng    float64 // Adam lr for theta (degrees/step)
	LRColor  float64 // Adam lr for R,G,B
	LRAlpha  float64 // Adam lr for A
	GradClip float64 // per-shape per-param gradient L2 cap (0 = none)
	OKLab    bool    // perceptual colour loss: compute the polish loss/gradient in OKLab (cube-root LMS of the linear working space) instead of raw channel SSE — hue/chroma errors cost what the eye charges (the "standout colour" failure). Loss, backward seed AND best-hard tracking all switch together so the optimisation is self-consistent; the caller's accept gate still measures backend SSE. Greedy scoring is untouched. Default false (CLI -polish-oklab).
	STE      bool    // straight-through estimator: FORWARD composites HARD coverage (the exact deliverable), BACKWARD keeps the SOFT surrogate dcov/dsdf for the geometry chain only. Closes the soft->hard snap gap (optimizes the shipped hard SSE directly). Default false (soft polish).

	// Early-stop: break the loop once the polish hits DIMINISHING RETURNS — when the best-HARD
	// improvement in one check falls below EarlyStopMargin × (total improvement so far) for
	// EarlyStopPatience consecutive late-phase checks. A diminishing-returns rule (not an absolute
	// margin) is the right signal: the hard loss keeps dropping a little to the very end (an
	// absolute threshold never fires), but the marginal drop shrinks RELATIVE to what's already
	// been gained. The best-hard point is UNCHANGED, so this only drops the wasteful tail (polish
	// is 60-85% of a run). 0 margin = disabled (run the full Iters).
	EarlyStopMargin   float64
	EarlyStopPatience int

	// OnPreview, if set, is called (time-throttled) during the refinement loop with the CURRENT
	// soft render (w*h*4, straight alpha) so a UI can animate the polish phase — it visibly sharpens
	// as tau anneals. This is a PURE READ of the render buffer the forward pass already produced; it
	// does NOT touch any optimization state, so the polish result is bit-identical (only a throttled
	// device->host copy is added to the polish wall-time). The slice is reused between calls — the
	// callee must copy/convert it immediately, not retain it.
	OnPreview func(render []float32, w, h int)

	// OnProgress, if set, is called once per refinement iteration with (iter 1..Iters, total Iters).
	// It carries NO device read, so it is cheap to fire every iteration — used by the Gaussian mode to
	// drive a training % bar (the greedy's shape-count progress is meaningless when all glows train at
	// once). nil for the normal greedy+polish (whose bar tracks placed shapes instead).
	OnProgress func(iter, total int)

	// PreviewInterval throttles OnPreview's device->host render read. 0 -> 50ms (the short greedy-polish
	// phase wants a smooth animation). The Gaussian mode trains for MINUTES, so each 50ms frame is a
	// full-canvas D2H copy stealing training time — it sets this longer (a slower preview, faster run).
	PreviewInterval time.Duration
}

// previewInterval returns the throttle for OnPreview reads (default 50ms).
func (o PolishOptions) previewInterval() time.Duration {
	if o.PreviewInterval > 0 {
		return o.PreviewInterval
	}
	return 50 * time.Millisecond
}

// DefaultPolishOptions returns the tuned defaults from the design.
func DefaultPolishOptions() PolishOptions {
	return PolishOptions{
		Iters: 200, Tau0: 2.0, Tau1: 0.15,
		LRPos: 0.5, LRRad: 0.5, LRAng: 0.5, LRColor: 0.01, LRAlpha: 0.01,
		GradClip: 8,
		// Early-stop defaults: stop when a check adds <2% of the TOTAL hard-loss gain so far, for 3
		// consecutive late-phase checks (checkEvery = Iters/25). Diminishing-returns, not an absolute
		// margin (which never fires — the hard loss keeps inching down to the end). best-hard is still
		// shipped, so quality is held while wall-time falls (the user's "less time" target).
		EarlyStopMargin: 0.02, EarlyStopPatience: 3,
	}
}

// clampPolishTau floors Tau0/Tau1 to positive defaults. The tau anneal computes
// tau = Tau0*(Tau1/Tau0)^t and the soft-coverage gradient divides by tau, so a zero
// tau yields NaN/Inf that corrupts the whole shape set. DefaultPolishOptions is only
// substituted when Iters<=0, so a caller passing Iters>0 with a zero tau would slip
// through; this guards that path independently.
func clampPolishTau(opt *PolishOptions) {
	if opt.Tau0 <= 0 {
		opt.Tau0 = 2.0
	}
	if opt.Tau1 <= 0 {
		opt.Tau1 = 0.15
	}
}

// pshape is a shape's mutable optimization state (float64 params + Adam moments).
type pshape struct {
	kind   model.ShapeKind
	P      [6]float64 // geometry (ellipse/rect: cx,cy,rx,ry,thetaDeg)
	col    [4]float64 // R,G,B,A in 0..1
	optGeo bool       // refine geometry (ellipse only in v1)
	// Adam moments for params: P[0..5] (6 geo — ellipse/rect use 5 + an unused slot;
	// triangle uses all 6 vertex coords) then col[0..3] (10 slots).
	m, v [10]float64
	grad [10]float64 // gradient staged by polishBackward, consumed by adamStep
}

// PolishResult reports what the pass did so the caller can gate on it.
type PolishResult struct {
	Shapes   []model.Shape
	PreLoss  float64 // soft-render weighted SSE before refinement
	PostLoss float64 // soft-render weighted SSE after refinement (tau1)
	Iters    int
	Phases   [7]time.Duration // GPU-polish per-phase wall time: upload,forward,loss,backward,readgrad,adam,hardloss (populated by PolishWithBackend)
}

// PolishAccel is the optional capability of a backend to run the polish forward/loss/
// backward per-pixel passes on the GPU (the heavy work), while the engine keeps the
// orchestration (expanded bbox, Adam, tau anneal, best-hard, snap) — so PolishWithBackend
// reuses every helper here and only the per-pixel math moves to the device. The CUDA
// backend implements it; the wire layout mirrors the shim's fp_polish_* API exactly.
type PolishAccel interface {
	PolishSupported() bool // false if the loaded DLL predates the polish API -> use CPU Polish
	PolishSetup(base []float32, n int)
	PolishSetSTE(on bool) // toggle straight-through (hard forward) on the device; no-op on DLLs predating the export (silently runs soft)
	PolishUpload(P, col []float64, kinds, bbx []int32, boff []int64, belowTotal int64)
	PolishForward(tau float64, bbxHost []int32)
	PolishLoss() float64
	PolishBackward(tau float64, bbxHost []int32)
	PolishReadGrad(dst []float64)
	// PolishHardLoss renders all shapes HARD (the shipped deliverable) on the device and
	// returns the weighted SSE for best-hard tracking. ok=false on DLLs predating the export
	// -> the engine falls back to the CPU polishHardLoss. The CURRENT params must be uploaded first.
	PolishHardLoss(bbxHost []int32) (float64, bool)
	PolishSync() // block until queued polish kernels finish (for correct async phase timing)
	PolishFree()
}

// PolishProbeResult is the CPU reference for ONE polish step (forward+loss+backward)
// at a fixed tau, plus the exact upload layout — so a GPU implementation can be driven
// identically and cross-checked against Render/Loss/Grad. Test-only.
type PolishProbeResult struct {
	N          int
	Base       []float32
	Render     []float32 // CPU soft render after forward
	Loss       float64   // CPU weighted SSE of render
	HardLoss   float64   // CPU weighted SSE of the HARD-coverage render (best-hard reference)
	Grad       []float64 // n*10: gP0..5, gR,gG,gB,gA
	P, Col     []float64 // n*6, n*4
	Kinds, BBX []int32   // n, n*4
	Boff       []int64   // n (below float-offset prefix sum)
	BelowTotal int64
}

// PolishStepProbe runs ONE CPU forward+loss+backward at the given tau and returns the
// result + layout. It is the reference the GPU polish primitives must match bit-for-bit.
func PolishStepProbe(shapes []model.Shape, target, weight []float32, w, h int, bg model.RGBA, transparent bool, tau float64, ste, oklab bool) PolishProbeResult {
	base := make([]float32, w*h*4)
	if !transparent {
		for i := 0; i < w*h; i++ {
			base[i*4+0], base[i*4+1], base[i*4+2], base[i*4+3] = bg.R, bg.G, bg.B, 1
		}
	}
	ps := make([]pshape, 0, len(shapes)-1)
	for _, s := range shapes[1:] {
		k := model.KindFromType(s.Type)
		pp := model.ParamsFromShape(s)
		var P [6]float64
		for i := 0; i < 6; i++ {
			P[i] = float64(pp[i])
		}
		var c [4]float64
		if len(s.Color) >= 4 {
			c = [4]float64{float64(model.DecChan(s.Color[0])), float64(model.DecChan(s.Color[1])), float64(model.DecChan(s.Color[2])), float64(s.Color[3]) / 255}
		} else {
			c[3] = 1
		}
		ps = append(ps, pshape{kind: k, P: P, col: c, optGeo: optimizableGeo(k)})
	}
	n := len(ps)
	render := make([]float32, w*h*4)
	dC := make([]float64, w*h*4)
	below := make([][]float32, n)
	bbx := make([][4]int, n)
	polishForward(ps, base, render, below, bbx, w, h, tau, ste)
	loss := polishLoss(render, target, weight, w, h, oklab)
	polishBackward(ps, base, render, target, weight, below, bbx, dC, w, h, tau, ste, oklab)
	hardScratch := make([]float32, w*h*4)
	hardLoss := polishHardLoss(ps, base, target, weight, hardScratch, w, h, oklab)

	res := PolishProbeResult{N: n, Base: base, Render: render, Loss: loss, HardLoss: hardLoss,
		Grad: make([]float64, n*10), P: make([]float64, n*6), Col: make([]float64, n*4),
		Kinds: make([]int32, n), BBX: make([]int32, n*4), Boff: make([]int64, n)}
	var off int64
	for i := range ps {
		copy(res.Grad[i*10:i*10+10], ps[i].grad[:])
		for k := 0; k < 6; k++ {
			res.P[i*6+k] = ps[i].P[k]
		}
		for k := 0; k < 4; k++ {
			res.Col[i*4+k] = ps[i].col[k]
		}
		res.Kinds[i] = int32(ps[i].kind)
		res.BBX[i*4+0], res.BBX[i*4+1], res.BBX[i*4+2], res.BBX[i*4+3] = int32(bbx[i][0]), int32(bbx[i][1]), int32(bbx[i][2]), int32(bbx[i][3])
		res.Boff[i] = off
		bw := int64(bbx[i][2] - bbx[i][0] + 1)
		bh := int64(bbx[i][3] - bbx[i][1] + 1)
		if bw > 0 && bh > 0 {
			off += bw * bh * 4
		}
	}
	res.BelowTotal = off
	return res
}

// PolishWithBackend is the GPU-accelerated twin of Polish: identical algorithm and
// orchestration (so results match the CPU reference within float tolerance), but each
// iteration's forward composite + loss + backward gradient is computed on the device.
// The host still computes expanded bboxes (geometry moves each iter), runs Adam, tracks
// the best HARD point, and snaps to game shapes. Falls back is the caller's job (use
// Polish when the backend lacks PolishAccel).
func PolishWithBackend(shapes []model.Shape, target, weight []float32, w, h int, bg model.RGBA, transparent bool, opt PolishOptions, accel PolishAccel) PolishResult {
	if opt.Iters <= 0 {
		opt = DefaultPolishOptions()
	}
	clampPolishTau(&opt)
	if len(shapes) <= 1 {
		return PolishResult{Shapes: shapes}
	}
	base := make([]float32, w*h*4)
	if !transparent {
		for i := 0; i < w*h; i++ {
			base[i*4+0], base[i*4+1], base[i*4+2], base[i*4+3] = bg.R, bg.G, bg.B, 1
		}
	}
	ps := make([]pshape, 0, len(shapes)-1)
	for _, s := range shapes[1:] {
		k := model.KindFromType(s.Type)
		pp := model.ParamsFromShape(s)
		var P [6]float64
		for i := 0; i < 6; i++ {
			P[i] = float64(pp[i])
		}
		var c [4]float64
		if len(s.Color) >= 4 {
			c = [4]float64{float64(model.DecChan(s.Color[0])), float64(model.DecChan(s.Color[1])), float64(model.DecChan(s.Color[2])), float64(s.Color[3]) / 255}
		} else {
			c[3] = 1
		}
		ps = append(ps, pshape{kind: k, P: P, col: c, optGeo: optimizableGeo(k)})
	}
	n := len(ps)
	accel.PolishSetup(base, n)
	accel.PolishSetSTE(opt.STE)
	// OKLab steers ONLY the fine-exploit phase's gradient (toggled around its backward calls);
	// the loss, hard loss and best-tracking stay on plain SSE — the gate's metric — so the
	// perceptual nudge can never ship an SSE regression. Optional device capability
	// (type-asserted, like the backend search extras); no support (old DLL) -> SSE-only pass.
	var okSetter interface{ PolishSetOKLab(on bool) bool }
	if opt.OKLab {
		if s, ok := accel.(interface{ PolishSetOKLab(on bool) bool }); ok && s.PolishSetOKLab(false) {
			okSetter = s
		} else {
			applog.Printf("polish: OKLab requested but the device lacks fp_set_polish_oklab — falling back to SSE")
			opt.OKLab = false
		}
	}
	defer accel.PolishFree()

	// Reused host staging buffers for the per-iter upload.
	hP := make([]float64, n*6)
	hCol := make([]float64, n*4)
	hKind := make([]int32, n)
	hBBX := make([]int32, n*4)
	hOff := make([]int64, n)
	hGrad := make([]float64, n*10)

	// upload packs current params + the expanded-bbox/below layout and ships them.
	upload := func(tau float64) {
		var off int64
		for i := range ps {
			bb := expandedBBox(ps[i], w, h, tau)
			hBBX[i*4+0], hBBX[i*4+1], hBBX[i*4+2], hBBX[i*4+3] = int32(bb[0]), int32(bb[1]), int32(bb[2]), int32(bb[3])
			hKind[i] = int32(ps[i].kind)
			for k := 0; k < 6; k++ {
				hP[i*6+k] = ps[i].P[k]
			}
			for k := 0; k < 4; k++ {
				hCol[i*4+k] = ps[i].col[k]
			}
			hOff[i] = off
			bw := int64(bb[2] - bb[0] + 1)
			bh := int64(bb[3] - bb[1] + 1)
			if bw > 0 && bh > 0 {
				off += bw * bh * 4
			}
		}
		accel.PolishUpload(hP, hCol, hKind, hBBX, hOff, off)
	}

	upload(opt.Tau0)
	accel.PolishForward(opt.Tau0, hBBX)
	pre := accel.PolishLoss()

	// Best-hard render runs on the GPU (fp_polish_hard_loss) — the only per-pixel polish
	// work that used to stay on the CPU. hardScratch is the CPU fallback for old DLLs.
	// Baseline: d_pbbx already holds the opt.Tau0 params (uploaded above), so no re-upload.
	hardScratch := make([]float32, w*h*4)
	hardLoss := func(tau float64, reupload bool) float64 {
		if reupload {
			upload(tau) // ship the CURRENT (post-Adam) params so the device hard render matches
		}
		if hl, ok := accel.PolishHardLoss(hBBX); ok {
			return hl
		}
		return polishHardLoss(ps, base, target, weight, hardScratch, w, h, false)
	}
	bestHard := hardLoss(opt.Tau0, false)
	bestP := snapshotParams(ps)
	checkEvery := maxInt(1, opt.Iters/25)
	earlyMargin := opt.EarlyStopMargin
	earlyPatience := opt.EarlyStopPatience
	if earlyPatience <= 0 {
		earlyPatience = 5
	}
	stall := 0
	initHard := bestHard   // hard loss at the greedy input — the baseline for "total gain"
	lastBest := bestHard   // best hard loss at the previous late-phase check
	doneIters := opt.Iters // actual iterations run (plateau early-stop may cut it short)

	var post float64
	var tUpload, tFwd, tLoss, tBwd, tGrad, tAdam, tHard time.Duration
	// Per-phase timing is always on (the time.Now overhead is negligible vs the kernels) so
	// every run reports where the polish wall-time goes (standing "profile every snapshot").
	tick := func(d *time.Duration, f func()) {
		t0 := time.Now()
		f()
		*d += time.Since(t0)
	}
	// Optional live preview of the refinement: read the (already-computed) device soft render,
	// throttled, and hand it to opt.OnPreview. Pure read — does not change the result. Skipped if
	// the backend lacks the read export (older DLL).
	var prevRd interface{ PolishReadRender([]float32) }
	var prevBuf []float32
	var lastPrev time.Time
	if opt.OnPreview != nil {
		if r, ok := accel.(interface{ PolishReadRender([]float32) }); ok {
			prevRd = r
			prevBuf = make([]float32, w*h*4)
		}
	}
	for it := 0; it < opt.Iters; it++ {
		t := float64(it) / float64(maxInt(1, opt.Iters-1))
		tau := opt.Tau0 * math.Pow(opt.Tau1/opt.Tau0, t)
		last := it == opt.Iters-1
		tick(&tUpload, func() { upload(tau) })
		// Kernel launches are async; sync inside the tick so the GPU time is attributed to
		// forward/backward (not hidden in the next sync). Net overhead ~0 — the work must
		// complete before readgrad anyway; the sync just moves the wait into the timer.
		tick(&tFwd, func() { accel.PolishForward(tau, hBBX); accel.PolishSync() })
		if prevRd != nil && time.Since(lastPrev) >= opt.previewInterval() {
			lastPrev = time.Now()
			prevRd.PolishReadRender(prevBuf)
			opt.OnPreview(prevBuf, w, h)
		}
		if opt.OnProgress != nil {
			opt.OnProgress(it+1, opt.Iters)
		}
		// PolishLoss forces a device sync but `post` is only used for the final report —
		// only fetch it on the last iteration (huge per-iter sync saving).
		if last {
			tick(&tLoss, func() { post = accel.PolishLoss() })
		}
		tick(&tBwd, func() { accel.PolishBackward(tau, hBBX); accel.PolishSync() })
		tick(&tGrad, func() {
			accel.PolishReadGrad(hGrad)
			for i := range ps {
				copy(ps[i].grad[:], hGrad[i*10:i*10+10])
			}
		})
		tick(&tAdam, func() { adamStep(ps, opt, it+1, w, h, 1) })
		if (it+1)%checkEvery == 0 || last {
			tick(&tHard, func() {
				// Re-upload: Adam just mutated ps on the host; the device still holds this
				// iter's pre-Adam params, so refresh them before the GPU hard render.
				hl := hardLoss(tau, true)
				if polishDebug {
					applog.Printf("polish-debug it=%d tau=%.3f hard=%.1f best=%.1f", it+1, tau, hl, bestHard)
				}
				if hl < bestHard {
					bestHard = hl
					bestP = snapshotParams(ps)
				}
				// Diminishing-returns tracking, gated to the LATE phase (see polishEarlyMinProgress):
				// only there does the hard loss converge. A check that adds < earlyMargin of the TOTAL
				// gain so far is a plateau; a relative rule fires (an absolute one never does — the loss
				// keeps inching down to the end).
				if t >= polishEarlyMinProgress {
					total := initHard - bestHard
					gain := lastBest - bestHard
					if total > 1e-9 && gain < earlyMargin*total {
						stall++
					} else {
						stall = 0
					}
					lastBest = bestHard
				}
			})
			if earlyMargin > 0 && !last && t >= polishEarlyMinProgress && stall >= earlyPatience {
				post = accel.PolishLoss() // fetch the final soft loss for the report (only on break)
				doneIters = it + 1
				break // diminishing returns in the late phase — drop the wasteful tail
			}
		}
	}
	// FINE-EXPLOIT phase — restart from the best point seen and descend gently: fresh Adam
	// moments, a small LR, and a fixed near-final tau. The main loop's tau anneal is a coarse
	// soft excursion: worth it while the input leaves headroom, but on a tightly-fitted canvas
	// (full-budget greedy) it wanders too far from the optimum to return within the budget —
	// polish never re-beats its own input and the caller's gate discards the whole pass as a
	// silent no-op. Exploiting from the best-known point (on a saturated input that IS the
	// input itself) harvests the small colour/alpha and sub-pixel geometry wins the hard render
	// still allows; on winning runs it squeezes a little further. Best-hard tracking continues
	// throughout, so the phase can never lose ground.
	restoreParams(ps, bestP)
	for i := range ps {
		ps[i].m, ps[i].v = [10]float64{}, [10]float64{}
	}
	fineIters := polishFineIters(opt.Iters)
	fineTau := math.Max(opt.Tau1, polishFineTauMin)
	fineOpt := opt
	fineOpt.Iters = fineIters // keys adamStep's warmup ramp to the fine budget
	fineGained, fineZero := false, 0
	for it := 0; it < fineIters; it++ {
		last := it == fineIters-1
		tick(&tUpload, func() { upload(fineTau) })
		tick(&tFwd, func() { accel.PolishForward(fineTau, hBBX); accel.PolishSync() })
		if prevRd != nil && time.Since(lastPrev) >= opt.previewInterval() {
			lastPrev = time.Now()
			prevRd.PolishReadRender(prevBuf)
			opt.OnPreview(prevBuf, w, h)
		}
		if opt.OnProgress != nil {
			opt.OnProgress(doneIters+it+1, doneIters+fineIters)
		}
		tick(&tBwd, func() {
			if okSetter != nil {
				okSetter.PolishSetOKLab(true) // perceptual gradient for the fine step only
			}
			accel.PolishBackward(fineTau, hBBX)
			accel.PolishSync()
			if okSetter != nil {
				okSetter.PolishSetOKLab(false) // loss/hard calls below stay on the gate's SSE metric
			}
		})
		tick(&tGrad, func() {
			accel.PolishReadGrad(hGrad)
			for i := range ps {
				copy(ps[i].grad[:], hGrad[i*10:i*10+10])
			}
		})
		tick(&tAdam, func() { adamStep(ps, fineOpt, it+1, w, h, polishFineLRScale) })
		if (it+1)%polishFineCheck == 0 || last {
			tick(&tHard, func() {
				hl := hardLoss(fineTau, true)
				if polishDebug {
					applog.Printf("polish-debug fine it=%d hard=%.1f best=%.1f", it+1, hl, bestHard)
				}
				if hl < bestHard {
					bestHard = hl
					bestP = snapshotParams(ps)
					fineGained, fineZero = true, 0
				} else {
					fineZero++
				}
			})
			// Give-up: a truly saturated input (greedy already at the budget-bound optimum) yields
			// nothing here either — if the first several checks bring zero gain, stop burning time.
			// Once ANY fine gain lands the phase runs to completion (it is in productive terrain).
			if !fineGained && fineZero >= 6 {
				doneIters += it + 1
				break
			}
		}
		if last {
			doneIters += fineIters
		}
	}
	restoreParams(ps, bestP)

	out := make([]model.Shape, 0, len(shapes))
	out = append(out, cloneShape(shapes[0])) // clone, not alias: recolorVisible mutates opaque shapes in place; on a polish-discard the caller's input bg must stay untouched
	for i := range ps {
		out = append(out, snapShape(ps[i], shapes[i+1], w, h))
	}
	return PolishResult{Shapes: out, PreLoss: pre, PostLoss: post, Iters: doneIters,
		Phases: [7]time.Duration{tUpload, tFwd, tLoss, tBwd, tGrad, tAdam, tHard}}
}

// Polish jointly refines shapes[1:] (shapes[0] = background, fixed) and returns
// snapped hard shapes. It does NOT gate on the hard FinalError itself — the
// caller renders the result through the backend and keeps it only if it does not
// regress (polish is strictly opt-in and must never make things worse).
func Polish(shapes []model.Shape, target, weight []float32, w, h int, bg model.RGBA, transparent bool, opt PolishOptions) PolishResult {
	if opt.Iters <= 0 {
		opt = DefaultPolishOptions()
	}
	clampPolishTau(&opt)
	if len(shapes) <= 1 {
		return PolishResult{Shapes: shapes}
	}
	// Base canvas (everything composites over this): bg fill, or transparent.
	base := make([]float32, w*h*4)
	if !transparent {
		for i := 0; i < w*h; i++ {
			base[i*4+0], base[i*4+1], base[i*4+2], base[i*4+3] = bg.R, bg.G, bg.B, 1
		}
	}

	ps := make([]pshape, 0, len(shapes)-1)
	for _, s := range shapes[1:] {
		k := model.KindFromType(s.Type)
		pp := model.ParamsFromShape(s)
		var P [6]float64
		for i := 0; i < 6; i++ {
			P[i] = float64(pp[i])
		}
		var c [4]float64
		if len(s.Color) >= 4 {
			c = [4]float64{float64(model.DecChan(s.Color[0])), float64(model.DecChan(s.Color[1])), float64(model.DecChan(s.Color[2])), float64(s.Color[3]) / 255}
		} else {
			c[3] = 1
		}
		ps = append(ps, pshape{kind: k, P: P, col: c, optGeo: optimizableGeo(k)})
	}

	render := make([]float32, w*h*4)
	dC := make([]float64, w*h*4)
	// Per-shape "color below" snapshot over the shape's expanded bbox.
	below := make([][]float32, len(ps))
	bbx := make([][4]int, len(ps)) // xMin,yMin,xMax,yMax per shape

	polishForward(ps, base, render, below, bbx, w, h, opt.Tau0, opt.STE)
	pre := polishLoss(render, target, weight, w, h, false)

	// Best-HARD tracking — the fix for the soft->hard "snap gap". The gradient
	// descent minimises the SOFT (sigmoid-coverage) render, whose loss keeps falling,
	// but the value we actually ship is the HARD (binary-coverage) render. Those
	// diverge: the soft optimum overshoots the hard optimum, so running to the final
	// iter and snapping can be WORSE (in hard loss) than an earlier iter. So we render
	// the HARD snap periodically and keep the params with the lowest hard loss seen.
	// Baseline = the greedy input itself (so polish can NEVER return worse than greedy).
	hardScratch := make([]float32, w*h*4)
	bestHard := polishHardLoss(ps, base, target, weight, hardScratch, w, h, false)
	bestP := snapshotParams(ps)
	checkEvery := maxInt(1, opt.Iters/25)
	earlyMargin := opt.EarlyStopMargin
	earlyPatience := opt.EarlyStopPatience
	if earlyPatience <= 0 {
		earlyPatience = 5
	}
	stall := 0
	initHard := bestHard
	lastBest := bestHard
	doneIters := opt.Iters

	var post float64
	var lastPrevCPU time.Time
	for it := 0; it < opt.Iters; it++ {
		t := float64(it) / float64(maxInt(1, opt.Iters-1))
		tau := opt.Tau0 * math.Pow(opt.Tau1/opt.Tau0, t)
		polishForward(ps, base, render, below, bbx, w, h, tau, opt.STE)
		if opt.OnPreview != nil && time.Since(lastPrevCPU) >= opt.previewInterval() {
			lastPrevCPU = time.Now()
			opt.OnPreview(render, w, h)
		}
		if opt.OnProgress != nil {
			opt.OnProgress(it+1, opt.Iters)
		}
		post = polishLoss(render, target, weight, w, h, false)
		polishBackward(ps, base, render, target, weight, below, bbx, dC, w, h, tau, opt.STE, false)
		adamStep(ps, opt, it+1, w, h, 1)
		last := it == opt.Iters-1
		if (it+1)%checkEvery == 0 || last {
			hl := polishHardLoss(ps, base, target, weight, hardScratch, w, h, false)
			if polishDebug {
				applog.Printf("polish-debug(cpu) it=%d tau=%.3f hard=%.1f best=%.1f soft=%.1f", it+1, tau, hl, bestHard, post)
			}
			if hl < bestHard {
				bestHard = hl
				bestP = snapshotParams(ps)
			}
			// Diminishing-returns early-stop, gated to the late phase (see polishEarlyMinProgress):
			// a check adding < earlyMargin of the total gain so far is a plateau.
			if t >= polishEarlyMinProgress {
				total := initHard - bestHard
				gain := lastBest - bestHard
				if total > 1e-9 && gain < earlyMargin*total {
					stall++
				} else {
					stall = 0
				}
				lastBest = bestHard
			}
			if earlyMargin > 0 && !last && t >= polishEarlyMinProgress && stall >= earlyPatience {
				doneIters = it + 1
				break // diminishing returns in the late phase — drop the wasteful tail
			}
		}
	}
	// FINE-EXPLOIT phase — mirrors PolishWithBackend (see the comment there): restart from the
	// best point seen with fresh moments, a small LR, and a fixed near-final tau, so saturated
	// inputs (where the tau-anneal excursion never pays back) still harvest the careful wins.
	restoreParams(ps, bestP)
	for i := range ps {
		ps[i].m, ps[i].v = [10]float64{}, [10]float64{}
	}
	fineIters := polishFineIters(opt.Iters)
	fineTau := math.Max(opt.Tau1, polishFineTauMin)
	fineOpt := opt
	fineOpt.Iters = fineIters // keys adamStep's warmup ramp to the fine budget
	fineGained, fineZero := false, 0
	for it := 0; it < fineIters; it++ {
		polishForward(ps, base, render, below, bbx, w, h, fineTau, opt.STE)
		if opt.OnPreview != nil && time.Since(lastPrevCPU) >= opt.previewInterval() {
			lastPrevCPU = time.Now()
			opt.OnPreview(render, w, h)
		}
		if opt.OnProgress != nil {
			opt.OnProgress(doneIters+it+1, doneIters+fineIters)
		}
		polishBackward(ps, base, render, target, weight, below, bbx, dC, w, h, fineTau, opt.STE, opt.OKLab)
		adamStep(ps, fineOpt, it+1, w, h, polishFineLRScale)
		if (it+1)%polishFineCheck == 0 || it == fineIters-1 {
			hl := polishHardLoss(ps, base, target, weight, hardScratch, w, h, false)
			if polishDebug {
				applog.Printf("polish-debug(cpu) fine it=%d hard=%.1f best=%.1f", it+1, hl, bestHard)
			}
			if hl < bestHard {
				bestHard = hl
				bestP = snapshotParams(ps)
				fineGained, fineZero = true, 0
			} else {
				fineZero++
			}
			// Give-up mirror of PolishWithBackend: zero gain across the first several checks on a
			// saturated input — stop burning time; any gain → run the phase to completion.
			if !fineGained && fineZero >= 6 {
				doneIters += it + 1
				break
			}
		}
		if it == fineIters-1 {
			doneIters += fineIters
		}
	}
	restoreParams(ps, bestP) // ship the best HARD point, not the final soft one

	// Snap back to hard, game-representable shapes.
	out := make([]model.Shape, 0, len(shapes))
	out = append(out, cloneShape(shapes[0])) // clone, not alias: recolorVisible mutates opaque shapes in place; on a polish-discard the caller's input bg must stay untouched
	for i := range ps {
		out = append(out, snapShape(ps[i], shapes[i+1], w, h))
	}
	return PolishResult{Shapes: out, PreLoss: pre, PostLoss: post, Iters: doneIters}
}

// snapshotParams copies each shape's geometry+color (the optimised state) so the
// best-hard point on the trajectory can be restored after the loop.
func snapshotParams(ps []pshape) [][10]float64 {
	out := make([][10]float64, len(ps))
	for i := range ps {
		copy(out[i][0:6], ps[i].P[:])
		copy(out[i][6:10], ps[i].col[:])
	}
	return out
}

func restoreParams(ps []pshape, snap [][10]float64) {
	for i := range ps {
		copy(ps[i].P[:], snap[i][0:6])
		copy(ps[i].col[:], snap[i][6:10])
	}
}

// polishHardLoss renders the shapes with HARD (binary) coverage and straight-alpha
// "over" — exactly the engine's Apply — into render (scratch), and returns the
// weighted SSE vs target. This is the loss we actually ship, so the polish optimises
// toward it via best-hard tracking even though its gradients come from the soft render.
func polishHardLoss(ps []pshape, base, target, weight, render []float32, w, h int, oklab bool) float64 {
	copy(render, base)
	for si := range ps {
		var fp [6]float32
		for i := 0; i < 6; i++ {
			fp[i] = float32(ps[si].P[i])
		}
		a := float32(clampF64(ps[si].col[3], 0, 1))
		cr, cg, cb := float32(ps[si].col[0]), float32(ps[si].col[1]), float32(ps[si].col[2])
		isGrad := raster.IsGradient(ps[si].kind)
		xMin, yMin, xMax, yMax := raster.BBox(ps[si].kind, fp, w, h)
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				// Gradient kinds composite with their per-pixel falloff; hard kinds with binary
				// coverage (aEff=a, byte-identical to the prior premultiplied path).
				var aEff float32
				if isGrad {
					cov := float32(raster.Coverage(ps[si].kind, fp, x, y))
					if cov <= 0 {
						continue
					}
					aEff = a * cov
				} else {
					if !raster.Inside(ps[si].kind, fp, x, y) {
						continue
					}
					aEff = a
				}
				ia := 1 - aEff
				p := (y*w + x) * 4
				render[p+0] = render[p+0]*ia + cr*aEff
				render[p+1] = render[p+1]*ia + cg*aEff
				render[p+2] = render[p+2]*ia + cb*aEff
				render[p+3] = render[p+3]*ia + aEff
			}
		}
	}
	return polishLoss(render, target, weight, w, h, oklab)
}

func maxIntF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// expandedBBox returns the shape bbox grown by ~3*tau so the soft edge is covered.
func expandedBBox(p pshape, w, h int, tau float64) [4]int {
	var fp [6]float32
	for i := 0; i < 6; i++ {
		fp[i] = float32(p.P[i])
	}
	xMin, yMin, xMax, yMax := raster.BBox(p.kind, fp, w, h)
	if p.optGeo {
		m := int(math.Ceil(3 * tau))
		xMin = maxInt(0, xMin-m)
		yMin = maxInt(0, yMin-m)
		xMax = minInt(w-1, xMax+m)
		yMax = minInt(h-1, yMax+m)
	}
	return [4]int{xMin, yMin, xMax, yMax}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// polishForward composites all shapes over base into render (soft cov for
// ellipses, hard for others) and stores each shape's "below" color over its
// bbox for the backward pass. The loss is computed separately by polishLoss.
func polishForward(ps []pshape, base, render []float32, below [][]float32, bbx [][4]int, w, h int, tau float64, ste bool) {
	copy(render, base)
	for si := range ps {
		bb := expandedBBox(ps[si], w, h, tau)
		bbx[si] = bb
		xMin, yMin, xMax, yMax := bb[0], bb[1], bb[2], bb[3]
		bw := xMax - xMin + 1
		bh := yMax - yMin + 1
		if bw < 1 || bh < 1 {
			below[si] = below[si][:0]
			continue
		}
		need := bw * bh * 4
		if cap(below[si]) < need {
			below[si] = make([]float32, need)
		}
		below[si] = below[si][:need]
		R, G, B, A := float32(ps[si].col[0]), float32(ps[si].col[1]), float32(ps[si].col[2]), float32(ps[si].col[3])
		var fp [6]float32
		for i := 0; i < 6; i++ {
			fp[i] = float32(ps[si].P[i])
		}
		li := 0
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				p := (y*w + x) * 4
				// snapshot color below (before compositing this shape)
				below[si][li+0] = render[p+0]
				below[si][li+1] = render[p+1]
				below[si][li+2] = render[p+2]
				below[si][li+3] = render[p+3]
				cov := coverage(ps[si], fp, x, y, tau, ste)
				if cov > 0 {
					a := A * cov
					ia := 1 - a
					render[p+0] = render[p+0]*ia + R*a
					render[p+1] = render[p+1]*ia + G*a
					render[p+2] = render[p+2]*ia + B*a
					render[p+3] = render[p+3]*ia + a // shape alpha-color = 1
				}
				li += 4
			}
		}
	}
}

// optimizableGeo reports whether a kind has a differentiable SDF (geometry refined in
// the polish). Ellipse + rectangle (5 params) and triangle (6 vertex params) all do.
func optimizableGeo(k model.ShapeKind) bool {
	// KindGlow (gaussian splat) has a SMOOTH analytic coverage gradient (raster.GaussianCovGrad), so its
	// geometry is trainable by the joint polish — the basis of the GaussianImage direction. KindDisk's
	// opaque core has zero geometry gradient, so it stays frozen (colour/alpha refit only).
	return k == model.KindEllipse || k == model.KindRectangle || k == model.KindTriangle || k == model.KindGlow
}

// sdfGrad dispatches the signed-distance + gradient by kind (negative inside). Returns
// a 6-slot gradient: ellipse/rect fill 5 (cx,cy,rx,ry,θ; slot 5 = 0); triangle fills all
// 6 (the 3 vertex coords x1,y1,x2,y2,x3,y3).
func sdfGrad(kind model.ShapeKind, P [6]float64, px, py float64) (sdf float64, g [6]float64) {
	switch kind {
	case model.KindRectangle:
		s, g5 := rectSDFGrad(P, px, py)
		copy(g[:5], g5[:])
		return s, g
	case model.KindTriangle:
		return triangleSDFGrad(P, px, py)
	default:
		s, g5 := ellipseSDFGrad(P, px, py)
		copy(g[:5], g5[:])
		return s, g
	}
}

// coverage returns the soft (ellipse/rect) or hard (other kinds) coverage in [0,1].
// Under STE, optimizable shapes use HARD coverage (sdf<=0) in the forward composite —
// the exact deliverable render — while the backward still uses the soft surrogate slope.
func coverage(p pshape, fp [6]float32, x, y int, tau float64, ste bool) float32 {
	if raster.IsGradient(p.kind) {
		return float32(raster.Coverage(p.kind, fp, x, y)) // radial falloff — same whether geometry is frozen or trained
	}
	if !p.optGeo {
		if raster.Inside(p.kind, fp, x, y) {
			return 1
		}
		return 0
	}
	sdf, _ := sdfGrad(p.kind, p.P, float64(x)+0.5, float64(y)+0.5)
	if ste {
		if sdf <= 0 {
			return 1
		}
		return 0
	}
	return float32(sigmoidCov(sdf, tau))
}

func sigmoidCov(sdf, tau float64) float64 {
	z := sdf / tau
	if z > 40 {
		return 0
	}
	if z < -40 {
		return 1
	}
	return 1 / (1 + math.Exp(z))
}

// polishLoss returns the weighted SSE of render vs target over all 4 channels.
func polishLoss(render, target, weight []float32, w, h int, oklab bool) float64 {
	var sum float64
	for idx := 0; idx < w*h; idx++ {
		wt := float64(weight[idx])
		p := idx * 4
		if oklab {
			sum += okLabPixelLoss(float64(render[p]), float64(render[p+1]), float64(render[p+2]), float64(render[p+3]),
				float64(target[p]), float64(target[p+1]), float64(target[p+2]), float64(target[p+3]), wt)
			continue
		}
		for c := 0; c < 4; c++ {
			d := float64(render[p+c] - target[p+c])
			sum += wt * d * d
		}
	}
	return sum
}

// polishBackward accumulates dLoss/dparam into each pshape's grad slice,
// recomputing per-pixel gradients in a reverse (top-to-bottom) pass.
func polishBackward(ps []pshape, base, render, target, weight []float32, below [][]float32, bbx [][4]int, dC []float64, w, h int, tau float64, ste, oklab bool) {
	_ = base
	// dL/dC_final = 2*weight*(C-target) per channel (OKLab mode: 2*weight*Jᵀ*ΔLab — see oklab.go).
	for idx := 0; idx < w*h; idx++ {
		wt := float64(weight[idx])
		p := idx * 4
		if oklab {
			dC[p+0], dC[p+1], dC[p+2], dC[p+3] = okLabPixelDC(
				float64(render[p]), float64(render[p+1]), float64(render[p+2]), float64(render[p+3]),
				float64(target[p]), float64(target[p+1]), float64(target[p+2]), float64(target[p+3]), wt)
			continue
		}
		for c := 0; c < 4; c++ {
			dC[p+c] = 2 * wt * float64(render[p+c]-target[p+c])
		}
	}
	for si := len(ps) - 1; si >= 0; si-- {
		s := &ps[si]
		bb := bbx[si]
		xMin, yMin, xMax, yMax := bb[0], bb[1], bb[2], bb[3]
		if xMax < xMin || yMax < yMin {
			continue
		}
		bw := xMax - xMin + 1
		R, G, B, A := s.col[0], s.col[1], s.col[2], s.col[3]
		var fp [6]float32
		for i := 0; i < 6; i++ {
			fp[i] = float32(s.P[i])
		}
		var gR, gG, gB, gA float64
		var gP [6]float64
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				p := (y*w + x) * 4
				li := ((y-yMin)*bw + (x - xMin)) * 4
				// covEff = the coverage the FORWARD composited with (soft sigmoid, or HARD
				// step under STE). dcovdsdf is ALWAYS the soft surrogate (the STE gradient).
				// SPLIT GUARD: the geometry gradient must flow over the whole outer soft band
				// (covS>0) even when covEff(hard)=0 there — else STE edges could only shrink.
				// The color/alpha/dC-propagation block runs only where the shape actually
				// composited (covEff>0). In soft mode covEff==covS so both collapse to the
				// old single guard (byte-identical).
				var covEff, covS, dcovdsdf float64
				var sdfg [6]float64
				geoActive := false
				if raster.IsGradient(s.kind) {
					// Gaussian/disk: coverage IS the radial falloff. For a TRAINABLE glow (optGeo) route
					// its analytic dcov/dparam through the geometry-grad block by carrying it in sdfg with
					// dcovdsdf=1 (so gP[i] += dcov·dcov/dparam[i]); a frozen gradient (disk) only composites.
					cov, gg := raster.GaussianCovGrad(s.kind, fp, x, y)
					covEff = cov
					if s.optGeo {
						covS = cov
						sdfg = gg
						dcovdsdf = 1
						geoActive = cov > 1e-9
					}
				} else if s.optGeo {
					var sdf float64
					sdf, sdfg = sdfGrad(s.kind, s.P, float64(x)+0.5, float64(y)+0.5)
					covS = sigmoidCov(sdf, tau)
					dcovdsdf = -covS * (1 - covS) / tau
					if ste {
						if sdf <= 0 {
							covEff = 1
						}
						geoActive = covS > 1e-12
					} else {
						covEff = covS
						geoActive = covS > 0
					}
				} else if raster.Inside(s.kind, fp, x, y) {
					covEff = 1
				}
				colorActive := covEff > 0
				if !geoActive && !colorActive {
					continue
				}
				d0, d1, d2, d3 := dC[p+0], dC[p+1], dC[p+2], dC[p+3]
				cb0, cb1, cb2, cb3 := float64(below[si][li+0]), float64(below[si][li+1]), float64(below[si][li+2]), float64(below[si][li+3])
				// dLoss/da = dC . (shapeColor - below); shapeColor = (R,G,B,1). Valid over the
				// whole bbox (dC + below are both defined regardless of covEff) — this is the
				// signal that pulls a soft edge outward under STE.
				da := d0*(R-cb0) + d1*(G-cb1) + d2*(B-cb2) + d3*(1-cb3)
				if colorActive {
					a := A * covEff
					// dLoss/dcolor (RGB); alpha-color is fixed at 1.
					gR += d0 * a
					gG += d1 * a
					gB += d2 * a
					gA += da * covEff
					// propagate to shapes below: dC *= (1-a)
					ia := 1 - a
					dC[p+0] *= ia
					dC[p+1] *= ia
					dC[p+2] *= ia
					dC[p+3] *= ia
				}
				if geoActive {
					dcov := da * A
					dsdf := dcov * dcovdsdf
					gP[0] += dsdf * sdfg[0]
					gP[1] += dsdf * sdfg[1]
					gP[2] += dsdf * sdfg[2]
					gP[3] += dsdf * sdfg[3]
					gP[4] += dsdf * sdfg[4]
					gP[5] += dsdf * sdfg[5]
				}
			}
		}
		s.grad = [10]float64{gP[0], gP[1], gP[2], gP[3], gP[4], gP[5], gR, gG, gB, gA}
	}
}

// adamStep applies one Adam update to every shape's parameters using the
// gradients computed by polishBackward, with per-group learning rates and the
// stability clamps from the design.
func adamStep(ps []pshape, opt PolishOptions, step int, w, h int, lrScale float64) {
	const b1, b2, eps = 0.9, 0.999, 1e-8
	bc1 := 1 - math.Pow(b1, float64(step))
	bc2 := 1 - math.Pow(b2, float64(step))
	// LR warmup. Adam's bias-corrected first step is ±lr regardless of the gradient magnitude
	// (m̂/√v̂ = g/|g| at step 1), so iteration 1 kicks EVERY param of EVERY shape by its full
	// learning rate at once. On a tightly-fitted greedy canvas (full-budget runs) that explodes
	// the hard loss ~3× and the whole iteration budget is spent crawling back — polish never
	// re-beats its own input, the gate discards it, and the pass is a silent no-op. Ramping the
	// LR linearly over the first ~5% of the run keeps the early steps proportional while the
	// moment estimates learn real directions, so the descent starts FROM the input basin.
	warmup := 1.0
	if ws := maxInt(5, opt.Iters/20); step < ws {
		warmup = float64(step) / float64(ws)
	}
	for si := range ps {
		s := &ps[si]
		g := s.grad
		lr := geoColorLR(s.kind, opt)
		if opt.GradClip > 0 {
			// clip geometry gradient L2 norm (6 geo slots; ellipse/rect leave slot 5 = 0)
			var n2 float64
			for k := 0; k < 6; k++ {
				n2 += g[k] * g[k]
			}
			if n := math.Sqrt(n2); n > opt.GradClip {
				sc := opt.GradClip / n
				for k := 0; k < 6; k++ {
					g[k] *= sc
				}
			}
		}
		for k := 0; k < 10; k++ {
			if k < 6 && !s.optGeo {
				continue // fixed geometry for non-optimizable kinds
			}
			s.m[k] = b1*s.m[k] + (1-b1)*g[k]
			s.v[k] = b2*s.v[k] + (1-b2)*g[k]*g[k]
			mh := s.m[k] / bc1
			vh := s.v[k] / bc2
			upd := warmup * lrScale * lr[k] * mh / (math.Sqrt(vh) + eps)
			if k < 6 {
				s.P[k] -= upd
			} else {
				s.col[k-6] -= upd
			}
		}
		clampGeoParams(s, w, h)
		for c := 0; c < 3; c++ {
			s.col[c] = clampF64(s.col[c], 0, 1)
		}
		s.col[3] = clampF64(s.col[3], 0.05, 1)
	}
}

// geoColorLR returns the per-slot Adam learning rates for a shape's 6 geometry + 4 color
// params. Triangle geometry = 3 vertex coords (all position-like -> LRPos); ellipse/rect =
// cx,cy,rx,ry,θ (slot 5 unused -> lr 0).
func geoColorLR(k model.ShapeKind, opt PolishOptions) [10]float64 {
	if k == model.KindTriangle {
		return [10]float64{opt.LRPos, opt.LRPos, opt.LRPos, opt.LRPos, opt.LRPos, opt.LRPos,
			opt.LRColor, opt.LRColor, opt.LRColor, opt.LRAlpha}
	}
	return [10]float64{opt.LRPos, opt.LRPos, opt.LRRad, opt.LRRad, opt.LRAng, 0,
		opt.LRColor, opt.LRColor, opt.LRColor, opt.LRAlpha}
}

// clampGeoParams applies kind-aware geometry clamps after an Adam step.
func clampGeoParams(s *pshape, w, h int) {
	if s.kind == model.KindTriangle {
		if !s.optGeo {
			return
		}
		for k := 0; k < 6; k++ { // 3 vertices: even=x clamp to width, odd=y clamp to height
			if k%2 == 0 {
				s.P[k] = clampF64(s.P[k], 0, float64(w-1))
			} else {
				s.P[k] = clampF64(s.P[k], 0, float64(h-1))
			}
		}
		return
	}
	s.P[0] = clampF64(s.P[0], 0, float64(w-1))
	s.P[1] = clampF64(s.P[1], 0, float64(h-1))
	if s.optGeo {
		s.P[2] = maxIntF(1, s.P[2])
		s.P[3] = maxIntF(1, s.P[3])
		s.P[4] = math.Mod(s.P[4], 360)
	}
}

func clampF64(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// snapShape converts a refined pshape back to a hard, game-representable Shape,
// preserving the original Type. Geometry rounded/clamped; color in 8-bit.
func snapShape(p pshape, orig model.Shape, w, h int) model.Shape {
	c := model.Candidate{Kind: p.kind, Color: model.RGBA{
		R: float32(p.col[0]), G: float32(p.col[1]), B: float32(p.col[2]), A: float32(p.col[3]),
	}}
	for i := 0; i < 6; i++ {
		c.P[i] = float32(p.P[i])
	}
	return c.ToShape(orig.Score)
}
