package stroke

import (
	"math"

	"fh6-paint-studio/internal/stylize"
)

// lumaOf extracts Rec.601 luminance (0..1) from a source image.
func lumaOf(src *stylize.SrcImage) []float64 {
	l := make([]float64, len(src.Pix))
	for i, p := range src.Pix {
		l[i] = 0.299*float64(p.R) + 0.587*float64(p.G) + 0.114*float64(p.B)
	}
	return l
}

// gaussBlur separably blurs a float plane with a Gaussian of the given sigma (edges clamped).
func gaussBlur(src []float64, w, h int, sigma float64) []float64 {
	if sigma <= 0 {
		out := make([]float64, len(src))
		copy(out, src)
		return out
	}
	r := int(math.Ceil(sigma * 3))
	k := make([]float64, 2*r+1)
	var sum float64
	for i := -r; i <= r; i++ {
		v := math.Exp(-float64(i*i) / (2 * sigma * sigma))
		k[i+r] = v
		sum += v
	}
	for i := range k {
		k[i] /= sum
	}
	clamp := func(v, lo, hi int) int {
		if v < lo {
			return lo
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
			for i := -r; i <= r; i++ {
				acc += src[y*w+clamp(x+i, 0, w-1)] * k[i+r]
			}
			tmp[y*w+x] = acc
		}
	}
	out := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var acc float64
			for i := -r; i <= r; i++ {
				acc += tmp[clamp(y+i, 0, h-1)*w+x] * k[i+r]
			}
			out[y*w+x] = acc
		}
	}
	return out
}

// xdogParams are the eXtended Difference-of-Gaussians line knobs (Winnemöller, Kyprianidis & Olsen 2012).
type xdogParams struct {
	Sigma float64 // base Gaussian (≈ line scale), px
	K     float64 // second-Gaussian ratio (1.6 ≈ LoG)
	Tau   float64 // inhibition (≈0.98)
	Eps   float64 // ink threshold on the DoG response
	Phi   float64 // tanh edge sharpness (bigger = harder line)
}

func defaultXDoG() xdogParams { return xdogParams{Sigma: 1.0, K: 1.6, Tau: 0.985, Eps: 0.0, Phi: 60} }

// xdogMask returns a binary ink map (true = line pixel) from a source image: the extended DoG
// D = G_σ - τ·G_{kσ} on luminance, soft-thresholded by T_{ε,φ}. Responds to the ink itself (a dark
// band on a light ground), so a later thinning collapses each band to one centerline.
func xdogMask(src *stylize.SrcImage, p xdogParams) []bool {
	luma := lumaOf(src)
	g1 := gaussBlur(luma, src.W, src.H, p.Sigma)
	g2 := gaussBlur(luma, src.W, src.H, p.K*p.Sigma)
	ink := make([]bool, len(luma))
	for i := range luma {
		d := g1[i] - p.Tau*g2[i]
		var t float64
		if d >= p.Eps {
			t = 1
		} else {
			t = 1 + math.Tanh(p.Phi*(d-p.Eps))
		}
		ink[i] = t < 0.5
	}
	return ink
}
