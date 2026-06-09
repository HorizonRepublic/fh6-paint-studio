package shape

import (
	"math"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
)

// Quantizer reduces an image to k palette colours plus a per-pixel index. The Fill and Stroke engines
// pick one by name (Config.Quant) — a DI seam so new quantizers register without touching the engines.
type Quantizer func(src *stylize.SrcImage, k int) (palette []model.RGBA, idx []int)

var quantizers = map[string]Quantizer{}

// RegisterQuantizer adds a named quantizer (idempotent overwrite).
func RegisterQuantizer(name string, q Quantizer) { quantizers[name] = q }

// QuantizeBy runs the named quantizer, falling back to median-cut for an unknown/empty name.
func QuantizeBy(name string, src *stylize.SrcImage, k int) ([]model.RGBA, []int) {
	if q, ok := quantizers[name]; ok {
		return q(src, k)
	}
	return Quantize(src, k)
}

func init() {
	RegisterQuantizer("median", Quantize)
	RegisterQuantizer("lab", QuantizeLab)
	// labvivid weights chroma over luminance so small saturated regions (eye irises, lips) keep their
	// own cluster instead of collapsing into a luma-similar grey — the "dead eyes" fix.
	RegisterQuantizer("labvivid", func(src *stylize.SrcImage, k int) ([]model.RGBA, []int) {
		return quantizeLabW(src, k, 2.2)
	})
}

// QuantizeLab is perceptual k-means in CIELab, seeded by median-cut (deterministic — no RNG). Lab
// distance matches human colour perception, so clusters split where the eye sees a colour change,
// giving cleaner, less fragmented regions than raw-RGB median-cut on smooth anime shading. The palette
// colour of each cluster is its members' mean sRGB (no inverse Lab transform needed).
func QuantizeLab(src *stylize.SrcImage, k int) (palette []model.RGBA, idx []int) {
	return quantizeLabW(src, k, 1.0)
}

// quantizeLabW is QuantizeLab with a chroma weight w on the a,b axes of the distance (w=1 is plain Lab).
// w>1 makes clusters split more by hue/chroma and less by luminance, so saturated regions survive.
func quantizeLabW(src *stylize.SrcImage, k int, w float32) (palette []model.RGBA, idx []int) {
	if k < 1 {
		k = 1
	}
	mcPal, _ := Quantize(src, k) // median-cut seed
	K := len(mcPal)
	cent := make([][3]float32, K)
	for i, c := range mcPal {
		cent[i] = srgbToLab(c)
	}
	step := 1
	if n := len(src.Pix); n > 24000 {
		step = n / 24000
	}
	var samp []model.RGBA
	var sampLab [][3]float32
	for i := 0; i < len(src.Pix); i += step {
		samp = append(samp, src.Pix[i])
		sampLab = append(sampLab, srgbToLab(src.Pix[i]))
	}
	for it := 0; it < 12; it++ {
		sumL := make([]float64, K)
		sumA := make([]float64, K)
		sumB := make([]float64, K)
		cnt := make([]int, K)
		for si := range sampLab {
			j := nearestLab(sampLab[si], cent, w)
			cnt[j]++
			sumL[j] += float64(sampLab[si][0])
			sumA[j] += float64(sampLab[si][1])
			sumB[j] += float64(sampLab[si][2])
		}
		for j := 0; j < K; j++ {
			if cnt[j] == 0 {
				continue
			}
			n := float64(cnt[j])
			cent[j] = [3]float32{float32(sumL[j] / n), float32(sumA[j] / n), float32(sumB[j] / n)}
		}
	}
	sumR := make([]float64, K)
	sumG := make([]float64, K)
	sumB := make([]float64, K)
	cnt := make([]int, K)
	for si := range samp {
		j := nearestLab(sampLab[si], cent, w)
		cnt[j]++
		sumR[j] += float64(samp[si].R)
		sumG[j] += float64(samp[si].G)
		sumB[j] += float64(samp[si].B)
	}
	palette = make([]model.RGBA, K)
	for j := 0; j < K; j++ {
		if cnt[j] == 0 {
			palette[j] = mcPal[j] // empty cluster keeps its seed colour
			continue
		}
		n := float64(cnt[j])
		palette[j] = model.RGBA{R: float32(sumR[j] / n), G: float32(sumG[j] / n), B: float32(sumB[j] / n), A: 1}
	}
	idx = make([]int, len(src.Pix))
	for i, p := range src.Pix {
		idx[i] = nearestLab(srgbToLab(p), cent, w)
	}
	return palette, idx
}

func nearestLab(p [3]float32, cent [][3]float32, w float32) int {
	best, bd := 0, float32(math.MaxFloat32)
	for j := range cent {
		dl, da, db := p[0]-cent[j][0], p[1]-cent[j][1], p[2]-cent[j][2]
		if d := dl*dl + w*w*(da*da+db*db); d < bd {
			bd, best = d, j
		}
	}
	return best
}

// srgbToLab converts an sRGB [0,1] colour to CIELab (D65).
func srgbToLab(c model.RGBA) [3]float32 {
	r := float64(model.SRGBToLinear(c.R))
	g := float64(model.SRGBToLinear(c.G))
	b := float64(model.SRGBToLinear(c.B))
	x := (0.4124*r + 0.3576*g + 0.1805*b) / 0.95047
	y := 0.2126*r + 0.7152*g + 0.0722*b
	z := (0.0193*r + 0.1192*g + 0.9505*b) / 1.08883
	fx, fy, fz := labf(x), labf(y), labf(z)
	return [3]float32{float32(116*fy - 16), float32(500 * (fx - fy)), float32(200 * (fy - fz))}
}

func labf(t float64) float64 {
	if t > 0.008856 {
		return math.Cbrt(t)
	}
	return 7.787*t + 16.0/116.0
}
