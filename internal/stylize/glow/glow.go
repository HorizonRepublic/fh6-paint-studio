// Package glow is the stylizer's Glow engine: it recovers the SMOOTH shading that flat colour cells band
// on (cheeks, hair sheen, iris/pupil falloff) by painting a few feathered FH6 glow primitives — the
// native radial-gradient splat — over the flat base. One anisotropic glow renders a smooth gradient that
// would cost a dozen flat cells, so it is the one lever that beats the 3000-shape budget bound:
// expressiveness-per-shape (the GaussianImage / 2D-Gaussian-splatting idea, mapped 1:1 onto KindGlow).
// Deterministic greedy residual matching-pursuit — no autodiff. Registered as "glow".
package glow

import (
	"encoding/json"
	"fmt"
	"math"
	"os"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
	"fh6-paint-studio/internal/stylize"
	"fh6-paint-studio/internal/stylize/shape"
)

var dbg = os.Getenv("GLOW_DEBUG") != ""

// Config holds the Glow engine's knobs (JSON keys = preset-stage config).
type Config struct {
	K         int     `json:"k"`         // posterize palette for the flat base (match fill's K so glows refine the same cells)
	Quant     string  `json:"quant"`     // quantizer for the base
	MinErr    float64 `json:"minErr"`    // stop when the peak residual (sRGB dist) drops below this
	RMin      float64 `json:"rMin"`      // glow footprint radius growth floor (px)
	RMax      float64 `json:"rMax"`      // glow footprint radius cap (px)
	ColorTol  float64 `json:"colorTol"`  // grow the footprint while the source stays within this colour dist of the seed
	SmoothMax float64 `json:"smoothMax"` // only seed where the source gradient < this (skip edges/lines — the ink layer owns those)
	Saliency  float64 `json:"saliency"`  // bias seeding toward colourful focal detail: score = residual×(1+w·chromaSaliency); concentrates glows on eyes/lips instead of spreading over flat skin (0=off)
	Budget    int     `json:"budget"`    // hard glow cap (0 = run's remaining budget)
}

// Defaults are the starting knobs (eye-tuned on the bank).
func Defaults() Config {
	return Config{K: 14, Quant: "lab", MinErr: 0.06, RMin: 3, RMax: 48, ColorTol: 0.10, SmoothMax: 0.16, Saliency: 0, Budget: 0}
}

type engine struct{ cfg Config }

func (e *engine) Name() string { return "glow" }

func (e *engine) Generate(ctx *stylize.Context) ([]model.Shape, error) {
	src := ctx.Src
	w, h := src.W, src.H
	palette, idx := shape.QuantizeBy(e.cfg.Quant, src, e.cfg.K)
	canvas := make([]model.RGBA, w*h) // the flat base the fill stage renders (palette[idx])
	for i := range canvas {
		canvas[i] = palette[idx[i]]
	}
	grad := sourceGradient(src) // edge magnitude → keep glows off lines/edges (ink owns those)
	var sal []float64
	if e.cfg.Saliency > 0 {
		sal = stylize.ChromaSaliency(src) // concentrate glows on focal colourful detail (eyes/lips)
	}
	budget := e.cfg.Budget
	if budget <= 0 || budget > ctx.Budget {
		budget = ctx.Budget
	}
	glows := fitResidualGlows(src, canvas, grad, sal, w, h, e.cfg)
	if len(glows) > budget {
		glows = glows[:budget]
	}
	if dbg {
		fmt.Fprintf(os.Stderr, "[glow] glows=%d budget=%d\n", len(glows), budget)
	}
	return glows, nil
}

// fitResidualGlows greedily paints feathered glows of the local source colour over canvas (the flat base)
// to recover smooth shading. Each step: find the highest-residual SMOOTH pixel, place a glow there sized
// to the local same-colour neighbourhood, composite it into canvas (FH6 glow falloff), repeat. canvas is
// mutated to the running reconstruction. Deterministic (max-residual seeding, closed-form radius).
func fitResidualGlows(src *stylize.SrcImage, canvas []model.RGBA, grad, sal []float64, w, h int, cfg Config) []model.Shape {
	n := w * h
	budget := cfg.Budget
	if budget <= 0 {
		budget = n
	}
	err := make([]float64, n)
	for i := 0; i < n; i++ {
		err[i] = colorDist(src.Pix[i], canvas[i])
	}
	var out []model.Shape
	for len(out) < budget {
		// Seed at the highest-residual smooth pixel, optionally biased toward focal colourful detail so
		// the glow budget concentrates on eyes/lips rather than spreading over flat skin.
		best, bi := 0.0, -1
		for i := 0; i < n; i++ {
			if grad[i] > cfg.SmoothMax { // skip edges/lines
				continue
			}
			if err[i] < cfg.MinErr { // gate on raw residual so flat areas spend nothing
				continue
			}
			score := err[i]
			if sal != nil {
				score *= 1 + cfg.Saliency*sal[i]
			}
			if score > best {
				best, bi = score, i
			}
		}
		if bi < 0 {
			break
		}
		px, py := bi%w, bi/w
		seed := src.Pix[bi]
		r := growRadius(src, w, h, px, py, seed, cfg)
		c := model.Candidate{Kind: model.KindGlow,
			P:     [6]float32{float32(px) + 0.5, float32(py) + 0.5, float32(r), float32(r), 0, 0},
			Color: model.RGBA{R: seed.R, G: seed.G, B: seed.B, A: 1}}
		out = append(out, c.ToShape(0))
		compositeGlow(canvas, err, src, w, h, px, py, r, seed)
	}
	return out
}

