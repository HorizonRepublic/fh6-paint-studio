// Package vulkan is the cross-vendor GPU backend: a second implementation of
// backend.Backend (alongside internal/backend/cuda) that mirrors the CPU reference
// (internal/backend/cpu). It drives fh6vk.dll (built from internal/backend/vulkan/shim
// by scripts/build-vulkan.ps1) through the standard library's syscall package — no cgo, so
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
	"syscall"
	"unsafe"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"
)

// candStride/resStride are the flat wire formats shared with shim.cpp (== shim.cu):
//
//	candidate: [kind, p0,p1,p2,p3,p4,p5, R,G,B,A]  (11 floats)
//	result:    [score, oR,oG,oB,oA]                (5 floats)
const (
	candStride = 11
	resStride  = 5
	maxCands   = 65535 // device scratch capacity AND the eval chunk size; capped at the guaranteed compute-workgroup dispatch limit (fp_eval launches one workgroup per candidate), so a full chunk never overflows it on Intel iGPUs
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

	// Reused per-shape on-device search scratch. The backend is single-goroutine, so one set is
	// safe; a fresh w*h CDF every placement was ~3000 large allocations a run.
	searchCDF []float32
	searchKF  []float32
	searchOut []float32

	survScratch []float32 // batch placement: the survivor pool read back per search

	masksOn       bool // word atlas uploaded — bank words score/composite/polish on device
	dll           *syscall.DLL
	procEval      *syscall.Proc
	procApply     *syscall.Proc
	procGrid      *syscall.Proc
	procReadCanv  *syscall.Proc
	procReset     *syscall.Proc
	procFree      *syscall.Proc
	procLastError *syscall.Proc
	procSampleBud *syscall.Proc
	// on-device search
	procSearchRand *syscall.Proc
	procSearchMom  *syscall.Proc
	procSearchMut  *syscall.Proc
	procSetCoarse  *syscall.Proc
	procSetBatch   *syscall.Proc
	procSurvivors  *syscall.Proc
	procSetOrient  *syscall.Proc
	procSetCoh     *syscall.Proc
	procSetBound   *syscall.Proc
	// joint-polish device primitives
	procGradients, procSetMasks,
	procSetProp, procPropOn, procPropGate, procRunProp, procPropMap, procPropDims,
	procPolSetup, procPolSTE, procPolOKLab, procPolFE, procPolLD, procPolSSIM, procPolEagle, procTermW, procKindGate, procGlowSwap, procRampGlow, procBigGlow, procAlphaGrid, procPolUpload, procPolFwd, procPolLoss, procPolBwd,
	procPolRdGrad, procPolRdRender, procPolHard, procPolSync, procPolFree *syscall.Proc
	// device health + VRAM budget (optional exports; nil on older DLLs)
	procDevLost, procMemInfo, procPolMemNeed, procProfDump, procApplyBatch *syscall.Proc
}

var _ backend.Backend = (*Vulkan)(nil)

