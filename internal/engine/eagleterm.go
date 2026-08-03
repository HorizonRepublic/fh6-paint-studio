package engine

import "math"

// EAGLE additive term for the polish loss — opt-in EXPERIMENT (PolishOptions.EagleLambda > 0).
// Edge-Aware Gradient-Localization loss (2403.10695, CT reconstruction), adapted: where the FE term
// charges the raw gradient-magnitude EXCESS per pixel, EAGLE compares the local STRUCTURE of the
// gradient field — 3×3 patch variance of the Scharr gradient maps, high-pass filtered so only
// localized structural mismatch is charged (a hard synthetic rim has a variance signature a soft
// 1-2px source ramp does not, even at equal gradient magnitude — the failure mode falseedge.go's
// one-sided magnitude relu cannot see):
//
//	L = SSE + λ · Σ_q |HP(var₃(∂x recon))(q) − HP(var₃(∂x target))(q)| + (same for ∂y)
//
// HP(v) = v − gauss(v), with gauss = two zero-padded separable 3-tap box passes (the paper's
// frequency-domain gaussian high-pass, approximated spatially — no FFT in the polish loop). All
// windowed ops use ZERO padding with fixed 1/9 (1/3) normalization: symmetric kernels under zero
// padding are exactly self-adjoint, so the backward chain is exact everywhere incl. borders (the FD
// test relies on it). Same contract as FE/SSIM: additive small-λ only, descent + best-hard tracking
// combined, the caller's accept gate stays pure SSE. Judge by EYE; expect the FE/SSIM ΔE trade-off
// (the "no fidelity cost" claim in the source literature was refuted 0-3 in verification).

type eagleState struct {
	w, h     int
	tHx, tHy []float64 // target high-passed gradient-variance maps (fixed)
	rl       []float32 // recon luma scratch
	gx, gy   []float64 // Scharr scratch
	vx, vy   []float64 // patch-variance scratch (also holds the box mean via mx/my)
	mx, my   []float64 // patch-mean scratch (kept for the adjoint)
	hx, hy   []float64 // high-pass scratch
	t1, t2   []float64 // blur/box scratch
	sx, sy   []float64 // adjoint scratch (sign → high-pass adjoint)
	adj      []float64 // dEagle/dLuma per pixel (the output polishBackward folds into dC)
	tw       []float32 // optional per-pixel term weight (PolishOptions.TermWeight); nil = uniform
}

func newEagleState(target []float32, w, h int, tw []float32) *eagleState {
	if w < 8 || h < 8 {
		return nil
	}
	n := w * h
	e := &eagleState{
		w: w, h: h,
		tHx: make([]float64, n), tHy: make([]float64, n),
		rl: make([]float32, n),
		gx: make([]float64, n), gy: make([]float64, n),
		vx: make([]float64, n), vy: make([]float64, n),
		mx: make([]float64, n), my: make([]float64, n),
		hx: make([]float64, n), hy: make([]float64, n),
		t1: make([]float64, n), t2: make([]float64, n),
		sx: make([]float64, n), sy: make([]float64, n),
		adj: make([]float64, n), tw: tw,
	}
	lumaOf(target, w, h, e.rl)
	e.scharr(e.rl)
	e.patchVar(e.gx, e.vx, e.mx)
	e.patchVar(e.gy, e.vy, e.my)
	e.highpass(e.vx, e.tHx)
	e.highpass(e.vy, e.tHy)
	return e
}

// scharr fills gx/gy with the /16-normalized Scharr gradients of luma; the 1px border stays 0.
func (e *eagleState) scharr(luma []float32) {
	w, h := e.w, e.h
	for i := range e.gx {
		e.gx[i], e.gy[i] = 0, 0
	}
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*w + x
			tl, tc, tr := float64(luma[i-w-1]), float64(luma[i-w]), float64(luma[i-w+1])
			ml, mr := float64(luma[i-1]), float64(luma[i+1])
			bl, bc, br := float64(luma[i+w-1]), float64(luma[i+w]), float64(luma[i+w+1])
			e.gx[i] = ((3*tr + 10*mr + 3*br) - (3*tl + 10*ml + 3*bl)) / 16
			e.gy[i] = ((3*bl + 10*bc + 3*br) - (3*tl + 10*tc + 3*tr)) / 16
		}
	}
}

// patchVar fills v with the 3×3 zero-padded fixed-1/9 patch variance of g, and m with the patch mean.
func (e *eagleState) patchVar(g, v, m []float64) {
	w, h := e.w, e.h
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s1, s2 float64
			for dy := -1; dy <= 1; dy++ {
				yy := y + dy
				if yy < 0 || yy >= h {
					continue
				}
				for dx := -1; dx <= 1; dx++ {
					xx := x + dx
					if xx < 0 || xx >= w {
						continue
					}
					gv := g[yy*w+xx]
					s1 += gv
					s2 += gv * gv
				}
			}
			mm := s1 / 9
			m[y*w+x] = mm
			v[y*w+x] = s2/9 - mm*mm
		}
	}
}

