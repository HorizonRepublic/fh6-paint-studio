//go:build vulkan || allgpu

// Package vulkan is the cross-vendor GPU backend: a second implementation of
// backend.Backend (alongside internal/backend/cuda) that mirrors the CPU reference
// (internal/backend/cpu). It drives fh6vk.dll (built from internal/backend/vulkan/shim
// by scripts/build-vulkan.ps1) through golang.org/x/sys/windows + syscall — no cgo, so
// the Go build stays CGO_ENABLED=0 and the DLL is the only native artifact.
//
// Build the DLL first (scripts/build-vulkan.ps1), then build Go with -tags vulkan. The
// DLL is found via the OS loader search order (exe dir, CWD, PATH). Unlike CUDA this
// runs on AMD/Intel/NVIDIA (any Vulkan 1.2 GPU); CUDA stays the primary path on NVIDIA.
//
// Phase 1 scope: the core Backend (Evaluate/Apply/…). Optional capabilities
// (sampleBudgeter is wired; coarse/random/moment search + joint polish) land in later
// phases; until then the engine's host fallbacks run, exactly as on the CPU backend.
package vulkan

import (
	"fmt"
	"math"
	"runtime"
	"unsafe"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"

	"golang.org/x/sys/windows"
)

// candStride/resStride are the flat wire formats shared with shim.cpp (== shim.cu):
//
//	candidate: [kind, p0,p1,p2,p3,p4,p5, R,G,B,A]  (11 floats)
//	result:    [score, oR,oG,oB,oA]                (5 floats)
const (
	candStride = 11
	resStride  = 5
	maxCands   = 65536 // device scratch capacity; Evaluate chunks if exceeded (each chunk is a launch plus a sync, so a bigger one is fewer round-trips for ~4 MB more device scratch)
)

// maskRejected is a large positive ΔSSE sentinel: a mask candidate can never be the
// per-shape argmin (the engine only accepts score < 0), so it is effectively dropped.
const maskRejected = float32(3.0e38)

// Vulkan is the cross-vendor GPU-backed Backend. Device memory lives in the DLL; this
// keeps host copies of target/weight to satisfy Target()/Weight() and the host-side
// ErrorGrid. Not safe for concurrent use: the DLL holds a single device context with
// global scratch, so callers must serialize all method calls (the engine drives one
// backend from a single goroutine).
type Vulkan struct {
	w, h, gridSize int
	target, weight []float32

	candBuf []float32 // reused Evaluate/Apply staging buffer
	outBuf  []float32

	masksOn       bool // word atlas uploaded — bank words score/composite/polish on device
	dll           *windows.DLL
	procEval      *windows.Proc
	procApply     *windows.Proc
	procGrid      *windows.Proc
	procReadCanv  *windows.Proc
	procReset     *windows.Proc
	procFree      *windows.Proc
	procLastError *windows.Proc
	procSampleBud *windows.Proc
	// on-device search
	procSearchRand *windows.Proc
	procSearchMom  *windows.Proc
	procSetOrient  *windows.Proc
	procSetCoh     *windows.Proc
	procSetBound   *windows.Proc
	// joint-polish device primitives
	procGradients, procSetMasks,
	procSetProp, procPropOn, procPropGate, procRunProp, procPropMap, procPropDims,
	procPolSetup, procPolSTE, procPolOKLab, procPolFE, procPolLD, procPolSSIM, procPolEagle, procTermW, procKindGate, procGlowSwap, procRampGlow, procBigGlow, procAlphaGrid, procPolUpload, procPolFwd, procPolLoss, procPolBwd,
	procPolRdGrad, procPolRdRender, procPolHard, procPolSync, procPolFree *windows.Proc
}

var _ backend.Backend = (*Vulkan)(nil)

