package stylize

import (
	"encoding/json"
	"fmt"
	"math"

	"fh6-paint-studio/internal/model"
)

// style.go — content-style analysis + the "auto" preset. Rather than slap one brittle style LABEL on an
// image (which mislabels boundary cases and silently ruins them — the geometrize content-classifier's
// documented failure), Analyze measures continuous features and AutoPreset decides EACH knob
// independently from the feature most relevant to it. A wrong call on one feature flips one knob, not the
// whole pipeline, so degradation is graceful. Features are computed on a resolution-normalised downscale.

// StyleFeatures are the content descriptors that separate line-art / cel / painterly / hatched / busy.
type StyleFeatures struct {
	Sat    float64 // mean chroma (max−min RGB): line-art ≈0, colourful ≥0.1
	White  float64 // near-white, low-chroma fraction: line-art high
	Edges  float64 // strong-gradient fraction: busy/hatched high, flat-cel low
	Flat   float64 // near-zero-gradient fraction: flat cel high
	Colors float64 // distinct 3-bit/chan bins: palette richness
	Fine   float64 // mean laplacian magnitude: hatching / fine texture energy
}

// Analyze computes the style features on a resolution-normalised (≤512 longest side) copy of src so the
// gradient-based features are comparable across input sizes.
func Analyze(src *SrcImage) StyleFeatures {
	s := downscaleForAnalysis(src, 512)
	w, h := s.W, s.H
	n := w * h
	if n == 0 {
		return StyleFeatures{}
	}
	lum := make([]float64, n)
	var satSum float64
	var whiteN int
	bins := make(map[int]struct{}, 512)
	for i, p := range s.Pix {
		hi := max3(p.R, p.G, p.B)
		lo := min3(p.R, p.G, p.B)
		sat := float64(hi - lo)
		satSum += sat
		lum[i] = 0.299*float64(p.R) + 0.587*float64(p.G) + 0.114*float64(p.B)
		if lum[i] > 0.85 && sat < 0.10 {
			whiteN++
		}
		bins[int(p.R*7)<<6|int(p.G*7)<<3|int(p.B*7)] = struct{}{}
	}
	var edgeN, flatN int
	var fineSum float64
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			i := y*w + x
			g := math.Hypot(lum[i+1]-lum[i-1], lum[i+w]-lum[i-w])
			if g > 0.12 {
				edgeN++
			}
			if g < 0.02 {
				flatN++
			}
			fineSum += math.Abs(4*lum[i] - lum[i-1] - lum[i+1] - lum[i-w] - lum[i+w])
		}
	}
	den := float64((w - 2) * (h - 2))
	if den < 1 {
		den = 1
	}
	return StyleFeatures{
		Sat:    satSum / float64(n),
		White:  float64(whiteN) / float64(n),
		Edges:  float64(edgeN) / den,
		Flat:   float64(flatN) / den,
		Colors: float64(len(bins)),
		Fine:   fineSum / den,
	}
}

// AutoPreset assembles a per-content-tuned anime preset (flat fill + coherent ink) by deciding each knob
// independently from the features. Defaults match the plain `anime` preset; the rules only deviate where
// a feature clearly calls for it.
func AutoPreset(f StyleFeatures) Preset {
	method := "fdog" // coherent lines by default
	if f.Sat < 0.06 && f.White < 0.6 && f.Edges > 0.18 {
		method = "xdog" // monochrome, non-white, dense edges = hatching/pencil: keep the texture
	}
	quant := "lab"
	if f.Sat > 0.08 && f.Flat > 0.6 && f.Edges < 0.18 {
		quant = "labvivid" // clean cel: weight chroma so flat colour cells stay vivid
	}
	smooth := "dt"
	if f.Sat > 0.09 && f.Edges < 0.16 && f.Flat > 0.6 {
		smooth = "dtadaptive" // clean & colourful: spare the eyes/colour detail from over-smoothing
	}
	fillCfg, _ := json.Marshal(map[string]any{"quant": quant, "budget": 2400})
	inkCfg, _ := json.Marshal(map[string]any{"method": method, "thresh": 0.75})
	return Preset{
		Name:   "auto",
		Smooth: &SmoothConfig{Method: smooth, Spatial: 32, Range: 0.9, Iters: 4},
		Stages: []Stage{
			{Engine: "fill", Config: fillCfg},
			{Engine: "ink", Config: inkCfg},
		},
	}
}

// String renders the features + the knobs AutoPreset would choose — for the studio "Auto" readout / debug.
func (f StyleFeatures) String() string {
	p := AutoPreset(f)
	return fmt.Sprintf("sat=%.3f white=%.2f edges=%.2f flat=%.2f colors=%.0f fine=%.3f → smooth=%s fill=%s ink=%s",
		f.Sat, f.White, f.Edges, f.Flat, f.Colors, f.Fine, p.Smooth.Method, string(p.Stages[0].Config), string(p.Stages[1].Config))
}

// downscaleForAnalysis box-averages src down so its longest side ≤ target (no-op if already small).
func downscaleForAnalysis(src *SrcImage, target int) *SrcImage {
	long := src.W
	if src.H > long {
		long = src.H
	}
	if long <= target {
		return src
	}
	nw := src.W * target / long
	nh := src.H * target / long
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	sx := float64(src.W) / float64(nw)
	sy := float64(src.H) / float64(nh)
	out := &SrcImage{W: nw, H: nh, Pix: make([]model.RGBA, nw*nh)}
	for oy := 0; oy < nh; oy++ {
		y0, y1 := int(float64(oy)*sy), int(float64(oy+1)*sy)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for ox := 0; ox < nw; ox++ {
			x0, x1 := int(float64(ox)*sx), int(float64(ox+1)*sx)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, b float32
			cnt := 0
			for y := y0; y < y1 && y < src.H; y++ {
				for x := x0; x < x1 && x < src.W; x++ {
					p := src.Pix[y*src.W+x]
					r, g, b = r+p.R, g+p.G, b+p.B
					cnt++
				}
			}
			if cnt > 0 {
				inv := 1.0 / float32(cnt)
				out.Pix[oy*nw+ox] = model.RGBA{R: r * inv, G: g * inv, B: b * inv, A: 1}
			}
		}
	}
	return out
}

func max3(a, b, c float32) float32 {
	if b > a {
		a = b
	}
	if c > a {
		a = c
	}
	return a
}

func min3(a, b, c float32) float32 {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
