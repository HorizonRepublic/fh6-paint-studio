// Package fill is the stylizer's Fill engine: posterize → segment flat regions → cover each region
// with FH6 shapes. It produces the flat-colour base of a stylized render (the `poster` preset alone,
// or Layer 1 of `anime`). Registered as "fill"; import for side-effect to make it available.
package fill

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
	"fh6-paint-studio/internal/stylize/shape"
)

var dbg = os.Getenv("FILL_DEBUG") != ""

// Config holds the Fill engine's eye-tunable knobs (JSON keys are the preset-stage config).
type Config struct {
	K              int     `json:"k"`              // palette size (colours)
	Quant          string  `json:"quant"`          // quantizer: "lab" (perceptual k-means) or "median"
	MinArea        int     `json:"minArea"`        // drop regions smaller than this (px)
	MinBlock       int     `json:"minBlock"`       // smallest block edge before forcing a rect (px)
	FillFrac       float64 `json:"fillFrac"`       // a block ≥ this fraction in-region is emitted as one solid rect
	Cover          string  `json:"cover"`          // cover strategy: "tri" (boundary-following triangles) or "blocks"
	TriEps         float64 `json:"triEps"`         // tri cover: contour simplify tolerance (px); higher = fewer triangles
	TriDilate      int     `json:"triDilate"`      // tri cover: mask dilation (px) so regions overlap (no slivers)
	TriMinFill     float64 `json:"triMinFill"`     // tri cover: regions filling less than this fraction of their bbox use blocks instead (thin/sprawling line-webs overfill under triangulation)
	EllipseIoU     float64 `json:"ellipseIoU"`     // if a region's moment-fit ellipse covers it at ≥ this IoU, emit ONE ellipse instead of many triangles/blocks (0 = off)
	EllipseScale   float64 `json:"ellipseScale"`   // enlarge the fitted ellipse by this (≥1) so it overlaps neighbours — closes edge gaps
	SaliencyBudget float64 `json:"saliencyBudget"` // weight each region's shape allocation by area×(1+w·chromaSaliency); >0 sends more shapes to small colourful detail (eyes/lips)
	BaseGrid       int     `json:"baseGrid"`       // underpainting: cells along the long side for the coarse full-coverage base (≤1 = single dominant-colour base); gaps then show the local colour, not bright background
	SuppressBg     bool    `json:"suppressBg"`     // skip regions that sit mostly in the border-connected light background (no halo shapes in the white margin; saves budget)
	BgLuma         float64 `json:"bgLuma"`         // luma threshold for "background light" (used by SuppressBg)
	Saturation     float64 `json:"saturation"`     // luma-preserving chroma boost on the palette (1=off); >1 = more pop
	Budget         int     `json:"budget"`         // hard shape cap (0 = use the run's remaining budget)
}

// Defaults are the starting knobs; tuned by eye on the image bank (see ROADMAP M1.1).
func Defaults() Config {
	return Config{K: 14, Quant: "lab", MinArea: 6, MinBlock: 2, FillFrac: 0.82,
		Cover: "tri", TriEps: 1.5, TriDilate: 2, TriMinFill: 0.15, EllipseIoU: 0.85, EllipseScale: 1.22,
		SaliencyBudget: 4, BaseGrid: 1, SuppressBg: true, BgLuma: 0.86, Saturation: 1.0, Budget: 0}
}

type engine struct{ cfg Config }

func (e *engine) Name() string { return "fill" }

