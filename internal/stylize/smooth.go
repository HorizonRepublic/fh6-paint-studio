package stylize

import (
	"math"

	"fh6-paint-studio/internal/model"
)

// SmoothConfig controls the edge-preserving pre-smooth applied to the source before the engines
// quantize it. Method "dt" = Domain Transform recursive filter (Gastal & Oliveira 2011): O(N),
// edge-stopping, the default — flattens soft anime shading + JPEG noise into clean cells while keeping
// strong colour edges (so quantize yields whole cells, not fragments). Method "bilateral" = the simpler
// iterated bilateral fallback. Spatial = spatial sigma (px; for DT the geodesic σ_s, larger = flatter),
// Range = colour sigma (RGB distance; smaller = preserves weaker edges), Iters = passes (DT: 3 is the knee).
type SmoothConfig struct {
	Method  string  `json:"method"`
	Spatial float64 `json:"spatial"`
	Range   float64 `json:"range"`
	Iters   int     `json:"iters"`
}

// Smooth returns a new SrcImage with the edge-preserving pre-smooth applied (original untouched). A
// zero/empty config is a no-op pass-through. This is the root fix for the noisy/fragmented look:
// flattening soft shading into clean near-flat regions before segmentation (hair especially).
func Smooth(src *SrcImage, c SmoothConfig) *SrcImage {
	if c.Iters <= 0 || c.Spatial <= 0 {
		return src
	}
	var out []model.RGBA
	switch c.Method {
	case "bilateral":
		out = bilateral(src.Pix, src.W, src.H, c.Spatial, c.Range, c.Iters)
	case "dtadaptive": // saliency-adaptive DT: smooths flat regions hard, spares colourful detail (eyes)
		out = domainTransformRFAdaptive(src.Pix, src.W, src.H, c.Spatial, c.Range, c.Iters, 8.0)
	default: // "dt" / "" — domain transform recursive filter
		out = domainTransformRF(src.Pix, src.W, src.H, c.Spatial, c.Range, c.Iters)
	}
	return &SrcImage{W: src.W, H: src.H, Pix: out}
}

// domainTransformRF is the edge-aware recursive filter of Gastal & Oliveira (SIGGRAPH 2011). It warps
// each scanline so colour edges become far apart, then runs a 1-D IIR low-pass that cannot cross them.
// Horizontal then vertical pass per iteration; the transform derivatives are computed once from the
// input and reused at every scale. Pure-Go, O(Iters·W·H·channels), deterministic.
func domainTransformRF(pix []model.RGBA, w, h int, sigmaS, sigmaR float64, N int) []model.RGBA {
	if sigmaR <= 0 {
		sigmaR = 0.5
	}
	n := w * h
	r := make([]float64, n)
	g := make([]float64, n)
	b := make([]float64, n)
	for i, p := range pix {
		r[i], g[i], b[i] = float64(p.R), float64(p.G), float64(p.B)
	}
	ratio := sigmaS / sigmaR
	// dH[i] = 1 + ratio*Σ_c|I(x,y)-I(x-1,y)|  (horizontal neighbour distance); dV[i] likewise vertical.
	dH := make([]float64, n)
	dV := make([]float64, n)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			dH[i] = 1
			if x > 0 {
				dH[i] = 1 + ratio*(math.Abs(r[i]-r[i-1])+math.Abs(g[i]-g[i-1])+math.Abs(b[i]-b[i-1]))
			}
			dV[i] = 1
			if y > 0 {
				dV[i] = 1 + ratio*(math.Abs(r[i]-r[i-w])+math.Abs(g[i]-g[i-w])+math.Abs(b[i]-b[i-w]))
			}
		}
	}
	pow4N := math.Pow(4, float64(N))
	for it := 1; it <= N; it++ {
		sigmaH := sigmaS * math.Sqrt(3) * math.Pow(2, float64(N-it)) / math.Sqrt(pow4N-1)
		lna := -math.Sqrt2 / sigmaH // ln(a), a = exp(-sqrt2/sigmaH)
		// horizontal: L->R then R->L, per row
		for y := 0; y < h; y++ {
			base := y * w
			for x := 1; x < w; x++ {
				i := base + x
				ad := math.Exp(dH[i] * lna)
				r[i] += ad * (r[i-1] - r[i])
				g[i] += ad * (g[i-1] - g[i])
				b[i] += ad * (b[i-1] - b[i])
			}
			for x := w - 2; x >= 0; x-- {
				i := base + x
				ad := math.Exp(dH[i+1] * lna)
				r[i] += ad * (r[i+1] - r[i])
				g[i] += ad * (g[i+1] - g[i])
				b[i] += ad * (b[i+1] - b[i])
			}
		}
		// vertical: T->B then B->T, per column
		for x := 0; x < w; x++ {
			for y := 1; y < h; y++ {
				i := y*w + x
				ad := math.Exp(dV[i] * lna)
				r[i] += ad * (r[i-w] - r[i])
				g[i] += ad * (g[i-w] - g[i])
				b[i] += ad * (b[i-w] - b[i])
			}
			for y := h - 2; y >= 0; y-- {
				i := y*w + x
				ad := math.Exp(dV[i+w] * lna)
				r[i] += ad * (r[i+w] - r[i])
				g[i] += ad * (g[i+w] - g[i])
				b[i] += ad * (b[i+w] - b[i])
			}
		}
	}
	out := make([]model.RGBA, n)
	for i := range out {
		out[i] = model.RGBA{R: float32(r[i]), G: float32(g[i]), B: float32(b[i]), A: pix[i].A}
	}
	return out
}