// New loads fh6vk.dll, initializes device buffers for a w*h image, and returns a ready
// Backend. weight may be nil (defaults to all-ones, matching cpu.New). The caller must
// Close to free device memory.
func New(target, weight []float32, w, h, gridSize int) (*Vulkan, error) {
	if gridSize < 1 {
		gridSize = 1
	}
	if weight == nil {
		weight = make([]float32, w*h)
		for i := range weight {
			weight[i] = 1
		}
	}
	dll, err := windows.LoadDLL("fh6vk.dll")
	if err != nil {
		return nil, fmt.Errorf("load fh6vk.dll (build it with scripts/build-vulkan.ps1): %w", err)
	}
	proc := func(name string) *windows.Proc {
		p, perr := dll.FindProc(name)
		if perr != nil && err == nil {
			err = perr
		}
		return p
	}
	g := &Vulkan{
		w: w, h: h, gridSize: gridSize,
		target: append([]float32(nil), target...), weight: append([]float32(nil), weight...),
		dll:             dll,
		procEval:        proc("fp_eval"),
		procApply:       proc("fp_apply"),
		procGrid:        proc("fp_error_grid"),
		procReadCanv:    proc("fp_read_canvas"),
		procReset:       proc("fp_reset"),
		procFree:        proc("fp_free"),
		procLastError:   proc("fp_last_error"),
		procSampleBud:   proc("fp_set_sample_budget"),
		procSearchRand:  proc("fp_search_random"),
		procSearchMom:   proc("fp_search_moment"),
		procSetOrient:   proc("fp_set_orient"),
		procSetCoh:      proc("fp_set_coherence"),
		procSetBound:    proc("fp_set_boundary_dist"),
		procPolSetup:    proc("fp_polish_setup"),
		procPolSTE:      proc("fp_set_polish_ste"),
		procPolUpload:   proc("fp_polish_upload"),
		procPolFwd:      proc("fp_polish_forward"),
		procPolLoss:     proc("fp_polish_loss"),
		procPolBwd:      proc("fp_polish_backward"),
		procPolRdGrad:   proc("fp_polish_read_grad"),
		procPolRdRender: proc("fp_polish_read_render"),
		procPolHard:     proc("fp_polish_hard_loss"),
		procPolSync:     proc("fp_polish_sync"),
		procPolFree:     proc("fp_polish_free"),
	}
	g.procPolOKLab, _ = dll.FindProc("fp_set_polish_oklab")   // optional: older DLLs lack it (engine falls back to SSE)
	g.procPolFE, _ = dll.FindProc("fp_set_polish_false_edge") // optional: false-edge additive polish term
	g.procPolLD, _ = dll.FindProc("fp_set_polish_lostdetail") // optional: lost-detail additive polish term
	g.procPolSSIM, _ = dll.FindProc("fp_set_polish_ssim")     // optional: SSIM additive polish term
	g.procPolEagle, _ = dll.FindProc("fp_set_polish_eagle")   // optional: EAGLE additive polish term
	g.procKindGate, _ = dll.FindProc("fp_set_kind_gate")      // optional: region-kinds per-pixel gate
	g.procGradients, _ = dll.FindProc("fp_set_gradients")     // optional: per-pixel-alpha eval for glow/disk
	g.procSetMasks, _ = dll.FindProc("fp_set_masks")          // optional: dictionary-word coverage atlas
	g.procGlowSwap, _ = dll.FindProc("fp_set_glow_swap")      // optional: deep-smooth glow swap
	g.procRampGlow, _ = dll.FindProc("fp_set_ramp_glow")      // optional: ramp-aware hotter glow swap
	g.procBigGlow, _ = dll.FindProc("fp_set_big_glow")        // optional: size-conditioned glow swap
	g.procAlphaGrid, _ = dll.FindProc("fp_set_alpha_grid")    // optional: analytic-alpha grid in the eval epilogue
	g.procTermW, _ = dll.FindProc("fp_set_term_weight")       // optional: region-weighted FE/EAGLE map
	g.procSetProp, _ = dll.FindProc("fp_set_proposer")        // optional: neural candidate proposer weights
	g.procPropOn, _ = dll.FindProc("fp_set_proposer_enabled") // optional: enable + progress + batch share
	g.procPropGate, _ = dll.FindProc("fp_set_proposer_gate")  // optional: learned gate instead of the region gate
	g.procRunProp, _ = dll.FindProc("fp_run_proposer")        // optional: refresh the proposal map
	g.procPropMap, _ = dll.FindProc("fp_proposer_map")        // test-only: read the map back
	g.procPropDims, _ = dll.FindProc("fp_proposer_dims")      // test-only: map dimensions
	if err != nil {
		g.Close()
		return nil, fmt.Errorf("resolve fh6vk.dll exports: %w", err)
	}
	procInit, perr := dll.FindProc("fp_init")
	if perr != nil {
		g.Close()
		return nil, fmt.Errorf("resolve fp_init: %w", perr)
	}
	ret, _, _ := procInit.Call(fptr(g.target), fptr(g.weight), uintptr(w), uintptr(h), uintptr(maxCands), uintptr(gridSize))
	runtime.KeepAlive(g.target)
	runtime.KeepAlive(g.weight)
	if ret != 0 {
		g.Close()
		return nil, fmt.Errorf("fp_init failed (code %d) — check GPU/Vulkan driver", ret)
	}
	g.uploadMasks()
	return g, nil
}