// New loads fh6vk.dll, initializes device buffers for a w*h image, and returns a ready
// Backend. weight may be nil (defaults to all-ones, matching cpu.New). The caller must
// Close to free device memory.
func New(target, weight []float32, w, h, gridSize int) (*Vulkan, error) {
	if gridSize < 1 {
		gridSize = 1
	}
	if gridSize > 255 {
		// grid.comp runs one workgroup per CELL, and 256 a side is 65536 — one past the
		// guaranteed 1-D dispatch ceiling Intel enforces exactly. The shipped resolutions are
		// 48-160; -grid is an expert knob, and the DLL clamps to the same value, so the two must
		// agree or ErrorGrid reads a tail the device never wrote.
		gridSize = 255
	}
	if weight == nil {
		weight = make([]float32, w*h)
		for i := range weight {
			weight[i] = 1
		}
	}
	dll, err := syscall.LoadDLL("fh6vk.dll")
	if err != nil {
		return nil, fmt.Errorf("load fh6vk.dll (build it with scripts/build-vulkan.ps1): %w", err)
	}
	proc := func(name string) *syscall.Proc {
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
	g.procSearchMut, _ = dll.FindProc("fp_search_mutate")     // optional: on-device hill climb (host rounds when absent)
	g.procSetCoarse, _ = dll.FindProc("fp_set_coarse")        // optional: coarse-to-fine search filter
	g.procSetBatch, _ = dll.FindProc("fp_set_batch")          // optional: batch placement survivor export
	g.procSurvivors, _ = dll.FindProc("fp_search_survivors")  // optional: ditto
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
	g.procDevLost, _ = dll.FindProc("fp_device_lost")         // optional: sticky TDR/device-loss flag
	g.procMemInfo, _ = dll.FindProc("fp_mem_info")            // optional: device-local heap budget/usage
	g.procPolMemNeed, _ = dll.FindProc("fp_polish_mem_need")  // optional: polish VRAM estimate
	g.procProfDump, _ = dll.FindProc("fp_prof_dump")          // optional: FH6VK_PROF GPU profile table
	g.procApplyBatch, _ = dll.FindProc("fp_apply_batch")      // optional: one-fence stack rerender
	if err != nil {
		g.Close()
		return nil, fmt.Errorf("resolve fh6vk.dll exports: %w", err)
	}
	procInit, perr := dll.FindProc("fp_init")
	if perr != nil {
		g.Close()
		return nil, fmt.Errorf("resolve fp_init: %w", perr)
	}
	ret, _, _ := procInit.Call(uintptr(unsafe.Pointer(&g.target[0])), uintptr(unsafe.Pointer(&g.weight[0])), uintptr(w), uintptr(h), uintptr(maxCands), uintptr(gridSize))
	runtime.KeepAlive(g.target)
	runtime.KeepAlive(g.weight)
	if ret != 0 {
		g.Close()
		if ret == 1061 {
			return nil, fmt.Errorf("this GPU cannot dispatch a %dx%d canvas (device workgroup ceiling) — lower the generation resolution", w, h)
		}
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
	g.procEval.Call(uintptr(unsafe.Pointer(&g.candBuf[0])), uintptr(n), uintptr(unsafe.Pointer(&g.outBuf[0])))
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
	g.procApply.Call(uintptr(unsafe.Pointer(&buf[0])))
	runtime.KeepAlive(buf)
	return nil
}

// ApplyBatch composites the candidates in order with one device fence per chunk instead of one
// per shape (a full-stack rerender used to be ~1000 fenced submits). Identical dispatches and
// ordering — the engine treats it as a drop-in for an Apply loop. false = DLL predates the export.
func (g *Vulkan) ApplyBatch(cands []model.Candidate) bool {
	if g.procApplyBatch == nil {
		return false
	}
	if len(cands) == 0 {
		return true
	}
	// Same reused staging as Apply/Evaluate — this runs per batch, not per run.
	need := len(cands) * candStride
	if cap(g.candBuf) < need {
		g.candBuf = make([]float32, need)
	}
	buf := g.candBuf[:need]
	for i, c := range cands {
		packCand(c, buf[i*candStride:(i+1)*candStride])
	}
	g.procApplyBatch.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(cands)))
	runtime.KeepAlive(buf)
	return true
}

// ErrorGrid computes the gridSize×gridSize weighted-SSE error grid on the device
// (grid.comp), reading back only the gw*gh cell values. Mirrors cpu.ErrorGrid.
func (g *Vulkan) ErrorGrid() ([]float32, int, int, error) {
	grid := make([]float32, g.gridSize*g.gridSize)
	g.procGrid.Call(uintptr(unsafe.Pointer(&grid[0])))
	runtime.KeepAlive(grid)
	return grid, g.gridSize, g.gridSize, nil
}

func (g *Vulkan) ReadCanvas(dst []float32) error {
	if need := g.w * g.h * 4; len(dst) < need {
		return fmt.Errorf("vulkan ReadCanvas: dst len %d < %d", len(dst), need)
	}
	g.procReadCanv.Call(uintptr(unsafe.Pointer(&dst[0])))
	runtime.KeepAlive(dst)
	return nil
}

