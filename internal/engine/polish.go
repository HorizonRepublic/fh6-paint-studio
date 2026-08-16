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

// polishPhaseTiming (FH6_POLISH_PHASES=1) splits the polish wall time across forward/backward in
// Timings.PolishPhases. It costs a device sync per phase per iteration — the work has to finish
// before the gradient readback anyway, so the total does not change, but each extra sync is another
// wait the host has to sit through. Off by default: the split is a profiling detail.
var polishPhaseTiming = os.Getenv("FH6_POLISH_PHASES") != ""

func phaseSync(accel PolishAccel) {
	if polishPhaseTiming {
		accel.PolishSync()
	}
}

// Joint differentiable "polish" pass — breaks the greedy plateau that pure greedy
// placement cannot. After greedy placement, ALL shapes are refined together by
// gradient descent on a SOFT-rasterized render vs the target, so shapes co-adapt
// (faceted fur merges into smooth coverage; the smeared face sharpens). The soft
// coverage cov = sigmoid(-SDF/tau) makes the hard inside-test differentiable;
// tau anneals soft->sharp (coarse-to-fine) and we SNAP back to the exact hard
// ellipse/rect/triangle the editor needs. Optimised in sRGB (the editor's display
// space). Ellipse geometry+color+alpha are refined; rect/triangle composite with
// their hard coverage (fixed geometry) but still get color+alpha refined.

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

	// Adaptive continuation: past the base budget the phase keeps going in chunks as long as
	// each chunk still earns its keep — the fine descent is often still productive at the base
	// boundary (measured: anime@3000 hard still falling at the last base iter). A chunk that
	// gains < polishFineExtMargin of the polish's TOTAL gain so far is the plateau signal.
	polishFineExtChunk  = 25   // extension chunk length (iters; 5 best-tracking checks)
	polishFineExtMargin = 0.02 // min chunk gain as a fraction of total gain to continue
	polishFineExtCap    = 3    // hard cap: at most this multiple of the base fine budget
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

	// FalseEdgeLambda adds λ·relu(|∇recon|−|∇target|) (Sobel on luma — the standout detector) to the
	// polish loss so shapes whose rims draw edges the target lacks are pressed DOWN during the descent
	// instead of by the post-hoc standout pass. EXPERIMENT, additive-only per the OKLab lesson; CPU
	// polish driver only (the option routes applyPolish off the GPU path). 0 = off (CLI -polish-false-edge).
	FalseEdgeLambda float64

	// SSIMLambda adds λ·Σ(1−SSIM_local) (uniform 8×8 luma windows — see ssimterm.go) to the polish
	// loss so local contrast/structure errors SSE undercharges are pressed during the descent.
	// EXPERIMENT, additive-only per the OKLab lesson; 0 = off (CLI -polish-ssim).
	SSIMLambda float64

	// EagleLambda adds λ·Σ|HP(var₃(Scharr recon)) − HP(var₃(Scharr target))| (edge-aware gradient
	// STRUCTURE mismatch — see eagleterm.go) to the polish loss. CPU polish driver only (a non-zero
	// λ routes polish off the GPU, like FE before its port). 0 = off.
	EagleLambda float64

	// LostDetailLambda adds λ·Σ relu(|∇target|−|∇recon|) — the MIRROR of FalseEdgeLambda. FE charges
	// edges the recon invents; this charges structure it ERASES. Nothing else in the loss sees that:
	// a rimless glow laid over detail draws no false edge, and a blob near the local mean is cheap in
	// SSE, so blur-over-structure was invisible to the whole objective (owner's "meat on the neck",
	// 2026-08-03). 0 = off. See lostdetail.go. NB no device port yet — a non-zero λ only takes effect
	// on the host polish path.
	LostDetailLambda float64

	// TermWeight (optional, len w*h) multiplies the FE/EAGLE per-pixel charges — REGION-WEIGHTED
	// perceptual terms. Built from 1−metric.HardEdgeMap (Options.TermRegionWeight): the rim
	// patchwork lives in SMOOTH zones, but a global λ strong enough to clean it over-presses
	// legitimate line-work, so λ was capped low; weighting by smoothness lets λ run much stronger
	// exactly where the artifact is and ~vanish on real edges. nil = uniform (the pre-weighting
	// behaviour, bit-identical). Applies to FE + EAGLE (SSIM windows span regions — left uniform).
	TermWeight []float32
	STE        bool // straight-through estimator: FORWARD composites HARD coverage (the exact deliverable), BACKWARD keeps the SOFT surrogate dcov/dsdf for the geometry chain only. Closes the soft->hard snap gap (optimizes the shipped hard SSE directly). Default false (soft polish).

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

	// AlphaMin floors the alpha the descent may reach. The greedy places candidates above the
	// preset's floor (organic 0.30, shipped 2026-06-10 and seed-replicated), and the polish then
	// optimised alpha freely down to a hard-coded 0.05 — a shipped quality default undone by the
	// next stage. It is also a candidate mechanism for the veiling complaint, since a veil is what
	// dozens of near-transparent layers look like. 0 = the historical 0.05.
	AlphaMin float64

	// OnProgress, if set, is called once per refinement iteration with (iter 1..Iters, total Iters).
	// It carries NO device read, so it is cheap to fire every iteration — used by the Gaussian mode to
	// drive a training % bar (the greedy's shape-count progress is meaningless when all glows train at
	// once). nil for the normal greedy+polish (whose bar tracks placed shapes instead).
	OnProgress func(iter, total int)

	// Cancel mirrors Options.Cancel into the polish loops. Without it, Stop only worked during the
	// greedy — the polish (and every LOO re-polish) ran its full budget while the UI already said
	// "stopped", which is most of a run's wall time and reads as "the app kept my GPU busy".
	// A cancelled polish keeps the best-hard params found so far, like an early-stop.
	Cancel func() bool

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

