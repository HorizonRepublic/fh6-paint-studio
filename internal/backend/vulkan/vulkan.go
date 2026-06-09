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
	"runtime"
	"unsafe"

	"fh6-paint-studio/internal/backend"
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
	maxCands   = 16384 // device scratch capacity; Evaluate chunks if exceeded
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
	procSetBound   *windows.Proc
	// joint-polish device primitives
	procPolSetup, procPolSTE, procPolUpload, procPolFwd, procPolLoss, procPolBwd,
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
	return g, nil
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
		// The Vulkan eval kernel has no mask/gradient geometry yet (Phase 2+): its inside
		// switch falls through to ELLIPSE for kind >= KindMaskBase and rejects gradients.
		// Reject mask candidates here (fail-loud, never selected); the on-device generator
		// never emits masks, so this is inert for the default pipeline.
		if cands[i].Kind >= model.KindMaskBase {
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
