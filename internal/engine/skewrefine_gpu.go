package engine

import (
	"os"
	"strconv"

	"fh6-paint-studio/internal/model"
)

// The device half of the local geometry refine. The pass itself lives in skewrefine.go; this is
// only the plumbing: which shapes the shader can frame, how much context memory a round may take,
// and the capability lookup that keeps the engine from importing the backend package.

// refineDevBudget caps the per-round context slab (samples x 14 floats). The slab is the whole
// memory cost of the device pass and it is transient, so this is sized to be invisible next to the
// polish's own allocations on a 4GB card rather than to be as large as possible.
// FH6_REFINE_VRAM overrides it, in megabytes.
var refineDevBudget = func() int {
	mb := 192
	if v := os.Getenv("FH6_REFINE_VRAM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 8 && n <= 4096 {
			mb = n
		}
	}
	return mb << 20
}()

// refineDevMinSamples is the window below which a shape stays on the host. See the call site.
var refineDevMinSamples = func() int {
	if v := os.Getenv("FH6_REFINE_MINSAMP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return 1024
}()

// refineGPU (FH6_REFINE_GPU=0) pins the pass back to the host.
var refineGPU = os.Getenv("FH6_REFINE_GPU") != "0"

// refineOnDevice reports whether refine.comp can frame this kind. It carries a bbox for the
// box-framed kinds only; a triangle, a line or a bank word keeps the host path, which costs
// nothing — those are a small minority of any stack and the host pass is still there.
func refineOnDevice(k model.ShapeKind) bool {
	switch k {
	case model.KindEllipse, model.KindRectangle, model.KindGlow, model.KindDisk:
		return true
	}
	return false
}

// vulkanRefineJobs mirrors the backend's RefineJobs without importing it: the engine talks to the
// backend through capability interfaces, and a struct in the interface would drag the package in.
type vulkanRefineJobs struct {
	n                             int
	shapeP, shapeCol              []float32
	shapeKind, shapeBox           []int32
	tileOff, tileIdx              []int32
	tw, th, tile                  int
	jobShape, jobWin, jobNAx      []int32
	jobAxes                       []float32
	ctxCap, sweeps                int
	unweighted                    bool
	minGain, intrudeMax, cleanDev float32
	shrink, minStepFrac           float32
}

// refiner is the optional backend capability. The argument is `any` for the same reason: the
// backend defines the concrete job struct and the engine hands one over untyped, which keeps the
// dependency pointing one way.
type refiner interface {
	RefineJobsFromEngine(n int, shapeP, shapeCol []float32, shapeKind, shapeBox, tileOff, tileIdx []int32,
		tw, th, tile int, jobShape, jobWin, jobNAx []int32, jobAxes []float32,
		ctxCap, sweeps int, unweighted bool, minGain, intrudeMax, cleanDev, shrink, minStepFrac float32,
		outP, outGain []float32) int
}

// deviceRefiner returns the call that runs one round on the device, or nil when the backend cannot
// (an older DLL, a non-Vulkan backend, or the pin turned off).
func deviceRefiner(be any) func(*vulkanRefineJobs, []float32, []float32) int {
	if !refineGPU {
		return nil
	}
	r, ok := be.(refiner)
	if !ok {
		return nil
	}
	return func(j *vulkanRefineJobs, outP, outGain []float32) int {
		return r.RefineJobsFromEngine(j.n, j.shapeP, j.shapeCol, j.shapeKind, j.shapeBox,
			j.tileOff, j.tileIdx, j.tw, j.th, j.tile, j.jobShape, j.jobWin, j.jobNAx, j.jobAxes,
			j.ctxCap, j.sweeps, j.unweighted, j.minGain, j.intrudeMax, j.cleanDev, j.shrink,
			j.minStepFrac, outP, outGain)
	}
}
