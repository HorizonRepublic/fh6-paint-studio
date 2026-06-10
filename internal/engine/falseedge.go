package engine

import "math"

// False-edge additive term for the polish loss — opt-in EXPERIMENT (PolishOptions.FalseEdgeLambda > 0).
// The SSE metric is blind to a shape whose RIM draws an edge the target lacks (the "standout"): the
// rim is thin, so its SSE cost is negligible, and neither greedy nor polish removes it — today only
// the post-hoc standout pass (CLI-only, worst-few) touches them. This term charges those rims DURING
// the optimisation:
//
//	L = SSE + λ · Σ_q relu(|∇L_recon|(q) − |∇L_target|(q))
//
// with ∇ = Sobel on Rec.601 luma (the standout.go falseEdgeMap detector), summed over INTERIOR pixels
// only (the 1px border ring is excluded so no read needs clamping — the adjoint scatter then never
// leaves the canvas, which keeps the CPU and GPU formulations exactly equivalent; border rims are
// irrelevant to the artifact). The OKLab lesson bounds the design: greedy-basin + gate + fine-phase
// are SSE-aligned, so a perceptual term may only be a small ADDITIVE λ-term, never a metric swap —
// the bet is that rim configurations are near-flat directions of the SSE landscape, so a small λ can
// clean them at ~zero SSE cost. Both the descent (per-pixel adjoint added to the dC seed) and the
// best-hard tracking use the same combined loss, so the optimisation is self-consistent; the caller's
// accept gate still measures pure backend SSE.

// lumaR/lumaG/lumaB mirror metric.Luma's Rec.601 weights for the adjoint's dLuma/dchannel chain.
const (
	feLumaR = 0.299
	feLumaG = 0.587
	feLumaB = 0.114
)

// feState holds the fixed target-luma plane and the per-iteration scratch for the false-edge term.
type feState struct {
	tl  []float32 // target luma (fixed)
	rl  []float32 // recon luma scratch
	adj []float64 // dFE/dLuma per pixel (the adjoint scattered through the Sobel stencil)
}

func newFEState(target []float32, w, h int) *feState {
	fe := &feState{tl: make([]float32, w*h), rl: make([]float32, w*h), adj: make([]float64, w*h)}
	lumaOf(target, w, h, fe.tl)
	return fe
}

// sobelAtFast returns the Sobel gradient components at an INTERIOR pixel (no clamping).
func sobelAtFast(luma []float32, w, x, y int) (gx, gy float64) {
	i := y*w + x
	tl, tc, tr := float64(luma[i-w-1]), float64(luma[i-w]), float64(luma[i-w+1])
	ml, mr := float64(luma[i-1]), float64(luma[i+1])
	bl, bc, br := float64(luma[i+w-1]), float64(luma[i+w]), float64(luma[i+w+1])
	gx = (tr + 2*mr + br) - (tl + 2*ml + bl)
	gy = (bl + 2*bc + br) - (tl + 2*tc + tr)
	return gx, gy
}

// total returns the false-edge energy of render — the hard-tracking term.
func (fe *feState) total(render []float32, w, h int) float64 {
	lumaOf(render, w, h, fe.rl)
	var f0 float64
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			gx, gy := sobelAtFast(fe.rl, w, x, y)
			tx, ty := sobelAtFast(fe.tl, w, x, y)
			if d := math.Hypot(gx, gy) - math.Hypot(tx, ty); d > 0 {
				f0 += d
			}
		}
	}
	return f0
}

// adjoint computes the false-edge energy AND fills fe.adj with dFE/dLuma(p): each active interior
// pixel q (|∇recon| > |∇target|) scatters its normalised gradient direction back through the Sobel
// stencil (all 8 neighbours are in range by the interior restriction).
func (fe *feState) adjoint(render []float32, w, h int) float64 {
	lumaOf(render, w, h, fe.rl)
	for i := range fe.adj {
		fe.adj[i] = 0
	}
	var f0 float64
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			gx, gy := sobelAtFast(fe.rl, w, x, y)
			gr := math.Hypot(gx, gy)
			tx, ty := sobelAtFast(fe.tl, w, x, y)
			d := gr - math.Hypot(tx, ty)
			if d <= 0 || gr < 1e-12 {
				continue
			}
			f0 += d
			cx, cy := gx/gr, gy/gr
			i := y*w + x
			fe.adj[i-w-1] += -cx - cy
			fe.adj[i-w] += -2 * cy
			fe.adj[i-w+1] += cx - cy
			fe.adj[i-1] += -2 * cx
			fe.adj[i+1] += 2 * cx
			fe.adj[i+w-1] += -cx + cy
			fe.adj[i+w] += 2 * cy
			fe.adj[i+w+1] += cx + cy
		}
	}
	return f0
}