func (e *engine) Generate(ctx *stylize.Context) ([]model.Shape, error) {
	src := ctx.Src
	palette, idx := shape.QuantizeBy(e.cfg.Quant, src, e.cfg.K)
	if e.cfg.Saturation != 1.0 && e.cfg.Saturation > 0 {
		for i := range palette {
			palette[i] = boostChroma(palette[i], float32(e.cfg.Saturation))
		}
	}
	regions := shape.Segment(src.W, src.H, idx, palette, e.cfg.MinArea)
	if dbg {
		fmt.Fprintf(os.Stderr, "[fill] palette=%d regions=%d\n", len(palette), len(regions))
		for i := 0; i < len(palette); i++ {
			fmt.Fprintf(os.Stderr, "  pal[%d] = %.2f,%.2f,%.2f\n", i, palette[i].R, palette[i].G, palette[i].B)
		}
	}
	// Back-to-front: largest area first (background), small details composite on top.
	sort.SliceStable(regions, func(a, b int) bool { return regions[a].Area > regions[b].Area })

	if len(regions) == 0 {
		return nil, nil
	}
	budget := e.cfg.Budget
	if budget <= 0 || budget > ctx.Budget {
		budget = ctx.Budget
	}

	// Base layer = the gap backstop. A coarse full-coverage mosaic of LOCAL average colours (underpainting)
	// so any seam/hole between detail cells shows the right local colour, not a bright dominant/background.
	// (BaseGrid≤1 falls back to a single dominant-colour rect.)
	var shapes []model.Shape
	if e.cfg.BaseGrid > 1 {
		shapes = coarseBase(src, e.cfg.BaseGrid)
	} else {
		dom := regions[0].Color
		shapes = []model.Shape{{Type: model.TypeRotatedRectangle,
			Color: []int{shape.C255(dom.R), shape.C255(dom.G), shape.C255(dom.B), 255},
			Data:  []float64{float64(src.W) / 2, float64(src.H) / 2, float64(src.W) / 2, float64(src.H) / 2, 0}}}
	}

	// Budget weights: area by default; with SaliencyBudget>0, boost small colourful regions (eyes/lips)
	// so they get more shapes than their area warrants — the focal point reads instead of muddying.
	var sal []float64
	if e.cfg.SaliencyBudget > 0 {
		sal = stylize.ChromaSaliency(src)
	}
	var bg []bool
	if e.cfg.SuppressBg {
		bg = stylize.BackgroundMask(src, e.cfg.BgLuma)
	}
	weights := make([]float64, len(regions))
	var totalW float64
	for i := 1; i < len(regions); i++ {
		w := float64(regions[i].Area)
		if sal != nil {
			w *= 1 + e.cfg.SaliencyBudget*regionMeanSaliency(sal, &regions[i], src.W)
		}
		weights[i] = w
		totalW += w
	}
	if totalW == 0 {
		totalW = 1
	}
	rem := budget - len(shapes) // the base/underpainting used some
	used := 0
	for i := 1; i < len(regions); i++ {
		if used >= rem {
			break
		}
		alloc := int(float64(rem) * weights[i] / totalW)
		if alloc < 1 {
			alloc = 1
		}
		if r := rem - used; alloc > r {
			alloc = r
		}
		if bg != nil && regionInBg(&regions[i], bg, src.W) > 0.6 {
			continue // region sits in the light background → don't paint a halo there, free its budget
		}
		var s []model.Shape
		fillRatio := float64(regions[i].Area) / float64(regions[i].BW*regions[i].BH)
		// One moment-fit ellipse covers a compact region (round blob OR straight sliver) where
		// triangulation spends ~6-10 triangles and blocks spend a dozen squares — freeing budget for
		// detail. Only when the ellipse genuinely matches the mask (IoU gate); concave/sprawling regions
		// fall through.
		if e.cfg.EllipseIoU > 0 && alloc >= 1 {
			if _, _, _, _, _, iou := shape.FitEllipse(&regions[i]); iou >= e.cfg.EllipseIoU {
				s = shape.CoverEllipse(&regions[i], e.cfg.EllipseScale)
			}
		}
		// Thin/sprawling regions (line-art ink webs) have a contour that, once simplified, encloses a
		// huge area — triangulating it floods the bbox with that colour. Cover those with blocks, which
		// only ever paint mask pixels. Blob regions keep the smooth, budget-cheap triangulation.
		if s == nil {
			if e.cfg.Cover == "blocks" || (e.cfg.Cover == "tri" && fillRatio < e.cfg.TriMinFill) {
				s = shape.CoverBlocks(&regions[i], alloc, e.cfg.MinBlock, e.cfg.FillFrac)
			} else {
				s = shape.CoverTriangles(&regions[i], alloc, e.cfg.TriEps, e.cfg.TriDilate)
			}
		}
		if dbg && i < 6 {
			fmt.Fprintf(os.Stderr, "  reg[%d] area=%d col=%.2f,%.2f,%.2f bbox=%dx%d alloc=%d -> %d shapes\n",
				i, regions[i].Area, regions[i].Color.R, regions[i].Color.G, regions[i].Color.B, regions[i].BW, regions[i].BH, alloc, len(s))
		}
		shapes = append(shapes, s...)
		used += len(s)
	}
	if dbg {
		fmt.Fprintf(os.Stderr, "[fill] base=%v total shapes=%d\n", shapes[0].Color, len(shapes))
	}
	return shapes, nil
}

