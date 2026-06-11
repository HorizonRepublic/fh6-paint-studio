//go:build cuda || allgpu

// Package cuda is the GPU backend: a second implementation of backend.Backend
// that mirrors the CPU reference (internal/backend/cpu). It drives fh6cuda.dll
// (built from shim.cu by nvcc) through golang.org/x/sys/windows + syscall — no
// cgo, so the Go build stays CGO_ENABLED=0 and the DLL is the only native artifact.
//
// Build the DLL first (see scripts/build-cuda.ps1), then build Go with -tags cuda. The
// DLL is found via the OS loader search order (exe dir, CWD, PATH).
package cuda

import (
	"fmt"
	"runtime"
	"unsafe"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"

	"golang.org/x/sys/windows"
)

// candStride/resStride are the flat wire formats shared with shim.cu:
//
//	candidate: [kind, p0,p1,p2,p3,p4,p5, R,G,B,A]  (11 floats)
//	result:    [score, oR,oG,oB,oA]                (5 floats)
const (
	candStride = 11
	resStride  = 5
	maxCands   = 16384 // device scratch capacity; Evaluate chunks if exceeded
)

// CUDA is the GPU-backed Backend. It owns no device memory directly — the DLL
// does — but keeps host copies of target/weight to satisfy Target()/Weight().
//
// Not safe for concurrent use: the DLL holds a single device context with global
// scratch, so callers must serialize all method calls (the engine drives one backend
// from a single goroutine).
type CUDA struct {
	w, h, gridSize int
	target, weight []float32

	candBuf []float32 // reused Evaluate/Apply staging buffer
	outBuf  []float32

	dll            *windows.DLL
	procEval       *windows.Proc
	procApply      *windows.Proc
	procGrid       *windows.Proc
	procReadCanv   *windows.Proc
	procReset      *windows.Proc
	procFree       *windows.Proc
	procSampleBud  *windows.Proc // optional: fp_set_sample_budget (nil on older DLLs)
	procLastError  *windows.Proc // optional: fp_last_error (surface a device fault; nil on older DLLs)
	procWarpEval   *windows.Proc // optional: fp_set_warp_eval
	procGradients  *windows.Proc // optional: fp_set_gradients (route gradient kinds to the block eval kernel)
	procCoarse     *windows.Proc // optional: fp_set_coarse (coarse-to-fine search)
	procCoarseFP16 *windows.Proc // optional: fp_set_coarse_fp16 (FP16 coarse filter)
	procSearchRand *windows.Proc // optional: fp_search_random (on-device search)
	procSearchMom  *windows.Proc // optional: fp_search_moment (on-device moment-seeded search)
	procSetOrient  *windows.Proc // optional: fp_set_orient
	procSetBound   *windows.Proc // optional: fp_set_boundary_dist (boundary-aware radius)
	procPolSTE     *windows.Proc // optional: fp_set_polish_ste (straight-through coverage)
	procPolOKLab   *windows.Proc // optional: fp_set_polish_oklab (perceptual OKLab polish loss)
	procPolFE      *windows.Proc // optional: fp_set_polish_false_edge (false-edge additive polish loss term)
	procPolSSIM    *windows.Proc // optional: fp_set_polish_ssim (SSIM additive polish loss term)
	procPolSync    *windows.Proc // optional: fp_polish_sync (cudaDeviceSynchronize, for phase profiling)
	procPolHard    *windows.Proc // optional: fp_polish_hard_loss (GPU best-hard render; nil -> CPU fallback)
	procSetMasks   *windows.Proc // optional: fp_set_masks (dictionary-mask atlas; nil on older DLLs)
	masksOn        bool          // atlas uploaded — mask-word candidates evaluate on device
	gradOn         bool          // shadow of fp_set_gradients (block-kernel routing)
	// optional: joint-polish device primitives (nil on older DLLs)
	procPolSetup, procPolUpload, procPolFwd, procPolLoss, procPolBwd, procPolRdGrad, procPolRdRender, procPolFree *windows.Proc
}

var _ backend.Backend = (*CUDA)(nil)