// SetProposer installs the trained candidate proposer (the blob written by export_weights.py), or
// clears it with nil. false = the DLL predates the feature, so the caller keeps the random search.
//
// The network only ever narrows WHICH candidates get scored; each one is still scored exactly by the
// same eval, so a weak or stale network costs wall-clock and cannot damage the result.
func (g *Vulkan) SetProposer(blob []byte) bool {
	if g.procSetProp == nil {
		return false
	}
	if len(blob) == 0 {
		r, _, _ := g.procSetProp.Call(0, 0)
		return r != 0
	}
	r, _, _ := g.procSetProp.Call(uintptr(unsafe.Pointer(&blob[0])), uintptr(len(blob)))
	runtime.KeepAlive(blob)
	return r != 0
}

// SetProposerGate switches the decision of WHERE proposals are used from the hand-made region gate
// to the network's own confidence head. Reports false on a shim that predates the head.
func (g *Vulkan) SetProposerGate(on bool, tau float32) bool {
	if g.procPropGate == nil {
		return false
	}
	v := uintptr(0)
	if on {
		v = 1
	}
	g.procPropGate.Call(v, uintptr(math.Float32bits(tau)))
	return true
}

// SetProposerEnabled turns the installed network on for the run. frac is the share of each candidate
// batch drawn from it; the rest stays uniform-random, so the search never loses the exploration that
// covers whatever the network is wrong about. jitter spreads the batch around each proposal -- the
// network emits a fixed set of modes per location, so without it every candidate that picks the same
// head is the same shape and a large batch carries only a handful of distinct proposals.
func (g *Vulkan) SetProposerEnabled(on bool, progress, frac, jitter float32) bool {
	if g.procPropOn == nil {
		return false
	}
	v := uintptr(0)
	if on {
		v = 1
	}
	g.procPropOn.Call(v, uintptr(math.Float32bits(progress)), uintptr(math.Float32bits(frac)),
		uintptr(math.Float32bits(jitter)))
	return true
}

// RunProposer refreshes the proposal map from the current canvas. The canvas moves slowly between
// adjacent shapes, so the caller refreshes every N steps rather than every step.
func (g *Vulkan) RunProposer(progress float32) bool {
	if g.procRunProp == nil {
		return false
	}
	r, _, _ := g.procRunProp.Call(uintptr(math.Float32bits(progress)))
	return r != 0
}

// ProposerMap reads the proposal map back. Test-only: the generator consumes it on the device.
func (g *Vulkan) ProposerMap() ([]float32, [4]int32, bool) {
	var dims [4]int32
	if g.procPropMap == nil || g.procPropDims == nil {
		return nil, dims, false
	}
	g.procPropDims.Call(uintptr(unsafe.Pointer(&dims[0])))
	n := int(dims[0]) * int(dims[1]) * int(dims[2]) * 8
	if n <= 0 {
		return nil, dims, false
	}
	out := make([]float32, n)
	r, _, _ := g.procPropMap.Call(uintptr(unsafe.Pointer(&out[0])), uintptr(n))
	runtime.KeepAlive(out)
	return out, dims, int(r) == n
}

func fptr(s []float32) uintptr {
	if len(s) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&s[0]))
}

func (g *Vulkan) Evaluate(cands []model.Candidate) ([]backend.EvalResult, error) {
	n := len(cands)
	out := make([]backend.EvalResult, n)
	if n == 0 {
		return out, nil
	}
	for lo := 0; lo < n; lo += maxCands {
		hi := lo + maxCands
		if hi > n {
			hi = n
		}
		g.evalChunk(cands[lo:hi], out[lo:hi])
	}
	if g.procLastError != nil {
		if r, _, _ := g.procLastError.Call(); r != 0 {
			return out, fmt.Errorf("vulkan Evaluate: device error %d (dispatch fault)", r)
		}
	}
	return out, nil
}

