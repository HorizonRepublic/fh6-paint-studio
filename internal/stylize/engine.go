// Package stylize is a second, independent generation pipeline that turns line-art / anime / cartoon
// images into FH6 vinyl liveries with a designed look (flat fills + ink outlines), distinct from the
// geometrize error-reduction engine. It is built on composable Engines (Fill, Stroke, Glow) wired
// through DI registries; a Preset is a recipe that runs engines in order and composites their shapes.
// See docs/stylize/ROADMAP.md.
package stylize

import (
	"encoding/json"
	"fmt"
	"os"

	"fh6-paint-studio/internal/model"
)

// Context is the shared input handed to every engine in a preset run. Engines read Src and emit shapes
// in source-pixel coordinates (the output canvas == Src.W×Src.H); Budget is the remaining shape budget.
type Context struct {
	Src    *SrcImage // engines read this (pre-smoothed if the preset smooths)
	Orig   *SrcImage // the un-smoothed source (for crisp line extraction, e.g. the ink engine)
	Budget int
}

// Engine produces the shapes for one layer of a stylized render. Implementations are fully independent
// — they never reference one another, only the shared Context — so adding one cannot affect the rest.
type Engine interface {
	Name() string
	Generate(ctx *Context) ([]model.Shape, error)
}

// Factory builds an engine from its preset-stage config (per-engine JSON, decoded by the engine).
type Factory func(cfg json.RawMessage) (Engine, error)

var engineRegistry = map[string]Factory{}

// RegisterEngine adds an engine factory under a name (Open/Closed: register, never edit existing).
func RegisterEngine(name string, f Factory) { engineRegistry[name] = f }

// Run executes a preset over the source image and returns the composited geometry. Shape 0 is the
// background (the source's average colour); each stage's shapes are appended in order (z-order).
func Run(src *SrcImage, presetName string, budget int) (model.Geometry, error) {
	var p Preset
	if presetName == "auto" {
		f := Analyze(src) // content-adaptive: pick each knob from the image's style features
		if os.Getenv("STYLE_DEBUG") != "" {
			fmt.Fprintln(os.Stderr, "[auto]", f.String())
		}
		p = AutoPreset(f)
	} else {
		var ok bool
		if p, ok = presetRegistry[presetName]; !ok {
			return model.Geometry{}, fmt.Errorf("stylize: unknown preset %q", presetName)
		}
	}
	orig := src
	if p.Smooth != nil {
		src = Smooth(src, *p.Smooth)
	}
	shapes := []model.Shape{bgShape(src)}
	ctx := &Context{Src: src, Orig: orig, Budget: budget - 1}
	for _, st := range p.Stages {
		f, ok := engineRegistry[st.Engine]
		if !ok {
			return model.Geometry{}, fmt.Errorf("stylize: preset %q: unknown engine %q", presetName, st.Engine)
		}
		eng, err := f(st.Config)
		if err != nil {
			return model.Geometry{}, fmt.Errorf("stylize: engine %q: %w", st.Engine, err)
		}
		s, err := eng.Generate(ctx)
		if err != nil {
			return model.Geometry{}, fmt.Errorf("stylize: engine %q generate: %w", st.Engine, err)
		}
		shapes = append(shapes, s...)
		if ctx.Budget -= len(s); ctx.Budget < 0 {
			ctx.Budget = 0
		}
	}
	// Hard cap: a stage that over-emits (e.g. a custom BaseGrid producing more cells than the budget,
	// or any future engine ignoring ctx.Budget) must never blow the ≤3000-layer injection limit.
	if budget > 0 && len(shapes) > budget {
		shapes = shapes[:budget]
	}
	return model.Geometry{Shapes: shapes}, nil
}

// bgShape is the canvas-filling background rect (shape 0): the average source colour. RenderFH6 uses
// shape 0's colour as the background fill and does not render it as a shape.
func bgShape(src *SrcImage) model.Shape {
	var r, g, b float64
	n := float64(len(src.Pix))
	if n == 0 {
		n = 1
	}
	for _, p := range src.Pix {
		r, g, b = r+float64(p.R), g+float64(p.G), b+float64(p.B)
	}
	return model.Shape{Type: model.TypeRotatedRectangle, Color: []int{c255(r / n), c255(g / n), c255(b / n), 255},
		Data: []float64{float64(src.W) / 2, float64(src.H) / 2, float64(src.W) / 2, float64(src.H) / 2, 0}}
}

func c255(v float64) int {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return int(v*255 + 0.5)
}