// New loads fh6cuda.dll, initializes device buffers for a w*h image, and
// returns a ready Backend. weight may be nil (defaults to all-ones, matching
// cpu.New). The caller must Close to free device memory.
func New(target, weight []float32, w, h, gridSize int) (*CUDA, error) {
	if gridSize < 1 {
		gridSize = 1
	}
	if weight == nil {
		weight = make([]float32, w*h)
		for i := range weight {
			weight[i] = 1
		}
	}
	dll, err := windows.LoadDLL("fh6cuda.dll")
	if err != nil {
		return nil, fmt.Errorf("load fh6cuda.dll (build it with scripts/build-cuda.ps1): %w", err)
	}
	proc := func(name string) *windows.Proc {
		p, perr := dll.FindProc(name)
		if perr != nil && err == nil {
			err = perr
		}
		return p
	}
	g := &CUDA{
		w: w, h: h, gridSize: gridSize,
		target: target, weight: weight,
		dll:          dll,
		procEval:     proc("fp_eval"),
		procApply:    proc("fp_apply"),
		procGrid:     proc("fp_error_grid"),
		procReadCanv: proc("fp_read_canvas"),
		procReset:    proc("fp_reset"),
		procFree:     proc("fp_free"),
	}
	if err != nil {
		g.Close() // release the loaded DLL handle
		return nil, fmt.Errorf("resolve fh6cuda.dll exports: %w", err)
	}
	// Optional export (added for runtime-configurable progressive sampling). Resolve
	// it WITHOUT failing New, so a DLL built before this export still loads — then
	// SetSampleBudget is a no-op and scoring uses the kernel's built-in 4000 default.
	if p, perr := dll.FindProc("fp_set_sample_budget"); perr == nil {
		g.procSampleBud = p
	}
	if p, perr := dll.FindProc("fp_last_error"); perr == nil {
		g.procLastError = p
	}
	// On-device search exports (build "B1"). Optional-resolved so a DLL built before
	// them still loads; the engine falls back to the host gen/pick path if absent.
	if p, perr := dll.FindProc("fp_search_random"); perr == nil {
		g.procSearchRand = p
	}
	if p, perr := dll.FindProc("fp_search_moment"); perr == nil {
		g.procSearchMom = p
	}
	if p, perr := dll.FindProc("fp_set_orient"); perr == nil {
		g.procSetOrient = p
	}
	if p, perr := dll.FindProc("fp_set_boundary_dist"); perr == nil {
		g.procSetBound = p
	}
	// Joint-polish device primitives (optional; nil on older DLLs -> engine uses CPU polish).
	g.procPolSetup, _ = dll.FindProc("fp_polish_setup")
	g.procPolUpload, _ = dll.FindProc("fp_polish_upload")
	g.procPolFwd, _ = dll.FindProc("fp_polish_forward")
	g.procPolLoss, _ = dll.FindProc("fp_polish_loss")
	g.procPolBwd, _ = dll.FindProc("fp_polish_backward")
	g.procPolRdGrad, _ = dll.FindProc("fp_polish_read_grad")
	g.procPolRdRender, _ = dll.FindProc("fp_polish_read_render")
	g.procPolFree, _ = dll.FindProc("fp_polish_free")
	g.procPolSync, _ = dll.FindProc("fp_polish_sync")
	g.procPolHard, _ = dll.FindProc("fp_polish_hard_loss")
	g.procPolSTE, _ = dll.FindProc("fp_set_polish_ste")
	g.procPolOKLab, _ = dll.FindProc("fp_set_polish_oklab")
	g.procPolFE, _ = dll.FindProc("fp_set_polish_false_edge")
	g.procPolSSIM, _ = dll.FindProc("fp_set_polish_ssim")
	g.procWarpEval, _ = dll.FindProc("fp_set_warp_eval")
	g.procGradients, _ = dll.FindProc("fp_set_gradients")
	g.procCoarse, _ = dll.FindProc("fp_set_coarse")
	g.procCoarseFP16, _ = dll.FindProc("fp_set_coarse_fp16")
	g.procSetMasks, _ = dll.FindProc("fp_set_masks")
	initProc, err := dll.FindProc("fp_init")
	if err != nil {
		g.Close()
		return nil, err
	}
	ret, _, _ := initProc.Call(
		fptr(target), fptr(weight),
		uintptr(w), uintptr(h), uintptr(maxCands), uintptr(gridSize),
	)
	if ret != 0 {
		g.Close() // fp_free releases any partial device allocations, then the DLL handle
		return nil, fmt.Errorf("fp_init failed (code %d) — check GPU/CUDA", ret)
	}
	g.uploadMasks()
	return g, nil
}