func (g *Vulkan) evalChunk(cands []model.Candidate, out []backend.EvalResult) {
	n := len(cands)
	if cap(g.candBuf) < n*candStride {
		g.candBuf = make([]float32, n*candStride)
	}
	g.candBuf = g.candBuf[:n*candStride]
	for i := range cands {
		packCand(cands[i], g.candBuf[i*candStride:])
	}
	if cap(g.outBuf) < n*resStride {
		g.outBuf = make([]float32, n*resStride)
	}
	g.outBuf = g.outBuf[:n*resStride]
	g.procEval.Call(fptr(g.candBuf), uintptr(n), fptr(g.outBuf))
	runtime.KeepAlive(g.candBuf)
	runtime.KeepAlive(g.outBuf)
	for i := 0; i < n; i++ {
		// Without the atlas a word has no coverage to score, so reject it fail-loud rather than
		// let the kernel's inside switch treat one as an ellipse and silently ship it.
		if cands[i].Kind >= model.KindMaskBase && !g.masksOn {
			out[i] = backend.EvalResult{Score: maskRejected}
			continue
		}
		b := g.outBuf[i*resStride:]
		out[i] = backend.EvalResult{
			Score: b[0],
			Color: model.RGBA{R: b[1], G: b[2], B: b[3], A: b[4]},
		}
	}
}

func (g *Vulkan) Apply(c model.Candidate) error {
	if cap(g.candBuf) < candStride {
		g.candBuf = make([]float32, candStride)
	}
	buf := g.candBuf[:candStride]
	packCand(c, buf)
	g.procApply.Call(fptr(buf))
	runtime.KeepAlive(buf)
	return nil
}

// ErrorGrid computes the gridSize×gridSize weighted-SSE error grid on the device
// (grid.comp), reading back only the gw*gh cell values. Mirrors cpu.ErrorGrid.
func (g *Vulkan) ErrorGrid() ([]float32, int, int, error) {
	grid := make([]float32, g.gridSize*g.gridSize)
	g.procGrid.Call(fptr(grid))
	runtime.KeepAlive(grid)
	return grid, g.gridSize, g.gridSize, nil
}

func (g *Vulkan) ReadCanvas(dst []float32) error {
	if need := g.w * g.h * 4; len(dst) < need {
		return fmt.Errorf("vulkan ReadCanvas: dst len %d < %d", len(dst), need)
	}
	g.procReadCanv.Call(fptr(dst))
	runtime.KeepAlive(dst)
	return nil
}

func (g *Vulkan) Reset(canvas []float32) error {
	if need := g.w * g.h * 4; len(canvas) < need {
		return fmt.Errorf("vulkan Reset: canvas len %d < %d", len(canvas), need)
	}
	g.procReset.Call(fptr(canvas))
	runtime.KeepAlive(canvas)
	return nil
}

func (g *Vulkan) Target() []float32 { return g.target }
func (g *Vulkan) Weight() []float32 { return g.weight }

// SetSampleBudget sets the device-side progressive-sampling pixel cap (mirrors
// cpu.SetSampleBudget). A budget >= image area makes scoring full-resolution.
func (g *Vulkan) SetSampleBudget(n int) {
	if g.procSampleBud != nil {
		g.procSampleBud.Call(uintptr(n))
	}
}

func (g *Vulkan) Close() error {
	if g.procFree != nil {
		g.procFree.Call()
	}
	if g.dll != nil {
		g.dll.Release()
		g.dll = nil
	}
	return nil
}

// --- joint-polish device primitives (mirror internal/engine/polish.go's heavy steps) ---
// The engine drives the iteration loop (Adam, tau anneal, best-hard, snap); these run the
// per-pixel forward/loss/backward on the GPU. The wire layout mirrors the shim's fp_polish_*
// API exactly (same as the CUDA backend). tau (a double) crosses the syscall by pointer.

func (g *Vulkan) PolishSupported() bool {
	return g.procPolSetup != nil && g.procPolUpload != nil && g.procPolFwd != nil &&
		g.procPolLoss != nil && g.procPolBwd != nil && g.procPolRdGrad != nil && g.procPolFree != nil
}

func (g *Vulkan) PolishSetup(base []float32, n int) {
	g.procPolSetup.Call(fptr(base), uintptr(n))
	runtime.KeepAlive(base)
}

func (g *Vulkan) PolishSetSTE(on bool) {
	if g.procPolSTE != nil {
		g.procPolSTE.Call(uintptr(b2i32(on)))
	}
}

