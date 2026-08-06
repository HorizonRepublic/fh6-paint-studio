// Package presets registers the built-in stylizer preset recipes. Importing it (side-effect) makes the
// presets and their engines available. Multi-engine presets (anime) live here because they span
// packages; single-engine ones (poster) are here too for one place to look.
package presets

import (
	"encoding/json"

	"fh6-paint-studio/internal/stylize"
	_ "fh6-paint-studio/internal/stylize/fill"   // register the "fill" engine
	_ "fh6-paint-studio/internal/stylize/glow"   // register the "glow" engine
	_ "fh6-paint-studio/internal/stylize/stroke" // register the "stroke" engine
)

// dtSmooth is the shared edge-preserving flatten (Domain Transform) for the cel-fill presets.
var dtSmooth = &stylize.SmoothConfig{Method: "dt", Spatial: 32, Range: 0.9, Iters: 4}

func init() {
	// poster — flat colour cells only (Fill on the flattened source). The M1.1 sanity preset.
	stylize.RegisterPreset(stylize.Preset{Name: "poster", Smooth: dtSmooth, Stages: []stylize.Stage{
		{Engine: "fill"},
	}})
	// lines — XDoG ink centerlines only (the ink engine), for seeing the line layer in isolation.
	stylize.RegisterPreset(stylize.Preset{Name: "lines", Stages: []stylize.Stage{
		{Engine: "ink"},
	}})
	// ink-fdog — FDoG coherent ink centerlines only (no fill), the clean designed-outline layer for the
	// HYBRID (geometrize colour/detail fill + these crisp lines on top). Unlike `anime` (whose heavier
	// eye-tuned weight REPLACES the source lines over flattened fills), the hybrid's lines land on a
	// fill that already reproduces the source — so they carry the SOURCE's weight: width floor 0.5
	// (1px strokes render 1px) + the -0.5 DT bias (the transform overshoots true half-width by ~0.5px).
	// Measured on img_1: the old config drew the line layer ~3× the source's median stroke width.
	stylize.RegisterPreset(stylize.Preset{Name: "ink-fdog", Stages: []stylize.Stage{
		{Engine: "ink", Config: json.RawMessage(`{"method":"fdog","thresh":0.75,"width":0.5,"widthBias":-0.5}`)},
	}})
	// ink-fdog-ridge — ink-fdog + the ridge gate: only lines the SOURCE actually drew (luma ridges);
	// one-sided step edges (glow rims, soft contrast boundaries) are dropped instead of outlined.
	// The faithful variant for owner A/B vs the stylised invented-outline default.
	stylize.RegisterPreset(stylize.Preset{Name: "ink-fdog-ridge", Stages: []stylize.Stage{
		{Engine: "ink", Config: json.RawMessage(`{"method":"fdog","thresh":0.75,"width":0.5,"widthBias":-0.5,"ridgeOnly":0.5}`)},
	}})
	// outline — the older region-boundary stroke engine, kept as an alternative line source.
	stylize.RegisterPreset(stylize.Preset{Name: "outline", Smooth: dtSmooth, Stages: []stylize.Stage{
		{Engine: "stroke"},
	}})
	// anime — flat triangulated cells + FDoG coherent ink lines. fill flattens the source; the ink
	// engine extracts connected, confident lines with Flow-based DoG (Kang 2007) — the defining clean
	// anime look, vs XDoG's sketchy fragments. (XDoG stays reachable via the ink "method" config.)
	stylize.RegisterPreset(stylize.Preset{Name: "anime", Smooth: dtSmooth, Stages: []stylize.Stage{
		{Engine: "fill", Config: json.RawMessage(`{"budget":2400}`)},
		{Engine: "ink", Config: json.RawMessage(`{"method":"fdog","thresh":0.75}`)},
	}})
	// anime-glow — anime + a Glow layer between fill and ink that recovers the smooth shading flat cells
	// band on (cheeks, hair sheen, iris falloff) with native FH6 radial-gradient splats. The glow budget
	// is bounded so the ink layer (drawn last, on top) still gets its share. The lever that beats the
	// budget bound: one anisotropic glow renders a gradient that costs a dozen flat cells.
	stylize.RegisterPreset(stylize.Preset{Name: "anime-glow", Smooth: dtSmooth, Stages: []stylize.Stage{
		{Engine: "fill", Config: json.RawMessage(`{"budget":2400}`)},
		{Engine: "glow", Config: json.RawMessage(`{"budget":500,"saliency":8}`)},
		{Engine: "ink", Config: json.RawMessage(`{"method":"fdog","thresh":0.75}`)},
	}})
}