func (g *Vulkan) Reset(canvas []float32) error {
	if need := g.w * g.h * 4; len(canvas) < need {
		return fmt.Errorf("vulkan Reset: canvas len %d < %d", len(canvas), need)
	}
	g.procReset.Call(uintptr(unsafe.Pointer(&canvas[0])))
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
	// FH6VK_PROF=1: dump the per-scope GPU profile before the state is freed. One applog line per
	// run — real per-phase GPU seconds + submit counts, the transparency wall-clock can't give.
	if s := g.ProfDump(); s != "" {
		applog.Printf("%s", s)
	}
	if g.procFree != nil {
		g.procFree.Call()
	}
	if g.dll != nil {
		g.dll.Release()
		g.dll = nil
	}
	return nil
}

// ProfDump returns the shim's built-in GPU profiler table, or "" when FH6VK_PROF is off or the
// DLL predates the export. Counters cover everything since this backend's fp_init.
func (g *Vulkan) ProfDump() string {
	if g.procProfDump == nil {
		return ""
	}
	buf := make([]byte, 4096)
	n, _, _ := g.procProfDump.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	runtime.KeepAlive(buf)
	if n == 0 {
		return ""
	}
	if int(n) > len(buf) {
		n = uintptr(len(buf) - 1)
	}
	return string(buf[:n])
}

// --- joint-polish device primitives (mirror internal/engine/polish.go's heavy steps) ---
// The engine drives the iteration loop (Adam, tau anneal, best-hard, snap); these run the
// per-pixel forward/loss/backward on the GPU. The wire layout mirrors the shim's fp_polish_*
// API exactly (same as the CUDA backend). tau (a double) crosses the syscall by pointer.

func (g *Vulkan) PolishSupported() bool {
	return g.procPolSetup != nil && g.procPolUpload != nil && g.procPolFwd != nil &&
		g.procPolLoss != nil && g.procPolBwd != nil && g.procPolRdGrad != nil && g.procPolFree != nil
}

func (g *Vulkan) PolishSetup(base []float32, n int) error {
	// Drain any STALE one-shot error first (the term setters record allocation failures nobody
	// reads) — otherwise a leftover code from an earlier call falsely fails THIS setup, and a
	// succeeded-on-device setup gets abandoned by the engine (state leaks until the next setup).
	if g.procLastError != nil {
		if e, _, _ := g.procLastError.Call(); e != 0 {
			applog.Printf("vulkan: draining stale device error %d before polish setup", e)
		}
	}
	g.procPolSetup.Call(uintptr(unsafe.Pointer(&base[0])), uintptr(n))
	runtime.KeepAlive(base)
	// The shim tears its polish state down on ANY allocation failure and every later fp_polish_*
	// call becomes a no-op — without this check the engine would run the whole Adam loop against
	// zeros and silently ship unpolished shapes (the "polish did nothing" failure on 4GB cards).
	if g.procLastError != nil {
		if e, _, _ := g.procLastError.Call(); e != 0 {
			return fmt.Errorf("polish setup failed on the device (code %d — usually out of VRAM)", e)
		}
	}
	return nil
}

// DeviceLost reports the shim's STICKY device-loss flag: true after any submit/fence failure
// (TDR, driver reset, OOM-killed context). Unlike fp_last_error it is never consumed, so the
// engine can poll it from its long loops and abort with an honest message. Old DLLs: false.
func (g *Vulkan) DeviceLost() bool {
	if g.procDevLost == nil {
		return false
	}
	r, _, _ := g.procDevLost.Call()
	return r != 0
}

// MemInfo returns the device-local heap's live budget/usage (VK_EXT_memory_budget) and total
// size in bytes. budget/usage are 0 when the driver lacks the extension — callers then fall
// back to a fraction of heap. ok=false means the DLL predates the export entirely.
func (g *Vulkan) MemInfo() (budget, usage, heap int64, ok bool) {
	if g.procMemInfo == nil {
		return 0, 0, 0, false
	}
	var out [3]int64
	g.procMemInfo.Call(uintptr(unsafe.Pointer(&out[0])))
	return out[0], out[1], out[2], true
}

