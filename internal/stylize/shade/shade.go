// Package shade is the stylizer's Shade engine: it recovers the soft tonal shading that the flat Fill
// engine throws away when it posterizes the image into constant-colour cells. It is the middle band of a
// frequency separation — flat colour (fill) + soft shading (shade) + ink lines (ink) — the way digital
// painters work.
//
// Mechanism (the project's durable "smoothness = semi-transparency, NOT a gradient primitive" finding):
// reproduce fill's flat quantization, take the per-pixel luma residual (source − flat), and where the
// source is meaningfully darker / lighter than its flat cell, lay TRANSLUCENT triangles of the local
// source colour. Stacking a couple of low-alpha bands shifts those pixels back toward their true tone
// with low-contrast steps, so a posterized gradient reads as smooth — using only the triangle primitive
// (no new primitive, no inject change). Registered as "shade"; import for side-effect.
package shade

import (
	"encoding/json"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
	"fh6-paint-studio/internal/stylize/shape"
)

// Config holds the Shade engine's eye+metric-tunable knobs (JSON keys are the preset-stage config).
type Config struct {
	Quant   string  `json:"quant"`   // quantizer to reproduce fill's flat cells ("lab"); MUST match the fill stage
	K       int     `json:"k"`       // palette size; MUST match the fill stage
	Levels  int     `json:"levels"`  // tonal bands per sign (shadow/highlight); deeper bands are subsets
	T       float64 `json:"t"`       // base luma-residual threshold; band k fires where |residual| > T*(k+1)
	Alpha   float64 `json:"alpha"`   // per-band overlay opacity (stacks across bands)
	MinArea int     `json:"minArea"` // drop shade blobs smaller than this (px)
	TriEps  float64 `json:"triEps"`  // triangle-cover simplify tolerance (px)
	Dilate  int     `json:"dilate"`  // triangle-cover dilation (px); 1 closes seams without bloating
	Budget  int     `json:"budget"`  // hard shape cap (0 = use the run's remaining budget)
}

// Defaults are the starting knobs; tuned by eye + ΔE/SSIM on the image bank.
func Defaults() Config {
	return Config{Quant: "lab", K: 14, Levels: 2, T: 0.05, Alpha: 0.30, MinArea: 10,
		TriEps: 1.5, Dilate: 1, Budget: 0}
}

type engine struct{ cfg Config }

func (e *engine) Name() string { return "shade" }

// band is one tonal layer: a connected blob of pixels deviating from their flat cell in one direction,
// to be painted as translucent triangles of its mean source colour.
type band struct {
	reg   shape.Region // bbox-local mask + mean source colour
	alpha int          // 0..255 overlay opacity
}

func (e *engine) Generate(ctx *stylize.Context) ([]model.Shape, error) {
	src := ctx.Src
	w, h := src.W, src.H
	if e.cfg.Levels < 1 || e.cfg.Alpha <= 0 {
		return nil, nil
	}
	// Reproduce the flat fill (same quantizer/K → same palette) and take the per-pixel luma residual:
	// how much darker/lighter the source is than the flat cell it was collapsed into.
	palette, idx := shape.QuantizeBy(e.cfg.Quant, src, e.cfg.K)
	res := make([]float32, w*h)
	for i := range src.Pix {
		res[i] = lumaOf(src.Pix[i]) - lumaOf(palette[idx[i]])
	}

	// Collect tonal bands: shadow (res < −thr) and highlight (res > +thr) at each level.
	var bands []band
	for k := 0; k < e.cfg.Levels; k++ {
		thr := float32(e.cfg.T * float64(k+1))
		alpha := shape.C255(float32(e.cfg.Alpha))
		for _, sign := range [2]float32{-1, +1} {
			mask := make([]bool, w*h)
			for i, r := range res {
				if r*sign > thr {
					mask[i] = true
				}
			}
			for _, reg := range bandRegions(mask, w, h, e.cfg.MinArea, src) {
				bands = append(bands, band{reg: reg, alpha: alpha})
			}
		}
	}
	if len(bands) == 0 {
		return nil, nil
	}

	budget := e.cfg.Budget
	if budget <= 0 || budget > ctx.Budget {
		budget = ctx.Budget
	}
	if budget < 1 {
		return nil, nil
	}
	var totalArea int
	for _, b := range bands {
		totalArea += b.reg.Area
	}
	if totalArea == 0 {
		return nil, nil
	}

	var shapes []model.Shape
	used := 0
	for i := range bands {
		if used >= budget {
			break
		}
		alloc := budget * bands[i].reg.Area / totalArea
		if alloc < 1 {
			alloc = 1
		}
		if r := budget - used; alloc > r {
			alloc = r
		}
		tris := shape.CoverTriangles(&bands[i].reg, alloc, e.cfg.TriEps, e.cfg.Dilate)
		for j := range tris {
			tris[j].Color[3] = bands[i].alpha // mean source colour at partial opacity
		}
		shapes = append(shapes, tris...)
		used += len(tris)
	}
	return shapes, nil
}

func lumaOf(p model.RGBA) float32 { return 0.299*p.R + 0.587*p.G + 0.114*p.B }

// bandRegions returns the 4-connected components of a band mask as Regions (bbox-local masks), each
// coloured by the MEAN source colour of its pixels — the local tone to paint back. Blobs below minArea
// are dropped as noise.
func bandRegions(mask []bool, w, h, minArea int, src *stylize.SrcImage) []shape.Region {
	visited := make([]bool, w*h)
	var regions []shape.Region
	var stack, pix []int
	for start := 0; start < w*h; start++ {
		if visited[start] || !mask[start] {
			visited[start] = true
			continue
		}
		stack = append(stack[:0], start)
		pix = pix[:0]
		visited[start] = true
		x0, y0, x1, y1 := w, h, -1, -1
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			pix = append(pix, p)
			px, py := p%w, p/w
			if px < x0 {
				x0 = px
			}
			if px > x1 {
				x1 = px
			}
			if py < y0 {
				y0 = py
			}
			if py > y1 {
				y1 = py
			}
			if px > 0 && !visited[p-1] && mask[p-1] {
				visited[p-1] = true
				stack = append(stack, p-1)
			}
			if px < w-1 && !visited[p+1] && mask[p+1] {
				visited[p+1] = true
				stack = append(stack, p+1)
			}
			if py > 0 && !visited[p-w] && mask[p-w] {
				visited[p-w] = true
				stack = append(stack, p-w)
			}
			if py < h-1 && !visited[p+w] && mask[p+w] {
				visited[p+w] = true
				stack = append(stack, p+w)
			}
		}
		if len(pix) < minArea {
			continue
		}
		bw, bh := x1-x0+1, y1-y0+1
		bm := make([]bool, bw*bh)
		var sr, sg, sb float64
		for _, p := range pix {
			px, py := p%w, p/w
			bm[(py-y0)*bw+(px-x0)] = true
			c := src.Pix[p]
			sr, sg, sb = sr+float64(c.R), sg+float64(c.G), sb+float64(c.B)
		}
		n := float64(len(pix))
		regions = append(regions, shape.Region{
			Color: model.RGBA{R: float32(sr / n), G: float32(sg / n), B: float32(sb / n), A: 1},
			X0:    x0, Y0: y0, BW: bw, BH: bh, Mask: bm, Area: len(pix)})
	}
	return regions
}

func init() {
	stylize.RegisterEngine("shade", func(cfg json.RawMessage) (stylize.Engine, error) {
		c := Defaults()
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &c); err != nil {
				return nil, err
			}
		}
		return &engine{cfg: c}, nil
	})
}
