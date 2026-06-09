package stroke

import (
	"math"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize/shape"
)

// outlineRegion appends one region's outline strokes. Only boundary whose colour contrast to the
// neighbour clears cfg.EdgeThresh is inked, and only by the darker of the two sides (so a shared edge
// is drawn once, not twice), tinted from that dark side. The significant boundary is split into runs,
// each simplified then laid as dictionary arcs + straight rects.
func outlineRegion(r *shape.Region, idx []int, palette []model.RGBA, w, h int, cfg Config, out *[]model.Shape, budget int) {
	loop := traceBoundary(r.Mask, r.BW, r.BH)
	n := len(loop)
	if n < 2 {
		return
	}
	rl := luma(r.Color)
	keep := make([]bool, n)
	for k := 0; k < n; k++ {
		gx, gy := r.X0+int(loop[k][0]), r.Y0+int(loop[k][1])
		nc, d := mostDifferentNeighbour(idx, palette, w, h, gx, gy, r.Color)
		if d >= cfg.EdgeThresh && rl <= luma(nc)+1e-6 { // significant edge, owned by the darker side
			keep[k] = true
		}
	}
	ink := inkColor(r.Color, cfg)
	for k := 0; k < n && len(*out) < budget; {
		if !keep[k] {
			k++
			continue
		}
		j := k
		for j < n && keep[j] {
			j++
		}
		if j-k < cfg.MinRun { // despeckle: drop noise-length runs
			k = j
			continue
		}
		if run := simplify(loop[k:j], cfg.Eps); len(run) >= 2 {
			emitOutline(run, r.X0, r.Y0, cfg.Width, ink, cfg, out, budget)
		}
		k = j
	}
}

// mostDifferentNeighbour returns the 4-neighbour palette colour most unlike c, and that distance.
func mostDifferentNeighbour(idx []int, palette []model.RGBA, w, h, gx, gy int, c model.RGBA) (model.RGBA, float64) {
	best, bestd := c, 0.0
	try := func(nx, ny int) {
		if nx < 0 || ny < 0 || nx >= w || ny >= h {
			return
		}
		if nc := palette[idx[ny*w+nx]]; colorDist(c, nc) > bestd {
			bestd, best = colorDist(c, nc), nc
		}
	}
	try(gx-1, gy)
	try(gx+1, gy)
	try(gx, gy-1)
	try(gx, gy+1)
	return best, bestd
}

func colorDist(a, b model.RGBA) float64 {
	dr, dg, db := float64(a.R-b.R), float64(a.G-b.G), float64(a.B-b.B)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

func luma(c model.RGBA) float64 {
	return 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
}

// inkColor tints the dark side's colour toward black (variable, lighter than a flat black line).
func inkColor(c model.RGBA, cfg Config) []int {
	f := float32(cfg.InkDarken)
	return []int{shape.C255(c.R * f), shape.C255(c.G * f), shape.C255(c.B * f), 255}
}
