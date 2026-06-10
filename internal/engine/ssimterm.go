package engine

// SSIM additive term for the polish loss — opt-in EXPERIMENT (PolishOptions.SSIMLambda > 0).
// SSE is blind to local-contrast and shadow-structure errors: a region rendered at the right mean
// but the wrong variance (washed-out shading, flattened gradients) costs almost nothing in SSE.
// This term charges structure directly:
//
//	L = SSE + λfe·FE + λs · Σ_p (1 − SSIM(p))
//
// with SSIM over uniform 8×8 windows on Rec.601 luma (the classic moving-window formulation),
// summed over VALID positions only (windows fully inside the canvas — no border clamping, so the
// CPU and GPU formulations are exactly equivalent, mirroring the false-edge interior rule). The
// OKLab lesson bounds the design: a perceptual term may only be a small ADDITIVE λ-term, never a
// metric swap — descent and best-hard tracking use the combined loss, the caller's accept gate
// stays pure SSE. The adjoint is closed-form through the five window moments; the recon enters
// only via mean(x), mean(x²), mean(x·y), so the backward is two box-filter passes over the
// per-window moment-gradients plus a per-pixel combine — the same scatter-free shape as the FE
// gather, which is what makes the CUDA/Vulkan port a stencil copy of the CPU loops.

const (
	ssimWin  = 8
	ssimInvN = 1.0 / (ssimWin * ssimWin)
	ssimC1   = 0.0001 // (K1·L)², K1=0.01, L=1
	ssimC2   = 0.0009 // (K2·L)², K2=0.03, L=1
)

// ssimState holds the fixed target-luma moments and the per-iteration scratch for the SSIM term.
// All sums run in float64 with the SAME direct 8-term loop order as the GPU kernels.
type ssimState struct {
	mw, mh int // valid window-position grid (w-7 × h-7)

	tl  []float32 // target luma (fixed)
	my  []float64 // target window mean (fixed, mw*mh)
	myy []float64 // target window mean of y² (fixed, mw*mh)

	rl            []float32 // recon luma scratch
	hx, hxx, hxy  []float64 // horizontal window sums of x, x², x·y (mw*h)
	g1, g2, g3    []float64 // dL/dmx, dL/dmxx, dL/dmxy per window (mw*mh)
	hg1, hg2, hg3 []float64 // horizontally transpose-filtered G (w*mh)
	adj           []float64 // dSSIMterm/dLuma per pixel
}

func newSSIMState(target []float32, w, h int) *ssimState {
	mw, mh := w-ssimWin+1, h-ssimWin+1
	if mw < 1 || mh < 1 {
		return nil
	}
	ss := &ssimState{
		mw: mw, mh: mh,
		tl: make([]float32, w*h), my: make([]float64, mw*mh), myy: make([]float64, mw*mh),
		rl: make([]float32, w*h),
		hx: make([]float64, mw*h), hxx: make([]float64, mw*h), hxy: make([]float64, mw*h),
		g1: make([]float64, mw*mh), g2: make([]float64, mw*mh), g3: make([]float64, mw*mh),
		hg1: make([]float64, w*mh), hg2: make([]float64, w*mh), hg3: make([]float64, w*mh),
		adj: make([]float64, w*h),
	}
	lumaOf(target, w, h, ss.tl)
	// Target moments via the same horizontal+vertical 8-term sums (hxy carries y·y here).
	ss.hpass(ss.tl, w, h)
	for gy := 0; gy < mh; gy++ {
		for gx := 0; gx < mw; gx++ {
			var sy, syy float64
			for k := 0; k < ssimWin; k++ {
				row := (gy + k) * mw
				sy += ss.hx[row+gx]
				syy += ss.hxx[row+gx]
			}
			ss.my[gy*mw+gx] = sy * ssimInvN
			ss.myy[gy*mw+gx] = syy * ssimInvN
		}
	}
	return ss
}

// hpass fills hx/hxx/hxy with horizontal 8-term window sums of luma, luma² and luma·targetLuma.
func (ss *ssimState) hpass(luma []float32, w, h int) {
	for y := 0; y < h; y++ {
		row := y * w
		out := y * ss.mw
		for px := 0; px < ss.mw; px++ {
			var sx, sxx, sxy float64
			for k := 0; k < ssimWin; k++ {
				v := float64(luma[row+px+k])
				sx += v
				sxx += v * v
				sxy += v * float64(ss.tl[row+px+k])
			}
			ss.hx[out+px] = sx
			ss.hxx[out+px] = sxx
			ss.hxy[out+px] = sxy
		}
	}
}