// PolishSetOKLab switches the device polish loss/dcinit kernels to the perceptual OKLab
// colour metric; reports whether the DLL supports it (the engine falls back to plain SSE
// when false so the host/device objectives stay consistent).
func (g *Vulkan) PolishSetOKLab(on bool) bool {
	if g.procPolOKLab == nil {
		return false
	}
	g.procPolOKLab.Call(uintptr(b2i32(on)))
	return true
}

// PolishSetFalseEdge sets the false-edge additive polish loss λ on the device (loss, hard loss
// and the dC seed fold the term in; λ<=0 disables). Reports whether the DLL supports it — the
// engine routes a non-zero λ to the CPU polish when false. Call AFTER PolishSetup.
func (g *Vulkan) PolishSetFalseEdge(lambda float64) bool {
	if g.procPolFE == nil {
		return false
	}
	g.procPolFE.Call(uintptr(unsafe.Pointer(&lambda)))
	return true
}

// PolishSetLostDetail sets the lost-detail additive polish loss λ — the MIRROR of the false edge
// (structure the recon ERASED rather than invented; see engine/lostdetail.go). Same contract as
// PolishSetFalseEdge: folded into loss, hard loss and the dC seed; λ<=0 disables; call AFTER
// PolishSetup. Reports whether the DLL exports it — an older shim silently has no such term, so the
// engine must treat false as "the term is NOT active" rather than assume it applied.
func (g *Vulkan) PolishSetLostDetail(lambda float64) bool {
	if g.procPolLD == nil {
		return false
	}
	g.procPolLD.Call(uintptr(unsafe.Pointer(&lambda)))
	return true
}

// PolishSetSSIM sets the SSIM additive polish loss λ on the device — same contract as
// PolishSetFalseEdge (fold into loss/hard-loss/dC; λ<=0 disables; call AFTER PolishSetup).
func (g *Vulkan) PolishSetSSIM(lambda float64) bool {
	if g.procPolSSIM == nil {
		return false
	}
	g.procPolSSIM.Call(uintptr(unsafe.Pointer(&lambda)))
	return true
}

// PolishSetEagle sets the EAGLE additive polish loss λ on the device — same contract as
// PolishSetFalseEdge (fold into loss/hard-loss/dC; λ<=0 disables; call AFTER PolishSetup).
func (g *Vulkan) PolishSetEagle(lambda float64) bool {
	if g.procPolEagle == nil {
		return false
	}
	g.procPolEagle.Call(uintptr(unsafe.Pointer(&lambda)))
	return true
}

// PolishSetTermWeight uploads (nil clears) the per-pixel FE/EAGLE term-weight map - the
// region-weighted perceptual lambda (1-HardEdgeMap). Call AFTER PolishSetup, like the lambda setters.
func (g *Vulkan) PolishSetTermWeight(tw []float32) bool {
	if g.procTermW == nil {
		return false
	}
	if tw == nil {
		g.procTermW.Call(0)
		return true
	}
	g.procTermW.Call(uintptr(unsafe.Pointer(&tw[0])))
	return true
}

// SetKindGate uploads the per-pixel region-kinds gate for the on-device generators, or clears it
// with nil. false = the export is missing (older DLL) — the engine disables the gate for the run.
func (g *Vulkan) SetKindGate(hard []float32) bool {
	if g.procKindGate == nil {
		return false
	}
	if hard == nil {
		g.procKindGate.Call(0)
		return true
	}
	if len(hard) != g.w*g.h {
		return false
	}
	g.procKindGate.Call(fptr(hard))
	runtime.KeepAlive(hard)
	return true
}

// uploadMasks ships the bank's coverage atlas to the device so words can be scored, composited and
// polished there. Same layout as the CUDA path: the atlas is every word's coverage concatenated and
// meta is (offset,w,h) per word. Without it the backend has no words and says so via MasksOnDevice.
func (g *Vulkan) uploadMasks() {
	if g.procSetMasks == nil {
		return
	}
	bank := maskbank.All()
	if len(bank) == 0 {
		return
	}
	var total int
	for _, e := range bank {
		total += e.W * e.H
	}
	atlas := make([]float32, 0, total)
	meta := make([]int32, 0, len(bank)*3)
	for _, e := range bank {
		meta = append(meta, int32(len(atlas)), int32(e.W), int32(e.H))
		atlas = append(atlas, e.Cov...)
	}
	ret, _, _ := g.procSetMasks.Call(
		fptr(atlas), uintptr(len(atlas)),
		uintptr(unsafe.Pointer(&meta[0])), uintptr(len(bank)),
	)
	runtime.KeepAlive(atlas)
	runtime.KeepAlive(meta)
	if ret == 0 {
		g.masksOn = true
	}
}