// ChromaSaliency is the exported chroma-variance saliency map (high where colourful detail varies — eyes,
// lips), used by the fill engine to bias budget toward small important regions. rad 3.
func ChromaSaliency(src *SrcImage) []float64 {
	return chromaVarianceSaliency(src.Pix, src.W, src.H, 3)
}

// chromaVarianceSaliency returns a [0,1] map that is high where COLOUR (not luminance) varies locally —
// eye irises, lips, small colourful accents — and low on flat or only-luma-varying regions (hair, skin).
// It is the discriminator that lets the adaptive smooth spare eyes while still flattening hair. Computed
// as the local variance of two opponent-colour channels via separable box sums (O(N)).
func chromaVarianceSaliency(pix []model.RGBA, w, h, rad int) []float64 {
	n := w * h
	o1 := make([]float64, n) // R−G
	o2 := make([]float64, n) // B − (R+G)/2
	for i, p := range pix {
		o1[i] = float64(p.R - p.G)
		o2[i] = float64(p.B) - 0.5*float64(p.R+p.G)
	}
	boxVar := func(v []float64) []float64 {
		mean := boxBlur1(v, w, h, rad)
		v2 := make([]float64, n)
		for i := range v {
			v2[i] = v[i] * v[i]
		}
		mean2 := boxBlur1(v2, w, h, rad)
		out := make([]float64, n)
		for i := range out {
			if d := mean2[i] - mean[i]*mean[i]; d > 0 {
				out[i] = d
			}
		}
		return out
	}
	va, vb := boxVar(o1), boxVar(o2)
	sal := make([]float64, n)
	var max float64
	for i := range sal {
		sal[i] = math.Sqrt(va[i] + vb[i])
		if sal[i] > max {
			max = sal[i]
		}
	}
	if max > 0 {
		for i := range sal {
			sal[i] /= max // normalise to [0,1]
		}
	}
	return sal
}

// boxBlur1 is a separable box blur (radius rad) on a single float plane; edges clamp.
func boxBlur1(src []float64, w, h, rad int) []float64 {
	clamp := func(v, hi int) int {
		if v < 0 {
			return 0
		}
		if v > hi {
			return hi
		}
		return v
	}
	tmp := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc float64
			for i := -rad; i <= rad; i++ {
				acc += src[y*w+clamp(x+i, w-1)]
			}
			tmp[y*w+x] = acc / float64(2*rad+1)
		}
	}
	out := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc float64
			for i := -rad; i <= rad; i++ {
				acc += tmp[clamp(y+i, h-1)*w+x]
			}
			out[y*w+x] = acc / float64(2*rad+1)
		}
	}
	return out
}