// PolishMemNeed estimates the device-local bytes the polish would allocate for n shapes with
// belowTotal snapshot pixels and the given term bitmask (1=FE/LD, 2=SSIM, 4=EAGLE). The
// formula lives in the shim next to the actual allocations (single source of truth). 0 = old DLL.
func (g *Vulkan) PolishMemNeed(n int, belowTotal int64, terms int) int64 {
	if g.procPolMemNeed == nil {
		return 0
	}
	var out int64
	g.procPolMemNeed.Call(uintptr(n), uintptr(belowTotal), uintptr(terms), uintptr(unsafe.Pointer(&out)))
	return out
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
	g.procKindGate.Call(uintptr(unsafe.Pointer(&hard[0])))
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
		uintptr(unsafe.Pointer(&atlas[0])), uintptr(len(atlas)),
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
	g.procGlowSwap.Call(uintptr(unsafe.Pointer(&tp[0])))
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
	g.procRampGlow.Call(uintptr(unsafe.Pointer(&ramp[0])), uintptr(unsafe.Pointer(&p[0])))
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
	g.procBigGlow.Call(uintptr(unsafe.Pointer(&p[0])))
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
	g.procAlphaGrid.Call(uintptr(unsafe.Pointer(&vals[0])), uintptr(len(vals)))
	runtime.KeepAlive(vals)
	return nil
}

func (g *Vulkan) PolishSync() {
	if g.procPolSync != nil {
		g.procPolSync.Call()
	}
}

func (g *Vulkan) PolishUpload(P, col []float64, kinds, bbx []int32, boff []int64, belowTotal int64) {
	if len(P) == 0 || len(col) == 0 || len(kinds) == 0 || len(bbx) == 0 || len(boff) == 0 {
		return // a zero-shape upload has nothing to point at; the shim would read a null anyway
	}
	g.procPolUpload.Call(uintptr(unsafe.Pointer(&P[0])), uintptr(unsafe.Pointer(&col[0])),
		uintptr(unsafe.Pointer(&kinds[0])), uintptr(unsafe.Pointer(&bbx[0])),
		uintptr(unsafe.Pointer(&boff[0])), uintptr(belowTotal))
	runtime.KeepAlive(P)
	runtime.KeepAlive(col)
	runtime.KeepAlive(kinds)
	runtime.KeepAlive(bbx)
	runtime.KeepAlive(boff)
}

// The DLL is handed the ADDRESS of these buffers and writes into several of them, so every
// conversion below is written inline. Proc.Call is //go:uintptrescapes, and the compiler only
// honours that for a uintptr(unsafe.Pointer(x)) written SYNTACTICALLY in the argument list: a
// helper returning uintptr hides it, and the buffer then stays on the stack, where the runtime is
// free to move it out from under the call. Verified with -gcflags=-m — the inline form reports
// "moved to heap: t", the helper form reports nothing. bbxHost is ignored by the shim (the bbx
// lives on-device) but still travels as a pointer, so it gets the same treatment.
func (g *Vulkan) PolishForward(tau float64, bbxHost []int32) {
	t := [1]float64{tau}
	if len(bbxHost) == 0 {
		g.procPolFwd.Call(0, uintptr(unsafe.Pointer(&t[0])))
	} else {
		g.procPolFwd.Call(uintptr(unsafe.Pointer(&bbxHost[0])), uintptr(unsafe.Pointer(&t[0])))
	}
	runtime.KeepAlive(bbxHost)
	runtime.KeepAlive(t[:])
}

func (g *Vulkan) PolishLoss() float64 {
	out := [1]float64{}
	g.procPolLoss.Call(uintptr(unsafe.Pointer(&out[0])))
	runtime.KeepAlive(out[:])
	return out[0]
}

