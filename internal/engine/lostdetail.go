package engine

import "math"

// Lost-detail additive term for the polish loss — opt-in (PolishOptions.LostDetailLambda > 0).
// The MIRROR of the false-edge term (falseedge.go), and the artifact it exists to catch is the one
// that survived every detector we had:
//
//	L = SSE + λ · Σ_q relu(|∇L_target|(q) − |∇L_recon|(q))ну
//
// FE charges edges the recon INVENTS (a shape rim in smooth content — the classic standout). Nothing
// charged the opposite: structure the recon ERASES. A rimless glow has no edge by construction, so
// FE and EAGLE are blind to it — which is exactly why the deep-smooth glow swap could be pushed into
// moderately-structured cells and come out metric-clean while the eye read a soft blob of "meat"
// over a neck (owner, 2026-08-03). SSE barely charges it either: a blob near the local mean is cheap.
// This term makes blur-over-structure visible to the optimisation, so glows survive where they
// dissolve rims and lose where they eat detail — the trade-off no single tau setting could resolve.
//
// EPSILON-SMOOTHED MAGNITUDE, and it is load-bearing. |∇r| has a kink at zero, so the exact
// derivative vanishes in a PERFECTLY flat recon region — precisely the case this term targets, where
// it would then push nothing. sqrt(gx²+gy²+ε²) keeps the derivative defined and small-but-nonzero as
// the recon flattens, at the cost of a constant ε offset in the energy (irrelevant: only differences
// drive the descent). ε is in luma-gradient units; the Sobel kernel has gain 8, so ε=1e-3 is well
// below any real edge while still regularising the flat case.
const lostDetailEps = 1e-3

// ldState mirrors feState. Deliberately a SEPARATE state rather than a second lambda inside
// feState: the false-edge path is tuned, shipped and sits next to a documented end-to-end burn, so
// this term must be addable and removable (delete the file + the Options gate) without touching a
// line of it. The duplicated luma/Sobel work is host-path only — the device folds both terms into
// the passes it already runs.
type ldState struct {
	tl  []float32 // target luma (fixed)
	rl  []float32 // recon luma scratch
	adj []float64 // dLD/dLuma per pixel (adjoint scattered through the Sobel stencil)
	tw  []float32 // optional per-pixel term weight (PolishOptions.TermWeight); nil = uniform
}

func newLDState(target []float32, w, h int, tw []float32) *ldState {
	ld := &ldState{tl: make([]float32, w*h), rl: make([]float32, w*h), adj: make([]float64, w*h), tw: tw}
	lumaOf(target, w, h, ld.tl)
	return ld
}

// smoothMag is the epsilon-smoothed gradient magnitude (see lostDetailEps).
func smoothMag(gx, gy float64) float64 {
	return math.Sqrt(gx*gx + gy*gy + lostDetailEps*lostDetailEps)
}

// total returns the lost-detail energy of render — the hard-tracking term. Interior pixels only,
// matching falseedge.go (no clamped reads, so the adjoint scatter never leaves the canvas).
func (ld *ldState) total(render []float32, w, h int) float64 {
	lumaOf(render, w, h, ld.rl)
	var f0 float64
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			gx, gy := sobelAtFast(ld.rl, w, x, y)
			tx, ty := sobelAtFast(ld.tl, w, x, y)
			if d := smoothMag(tx, ty) - smoothMag(gx, gy); d > 0 {
				if ld.tw != nil {
					d *= float64(ld.tw[y*w+x])
				}
				f0 += d
			}
		}
	}
	return f0
}

// adjoint computes the lost-detail energy AND fills ld.adj with dLD/dLuma(p). Active pixels are
// those where the TARGET out-gradients the recon; the scatter is the false-edge scatter NEGATED,
// because d = |∇t| − |∇r| differentiates the recon term with the opposite sign.
func (ld *ldState) adjoint(render []float32, w, h int) float64 {
	lumaOf(render, w, h, ld.rl)
	for i := range ld.adj {
		ld.adj[i] = 0
	}
	var f0 float64
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			gx, gy := sobelAtFast(ld.rl, w, x, y)
			gr := smoothMag(gx, gy)
			tx, ty := sobelAtFast(ld.tl, w, x, y)
			d := smoothMag(tx, ty) - gr
			if d <= 0 {
				continue
			}
			wi := 1.0
			if ld.tw != nil {
				wi = float64(ld.tw[y*w+x])
			}
			f0 += wi * d
			// Negated vs falseedge.go: raising |∇recon| LOWERS this energy.
			cx, cy := -wi*gx/gr, -wi*gy/gr
			i := y*w + x
			ld.adj[i-w-1] += -cx - cy
			ld.adj[i-w] += -2 * cy
			ld.adj[i-w+1] += cx - cy
			ld.adj[i-1] += -2 * cx
			ld.adj[i+1] += 2 * cx
			ld.adj[i+w-1] += -cx + cy
			ld.adj[i+w] += 2 * cy
			ld.adj[i+w+1] += cx + cy
		}
	}
	return f0
}
