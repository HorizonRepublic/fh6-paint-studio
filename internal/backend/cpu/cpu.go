package cpu

import (
	"math"
	"runtime"
	"sync"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

const rejected = float32(math.MaxFloat32)

// CPU is the pure-Go reference backend.
type CPU struct {
	w, h         int
	target       []float32 // RGBA 0..1, len w*h*4
	canvas       []float32 // RGBA 0..1, len w*h*4
	weight       []float32 // per-pixel importance, len w*h (default all 1)
	gridSize     int
	sampleBudget int // progressive-sampling target pixel count (see sampleStep)
}

// defaultSampleBudget is the per-shape sampled-pixel cap for progressive sampling.
// Big shapes are scored on a strided subset of this many pixels (then the ΔSSE is
// scaled by step²). 4000 trades a little accuracy for CPU speed; full-res scoring
// is most accurate. Raise via SetSampleBudget (quality) — on GPU full-res is cheap.
const defaultSampleBudget = 4000

// New builds a CPU backend for target (RGBA float, len w*h*4); canvas starts black-opaque.
func New(target []float32, w, h, gridSize int) *CPU {
	canvas := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		canvas[i*4+3] = 1 // opaque black
	}
	if gridSize < 1 {
		gridSize = 1
	}
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1
	}
	return &CPU{w: w, h: h, target: append([]float32(nil), target...), canvas: canvas, weight: weight, gridSize: gridSize, sampleBudget: defaultSampleBudget}
}

// SetSampleBudget sets the progressive-sampling target pixel count. A very large
// value (e.g. >= image area) makes scoring effectively full-resolution, matching the
// reference and fixing mis-scored big early shapes. Values < 1 reset to the default.
func (c *CPU) SetSampleBudget(n int) {
	if n < 1 {
		n = defaultSampleBudget
	}
	c.sampleBudget = n
}

// SetWeight installs a per-pixel importance map (len w*h). Values scale each
// pixel's contribution to the optimal-color solve, ΔSSE, and the error grid.
// A no-op if the length does not match the image.
func (c *CPU) SetWeight(weight []float32) {
	if len(weight) == c.w*c.h {
		c.weight = weight
	}
}

var _ backend.Backend = (*CPU)(nil)

// Evaluate scores all candidates in parallel across CPU cores. evalShape only
// reads shared state (target/canvas/weight) and writes to a distinct out[i],
// so concurrent evaluation is data-race free.
func (c *CPU) Evaluate(cands []model.Candidate) ([]backend.EvalResult, error) {
	out := make([]backend.EvalResult, len(cands))
	if len(cands) == 0 {
		return out, nil
	}
	workers := runtime.NumCPU()
	if workers > len(cands) {
		workers = len(cands)
	}
	if workers <= 1 {
		for i, cand := range cands {
			out[i] = c.evalShape(cand)
		}
		return out, nil
	}
	chunk := (len(cands) + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < len(cands); w += chunk {
		lo, hi := w, w+chunk
		if hi > len(cands) {
			hi = len(cands)
		}
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				out[i] = c.evalShape(cands[i])
			}
		}(lo, hi)
	}
	wg.Wait()
	return out, nil
}

// evalShape computes the analytic optimal RGB that minimizes SSE under
// alpha-compositing, plus the resulting ΔSSE.
func (c *CPU) evalShape(cand model.Candidate) backend.EvalResult {
	if raster.IsGradient(cand.Kind) {
		return c.evalGradient(cand)
	}
	a := float64(cand.Color.A)
	if a < 1e-3 {
		a = 1e-3
	}
	if a > 1 {
		a = 1
	}
	xMin, yMin, xMax, yMax := raster.BBox(cand.Kind, cand.P, c.w, c.h)
	step := c.sampleStep(xMin, yMin, xMax, yMax)
	var n, nt int // n = opaque target pixels covered; nt = transparent (overhang)
	var W float64 // sum of weights over covered opaque pixels
	var sTR, sTG, sTB, sTA float64
	var sCR, sCG, sCB, sCA float64
	var sCR2, sCG2, sCB2, sCA2 float64
	var sTCR, sTCG, sTCB, sTCA float64
	for y := yMin; y <= yMax; y += step {
		for x := xMin; x <= xMax; x += step {
			if !raster.Inside(cand.Kind, cand.P, x, y) {
				continue
			}
			idx := y*c.w + x
			p := idx * 4
			// Transparent target pixels are overhang: they have no valid color to
			// match, so they are excluded from the color solve and counted as spill.
			if c.target[p+3] < 0.5 {
				nt++
				continue
			}
			wgt := float64(c.weight[idx])
			tr, tg, tb, ta := float64(c.target[p]), float64(c.target[p+1]), float64(c.target[p+2]), float64(c.target[p+3])
			sr, sg, sb, sa := float64(c.canvas[p]), float64(c.canvas[p+1]), float64(c.canvas[p+2]), float64(c.canvas[p+3])
			sTR, sTG, sTB, sTA = sTR+wgt*tr, sTG+wgt*tg, sTB+wgt*tb, sTA+wgt*ta
			sCR, sCG, sCB, sCA = sCR+wgt*sr, sCG+wgt*sg, sCB+wgt*sb, sCA+wgt*sa
			sCR2, sCG2, sCB2, sCA2 = sCR2+wgt*sr*sr, sCG2+wgt*sg*sg, sCB2+wgt*sb*sb, sCA2+wgt*sa*sa
			sTCR, sTCG, sTCB, sTCA = sTCR+wgt*tr*sr, sTCG+wgt*tg*sg, sTCB+wgt*tb*sb, sTCA+wgt*ta*sa
			W += wgt
			n++
		}
	}
	if n == 0 || W <= 0 {
		return backend.EvalResult{Score: rejected}
	}
	// Reject shapes that sit mostly on the transparent background (>60% overhang):
	// these produce dark edge specks on cutouts. Inert for opaque images (nt==0).
	if nt > 0 && float64(nt) > 1.5*float64(n) {
		return backend.EvalResult{Score: rejected}
	}
	invW := 1.0 / W
	invA := 1.0 - a
	oR := clamp01((sTR*invW - (sCR*invW)*invA) / a)
	oG := clamp01((sTG*invW - (sCG*invW)*invA) / a)
	oB := clamp01((sTB*invW - (sCB*invW)*invA) / a)
	a2, twoA := a*a, 2*a
	dR := a2*(W*oR*oR-2*oR*sCR+sCR2) - twoA*(oR*sTR-oR*sCR-sTCR+sCR2)
	dG := a2*(W*oG*oG-2*oG*sCG+sCG2) - twoA*(oG*sTG-oG*sCG-sTCG+sCG2)
	dB := a2*(W*oB*oB-2*oB*sCB+sCB2) - twoA*(oB*sTB-oB*sCB-sTCB+sCB2)
	dA := a2*(W-2*sCA+sCA2) - twoA*(sTA-sCA-sTCA+sCA2)
	totalDelta := dR + dG + dB + dA
	// Overhang/spill penalty: painting onto transparent background pixels adds
	// error (raises alpha where the target wants 0, with the shape's color). This
	// pushes shapes inward off the cutout edge, killing the dark halo. nt is 0 for
	// fully-opaque images, so this is inert there.
	if nt > 0 {
		spillFrac := float64(nt) / float64(n+nt)
		totalDelta += a2 * float64(nt) * (1 + 2*spillFrac) * (oR*oR + oG*oG + oB*oB + 1)
	}
	return backend.EvalResult{
		Score: float32(totalDelta * float64(step*step)),
		Color: model.RGBA{R: float32(oR), G: float32(oG), B: float32(oB), A: cand.Color.A},
	}
}

