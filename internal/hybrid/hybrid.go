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
	"fh6-paint-studio/internal/raster"
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
// ridgeOnly switches to the faithful variant: only lines the source actually drew (luma ridges) —
// step-edge responses like the rim of a bright glow on a dark face are dropped instead of outlined.
func Ink(prep *imageio.Prepared, inkBudget int, ridgeOnly bool) []model.Shape {
	if inkBudget <= 0 {
		return nil
	}
	preset := "ink-fdog"
	if ridgeOnly {
		preset = "ink-fdog-ridge"
	}
	geo, err := stylize.Run(prepToStylize(prep), preset, inkBudget)
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

// SuppressLines returns a Prepared copy whose pixels UNDER the drawn ink lines are inpainted from
// their surroundings, so the geometrize fill stops competing with the ink layer for the same
// strokes — the source of the hybrid's double-line/ghosting artifact (the fill reconstructs a
// soft copy of every source line, then the FDoG line lands next to it slightly offset). Each
// DRAWN line claims its pixels; lines the ink budget did not draw stay in the target and the
// fill keeps reproducing them, so nothing vanishes from the result.
func SuppressLines(prep *imageio.Prepared, lines []model.Shape) *imageio.Prepared {
	if len(lines) == 0 {
		return prep
	}
	w, h := prep.W, prep.H
	masked := make([]bool, w*h)
	for _, s := range lines {
		kind := model.KindFromType(s.Type)
		p := model.ParamsFromShape(s)
		xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				if raster.Inside(kind, p, x, y) {
					masked[y*w+x] = true
				}
			}
		}
	}
	// 1px dilation: claim the anti-aliased rim of the source line too, not just its core.
	dil := make([]bool, w*h)
	copy(dil, masked)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !masked[y*w+x] {
				continue
			}
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := x+dx, y+dy
					if xx >= 0 && yy >= 0 && xx < w && yy < h {
						dil[yy*w+xx] = true
					}
				}
			}
		}
	}
	// Iterative inpaint: every claimed pixel takes the mean of its already-known neighbours,
	// peeling the claimed band from its edges inward (lines are a few px wide -> a few passes).
	px := append([]float32(nil), prep.Pixels...)
	known := make([]bool, w*h)
	for i := range known {
		known[i] = !dil[i]
	}
	for pass := 0; pass < 16; pass++ {
		changed := false
		next := append([]bool(nil), known...)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := y*w + x
				if known[i] {
					continue
				}
				var r, g, b, a float32
				var n int
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						xx, yy := x+dx, y+dy
						if xx < 0 || yy < 0 || xx >= w || yy >= h {
							continue
						}
						j := yy*w + xx
						if known[j] {
							r += px[j*4]
							g += px[j*4+1]
							b += px[j*4+2]
							a += px[j*4+3]
							n++
						}
					}
				}
				if n > 0 {
					fn := float32(n)
					px[i*4], px[i*4+1], px[i*4+2], px[i*4+3] = r/fn, g/fn, b/fn, a/fn
					next[i] = true
					changed = true
				}
			}
		}
		known = next
		if !changed {
			break
		}
	}
	out := *prep
	out.Pixels = px
	applog.Printf("hybrid-claim: %d drawn lines claimed their pixels from the fill target", len(lines))
	return &out
}