func (g *Vulkan) PolishHardLoss(bbxHost []int32) (float64, bool) {
	if g.procPolHard == nil {
		return 0, false
	}
	out := [1]float64{}
	if len(bbxHost) == 0 {
		g.procPolHard.Call(0, uintptr(unsafe.Pointer(&out[0])))
	} else {
		g.procPolHard.Call(uintptr(unsafe.Pointer(&bbxHost[0])), uintptr(unsafe.Pointer(&out[0])))
	}
	runtime.KeepAlive(bbxHost)
	runtime.KeepAlive(out[:])
	return out[0], true
}

func (g *Vulkan) PolishBackward(tau float64, bbxHost []int32) {
	t := [1]float64{tau}
	if len(bbxHost) == 0 {
		g.procPolBwd.Call(0, uintptr(unsafe.Pointer(&t[0])))
	} else {
		g.procPolBwd.Call(uintptr(unsafe.Pointer(&bbxHost[0])), uintptr(unsafe.Pointer(&t[0])))
	}
	runtime.KeepAlive(bbxHost)
	runtime.KeepAlive(t[:])
}

func (g *Vulkan) PolishReadGrad(dst []float64) {
	if len(dst) == 0 {
		return
	}
	g.procPolRdGrad.Call(uintptr(unsafe.Pointer(&dst[0])))
	runtime.KeepAlive(dst)
}

func (g *Vulkan) PolishReadRender(dst []float32) {
	if len(dst) == 0 {
		return
	}
	if g.procPolRdRender != nil {
		g.procPolRdRender.Call(uintptr(unsafe.Pointer(&dst[0])))
		runtime.KeepAlive(dst)
	}
}

func (g *Vulkan) PolishFree() {
	if g.procPolFree != nil {
		g.procPolFree.Call()
	}
}

// The fptr/f64ptr/i32ptr/i64ptr helpers that used to live here are GONE on purpose. Every
// uintptr(unsafe.Pointer(x)) handed to Proc.Call must be written in the argument list itself:
// that is the only form the //go:uintptrescapes pragma recognises, and the helper silently
// dropped every buffer back onto the stack. Do not reintroduce them.
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
	g.procSetCoh.Call(uintptr(unsafe.Pointer(&coh[0])), uintptr(unsafe.Pointer(&p[0])))
	runtime.KeepAlive(coh)
	runtime.KeepAlive(p[:])
	return true
}