// sampleStep returns a pixel stride that caps sampled pixels for large shapes
// (progressive sampling). Accumulated sums are scaled by step² so the ΔSSE
// estimates the full-coverage value; the optimal color is a ratio so the scale
// cancels. The chosen shape is later applied EXACTLY by Apply. Mirrors the
// reference GPU kernel's sampleStep, so it carries to the CUDA backend. The cap is
// c.sampleBudget (SetSampleBudget); a budget >= area yields step 1 (full-res).
func (c *CPU) sampleStep(xMin, yMin, xMax, yMax int) int {
	target := c.sampleBudget
	if target < 1 {
		target = defaultSampleBudget
	}
	area := (xMax - xMin + 1) * (yMax - yMin + 1)
	if area <= target {
		return 1
	}
	step := int(math.Sqrt(float64(area) / float64(target)))
	if step < 1 {
		return 1
	}
	return step
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func (c *CPU) Apply(cand model.Candidate) error {
	if raster.IsGradient(cand.Kind) {
		c.applyGradient(cand)
		return nil
	}
	a := cand.Color.A
	if a < 0 {
		a = 0
	}
	if a > 1 {
		a = 1
	}
	invA := 1 - a
	xMin, yMin, xMax, yMax := raster.BBox(cand.Kind, cand.P, c.w, c.h)
	for y := yMin; y <= yMax; y++ {
		for x := xMin; x <= xMax; x++ {
			if !raster.Inside(cand.Kind, cand.P, x, y) {
				continue
			}
			p := (y*c.w + x) * 4
			c.canvas[p+0] = c.canvas[p+0]*invA + cand.Color.R*a
			c.canvas[p+1] = c.canvas[p+1]*invA + cand.Color.G*a
			c.canvas[p+2] = c.canvas[p+2]*invA + cand.Color.B*a
			c.canvas[p+3] = c.canvas[p+3]*invA + a
		}
	}
	return nil
}

func (c *CPU) ErrorGrid() ([]float32, int, int, error) {
	gw, gh := c.gridSize, c.gridSize
	grid := make([]float32, gw*gh)
	for gy := 0; gy < gh; gy++ {
		y0 := gy * c.h / gh
		y1 := (gy + 1) * c.h / gh
		for gx := 0; gx < gw; gx++ {
			x0 := gx * c.w / gw
			x1 := (gx + 1) * c.w / gw
			var sum float64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					idx := y*c.w + x
					wgt := float64(c.weight[idx])
					p := idx * 4
					for k := 0; k < 4; k++ {
						d := float64(c.target[p+k] - c.canvas[p+k])
						sum += wgt * d * d
					}
				}
			}
			grid[gy*gw+gx] = float32(sum)
		}
	}
	return grid, gw, gh, nil
}

func (c *CPU) ReadCanvas(dst []float32) error { copy(dst, c.canvas); return nil }

// Target returns the read-only target buffer. Callers must not mutate it.
func (c *CPU) Target() []float32 { return c.target }

// Weight returns the read-only per-pixel weight buffer. Callers must not mutate it.
func (c *CPU) Weight() []float32 { return c.weight }

func (c *CPU) Reset(canvas []float32) error { copy(c.canvas, canvas); return nil }

func (c *CPU) Close() error { return nil }