// uploadMasks ships the embedded mask bank to the device as one coverage atlas, enabling mask-word
// candidates in Evaluate/Apply (kind = KindMaskBase+i -> atlas slot i, in registration order). On a
// DLL without the export the bank stays host-only and mask candidates keep the reject sentinel.
func (g *CUDA) uploadMasks() {
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

// fptr returns the address of a float32 slice's backing array as a uintptr for
// syscall. The slice must be non-empty and stay referenced across the Call.
func fptr(s []float32) uintptr {
	if len(s) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&s[0]))
}

func (g *CUDA) Evaluate(cands []model.Candidate) ([]backend.EvalResult, error) {
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
	// Surface a device fault (kernel launch/exec error) instead of returning silently-garbage scores.
	if g.procLastError != nil {
		if r, _, _ := g.procLastError.Call(); r != 0 {
			return out, fmt.Errorf("cuda Evaluate: device error %d (kernel launch/exec fault)", r)
		}
	}
	return out, nil
}

func (g *CUDA) evalChunk(cands []model.Candidate, out []backend.EvalResult) {
	n := len(cands)
	if cap(g.candBuf) < n*candStride {
		g.candBuf = make([]float32, n*candStride)
	}
	g.candBuf = g.candBuf[:n*candStride]
	hasMask := false
	for i := range cands {
		packCand(cands[i], g.candBuf[i*candStride:])
		if cands[i].Kind >= model.KindMaskBase {
			hasMask = true
		}
	}
	if cap(g.outBuf) < n*resStride {
		g.outBuf = make([]float32, n*resStride)
	}
	g.outBuf = g.outBuf[:n*resStride]
	// Mask candidates need the block kernel (only it carries the per-pixel-alpha branch). Route this
	// chunk there and restore, so pure-hard batches keep the fast warp path.
	if hasMask && g.masksOn && !g.gradOn && g.procGradients != nil {
		g.procGradients.Call(1)
		defer g.procGradients.Call(0)
	}
	g.procEval.Call(fptr(g.candBuf), uintptr(n), fptr(g.outBuf))
	runtime.KeepAlive(g.candBuf)
	runtime.KeepAlive(g.outBuf)
	for i := 0; i < n; i++ {
		// Without the atlas (older DLL) the kernel's inside/bbox switches would silently mis-score a
		// mask as an ellipse — reject fail-loud so one can never be selected.
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

// maskRejected is a large positive ΔSSE sentinel: a mask candidate can never be the
// per-shape argmin (the engine only accepts a score < 0), so it is effectively dropped.
const maskRejected = float32(3.0e38)

func (g *CUDA) Apply(c model.Candidate) error {
	if cap(g.candBuf) < candStride {
		g.candBuf = make([]float32, candStride)
	}
	buf := g.candBuf[:candStride]
	packCand(c, buf)
	g.procApply.Call(fptr(buf))
	runtime.KeepAlive(buf)
	return nil
}

func (g *CUDA) ErrorGrid() ([]float32, int, int, error) {
	grid := make([]float32, g.gridSize*g.gridSize)
	g.procGrid.Call(fptr(grid))
	runtime.KeepAlive(grid)
	return grid, g.gridSize, g.gridSize, nil
}

func (g *CUDA) ReadCanvas(dst []float32) error {
	if need := g.w * g.h * 4; len(dst) < need {
		return fmt.Errorf("cuda ReadCanvas: dst len %d < %d (the DLL writes %dx%dx4 floats)", len(dst), need, g.w, g.h)
	}
	g.procReadCanv.Call(fptr(dst))
	runtime.KeepAlive(dst)
	return nil
}

func (g *CUDA) Reset(canvas []float32) error {
	if need := g.w * g.h * 4; len(canvas) < need {
		return fmt.Errorf("cuda Reset: canvas len %d < %d (the DLL reads %dx%dx4 floats)", len(canvas), need, g.w, g.h)
	}
	g.procReset.Call(fptr(canvas))
	runtime.KeepAlive(canvas)
	return nil
}

func (g *CUDA) Target() []float32 { return g.target }
func (g *CUDA) Weight() []float32 { return g.weight }

// SetSampleBudget sets the device-side progressive-sampling pixel cap (mirrors
// cpu.SetSampleBudget). No-op if the loaded DLL predates the fp_set_sample_budget
// export. A budget >= image area makes scoring full-resolution.
func (g *CUDA) SetSampleBudget(n int) {
	if g.procSampleBud != nil {
		g.procSampleBud.Call(uintptr(n))
	}
}

// SetOrient uploads the per-pixel edge-orientation map (len w*h, degrees) to the
// device once, for the on-device search's orientation-seeded angles. No-op if the
// DLL predates fp_set_orient or the length mismatches.
func (g *CUDA) SetOrient(orient []float32) {
	if g.procSetOrient != nil && len(orient) == g.w*g.h {
		g.procSetOrient.Call(fptr(orient))
		runtime.KeepAlive(orient)
	}
}

// SetBoundaryDist uploads the per-pixel distance-to-boundary field (len w*h, px) to the
// device once, for boundary-aware radius capping in the on-device generator. nil clears
// it. No-op if the DLL predates fp_set_boundary_dist or the length mismatches.
func (g *CUDA) SetBoundaryDist(dist []float32) {
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

// SearchRandom runs the random-candidate phase for one shape entirely on-device
// (generate -> score -> argmin) and returns the single best candidate (geometry +
// backend-computed optimal color) with its RAW score. ok=false means the DLL lacks
// the export — the caller must fall back to the host RandomShapes/pickBest path.
//
// grid is the current error grid (gw*gh); a cumulative CDF is built here and uploaded
// so the device samples centers with the same importance bias as the host sampler.
// boundPad/boundMix carry the boundary-aware radius cap for THIS shape (boundMix=0 ->
// disabled); the dist field itself is uploaded once via SetBoundaryDist. Scalars cross
// the syscall via memory (ip/fp slices) to dodge the Win64 float-arg (XMM) ABI that
// windows.Proc.Call cannot satisfy.
func (g *CUDA) SearchRandom(seed int64, n int, kinds []model.ShapeKind, kindCDF []float32,
	maxR float32, allowAlpha bool, alphaMin, aspectMax float32, compact bool, shapeCount int,
	grid []float32, gw, gh int, boundPad, boundMix, canvasPad float32) (model.Candidate, float32, bool) {
	if g.procSearchRand == nil || len(kinds) == 0 || n < 1 {
		return model.Candidate{}, 0, false
	}
	// Cumulative error-grid CDF (mirrors engine.NewErrorSampler; float32 is enough
	// for the device binary search — the total is passed implicitly as cdf[last]).
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
// candidates, score + argmin, return the single best. ok=false when the DLL predates
// fp_search_moment (caller falls back to the host moment pool). aspectMax is unused here —
// the per-candidate aspect comes from each seed's fitted axes. Mirrors SearchRandom's wire
// format with K appended to ip; scalars cross the syscall via the ip/fp slices.
func (g *CUDA) SearchMoment(seed int64, n, centers int, kinds []model.ShapeKind, kindCDF []float32,
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

func b2i32(b bool) int32 {
	if b {
		return 1
	}
	return 0
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

// --- joint-polish device primitives (mirror internal/engine/polish.go's heavy steps) ---
// The engine drives the iteration loop (Adam, tau anneal, best-hard, snap) and computes
// each shape's expanded bbox + below-buffer offsets; these methods do the per-pixel
// forward/loss/backward on the GPU. tau (a double) is passed by pointer because the Go
// syscall path cannot load XMM registers (the Win64 ABI slot for float/double scalars).

// PolishSupported reports whether the loaded DLL exposes the polish API.
func (g *CUDA) PolishSupported() bool {
	return g.procPolSetup != nil && g.procPolUpload != nil && g.procPolFwd != nil &&
		g.procPolLoss != nil && g.procPolBwd != nil && g.procPolRdGrad != nil && g.procPolFree != nil
}

func (g *CUDA) PolishSetup(base []float32, n int) {
	g.procPolSetup.Call(fptr(base), uintptr(n))
	runtime.KeepAlive(base)
}

// PolishSetSTE toggles straight-through (hard forward) coverage in the device polish
// kernels. No-op on DLLs predating fp_set_polish_ste (silently runs soft polish).
// NOTE: the name MUST match the engine.PolishAccel interface method exactly, else *CUDA
// silently fails the be.(PolishAccel) assertion and the engine falls back to CPU polish.
func (g *CUDA) PolishSetSTE(on bool) {
	if g.procPolSTE != nil {
		g.procPolSTE.Call(uintptr(b2i32(on)))
	}
}

// PolishSetOKLab switches the device polish loss/dcinit/hard kernels to the perceptual
// OKLab colour metric. Reports whether the DLL supports it (the engine falls back to the
// plain SSE loss when false, keeping host and device objectives consistent).
func (g *CUDA) PolishSetOKLab(on bool) bool {
	if g.procPolOKLab == nil {
		return false
	}
	g.procPolOKLab.Call(uintptr(b2i32(on)))
	return true
}

// PolishSetFalseEdge sets the false-edge additive polish loss λ on the device (loss, hard loss
// and the dC seed all fold the term in; λ<=0 disables). Reports whether the DLL supports it —
// the engine routes a non-zero λ to the CPU polish when false. Call AFTER PolishSetup (the
// device computes the target-luma plane against the current canvas size).
func (g *CUDA) PolishSetFalseEdge(lambda float64) bool {
	if g.procPolFE == nil {
		return false
	}
	g.procPolFE.Call(uintptr(unsafe.Pointer(&lambda)))
	return true
}

// PolishSetSSIM sets the SSIM additive polish loss λ on the device — same contract as
// PolishSetFalseEdge (fold into loss/hard-loss/dC; λ<=0 disables; call AFTER PolishSetup).
func (g *CUDA) PolishSetSSIM(lambda float64) bool {
	if g.procPolSSIM == nil {
		return false
	}
	g.procPolSSIM.Call(uintptr(unsafe.Pointer(&lambda)))
	return true
}

// PolishSync blocks until all queued polish kernels finish (cudaDeviceSynchronize) so the
// host loop can attribute async GPU time to the right phase. No-op on older DLLs.
func (g *CUDA) PolishSync() {
	if g.procPolSync != nil {
		g.procPolSync.Call()
	}
}

// PolishUpload sends the current per-shape params + bbox/below layout to the device.
// P is n*6 (geometry), col is n*4 (RGBA), kinds is n, bbx is n*4, boff is n (below
// float-offset prefix sum), belowTotal is the total below-buffer float count.
func (g *CUDA) PolishUpload(P, col []float64, kinds, bbx []int32, boff []int64, belowTotal int64) {
	g.procPolUpload.Call(f64ptr(P), f64ptr(col), i32ptr(kinds), i32ptr(bbx), i64ptr(boff), uintptr(belowTotal))
	runtime.KeepAlive(P)
	runtime.KeepAlive(col)
	runtime.KeepAlive(kinds)
	runtime.KeepAlive(bbx)
	runtime.KeepAlive(boff)
}

// PolishForward composites all shapes soft over the base into the device render.
// bbxHost is the same bbox array (host copy) used to size each shape's launch grid.
func (g *CUDA) PolishForward(tau float64, bbxHost []int32) {
	t := [1]float64{tau}
	g.procPolFwd.Call(i32ptr(bbxHost), f64ptr(t[:]))
	runtime.KeepAlive(bbxHost)
	runtime.KeepAlive(t[:])
}

func (g *CUDA) PolishLoss() float64 {
	out := [1]float64{}
	g.procPolLoss.Call(f64ptr(out[:]))
	runtime.KeepAlive(out[:])
	return out[0]
}

// PolishHardLoss renders all shapes with HARD coverage (the shipped deliverable) on the
// device and returns the weighted SSE vs target — the GPU port of engine.polishHardLoss
// for best-hard tracking. ok=false if the loaded DLL predates fp_polish_hard_loss, so the
// caller falls back to the CPU render. The CURRENT (post-Adam) params must be uploaded first.
func (g *CUDA) PolishHardLoss(bbxHost []int32) (float64, bool) {
	if g.procPolHard == nil {
		return 0, false
	}
	out := [1]float64{}
	g.procPolHard.Call(i32ptr(bbxHost), f64ptr(out[:]))
	runtime.KeepAlive(bbxHost)
	runtime.KeepAlive(out[:])
	return out[0], true
}

// PolishBackward computes per-shape gradients on the device. bbxHost sizes each shape's
// launch grid (multi-block, mirrors PolishForward) so large shapes parallelize.
func (g *CUDA) PolishBackward(tau float64, bbxHost []int32) {
	t := [1]float64{tau}
	g.procPolBwd.Call(i32ptr(bbxHost), f64ptr(t[:]))
	runtime.KeepAlive(bbxHost)
	runtime.KeepAlive(t[:])
}

// PolishReadGrad copies the per-shape gradients (n*10: gP0..5, gR,gG,gB,gA) to the host.
func (g *CUDA) PolishReadGrad(dst []float64) {
	g.procPolRdGrad.Call(f64ptr(dst))
	runtime.KeepAlive(dst)
}

// PolishReadRender copies the device soft render (w*h*4) to the host.
func (g *CUDA) PolishReadRender(dst []float32) {
	if g.procPolRdRender != nil {
		g.procPolRdRender.Call(fptr(dst))
		runtime.KeepAlive(dst)
	}
}

func (g *CUDA) PolishFree() {
	if g.procPolFree != nil {
		g.procPolFree.Call()
	}
}

// SetWarpEval toggles the eval kernel (true=warp-per-candidate, faster; false=block,
// golden fallback). No-op if the DLL predates the export (then the DLL default applies).
func (g *CUDA) SetWarpEval(on bool) {
	if g.procWarpEval != nil {
		g.procWarpEval.Call(uintptr(b2i32(on)))
	}
}

// SetGradients routes the eval to the block kernel so gradient kinds (KindGlow/KindDisk) get their
// per-pixel-alpha branch. Call with the run's gradient flag before evaluating. No-op if the DLL
// predates the export (then gradients require the CPU backend). Returns whether the export exists.
func (g *CUDA) SetGradients(on bool) bool {
	if g.procGradients == nil {
		return false
	}
	g.procGradients.Call(uintptr(b2i32(on)))
	g.gradOn = on
	return true
}

// SetCoarse enables coarse-to-fine on-device search: the random batch is scored at the
// cheap `budget` pixel cap to filter, then the survivors are re-scored at the full
// SampleBudget and the winner picked from those (so the winner is full-budget scored —
// quality-safe — while the bulk pays only the coarse cost). No-op if the DLL predates the
// export. enable=false restores the single-pass full-budget search.
func (g *CUDA) SetCoarse(enable bool, budget, kpart int) {
	if g.procCoarse != nil {
		g.procCoarse.Call(uintptr(b2i32(enable)), uintptr(budget), uintptr(kpart))
	}
}

// SetCoarseFP16 toggles FP16/half2 accumulation for the coarse filter pass (re-eval stays
// FP32). No-op if the DLL predates the export.
func (g *CUDA) SetCoarseFP16(on bool) {
	if g.procCoarseFP16 != nil {
		g.procCoarseFP16.Call(uintptr(b2i32(on)))
	}
}

func (g *CUDA) Close() error {
	if g.procFree != nil {
		g.procFree.Call()
	}
	if g.dll != nil {
		return g.dll.Release()
	}
	return nil
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