// SetOrient uploads the per-pixel edge-orientation map (degrees, len w*h) for the on-device
// search's orientation-seeded angles. No-op if the length mismatches.
func (g *Vulkan) SetOrient(orient []float32) {
	if g.procSetOrient != nil && len(orient) == g.w*g.h {
		g.procSetOrient.Call(uintptr(unsafe.Pointer(&orient[0])))
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
		g.procSetBound.Call(uintptr(unsafe.Pointer(&dist[0])))
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
	if cap(g.searchCDF) < len(grid) {
		g.searchCDF = make([]float32, len(grid))
	}
	cdf := g.searchCDF[:len(grid)]
	var tot float32
	for i, v := range grid {
		if v < 0 {
			v = 0
		}
		tot += v
		cdf[i] = tot
	}
	if cap(g.searchKF) < len(kinds) {
		g.searchKF = make([]float32, len(kinds))
	}
	kf := g.searchKF[:len(kinds)]
	for i, k := range kinds {
		kf[i] = float32(k)
	}
	ip := []int32{int32(n), int32(len(kinds)), int32(gw), int32(gh), b2i32(compact), int32(shapeCount), b2i32(allowAlpha)}
	fp := []float32{maxR, alphaMin, aspectMax, boundPad, boundMix, canvasPad}
	if cap(g.searchOut) < 12 {
		g.searchOut = make([]float32, 12)
	}
	out := g.searchOut[:12]
	if len(kindCDF) == 0 || len(cdf) == 0 {
		return model.Candidate{}, 0, false // nothing to sample from; the shim would read a null
	}
	// Every one of these is written inline: see the comment above PolishForward. ip/fp/kf are
	// composite literals, so the helper form left them on the stack while the DLL held their
	// addresses, and `out` is a buffer the DLL WRITES.
	g.procSearchRand.Call(
		uintptr(uint64(seed)),
		uintptr(unsafe.Pointer(&ip[0])),
		uintptr(unsafe.Pointer(&fp[0])),
		uintptr(unsafe.Pointer(&kf[0])),
		uintptr(unsafe.Pointer(&kindCDF[0])),
		uintptr(unsafe.Pointer(&cdf[0])),
		uintptr(unsafe.Pointer(&out[0])),
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
	// Reuse the same scratch SearchRandom does. The moment search is the PRODUCT default (the
	// nextgen hybrid runs it below MomentDetailStart), so it was allocating a fresh w*h CDF, a
	// kinds slice and the out buffer on every placed shape — thousands of large allocations a run
	// on the hot path, while its sibling had reused buffers for exactly this reason.
	if cap(g.searchCDF) < len(grid) {
		g.searchCDF = make([]float32, len(grid))
	}
	cdf := g.searchCDF[:len(grid)]
	var tot float32
	for i, v := range grid {
		if v < 0 {
			v = 0
		}
		tot += v
		cdf[i] = tot
	}
	if cap(g.searchKF) < len(kinds) {
		g.searchKF = make([]float32, len(kinds))
	}
	kf := g.searchKF[:len(kinds)]
	for i, k := range kinds {
		kf[i] = float32(k)
	}
	ip := []int32{int32(n), int32(len(kinds)), int32(gw), int32(gh), b2i32(compact), int32(shapeCount), b2i32(allowAlpha), int32(centers)}
	fp := []float32{maxR, alphaMin, 0, boundPad, boundMix, canvasPad}
	if cap(g.searchOut) < 12 {
		g.searchOut = make([]float32, 12)
	}
	out := g.searchOut[:12]
	if len(kindCDF) == 0 || len(cdf) == 0 {
		return model.Candidate{}, 0, false
	}
	g.procSearchMom.Call(
		uintptr(uint64(seed)),
		uintptr(unsafe.Pointer(&ip[0])),
		uintptr(unsafe.Pointer(&fp[0])),
		uintptr(unsafe.Pointer(&kf[0])),
		uintptr(unsafe.Pointer(&kindCDF[0])),
		uintptr(unsafe.Pointer(&cdf[0])),
		uintptr(unsafe.Pointer(&out[0])),
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

// SearchMutate runs the WHOLE hill-climb mutation phase for one shape on-device (see the engine's
// mutateSearcher contract): io_best carries the incumbent in the best[12] wire format and comes
// back holding the final winner. ok=false on a missing export or a device fault — the caller then
// runs the host rounds instead of trusting garbage.
func (g *Vulkan) SearchMutate(seed int64, incumbent model.Candidate, score float32, rounds, perRound int,
	moveStep, radiusStep float32, allowAlpha bool, alphaMin float32,
	compact bool, shapeCount int, canvasPad float32) (model.Candidate, float32, bool) {
	if g.procSearchMut == nil || rounds < 1 || perRound < 1 {
		return model.Candidate{}, 0, false
	}
	// Drain a stale one-shot error first: an unread code left by an unrelated earlier call (term
	// setters don't get their errors read) would fail THIS check, and the engine reads ok=false as
	// "old DLL" and disables the device hill climb for the REST of the run — a silent RNG change.
	if g.procLastError != nil {
		if e, _, _ := g.procLastError.Call(); e != 0 {
			applog.Printf("vulkan: draining stale device error %d before mutate search", e)
		}
	}
	ip := []int32{int32(perRound), int32(rounds), b2i32(compact), int32(shapeCount), b2i32(allowAlpha)}
	fp := []float32{moveStep, radiusStep, alphaMin, canvasPad}
	if cap(g.searchOut) < 12 {
		g.searchOut = make([]float32, 12)
	}
	io := g.searchOut[:12]
	io[0] = score
	io[1] = float32(incumbent.Kind)
	io[2], io[3], io[4], io[5], io[6], io[7] = incumbent.P[0], incumbent.P[1], incumbent.P[2], incumbent.P[3], incumbent.P[4], incumbent.P[5]
	io[8], io[9], io[10], io[11] = incumbent.Color.R, incumbent.Color.G, incumbent.Color.B, incumbent.Color.A
	g.procSearchMut.Call(
		uintptr(uint64(seed)),
		uintptr(unsafe.Pointer(&ip[0])),
		uintptr(unsafe.Pointer(&fp[0])),
		uintptr(unsafe.Pointer(&io[0])),
	)
	runtime.KeepAlive(ip)
	runtime.KeepAlive(fp)
	runtime.KeepAlive(io)
	if g.procLastError != nil {
		if e, _, _ := g.procLastError.Call(); e != 0 {
			return model.Candidate{}, 0, false
		}
	}
	// The device applies the same strict-improvement rule as the host loop, so the returned score
	// can only be <= the incumbent's. Anything else is a fault surfacing as data.
	if !(io[0] <= score) {
		return model.Candidate{}, 0, false
	}
	c := model.Candidate{
		Kind:  model.ShapeKind(int(io[1] + 0.5)),
		P:     [6]float32{io[2], io[3], io[4], io[5], io[6], io[7]},
		Color: model.RGBA{R: io[8], G: io[9], B: io[10], A: io[11]},
	}
	return c, io[0], true
}

// SetCoarse configures the coarse-to-fine filter for both on-device searches (see fp_set_coarse).
// A DLL without the export leaves the single-pass behaviour — the engine calls this
// unconditionally per run, so the silent degradation is exactly the pre-port state.
func (g *Vulkan) SetCoarse(enable bool, budget, kpart int) {
	if g.procSetCoarse == nil {
		return
	}
	g.procSetCoarse.Call(uintptr(b2i32(enable)), uintptr(int32(budget)), uintptr(int32(kpart)))
}

// SetCoarseFP16 is accepted for interface parity and does nothing: the FP32 coarse filter is the
// win (the CUDA fp16 half needed a separate eval shader and bought a fraction of it).
func (g *Vulkan) SetCoarseFP16(bool) {}

// SetBatch arms the survivor export so SearchSurvivors has something to return. Off by default:
// it appends three buffer copies to every search submit. A DLL without the export leaves the
// engine's batch placement disabled (SearchSurvivors then returns 0).
func (g *Vulkan) SetBatch(on bool) {
	if g.procSetBatch == nil {
		return
	}
	g.procSetBatch.Call(uintptr(b2i32(on)))
}

// SearchSurvivors returns the pool the last on-device search re-scored at the FULL sample budget:
// the coarse filter's kpart survivors, with the device's selection-adjusted score in adj and the
// raw ΔSSE in raw. Batch placement ranks by adj (matching the argmin's own choice) and gates on
// raw. Returns 0 when the pool does not exist — an old DLL, the coarse filter off, or a candidate
// batch too small to trigger it — and the caller then places one shape as before.
func (g *Vulkan) SearchSurvivors(cands []model.Candidate, raw, adj []float32) int {
	if g.procSurvivors == nil {
		return 0
	}
	k := len(cands)
	if k > len(raw) {
		k = len(raw)
	}
	if k > len(adj) {
		k = len(adj)
	}
	if k < 1 {
		return 0
	}
	need := k * 17
	if cap(g.survScratch) < need {
		g.survScratch = make([]float32, need)
	}
	buf := g.survScratch[:need]
	n, _, _ := g.procSurvivors.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(int32(need)))
	runtime.KeepAlive(buf)
	got := int(int32(n))
	if got < 1 || got > k {
		return 0
	}
	// Device block layout: [got adj][got*11 candidate][got*5 eval].
	cb := buf[got:]
	eb := buf[got*12:]
	for i := 0; i < got; i++ {
		adj[i] = buf[i]
		c := cb[i*11:]
		e := eb[i*5:]
		raw[i] = e[0]
		cands[i] = model.Candidate{
			Kind:  model.ShapeKind(int(c[0] + 0.5)),
			P:     [6]float32{c[1], c[2], c[3], c[4], c[5], c[6]},
			Color: model.RGBA{R: e[1], G: e[2], B: e[3], A: e[4]},
		}
	}
	return got
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