// growRadius expands an isotropic footprint from RMin while the source colour out at that radius stays
// within ColorTol of the seed (so a glow stays inside one smooth-shading patch and never bleeds across a
// colour boundary). Capped at RMax.
func growRadius(src *stylize.SrcImage, w, h, px, py int, seed model.RGBA, cfg Config) float64 {
	r := cfg.RMin
	for r < cfg.RMax {
		nr := r + 2
		if ringDeviates(src, w, h, px, py, nr, seed, cfg.ColorTol) {
			break
		}
		r = nr
	}
	return r
}

// ringDeviates reports whether the mean source colour on the circle of radius r around (px,py) is more
// than tol from seed (a colour boundary → stop growing).
func ringDeviates(src *stylize.SrcImage, w, h, px, py int, r float64, seed model.RGBA, tol float64) bool {
	const steps = 16
	var sr, sg, sb float64
	cnt := 0
	for k := 0; k < steps; k++ {
		a := 2 * math.Pi * float64(k) / steps
		x := px + int(math.Round(r*math.Cos(a)))
		y := py + int(math.Round(r*math.Sin(a)))
		if x < 0 || y < 0 || x >= w || y >= h {
			continue
		}
		p := src.Pix[y*w+x]
		sr += float64(p.R)
		sg += float64(p.G)
		sb += float64(p.B)
		cnt++
	}
	if cnt == 0 {
		return true
	}
	dr := sr/float64(cnt) - float64(seed.R)
	dg := sg/float64(cnt) - float64(seed.G)
	db := sb/float64(cnt) - float64(seed.B)
	return math.Sqrt(dr*dr+dg*dg+db*db) > tol
}

// compositeGlow alpha-composites a glow (colour=seed, FH6 KindGlow falloff, radius r) into canvas over its
// footprint and refreshes err there. a = FalloffGlow(t), t = elliptical normalised radius (isotropic here).
func compositeGlow(canvas []model.RGBA, err []float64, src *stylize.SrcImage, w, h, px, py int, r float64, seed model.RGBA) {
	if r < 1 {
		r = 1 // floor: r=0 (custom rMin with no growth) would divide by zero at t = hypot/r -> NaN canvas
	}
	ri := int(math.Ceil(r))
	for dy := -ri; dy <= ri; dy++ {
		y := py + dy
		if y < 0 || y >= h {
			continue
		}
		for dx := -ri; dx <= ri; dx++ {
			x := px + dx
			if x < 0 || x >= w {
				continue
			}
			t := math.Hypot(float64(dx), float64(dy)) / r
			a := raster.FalloffGlow(t)
			if a <= 0 {
				continue
			}
			i := y*w + x
			cv := canvas[i]
			af := float32(a)
			canvas[i] = model.RGBA{
				R: cv.R*(1-af) + seed.R*af,
				G: cv.G*(1-af) + seed.G*af,
				B: cv.B*(1-af) + seed.B*af,
				A: 1,
			}
			err[i] = colorDist(src.Pix[i], canvas[i])
		}
	}
}

// sourceGradient returns a normalised Sobel luma-gradient magnitude per pixel (0..~1) — high at edges/lines.
func sourceGradient(src *stylize.SrcImage) []float64 {
	w, h := src.W, src.H
	luma := func(x, y int) float64 {
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
		p := src.Pix[y*w+x]
		return 0.299*float64(p.R) + 0.587*float64(p.G) + 0.114*float64(p.B)
	}
	g := make([]float64, w*h)
	var mx float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			gx := (luma(x+1, y-1) + 2*luma(x+1, y) + luma(x+1, y+1)) - (luma(x-1, y-1) + 2*luma(x-1, y) + luma(x-1, y+1))
			gy := (luma(x-1, y+1) + 2*luma(x, y+1) + luma(x+1, y+1)) - (luma(x-1, y-1) + 2*luma(x, y-1) + luma(x+1, y-1))
			m := math.Hypot(gx, gy)
			g[y*w+x] = m
			if m > mx {
				mx = m
			}
		}
	}
	if mx > 0 {
		for i := range g {
			g[i] /= mx
		}
	}
	return g
}

func colorDist(a, b model.RGBA) float64 {
	dr := float64(a.R - b.R)
	dg := float64(a.G - b.G)
	db := float64(a.B - b.B)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

func init() {
	stylize.RegisterEngine("glow", func(cfg json.RawMessage) (stylize.Engine, error) {
		c := Defaults()
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &c); err != nil {
				return nil, err
			}
		}
		return &engine{cfg: c}, nil
	})
}