// coarseBase emits a full-coverage grid of local-average-colour rects (an underpainting). cellsLong sets
// the grid resolution along the long side. Cells are over-sized by 1px so the base itself has no seams;
// the detail cover draws on top, so this colour shows only in the gaps — where it is the right local tone.
func coarseBase(src *stylize.SrcImage, cellsLong int) []model.Shape {
	long := src.W
	if src.H > long {
		long = src.H
	}
	cell := long / cellsLong
	if cell < 1 {
		cell = 1
	}
	var shapes []model.Shape
	for y0 := 0; y0 < src.H; y0 += cell {
		y1 := y0 + cell
		if y1 > src.H {
			y1 = src.H
		}
		for x0 := 0; x0 < src.W; x0 += cell {
			x1 := x0 + cell
			if x1 > src.W {
				x1 = src.W
			}
			var r, g, b float64
			n := 0
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					p := src.Pix[y*src.W+x]
					r, g, b = r+float64(p.R), g+float64(p.G), b+float64(p.B)
					n++
				}
			}
			if n == 0 {
				continue
			}
			inv := 1 / float64(n)
			shapes = append(shapes, model.Shape{Type: model.TypeRotatedRectangle,
				Color: []int{shape.C255(float32(r * inv)), shape.C255(float32(g * inv)), shape.C255(float32(b * inv)), 255},
				Data:  []float64{float64(x0+x1) / 2, float64(y0+y1) / 2, float64(x1-x0)/2 + 1, float64(y1-y0)/2 + 1, 0}})
		}
	}
	return shapes
}

// regionInBg returns the fraction of a region's mask pixels that fall in the background mask.
func regionInBg(r *shape.Region, bg []bool, w int) float64 {
	in, n := 0, 0
	for y := 0; y < r.BH; y++ {
		for x := 0; x < r.BW; x++ {
			if !r.Mask[y*r.BW+x] {
				continue
			}
			n++
			if i := (r.Y0+y)*w + (r.X0 + x); i >= 0 && i < len(bg) && bg[i] {
				in++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return float64(in) / float64(n)
}

// regionMeanSaliency averages the canvas-space saliency map over a region's mask.
func regionMeanSaliency(sal []float64, r *shape.Region, w int) float64 {
	var sum float64
	n := 0
	for y := 0; y < r.BH; y++ {
		for x := 0; x < r.BW; x++ {
			if !r.Mask[y*r.BW+x] {
				continue
			}
			gx, gy := r.X0+x, r.Y0+y
			if i := gy*w + gx; i >= 0 && i < len(sal) {
				sum += sal[i]
				n++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// boostChroma scales a colour's distance from its own luma by s (luma-preserving saturation), clamped
// to [0,1]. s>1 adds chroma ("pop"); s=1 is identity.
func boostChroma(c model.RGBA, s float32) model.RGBA {
	l := 0.299*c.R + 0.587*c.G + 0.114*c.B
	clamp := func(v float32) float32 {
		if v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	}
	return model.RGBA{R: clamp(l + s*(c.R-l)), G: clamp(l + s*(c.G-l)), B: clamp(l + s*(c.B-l)), A: c.A}
}

func init() {
	stylize.RegisterEngine("fill", func(cfg json.RawMessage) (stylize.Engine, error) {
		c := Defaults()
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &c); err != nil {
				return nil, err
			}
		}
		return &engine{cfg: c}, nil
	})
}