// MasksOnDevice reports whether the word atlas made it to the device — the engine gates the glyph
// and shade pre-passes on this, and it is what tells a bank word apart from a rejected candidate.
func (g *Vulkan) MasksOnDevice() bool { return g.masksOn }

// SetGradients tells the eval kernel the batch may contain the native gradient kinds, which carry
// a per-pixel alpha. Off (the greedy's hard path), a glow scores as a flat ellipse — mirroring the
// CUDA warp/block split, so the on-device search picks the same shapes on both backends.
func (g *Vulkan) SetGradients(on bool) bool {
	if g.procGradients == nil {
		return false
	}
	g.procGradients.Call(uintptr(b2i32(on)))
	return true
}

// SetGlowSwap sets the deep-smooth glow-swap pair on the device generators (tau=prob=0 disables).
// Companion of SetKindGate; only meaningful while a gate map is live.
func (g *Vulkan) SetGlowSwap(tau, prob float32) bool {
	if g.procGlowSwap == nil {
		return false
	}
	tp := [2]float32{tau, prob}
	g.procGlowSwap.Call(fptr(tp[:]))
	runtime.KeepAlive(tp[:])
	return true
}

// SetRampGlow uploads the per-pixel smooth-gradient map (metric.RampMap) and the hot-glow triple
// {thresh, tau, prob}: where ramp[i] > thresh the deep-smooth glow swap runs at the hotter (tau, prob).
// nil clears it (the glow swap falls back to the global pair everywhere). Rides SetKindGate +
// SetGlowSwap; false = the DLL lacks the export (older build) — the engine keeps the plain glow swap.
func (g *Vulkan) SetRampGlow(ramp []float32, thresh, tau, prob float32) bool {
	if g.procRampGlow == nil {
		return false
	}
	if ramp == nil {
		g.procRampGlow.Call(0, 0)
		return true
	}
	if len(ramp) != g.w*g.h {
		return false
	}
	p := [3]float32{thresh, tau, prob}
	g.procRampGlow.Call(fptr(ramp), fptr(p[:]))
	runtime.KeepAlive(ramp)
	runtime.KeepAlive(p[:])
	return true
}

// SetBigGlow sets the size-conditioned glow swap: a candidate larger than tau*min(w,h) becomes a
// rimless glow with probability prob, independent of the hardness gate. allKinds extends it from
// ellipses to rects and triangles. prob 0 disables; false = the DLL lacks the export (older build).
func (g *Vulkan) SetBigGlow(tau, prob float32, allKinds bool, kind int32) bool {
	if g.procBigGlow == nil {
		return false
	}
	p := [4]float32{tau, prob, 0, float32(kind)}
	if allKinds {
		p[2] = 1
	}
	g.procBigGlow.Call(fptr(p[:]))
	runtime.KeepAlive(p[:])
	return true
}

// SetAlphaGrid installs (nil clears) the analytic-alpha grid in the eval epilogue: every grid
// alpha is re-solved for its optimal color and the ΔSSE-min (alpha, color) pair wins.
// Mirrors the CPU reference's SetAlphaGrid; errors when the loaded DLL predates the export.
func (g *Vulkan) SetAlphaGrid(vals []float32) error {
	if g.procAlphaGrid == nil {
		return fmt.Errorf("vulkan: DLL lacks fp_set_alpha_grid")
	}
	if len(vals) == 0 {
		g.procAlphaGrid.Call(0, 0)
		return nil
	}
	g.procAlphaGrid.Call(fptr(vals), uintptr(len(vals)))
	runtime.KeepAlive(vals)
	return nil
}

func (g *Vulkan) PolishSync() {
	if g.procPolSync != nil {
		g.procPolSync.Call()
	}
}

func (g *Vulkan) PolishUpload(P, col []float64, kinds, bbx []int32, boff []int64, belowTotal int64) {
	g.procPolUpload.Call(f64ptr(P), f64ptr(col), i32ptr(kinds), i32ptr(bbx), i64ptr(boff), uintptr(belowTotal))
	runtime.KeepAlive(P)
	runtime.KeepAlive(col)
	runtime.KeepAlive(kinds)
	runtime.KeepAlive(bbx)
	runtime.KeepAlive(boff)
}

