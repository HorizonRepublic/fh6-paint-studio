// Package hybrid composes the geometrize colour/detail fill with the stylizer's FDoG ink lines — the
// "hybrid" generation path (optimised semi-transparent fill that renders alive eyes + smooth shading, plus
// the designed anime outline on top). Shared by the CLI (cmd/fh6paint) and the studio so both produce the
// identical result. The fill is the geometrize engine's job (run separately); this package supplies the
// content-adaptive ink budget and the ink lines to append.
package hybrid

import (
	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
	_ "fh6-paint-studio/internal/stylize/presets" // register the "ink-fdog" preset (side-effect)
)

// AutoInkCeiling picks the FDoG ink-budget ceiling by content (the lines self-limit below it): a photo
// gets no designed lines; line-art / hatching is line-heavy (the lines ARE the content); a clean colourful
// cel is fill-heavy (colour dominates, few major contours); else a balanced anime fraction. mode is the
// resolved content mode ("photo"/"anime"/"flat"). Uses stylize.Analyze (white/sat/edges/flat).
func AutoInkCeiling(prep *imageio.Prepared, total int, mode string) int {
	if mode == "photo" {
		applog.Printf("hybrid: photo → no designed lines")
		return 0
	}
	f := stylize.Analyze(prepToStylize(prep))
	var frac float64
	var kind string
	switch {
	case f.White > 0.5 || (f.Sat < 0.06 && f.Edges > 0.16):
		frac, kind = 0.35, "line-art" // the lines ARE the content
	case f.Sat > 0.10 && f.Flat > 0.55:
		frac, kind = 0.12, "cel" // colour dominates, few major contours
	default:
		frac, kind = 0.20, "anime"
	}
	ceiling := int(float64(total) * frac)
	applog.Printf("hybrid: %s (white=%.2f sat=%.3f edges=%.2f flat=%.2f) → ink ceiling %d (fill gets the rest of %d)",
		kind, f.White, f.Sat, f.Edges, f.Flat, ceiling, total)
	return ceiling
}

// Ink runs the stylizer's FDoG ink engine over the source and returns its clean centerlines (the hybrid's
// designed-outline layer, up to inkBudget lines), dropping the stylizer background shape. Append these
// AFTER the geometrize fill so they composite on top. Returns nil on error or inkBudget<=0.
func Ink(prep *imageio.Prepared, inkBudget int) []model.Shape {
	if inkBudget <= 0 {
		return nil
	}
	geo, err := stylize.Run(prepToStylize(prep), "ink-fdog", inkBudget)
	if err != nil {
		applog.Printf("hybrid-ink: %v (skipping lines)", err)
		return nil
	}
	if len(geo.Shapes) > 1 {
		return geo.Shapes[1:] // skip the stylizer bg; only the lines
	}
	return nil
}

// prepToStylize converts a Prepared source into a stylizer SrcImage, sRGB-encoding the pixels (prep.Pixels
// is LINEAR in -linear mode; the stylizer's ink extractor expects sRGB luma).
func prepToStylize(prep *imageio.Prepared) *stylize.SrcImage {
	src := imageio.EncodeForDisplay(prep.Pixels) // linear → sRGB (no-op when not -linear)
	sp := make([]model.RGBA, prep.W*prep.H)
	for i := range sp {
		sp[i] = model.RGBA{R: src[i*4], G: src[i*4+1], B: src[i*4+2], A: src[i*4+3]}
	}
	return &stylize.SrcImage{W: prep.W, H: prep.H, Pix: sp}
}