// box3 writes the zero-padded fixed-1/3 3-tap box of src along x then y into dst (via e.t2).
func (e *eagleState) box3(src, dst []float64) {
	w, h := e.w, e.h
	tmp := e.t2
	for y := 0; y < h; y++ {
		row := y * w
		for x := 0; x < w; x++ {
			s := src[row+x]
			if x > 0 {
				s += src[row+x-1]
			}
			if x < w-1 {
				s += src[row+x+1]
			}
			tmp[row+x] = s / 3
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := tmp[y*w+x]
			if y > 0 {
				s += tmp[(y-1)*w+x]
			}
			if y < h-1 {
				s += tmp[(y+1)*w+x]
			}
			dst[y*w+x] = s / 3
		}
	}
}

// highpass writes dst = src − gauss(src), gauss = box3∘box3 (zero-padded → exactly self-adjoint).
func (e *eagleState) highpass(src, dst []float64) {
	e.box3(src, e.t1)
	e.box3(e.t1, dst)
	for i := range dst {
		dst[i] = src[i] - dst[i]
	}
}

// forward computes the recon-side maps and returns the loss Σ|Hx−tHx|+|Hy−tHy|.
func (e *eagleState) forward(render []float32) float64 {
	lumaOf(render, e.w, e.h, e.rl)
	e.scharr(e.rl)
	e.patchVar(e.gx, e.vx, e.mx)
	e.patchVar(e.gy, e.vy, e.my)
	e.highpass(e.vx, e.hx)
	e.highpass(e.vy, e.hy)
	var f float64
	for i := range e.hx {
		d := math.Abs(e.hx[i]-e.tHx[i]) + math.Abs(e.hy[i]-e.tHy[i])
		if e.tw != nil {
			d *= float64(e.tw[i])
		}
		f += d
	}
	return f
}

// total returns the EAGLE energy of render — the hard-tracking term.
func (e *eagleState) total(render []float32, _, _ int) float64 {
	return e.forward(render)
}

// adjoint computes the energy AND fills e.adj with dEagle/dLuma via the exact chain
// sign → high-pass (self-adjoint) → patch-variance ((2/9)(g−mean) over the window) → Scharr scatter.
func (e *eagleState) adjoint(render []float32, _, _ int) float64 {
	f := e.forward(render)
	w, h := e.w, e.h
	for i := range e.adj {
		e.adj[i] = 0
	}
	for dir := 0; dir < 2; dir++ {
		hp, tH, g, m, s := e.hx, e.tHx, e.gx, e.mx, e.sx
		if dir == 1 {
			hp, tH, g, m, s = e.hy, e.tHy, e.gy, e.my, e.sy
		}
		// dL/dV = HPᵀ(w·sign) = w·sign − gauss(w·sign) (zero-padded gauss is self-adjoint; the
		// per-pixel term weight enters the chain exactly where the loss charges it).
		for i := range s {
			wi := 1.0
			if e.tw != nil {
				wi = float64(e.tw[i])
			}
			switch {
			case hp[i] > tH[i]:
				s[i] = wi
			case hp[i] < tH[i]:
				s[i] = -wi
			default:
				s[i] = 0
			}
		}
		e.highpass(s, e.t1) // t1 = u = dL/dV
		// dL/dg_j = (2/9)·[ g_j·Box(u)_j − Box(u·m)_j ], Box = zero-padded 3×3 SUM.
		u := e.t1
		vscr := e.vx // the variance map is consumed by this point — reuse as u·m scratch
		if dir == 1 {
			vscr = e.vy
		}
		for i := range vscr {
			vscr[i] = u[i] * m[i]
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var su, sum float64
				for dy := -1; dy <= 1; dy++ {
					yy := y + dy
					if yy < 0 || yy >= h {
						continue
					}
					for dx := -1; dx <= 1; dx++ {
						xx := x + dx
						if xx < 0 || xx >= w {
							continue
						}
						su += u[yy*w+xx]
						sum += vscr[yy*w+xx]
					}
				}
				j := y*w + x
				// dL/dg into s (reuse: s no longer needed once u is built)
				s[j] = (2.0 / 9.0) * (g[j]*su - sum)
			}
		}
		// Scharr scatter: g_j reads its 8 neighbours with kernel K/16; adjoint scatters W_j = s[j]
		// back through the same weights (interior j only — matches the forward's zeroed border).
		W := s
		for y := 1; y < h-1; y++ {
			for x := 1; x < w-1; x++ {
				j := y*w + x
				wj := W[j] / 16
				if wj == 0 {
					continue
				}
				if dir == 0 {
					e.adj[j-w+1] += 3 * wj
					e.adj[j+1] += 10 * wj
					e.adj[j+w+1] += 3 * wj
					e.adj[j-w-1] += -3 * wj
					e.adj[j-1] += -10 * wj
					e.adj[j+w-1] += -3 * wj
				} else {
					e.adj[j+w-1] += 3 * wj
					e.adj[j+w] += 10 * wj
					e.adj[j+w+1] += 3 * wj
					e.adj[j-w-1] += -3 * wj
					e.adj[j-w] += -10 * wj
					e.adj[j-w+1] += -3 * wj
				}
			}
		}
	}
	return f
}