func (g *Vulkan) PolishForward(tau float64, bbxHost []int32) {
	t := [1]float64{tau}
	g.procPolFwd.Call(i32ptr(bbxHost), f64ptr(t[:]))
	runtime.KeepAlive(bbxHost)
	runtime.KeepAlive(t[:])
}

func (g *Vulkan) PolishLoss() float64 {
	out := [1]float64{}
	g.procPolLoss.Call(f64ptr(out[:]))
	runtime.KeepAlive(out[:])
	return out[0]
}

func (g *Vulkan) PolishHardLoss(bbxHost []int32) (float64, bool) {
	if g.procPolHard == nil {
		return 0, false
	}
	out := [1]float64{}
	g.procPolHard.Call(i32ptr(bbxHost), f64ptr(out[:]))
	runtime.KeepAlive(bbxHost)
	runtime.KeepAlive(out[:])
	return out[0], true
}

func (g *Vulkan) PolishBackward(tau float64, bbxHost []int32) {
	t := [1]float64{tau}
	g.procPolBwd.Call(i32ptr(bbxHost), f64ptr(t[:]))
	runtime.KeepAlive(bbxHost)
	runtime.KeepAlive(t[:])
}

func (g *Vulkan) PolishReadGrad(dst []float64) {
	g.procPolRdGrad.Call(f64ptr(dst))
	runtime.KeepAlive(dst)
}

func (g *Vulkan) PolishReadRender(dst []float32) {
	if g.procPolRdRender != nil {
		g.procPolRdRender.Call(fptr(dst))
		runtime.KeepAlive(dst)
	}
}

func (g *Vulkan) PolishFree() {
	if g.procPolFree != nil {
		g.procPolFree.Call()
	}
}