// alphaFloor returns the lower bound the descent may push a shape's alpha to. Unset keeps the
// historical 0.05 so every caller that does not thread a preset floor is bit-identical.
func (o PolishOptions) alphaFloor() float64 {
	if o.AlphaMin > 0 && o.AlphaMin < 1 {
		return o.AlphaMin
	}
	return 0.05
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
	grad [10]float64 // gradient staged from the device grad readback, consumed by adamStep
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
	PolishSupported() bool // false if the loaded DLL predates the polish API (polish is then skipped)
	// PolishSetup allocates the device state; a non-nil error means the device could not hold it
	// (usually VRAM) and every later fp_polish_* call is a no-op — the caller must skip the pass.
	PolishSetup(base []float32, n int) error
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
		return PolishResult{Shapes: cloneShapes(shapes)} // CLONE: callers recolor the result in place, and aliasing the input let a gate-rejected recolor corrupt it
	}
	tCall := time.Now()
	var tSetup, tPre, tMain, tFine time.Duration
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
	// VRAM ladder. The polish commits ~1.5GB of device memory at 4Mpx (more with the perceptual
	// term planes); on a card that cannot hold it the driver either fails the allocations (the
	// pass used to die SILENTLY — zero losses, zero grads, unpolished output) or demotes them to
	// system memory and crawls over PCIe. Ask the driver for the live budget first and degrade
	// honestly: drop the FE/SSIM/EAGLE planes, then skip the pass with a clear message. On a card
	// with room the numbers never trip and the path is byte-identical.
	if ma, ok := accel.(interface {
		MemInfo() (budget, usage, heap int64, ok bool)
		PolishMemNeed(n int, belowTotal int64, terms int) int64
	}); ok {
		if budget, usage, heap, ok2 := ma.MemInfo(); ok2 && heap > 0 {
			avail := budget - usage
			if budget == 0 {
				avail = heap * 17 / 20 // driver lacks VK_EXT_memory_budget: assume 85% of the heap
			}
			var belowEst int64
			for i := range ps {
				bb := expandedBBox(ps[i], w, h, opt.Tau0) // Tau0 = the widest bboxes of the anneal
				if bw, bh := int64(bb[2]-bb[0]+1), int64(bb[3]-bb[1]+1); bw > 0 && bh > 0 {
					belowEst += bw * bh
				}
			}
			terms := 0
			if opt.FalseEdgeLambda > 0 || opt.LostDetailLambda > 0 {
				terms |= 1
			}
			if opt.SSIMLambda > 0 {
				terms |= 2
			}
			if opt.EagleLambda > 0 {
				terms |= 4
			}
			need := ma.PolishMemNeed(n, belowEst, terms)
			if need > 0 && need > avail && terms != 0 {
				if slim := ma.PolishMemNeed(n, belowEst, 0); slim <= avail {
					applog.Printf("polish: low VRAM (needs ~%dMB with perceptual terms, ~%dMB free) — dropping FE/SSIM/EAGLE terms, polish continues", need>>20, avail>>20)
					opt.FalseEdgeLambda, opt.LostDetailLambda, opt.SSIMLambda, opt.EagleLambda = 0, 0, 0, 0
					need = slim
				}
			}
			if need > 0 && need > avail {
				applog.Printf("polish: SKIPPED — not enough free VRAM (needs ~%dMB, ~%dMB free). Close other GPU apps or lower the resolution; shapes ship unpolished.", need>>20, avail>>20)
				return PolishResult{Shapes: cloneShapes(shapes)} // CLONE: callers recolor the result in place, and aliasing the input let a gate-rejected recolor corrupt it
			}
		}
	}
	if err := accel.PolishSetup(base, n); err != nil {
		applog.Printf("polish: %v — skipping the pass, shapes ship unpolished", err)
		return PolishResult{Shapes: cloneShapes(shapes)} // CLONE: callers recolor the result in place, and aliasing the input let a gate-rejected recolor corrupt it
	}
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
	// False-edge λ: ALWAYS set when the capability exists (0 resets a previous run's value — the
	// shim global outlives a polish). The device folds λ·FE into loss/hard-loss/dC, so the loop
	// below needs no FE-specific work. applyPolish routes λ>0 to the CPU driver on old DLLs.
	if s, ok := accel.(interface{ PolishSetFalseEdge(lambda float64) bool }); ok {
		if !s.PolishSetFalseEdge(opt.FalseEdgeLambda) && opt.FalseEdgeLambda > 0 {
			applog.Printf("polish: false-edge λ requested but the device lacks fp_set_polish_false_edge — term disabled")
		}
	}
	// SSIM λ: same contract as false-edge (always reset, device folds the term in).
	if s, ok := accel.(interface{ PolishSetSSIM(lambda float64) bool }); ok {
		if !s.PolishSetSSIM(opt.SSIMLambda) && opt.SSIMLambda > 0 {
			applog.Printf("polish: SSIM λ requested but the device lacks fp_set_polish_ssim — term disabled")
		}
	}
	// EAGLE λ: same contract (always reset, device folds the term in).
	// Lost-detail λ: same contract as the others. A DLL without the export means the term is simply
	// ABSENT on the device — the polish stays on the GPU and silently optimises without it, which is
	// exactly how a shipped term can read as "enabled" while doing nothing. Say so in the log.
	if s, ok := accel.(interface{ PolishSetLostDetail(lambda float64) bool }); ok {
		if !s.PolishSetLostDetail(opt.LostDetailLambda) && opt.LostDetailLambda > 0 {
			applog.Printf("polish: lost-detail λ requested but the device lacks fp_set_polish_lostdetail — term INACTIVE (rebuild the shim)")
		}
	} else if opt.LostDetailLambda > 0 {
		applog.Printf("polish: lost-detail λ requested but this backend has no lost-detail support — term INACTIVE")
	}
	if s, ok := accel.(interface{ PolishSetEagle(lambda float64) bool }); ok {
		if !s.PolishSetEagle(opt.EagleLambda) && opt.EagleLambda > 0 {
			applog.Printf("polish: EAGLE λ requested but the device lacks fp_set_polish_eagle — term disabled")
		}
	}
	// Region term weight: same contract (always reset — nil clears a previous run's map). An old
	// DLL without the export runs the terms UNWEIGHTED at the given λ (log, no CPU fallback).
	if s, ok := accel.(interface{ PolishSetTermWeight(tw []float32) bool }); ok {
		if !s.PolishSetTermWeight(opt.TermWeight) && opt.TermWeight != nil {
			applog.Printf("polish: term weight requested but the device lacks fp_set_term_weight — weighting disabled")
		}
	} else if opt.TermWeight != nil {
		applog.Printf("polish: term weight requested but the accel backend has no PolishSetTermWeight — weighting disabled")
	}
	defer accel.PolishFree()

	// Reused host staging buffers for the per-iter upload.
	hP := make([]float64, n*6)
	hCol := make([]float64, n*4)
	hKind := make([]int32, n)
	hBBX := make([]int32, n*4)
	hOff := make([]int64, n)
	hGrad := make([]float64, n*10)

	// upload packs current params + the expanded-bbox/below layout and ships them. quant
	// rounds params/colours to the export precision first (hard checks measure the deliverable).
	upload := func(tau float64, quant bool) {
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
			if quant {
				quantizeExport(ps[i].kind, hP[i*6:i*6+6], hCol[i*4:i*4+4])
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

	tSetup = time.Since(tCall)
	tPreStart := time.Now()
	upload(opt.Tau0, false)
	accel.PolishForward(opt.Tau0, hBBX)
	pre := accel.PolishLoss()

	// Best-hard render runs on the GPU (fp_polish_hard_loss) — the only per-pixel polish
	// work that used to stay on the CPU. hardScratch is the CPU fallback for old DLLs.
	// Baseline: d_pbbx already holds the opt.Tau0 params (uploaded above), so no re-upload.
	// Allocated LAZILY: on the shipped path (a DLL with fp_polish_hard_loss) this fallback never
	// runs, and the eager make() was a canvas-sized zero-fill per polish call for nothing.
	var hardScratch []float32
	hardLoss := func(tau float64, reupload bool) float64 {
		if reupload {
			upload(tau, true) // ship the CURRENT (post-Adam) params at EXPORT precision
		}
		if hl, ok := accel.PolishHardLoss(hBBX); ok {
			return hl
		}
		if hardScratch == nil {
			hardScratch = make([]float32, w*h*4)
		}
		return polishHardLoss(ps, base, target, weight, hardScratch, w, h, false, true)
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
	tPre = time.Since(tPreStart)
	var tUpload, tFwd, tLoss, tBwd, tGrad, tAdam, tHard time.Duration
	tMainStart := time.Now()
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
	// Device-loss watch: after a TDR/driver reset every device call is a no-op returning zeros,
	// so without this the loop would burn the full iteration budget optimising against nothing.
	// The same poll point carries the user's Stop (opt.Cancel) into the loops.
	devLost, _ := accel.(interface{ DeviceLost() bool })
	lostNow := func(every, it int) bool {
		return devLost != nil && it%every == 0 && devLost.DeviceLost()
	}
	cancelled := func() bool { return opt.Cancel != nil && opt.Cancel() }
	for it := 0; it < opt.Iters; it++ {
		if cancelled() {
			doneIters = it
			break // Stop pressed: ship the best-hard point found so far, like an early-stop
		}
		if lostNow(16, it) {
			applog.Printf("polish: GPU device lost mid-pass (driver reset/TDR) — stopping at iter %d, best params so far are kept", it)
			doneIters = it
			break
		}
		t := float64(it) / float64(maxInt(1, opt.Iters-1))
		tau := opt.Tau0 * math.Pow(opt.Tau1/opt.Tau0, t)
		last := it == opt.Iters-1
		tick(&tUpload, func() { upload(tau, false) })
		// Kernel launches are async; sync inside the tick so the GPU time is attributed to
		// forward/backward (not hidden in the next sync). Net overhead ~0 — the work must
		// complete before readgrad anyway; the sync just moves the wait into the timer.
		tick(&tFwd, func() { accel.PolishForward(tau, hBBX); phaseSync(accel) })
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
		tick(&tBwd, func() { accel.PolishBackward(tau, hBBX); phaseSync(accel) })
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
	tMain = time.Since(tMainStart)
	tFineStart := time.Now()
	restoreParams(ps, bestP)
	for i := range ps {
		ps[i].m, ps[i].v = [10]float64{}, [10]float64{}
	}
	fineIters := polishFineIters(opt.Iters)
	fineCap := fineIters * polishFineExtCap
	fineTau := math.Max(opt.Tau1, polishFineTauMin)
	fineOpt := opt
	fineOpt.Iters = fineIters // keys adamStep's warmup ramp to the fine budget
	fineGained, fineZero := false, 0
	chunkBest := bestHard // best at the previous chunk boundary (adaptive-continuation gate)
	fineDone := fineCap
	for it := 0; it < fineCap; it++ {
		if cancelled() {
			fineDone = it
			break
		}
		if lostNow(16, it) {
			applog.Printf("polish: GPU device lost in the fine phase — stopping at iter %d", it)
			fineDone = it
			break
		}
		last := it == fineCap-1
		tick(&tUpload, func() { upload(fineTau, false) })
		tick(&tFwd, func() { accel.PolishForward(fineTau, hBBX); phaseSync(accel) })
		if prevRd != nil && time.Since(lastPrev) >= opt.previewInterval() {
			lastPrev = time.Now()
			prevRd.PolishReadRender(prevBuf)
			opt.OnPreview(prevBuf, w, h)
		}
		if opt.OnProgress != nil {
			opt.OnProgress(doneIters+it+1, doneIters+fineCap)
		}
		tick(&tBwd, func() {
			if okSetter != nil {
				okSetter.PolishSetOKLab(true) // perceptual gradient for the fine step only
			}
			accel.PolishBackward(fineTau, hBBX)
			phaseSync(accel)
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
				fineDone = it + 1
				break
			}
		}
		// Adaptive continuation: past the base budget, keep descending in chunks while each
		// chunk still gains ≥ polishFineExtMargin of the TOTAL polish gain (the descent is often
		// still productive at the base boundary); a sub-margin chunk is the plateau — stop.
		if (it+1)%polishFineExtChunk == 0 && !last {
			total := initHard - bestHard
			if it+1 >= fineIters && (total <= 1e-9 || chunkBest-bestHard < polishFineExtMargin*total) {
				fineDone = it + 1
				break
			}
			chunkBest = bestHard
		}
	}
	tFine = time.Since(tFineStart)
	// The pass is done: complete the progress counter explicitly. Both loops stop EARLY on
	// plateaus, so the last in-loop report can be well short of the denominator — the ETA phase
	// then sat at ~85-95% waiting for iterations that were never coming.
	if opt.OnProgress != nil {
		opt.OnProgress(doneIters+fineCap, doneIters+fineCap)
	}
	doneIters += fineDone
	restoreParams(ps, bestP)
	if polishPhaseTiming {
		applog.Printf("polish-account: total=%.1fs setup=%.1fs pre=%.1fs main=%.1fs fine=%.1fs | ticked upload=%.1f fwd=%.1f loss=%.1f bwd=%.1f grad=%.1f adam=%.1f hard=%.1f",
			time.Since(tCall).Seconds(), tSetup.Seconds(), tPre.Seconds(), tMain.Seconds(), tFine.Seconds(),
			tUpload.Seconds(), tFwd.Seconds(), tLoss.Seconds(), tBwd.Seconds(), tGrad.Seconds(), tAdam.Seconds(), tHard.Seconds())
	}

	out := make([]model.Shape, 0, len(shapes))
	out = append(out, cloneShape(shapes[0])) // clone, not alias: recolorVisible mutates opaque shapes in place; on a polish-discard the caller's input bg must stay untouched
	for i := range ps {
		out = append(out, snapShape(ps[i], shapes[i+1], w, h))
	}
	return PolishResult{Shapes: out, PreLoss: pre, PostLoss: post, Iters: doneIters,
		Phases: [7]time.Duration{tUpload, tFwd, tLoss, tBwd, tGrad, tAdam, tHard}}
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
func polishHardLoss(ps []pshape, base, target, weight, render []float32, w, h int, oklab, quant bool) float64 {
	copy(render, base)
	for si := range ps {
		var P [6]float64
		copy(P[:], ps[si].P[:])
		col := [4]float64{ps[si].col[0], ps[si].col[1], ps[si].col[2], clampF64(ps[si].col[3], 0, 1)}
		if quant {
			quantizeExport(ps[si].kind, P[:], col[:])
		}
		var fp [6]float32
		for i := 0; i < 6; i++ {
			fp[i] = float32(P[i])
		}
		a := float32(col[3])
		cr, cg, cb := float32(col[0]), float32(col[1]), float32(col[2])
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

// optimizableGeo reports whether a kind has a differentiable SDF (geometry refined in
// the polish). Ellipse + rectangle (5 params) and triangle (6 vertex params) all do.
func optimizableGeo(k model.ShapeKind) bool {
	// KindGlow (gaussian splat) has a SMOOTH analytic coverage gradient (raster.GaussianCovGrad), so its
	// geometry is trainable by the joint polish — the basis of the GaussianImage direction. KindDisk's
	// opaque core has zero geometry gradient, so it stays frozen (colour/alpha refit only).
	return k == model.KindEllipse || k == model.KindRectangle || k == model.KindTriangle || k == model.KindGlow
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
		if !polishRectSkew && s.kind == model.KindRectangle {
			g[5] = 0 // freeze the parallelogram skew DOF by default: no step AND no grad-clip contribution
		}
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
		s.col[3] = clampF64(s.col[3], opt.alphaFloor(), 1)
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
		if polishRectSkew && s.kind == model.KindRectangle {
			s.P[5] = clampF64(s.P[5], -rectSkewMax, rectSkewMax) // parallelogram DOF; keep the shear sane
		}
	}
}

// polishRectSkew gates the rectangle skew degree of freedom (rect -> parallelogram) in the joint
// polish. Default OFF => bit-identical to the historical skew-frozen polish; FH6_SKEWDOF=1 turns the
// analytic skew gradient on for A/B. Skew is redundant for the ellipse and the radial gradients (a
// sheared ellipse is just another rotated ellipse), so ONLY the rectangle gets the new shape.
var polishRectSkew = os.Getenv("FH6_SKEWDOF") == "1"

const rectSkewMax = 2.0

func clampF64(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// quantizeExport rounds a shape's optimisable params to the EXPORT precision — what ToShape
// actually ships: colours through the sRGB-byte round-trip (EncByte/DecChan), the rotation of
// angle-carrying kinds to 0.01° (normAngle). Best-hard tracking measures THIS render, so the
// polish picks the state that is best AFTER the snap instead of a continuous optimum whose
// sub-quantum micro-gains evaporate at export (and the fine phase's adaptive continuation
// stops as soon as quantized gains plateau, not when un-shippable continuous ones do).
func quantizeExport(kind model.ShapeKind, P, col []float64) {
	switch kind {
	case model.KindEllipse, model.KindRectangle, model.KindGlow, model.KindDisk:
		P[4] = math.Round(P[4]*100) / 100
	}
	for c := 0; c < 3; c++ {
		col[c] = float64(model.DecChan(model.EncByte(float32(col[c]))))
	}
	col[3] = float64(model.F2B(float32(col[3]))) / 255
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