// domainTransformRFAdaptive is domainTransformRF whose per-pixel edge distances dH/dV are amplified by a
// chroma-variance saliency map: where colourful detail lives (eyes, lips), the transform sees "stronger
// edges" and barely smooths, preserving the colour; flat/hair regions smooth normally. salBoost scales
// the effect.
func domainTransformRFAdaptive(pix []model.RGBA, w, h int, sigmaS, sigmaR float64, N int, salBoost float64) []model.RGBA {
	if sigmaR <= 0 {
		sigmaR = 0.5
	}
	n := w * h
	r := make([]float64, n)
	g := make([]float64, n)
	b := make([]float64, n)
	for i, p := range pix {
		r[i], g[i], b[i] = float64(p.R), float64(p.G), float64(p.B)
	}
	sal := chromaVarianceSaliency(pix, w, h, 3)
	ratio := sigmaS / sigmaR
	dH := make([]float64, n)
	dV := make([]float64, n)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			boost := 1 + salBoost*sal[i] // colourful detail → bigger d → less smoothing here
			dH[i] = boost
			if x > 0 {
				dH[i] = boost * (1 + ratio*(math.Abs(r[i]-r[i-1])+math.Abs(g[i]-g[i-1])+math.Abs(b[i]-b[i-1])))
			}
			dV[i] = boost
			if y > 0 {
				dV[i] = boost * (1 + ratio*(math.Abs(r[i]-r[i-w])+math.Abs(g[i]-g[i-w])+math.Abs(b[i]-b[i-w])))
			}
		}
	}
	pow4N := math.Pow(4, float64(N))
	for it := 1; it <= N; it++ {
		sigmaH := sigmaS * math.Sqrt(3) * math.Pow(2, float64(N-it)) / math.Sqrt(pow4N-1)
		lna := -math.Sqrt2 / sigmaH
		for y := 0; y < h; y++ {
			base := y * w
			for x := 1; x < w; x++ {
				i := base + x
				ad := math.Exp(dH[i] * lna)
				r[i] += ad * (r[i-1] - r[i])
				g[i] += ad * (g[i-1] - g[i])
				b[i] += ad * (b[i-1] - b[i])
			}
			for x := w - 2; x >= 0; x-- {
				i := base + x
				ad := math.Exp(dH[i+1] * lna)
				r[i] += ad * (r[i+1] - r[i])
				g[i] += ad * (g[i+1] - g[i])
				b[i] += ad * (b[i+1] - b[i])
			}
		}
		for x := 0; x < w; x++ {
			for y := 1; y < h; y++ {
				i := y*w + x
				ad := math.Exp(dV[i] * lna)
				r[i] += ad * (r[i-w] - r[i])
				g[i] += ad * (g[i-w] - g[i])
				b[i] += ad * (b[i-w] - b[i])
			}
			for y := h - 2; y >= 0; y-- {
				i := y*w + x
				ad := math.Exp(dV[i+w] * lna)
				r[i] += ad * (r[i+w] - r[i])
				g[i] += ad * (g[i+w] - g[i])
				b[i] += ad * (b[i+w] - b[i])
			}
		}
	}
	out := make([]model.RGBA, n)
	for i := range out {
		out[i] = model.RGBA{R: float32(r[i]), G: float32(g[i]), B: float32(b[i]), A: pix[i].A}
	}
	return out
}

func bilateral(pix []model.RGBA, w, h int, spatial, rng float64, iters int) []model.RGBA {
	r := int(math.Ceil(spatial * 2))
	side := 2*r + 1
	sw := make([]float64, side*side)
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			sw[(dy+r)*side+(dx+r)] = math.Exp(-float64(dx*dx+dy*dy) / (2 * spatial * spatial))
		}
	}
	if rng <= 0 {
		rng = 0.1
	}
	inv := 1.0 / (2 * rng * rng)
	cur := pix
	for it := 0; it < iters; it++ {
		out := make([]model.RGBA, len(cur))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				cp := cur[y*w+x]
				var sr, sg, sb, wsum float64
				for dy := -r; dy <= r; dy++ {
					yy := y + dy
					if yy < 0 || yy >= h {
						continue
					}
					row := yy * w
					srow := (dy + r) * side
					for dx := -r; dx <= r; dx++ {
						xx := x + dx
						if xx < 0 || xx >= w {
							continue
						}
						q := cur[row+xx]
						dr, dg, db := float64(cp.R-q.R), float64(cp.G-q.G), float64(cp.B-q.B)
						wt := sw[srow+(dx+r)] * math.Exp(-(dr*dr+dg*dg+db*db)*inv)
						sr += float64(q.R) * wt
						sg += float64(q.G) * wt
						sb += float64(q.B) * wt
						wsum += wt
					}
				}
				if wsum > 0 {
					out[y*w+x] = model.RGBA{R: float32(sr / wsum), G: float32(sg / wsum), B: float32(sb / wsum), A: cp.A}
				} else {
					out[y*w+x] = cp
				}
			}
		}
		cur = out
	}
	return cur
}