func f64ptr(s []float64) uintptr {
	if len(s) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&s[0]))
}
func i32ptr(s []int32) uintptr {
	if len(s) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&s[0]))
}
func i64ptr(s []int64) uintptr {
	if len(s) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&s[0]))
}
func b2i32(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

// SetCoherence uploads the structure tensor's per-pixel coherence and the maximum aspect ratio at
// full coherence, so the on-device generator can decide how ELONGATED a candidate should be rather
// than applying one global aspect everywhere. A nil map (or cap <= 1) clears it. Returns false when
// the DLL predates the export, which lets the caller keep the host path instead of silently running
// without the prior.
func (g *Vulkan) SetCoherence(coh []float32, aspectCap float32) bool {
	if g.procSetCoh == nil {
		return false
	}
	p := [1]float32{aspectCap}
	if len(coh) != g.w*g.h || aspectCap <= 1 {
		g.procSetCoh.Call(0, 0)
		return true
	}
	g.procSetCoh.Call(fptr(coh), fptr(p[:]))
	runtime.KeepAlive(coh)
	runtime.KeepAlive(p[:])
	return true
}

// SetOrient uploads the per-pixel edge-orientation map (degrees, len w*h) for the on-device
// search's orientation-seeded angles. No-op if the length mismatches.
func (g *Vulkan) SetOrient(orient []float32) {
	if g.procSetOrient != nil && len(orient) == g.w*g.h {
		g.procSetOrient.Call(fptr(orient))
		runtime.KeepAlive(orient)
	}
}

// SetBoundaryDist uploads the per-pixel distance-to-boundary field (px, len w*h) for the
// on-device generator's boundary-aware radius cap. nil clears it.
func (g *Vulkan) SetBoundaryDist(dist []float32) {
	if g.procSetBound == nil {
		return
	}
	if dist == nil {
		g.procSetBound.Call(0)
		return
	}
	if len(dist) == g.w*g.h {
		g.procSetBound.Call(fptr(dist))
		runtime.KeepAlive(dist)
	}
}

// SearchRandom runs the random-candidate phase for one shape entirely on-device (generate ->
// score -> argmin in one submit) and returns the single best candidate with its RAW score.
// ok=false means the export is missing -> caller falls back to the host RandomShapes/pickBest
// path. The candidate stream uses the same wanghash RNG as the CUDA backend (bit-identical
// generation); the search is validated by end-to-end SSE, not golden-diff.
func (g *Vulkan) SearchRandom(seed int64, n int, kinds []model.ShapeKind, kindCDF []float32,
	maxR float32, allowAlpha bool, alphaMin, aspectMax float32, compact bool, shapeCount int,
	grid []float32, gw, gh int, boundPad, boundMix, canvasPad float32) (model.Candidate, float32, bool) {
	if g.procSearchRand == nil || len(kinds) == 0 || n < 1 {
		return model.Candidate{}, 0, false
	}
	cdf := make([]float32, len(grid))
	var tot float32
	for i, v := range grid {
		if v < 0 {
			v = 0
		}
		tot += v
		cdf[i] = tot
	}
	kf := make([]float32, len(kinds))
	for i, k := range kinds {
		kf[i] = float32(k)
	}
	ip := []int32{int32(n), int32(len(kinds)), int32(gw), int32(gh), b2i32(compact), int32(shapeCount), b2i32(allowAlpha)}
	fp := []float32{maxR, alphaMin, aspectMax, boundPad, boundMix, canvasPad}
	out := make([]float32, 12)
	g.procSearchRand.Call(
		uintptr(uint64(seed)),
		uintptr(unsafe.Pointer(&ip[0])),
		fptr(fp),
		fptr(kf),
		fptr(kindCDF),
		fptr(cdf),
		fptr(out),
	)
	runtime.KeepAlive(ip)
	runtime.KeepAlive(fp)
	runtime.KeepAlive(kf)
	runtime.KeepAlive(kindCDF)
	runtime.KeepAlive(cdf)
	runtime.KeepAlive(out)
	c := model.Candidate{
		Kind:  model.ShapeKind(int(out[1] + 0.5)),
		P:     [6]float32{out[2], out[3], out[4], out[5], out[6], out[7]},
		Color: model.RGBA{R: out[8], G: out[9], B: out[10], A: out[11]},
	}
	return c, out[0], true
}

// SearchMoment runs the on-device MOMENT-seeded search for one shape: fit `centers`
// covariance-ellipse seeds from the residual grid + a localised refine pool of `n` total
// candidates, score + argmin, return the single best. ok=false when the export is missing.
// Mirrors SearchRandom's wire format with K appended to ip; aspectMax is unused (per-seed).
func (g *Vulkan) SearchMoment(seed int64, n, centers int, kinds []model.ShapeKind, kindCDF []float32,
	maxR float32, allowAlpha bool, alphaMin float32, compact bool, shapeCount int,
	grid []float32, gw, gh int, boundPad, boundMix, canvasPad float32) (model.Candidate, float32, bool) {
	if g.procSearchMom == nil || len(kinds) == 0 || n < 1 || centers < 1 {
		return model.Candidate{}, 0, false
	}
	cdf := make([]float32, len(grid))
	var tot float32
	for i, v := range grid {
		if v < 0 {
			v = 0
		}
		tot += v
		cdf[i] = tot
	}
	kf := make([]float32, len(kinds))
	for i, k := range kinds {
		kf[i] = float32(k)
	}
	ip := []int32{int32(n), int32(len(kinds)), int32(gw), int32(gh), b2i32(compact), int32(shapeCount), b2i32(allowAlpha), int32(centers)}
	fp := []float32{maxR, alphaMin, 0, boundPad, boundMix, canvasPad}
	out := make([]float32, 12)
	g.procSearchMom.Call(
		uintptr(uint64(seed)),
		uintptr(unsafe.Pointer(&ip[0])),
		fptr(fp),
		fptr(kf),
		fptr(kindCDF),
		fptr(cdf),
		fptr(out),
	)
	runtime.KeepAlive(ip)
	runtime.KeepAlive(fp)
	runtime.KeepAlive(kf)
	runtime.KeepAlive(kindCDF)
	runtime.KeepAlive(cdf)
	runtime.KeepAlive(out)
	c := model.Candidate{
		Kind:  model.ShapeKind(int(out[1] + 0.5)),
		P:     [6]float32{out[2], out[3], out[4], out[5], out[6], out[7]},
		Color: model.RGBA{R: out[8], G: out[9], B: out[10], A: out[11]},
	}
	return c, out[0], true
}

// packCand writes a candidate into the 11-float wire format at dst[0:11].
func packCand(c model.Candidate, dst []float32) {
	dst[0] = float32(c.Kind)
	dst[1] = c.P[0]
	dst[2] = c.P[1]
	dst[3] = c.P[2]
	dst[4] = c.P[3]
	dst[5] = c.P[4]
	dst[6] = c.P[5]
	dst[7] = c.Color.R
	dst[8] = c.Color.G
	dst[9] = c.Color.B
	dst[10] = c.Color.A
}
