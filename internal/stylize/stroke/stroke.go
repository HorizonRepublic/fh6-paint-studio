// Package stroke is the stylizer's Stroke engine: it draws the ink OUTLINES of a posterized image as
// thin strokes along the region contours. This is the crisp layer that makes flat fills read as anime
// (the dictionary-word matching — arcs/swooshes on curved segments — is the next refinement; v0 places
// thin rotated rects, which already inject in-game as Square+scale+rotation). Registered as "stroke".
package stroke

import (
	"encoding/json"
	"math"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
	"fh6-paint-studio/internal/stylize/shape"
)

// Config holds the Stroke engine's eye-tunable knobs.
type Config struct {
	K          int     `json:"k"`          // posterize palette size (coarse → major contours)
	Quant      string  `json:"quant"`      // quantizer: "lab" (perceptual k-means) or "median"
	MinArea    int     `json:"minArea"`    // ignore regions smaller than this (px)
	Width      float64 `json:"width"`      // stroke half-width (px) for the straight rects
	Eps        float64 `json:"eps"`        // contour simplification tolerance (px)
	EdgeThresh float64 `json:"edgeThresh"` // min neighbour colour distance (0..~1.7) to ink a boundary
	InkDarken  float64 `json:"inkDarken"`  // ink = dark-side colour × this (variable, <1 = darker)
	Budget     int     `json:"budget"`     // hard cap (0 = run's remaining budget)
	Arcs       bool    `json:"arcs"`       // match dictionary arcs to curved contour runs
	ArcTol     float64 `json:"arcTol"`     // max circle-fit residual (px) for a run to read as one arc
	MinSweep   float64 `json:"minSweep"`   // min run sweep (deg) to spend an arc word instead of rects
	MinRun     int     `json:"minRun"`     // skip outline runs shorter than this (boundary px) — despeckle
}

// Defaults are the starting knobs; tuned by eye (see ROADMAP M1.2).
func Defaults() Config {
	return Config{K: 6, Quant: "lab", MinArea: 90, Width: 1.0, Eps: 3.5, EdgeThresh: 0.18, InkDarken: 0.5,
		Budget: 0, Arcs: true, ArcTol: 1.6, MinSweep: 35, MinRun: 9}
}

type engine struct{ cfg Config }

func (e *engine) Name() string { return "stroke" }

func (e *engine) Generate(ctx *stylize.Context) ([]model.Shape, error) {
	src := ctx.Src
	palette, idx := shape.QuantizeBy(e.cfg.Quant, src, e.cfg.K)
	regions := shape.Segment(src.W, src.H, idx, palette, e.cfg.MinArea)
	if len(regions) == 0 {
		return nil, nil
	}
	// Skip the dominant region — its boundary is the image frame, not a feature outline.
	skip, maxA := -1, -1
	for i := range regions {
		if regions[i].Area > maxA {
			maxA, skip = regions[i].Area, i
		}
	}
	budget := e.cfg.Budget
	if budget <= 0 || budget > ctx.Budget {
		budget = ctx.Budget
	}
	var shapes []model.Shape
	for i := range regions {
		if i == skip || len(shapes) >= budget {
			continue
		}
		outlineRegion(&regions[i], idx, palette, src.W, src.H, e.cfg, &shapes, budget)
	}
	return shapes, nil
}

// strokeRect makes a thin rotated rectangle from local point a to b (bbox offset ox,oy), or nil for a
// sub-pixel segment.
func strokeRect(ox, oy int, a, b [2]float64, halfW float64, col []int) *model.Shape {
	dx, dy := b[0]-a[0], b[1]-a[1]
	l := math.Hypot(dx, dy)
	if l < 1 {
		return nil
	}
	return &model.Shape{Type: model.TypeRotatedRectangle, Color: col,
		Data: []float64{
			float64(ox) + (a[0]+b[0])/2 + 0.5, float64(oy) + (a[1]+b[1])/2 + 0.5,
			l/2 + halfW, halfW, math.Atan2(dy, dx) * 180 / math.Pi,
		}}
}

func init() {
	stylize.RegisterEngine("stroke", func(cfg json.RawMessage) (stylize.Engine, error) {
		c := Defaults()
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &c); err != nil {
				return nil, err
			}
		}
		return &engine{cfg: c}, nil
	})
}
