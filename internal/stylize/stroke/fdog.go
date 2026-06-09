package stroke

import (
	"fmt"
	"math"
	"os"

	"fh6-paint-studio/internal/stylize"
)

// fdog.go — Coherent Line Drawing (Kang, Lee & Chui 2007): a Flow-based Difference-of-Gaussians that
// produces long, connected, smooth lines where plain XDoG fragments into sketchy per-pixel specks. Two
// stages: an Edge Tangent Flow (ETF) field that aligns edge directions into a smooth flow, then a DoG
// taken ACROSS the flow and accumulated ALONG it (line-integral-convolution style) so coherent edges
// reinforce and noise cancels. Same interface as xdogMask → drops straight into the ink engine.

// fdogParams are the coherent-line knobs.
type fdogParams struct {
	SigmaC   float64 // DoG centre sigma (line scale, px)
	K        float64 // surround/centre ratio (≈1.6 → LoG)
	Tau      float64 // DoG inhibition (≈0.99); higher = thinner, more selective lines
	SigmaM   float64 // along-flow accumulation sigma (px); the coherence length that connects edges
	Phi      float64 // tanh threshold sharpness
	Thresh   float64 // binarisation: ink where 1+tanh(phi·H) < Thresh (≈0.5)
	ETFRad   int     // ETF kernel radius (px)
	ETFIters int     // ETF refinement passes
	FDoGIter int     // FDoG re-applications (each sharpens lines; 1..3)
}

func defaultFDoG() fdogParams {
	return fdogParams{SigmaC: 1.0, K: 1.6, Tau: 0.99, SigmaM: 3.0, Phi: 20.0, Thresh: 0.5,
		ETFRad: 4, ETFIters: 3, FDoGIter: 2}
}

// fdogMask returns a binary ink map (true = line pixel) via coherent line drawing.
func fdogMask(src *stylize.SrcImage, p fdogParams) []bool {
	w, h := src.W, src.H
	luma := lumaOf(src)
	tx, ty := edgeTangentFlow(luma, w, h, p.ETFRad, p.ETFIters)

	in := make([]float64, len(luma)) // working luma, sharpened each FDoG iteration
	copy(in, luma)
	var H []float64
	iters := p.FDoGIter
	if iters < 1 {
		iters = 1
	}
	for it := 0; it < iters; it++ {
		H = flowDoG(in, tx, ty, w, h, p)
		if it < iters-1 {
			// Re-inject: keep detected edges dark, push the rest toward white, so the next pass locks
			// onto the coherent lines and drops faint noise (Kang's iterative sharpening).
			for i := range in {
				v := 1 + math.Tanh(p.Phi*H[i])
				if H[i] >= 0 || v >= p.Thresh { // not (yet) an edge → bleach toward white
					if in[i] < 1 {
						in[i] = math.Min(1, in[i]+0.5*(1-in[i]))
					}
				}
			}
		}
	}
	ink := make([]bool, len(luma))
	for i := range H {
		if H[i] < 0 && 1+math.Tanh(p.Phi*H[i]) < p.Thresh {
			ink[i] = true
		}
	}
	if os.Getenv("FDOG_DEBUG") != "" {
		var hmin, hmax, hsum float64
		hmin, hmax = 1e9, -1e9
		var nink, nneg int
		for _, v := range H {
			if v < hmin {
				hmin = v
			}
			if v > hmax {
				hmax = v
			}
			hsum += v
			if v < 0 {
				nneg++
			}
		}
		for _, b := range ink {
			if b {
				nink++
			}
		}
		fmt.Fprintf(os.Stderr, "[fdog] H[min=%.4f max=%.4f mean=%.4f] neg=%d ink=%d/%d\n",
			hmin, hmax, hsum/float64(len(H)), nneg, nink, len(H))
	}
	return ink
}

// edgeTangentFlow builds a smooth unit tangent field that follows edges (Kang et al. eq.1): start from
// the gradient-perpendicular tangent, then iteratively average each tangent with its neighbours weighted
// by edge magnitude (w_m), direction alignment (w_d) and a sign that flips antiparallel tangents.
func edgeTangentFlow(luma []float64, w, h, rad, iters int) (tx, ty []float64) {
	n := w * h
	gx := make([]float64, n)
	gy := make([]float64, n)
	mag := make([]float64, n)
	at := func(x, y int) float64 {
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x >= w {
			x = w - 1
		}
		if y >= h {
			y = h - 1
		}
		return luma[y*w+x]
	}
	var maxMag float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := (at(x+1, y-1) + 2*at(x+1, y) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x-1, y) + at(x-1, y+1))
			sy := (at(x-1, y+1) + 2*at(x, y+1) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x, y-1) + at(x+1, y-1))
			gx[y*w+x], gy[y*w+x] = sx, sy
			m := math.Hypot(sx, sy)
			mag[y*w+x] = m
			if m > maxMag {
				maxMag = m
			}
		}
	}
	if maxMag <= 0 {
		maxMag = 1
	}
	tx = make([]float64, n)
	ty = make([]float64, n)
	for i := 0; i < n; i++ {
		mag[i] /= maxMag
		// tangent = gradient rotated +90° = (-gy, gx), unit.
		vx, vy := -gy[i], gx[i]
		l := math.Hypot(vx, vy)
		if l > 1e-12 {
			tx[i], ty[i] = vx/l, vy/l
		}
	}
	nx := make([]float64, n)
	ny := make([]float64, n)
	for it := 0; it < iters; it++ {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := y*w + x
				cx, cy := tx[i], ty[i]
				cmag := mag[i]
				var sx, sy float64
				for dy := -rad; dy <= rad; dy++ {
					yy := y + dy
					if yy < 0 || yy >= h {
						continue
					}
					for dx := -rad; dx <= rad; dx++ {
						xx := x + dx
						if xx < 0 || xx >= w {
							continue
						}
						if dx*dx+dy*dy > rad*rad {
							continue
						}
						j := yy*w + xx
						dot := cx*tx[j] + cy*ty[j]
						phi := 1.0
						if dot < 0 {
							phi = -1.0
						}
						wm := (1 + math.Tanh(mag[j]-cmag)) / 2 // favour flowing toward stronger edges
						wd := math.Abs(dot)                    // favour aligned neighbours
						wgt := phi * wm * wd
						sx += tx[j] * wgt
						sy += ty[j] * wgt
					}
				}
				l := math.Hypot(sx, sy)
				if l > 1e-12 {
					nx[i], ny[i] = sx/l, sy/l
				} else {
					nx[i], ny[i] = cx, cy
				}
			}
		}
		copy(tx, nx)
		copy(ty, ny)
	}
	return tx, ty
}