// windowTerms computes the SSIM pieces at window (gx,gy) from the horizontal sums.
func (ss *ssimState) windowTerms(gx, gy int) (s, a1, a2, b1, b2, mx, mxx, mxy float64) {
	var sx, sxx, sxy float64
	for k := 0; k < ssimWin; k++ {
		row := (gy + k) * ss.mw
		sx += ss.hx[row+gx]
		sxx += ss.hxx[row+gx]
		sxy += ss.hxy[row+gx]
	}
	mx, mxx, mxy = sx*ssimInvN, sxx*ssimInvN, sxy*ssimInvN
	gi := gy*ss.mw + gx
	my, myy := ss.my[gi], ss.myy[gi]
	a1 = 2*mx*my + ssimC1
	a2 = 2*(mxy-mx*my) + ssimC2
	b1 = mx*mx + my*my + ssimC1
	b2 = (mxx - mx*mx) + (myy - my*my) + ssimC2
	s = (a1 * a2) / (b1 * b2)
	return
}

// total returns the SSIM energy Σ(1−S) of render — the hard-tracking term.
func (ss *ssimState) total(render []float32, w, h int) float64 {
	lumaOf(render, w, h, ss.rl)
	ss.hpass(ss.rl, w, h)
	var f0 float64
	for gy := 0; gy < ss.mh; gy++ {
		for gx := 0; gx < ss.mw; gx++ {
			s, _, _, _, _, _, _, _ := ss.windowTerms(gx, gy)
			f0 += 1 - s
		}
	}
	return f0
}

// adjoint computes the SSIM energy AND fills ss.adj with dΣ(1−S)/dLuma(q). Per window:
// dS/dmx = 2my(A2−A1)/(B1B2) − S·2mx(1/B1−1/B2), dS/dmxx = −S/B2, dS/dmxy = 2A1/(B1B2);
// G = −dS/dm. The recon enters each window mean linearly (∂mx/∂x = 1/N, ∂mxx/∂x = 2x/N,
// ∂mxy/∂x = y/N), so adj(q) = (T1 + 2x·T2 + y·T3)/N with T = the box-correlation of G over
// the windows covering q — two more 8-term passes, no scatter.
func (ss *ssimState) adjoint(render []float32, w, h int) float64 {
	lumaOf(render, w, h, ss.rl)
	ss.hpass(ss.rl, w, h)
	var f0 float64
	for gy := 0; gy < ss.mh; gy++ {
		for gx := 0; gx < ss.mw; gx++ {
			s, a1, a2, b1, b2, mx, _, _ := ss.windowTerms(gx, gy)
			f0 += 1 - s
			denom := b1 * b2
			gi := gy*ss.mw + gx
			dsdmx := 2*ss.my[gi]*(a2-a1)/denom - s*2*mx*(1/b1-1/b2)
			ss.g1[gi] = -dsdmx
			ss.g2[gi] = s / b2
			ss.g3[gi] = -2 * a1 / denom
		}
	}
	// Horizontal transpose pass: HG(y,qx) = Σ G(y,px) over valid px ∈ [qx-7, qx].
	for gy := 0; gy < ss.mh; gy++ {
		grow := gy * ss.mw
		orow := gy * w
		for qx := 0; qx < w; qx++ {
			p0 := qx - ssimWin + 1
			if p0 < 0 {
				p0 = 0
			}
			p1 := qx
			if p1 > ss.mw-1 {
				p1 = ss.mw - 1
			}
			var t1, t2, t3 float64
			for px := p0; px <= p1; px++ {
				t1 += ss.g1[grow+px]
				t2 += ss.g2[grow+px]
				t3 += ss.g3[grow+px]
			}
			ss.hg1[orow+qx] = t1
			ss.hg2[orow+qx] = t2
			ss.hg3[orow+qx] = t3
		}
	}
	// Vertical transpose pass + per-pixel combine.
	for qy := 0; qy < h; qy++ {
		p0 := qy - ssimWin + 1
		if p0 < 0 {
			p0 = 0
		}
		p1 := qy
		if p1 > ss.mh-1 {
			p1 = ss.mh - 1
		}
		for qx := 0; qx < w; qx++ {
			var t1, t2, t3 float64
			for py := p0; py <= p1; py++ {
				row := py * w
				t1 += ss.hg1[row+qx]
				t2 += ss.hg2[row+qx]
				t3 += ss.hg3[row+qx]
			}
			i := qy*w + qx
			ss.adj[i] = (t1 + 2*float64(ss.rl[i])*t2 + float64(ss.tl[i])*t3) * ssimInvN
		}
	}
	return f0
}