// flowDoG applies the DoG across the flow then accumulates it along the flow (Kang et al. eq.5–8).
func flowDoG(luma, tx, ty []float64, w, h int, p fdogParams) []float64 {
	n := w * h
	sigmaS := p.SigmaC * p.K
	tg := int(math.Ceil(3 * sigmaS)) // gradient-direction half-extent
	sm := int(math.Ceil(3 * p.SigmaM))
	sample := func(x, y float64) float64 { return sampleBilinear(luma, w, h, x, y) }

	// Stage 1: 1D DoG across the flow (along the gradient direction = tangent rotated −90°).
	hg := make([]float64, n)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			gxd, gyd := ty[i], -tx[i] // gradient direction ⟂ tangent
			if gxd == 0 && gyd == 0 {
				gxd = 1
			}
			var acc float64
			for t := -tg; t <= tg; t++ {
				ft := gauss1(float64(t), p.SigmaC) - p.Tau*gauss1(float64(t), sigmaS)
				v := sample(float64(x)+float64(t)*gxd, float64(y)+float64(t)*gyd)
				acc += ft * v
			}
			hg[i] = acc // un-normalised DoG response (negative at a dark line on a light ground)
		}
	}

	// Stage 2: accumulate Hg along the flow (line integral convolution with a Gaussian).
	out := make([]float64, n)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			g0 := gauss1(0, p.SigmaM)
			acc := g0 * hg[i]
			wsum := g0
			for _, dir := range [2]float64{1, -1} {
				px, py := float64(x), float64(y)
				vx, vy := tx[i]*dir, ty[i]*dir
				for s := 1; s <= sm; s++ {
					px += vx
					py += vy
					g := gauss1(float64(s), p.SigmaM)
					acc += g * sampleBilinearPlane(hg, w, h, px, py)
					wsum += g
					// re-orient to the local flow so the walk follows curves (sign-consistent).
					ntx, nty := sampleTangent(tx, ty, w, h, px, py)
					if ntx*vx+nty*vy < 0 {
						ntx, nty = -ntx, -nty
					}
					if ntx != 0 || nty != 0 {
						vx, vy = ntx, nty
					}
				}
			}
			if wsum > 0 {
				out[i] = acc / wsum
			}
		}
	}
	return out
}

func gauss1(x, sigma float64) float64 {
	if sigma < 1e-3 {
		sigma = 1e-3 // floor: a 0 sigma (custom config) would divide by zero -> NaN kernel; the spike is normalized away by the caller's wsum
	}
	return math.Exp(-x*x/(2*sigma*sigma)) / (sigma * math.Sqrt(2*math.Pi))
}

func sampleBilinear(plane []float64, w, h int, x, y float64) float64 {
	return sampleBilinearPlane(plane, w, h, x, y)
}

func sampleBilinearPlane(plane []float64, w, h int, x, y float64) float64 {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x > float64(w-1) {
		x = float64(w - 1)
	}
	if y > float64(h-1) {
		y = float64(h - 1)
	}
	x0, y0 := int(x), int(y)
	x1, y1 := x0+1, y0+1
	if x1 >= w {
		x1 = w - 1
	}
	if y1 >= h {
		y1 = h - 1
	}
	fx, fy := x-float64(x0), y-float64(y0)
	a := plane[y0*w+x0]
	b := plane[y0*w+x1]
	c := plane[y1*w+x0]
	d := plane[y1*w+x1]
	return a*(1-fx)*(1-fy) + b*fx*(1-fy) + c*(1-fx)*fy + d*fx*fy
}

func sampleTangent(tx, ty []float64, w, h int, x, y float64) (float64, float64) {
	xi, yi := int(x+0.5), int(y+0.5)
	if xi < 0 || yi < 0 || xi >= w || yi >= h {
		return 0, 0
	}
	return tx[yi*w+xi], ty[yi*w+xi]
}
