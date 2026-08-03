package engine

// mergerefit.go — EXPERIMENTAL merge consolidation inside the LOO-refit round (Options.MergeRefit,
// default off). The LOO prune frees slots only where a shape is REDUNDANT; near-duplicate pairs —
// same kind, near-same color, high overlap (the translucent-stack convergence the greedy leaves
// behind) — waste a slot even though both shapes "help". Measured supply on final @3000 stacks:
// 176-253 such pairs (6-8% of the budget). Each pair is replaced by ONE moment-fitted shape; the
// hosting LOO round's regrow + re-polish + end-to-end gate then restores quality — the same
// refit-loop pattern that shipped LooRefit (point edits without re-co-adaptation measurably fail).

import (
	"math"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

const (
	mergeColorTol = 25.0 / 255 // per-channel color distance cap (8-bit 25)
	mergeAlphaTol = 40.0 / 255 // alpha distance cap
	mergeIoUMin   = 0.55       // bbox IoU floor
	mergeMaxFrame = 160        // moment-fit frame cap per axis (large pairs are strided into it)
)

// mergePairs greedily consolidates near-duplicate pairs in shapes (index 0 = background, never
// touched) and returns the new stack plus the number of freed slots. Only primitive kinds merge —
// mask words and lines carry structure a moment ellipse cannot represent.
func mergePairs(shapes []model.Shape, w, h int) ([]model.Shape, int) {
	type geo struct {
		kind model.ShapeKind
		p    [6]float32
		col  [4]float32
		bbox [4]int
		ok   bool
	}
	gs := make([]geo, len(shapes))
	for i := 1; i < len(shapes); i++ {
		s := shapes[i]
		k := model.KindFromType(s.Type)
		switch k {
		case model.KindEllipse, model.KindRectangle, model.KindTriangle, model.KindGlow, model.KindDisk:
		default:
			continue
		}
		p := model.ParamsFromShape(s)
		var col [4]float32
		if len(s.Color) >= 4 {
			col = [4]float32{model.DecChan(s.Color[0]), model.DecChan(s.Color[1]), model.DecChan(s.Color[2]), float32(s.Color[3]) / 255}
		} else {
			col[3] = 1
		}
		x0, y0, x1, y1 := raster.BBox(k, p, w, h)
		if x1 <= x0 || y1 <= y0 {
			continue
		}
		gs[i] = geo{kind: k, p: p, col: col, bbox: [4]int{x0, y0, x1, y1}, ok: true}
	}

	iou := func(a, b [4]int) float64 {
		x0, y0 := maxi(a[0], b[0]), maxi(a[1], b[1])
		x1, y1 := mini(a[2], b[2]), mini(a[3], b[3])
		if x1 <= x0 || y1 <= y0 {
			return 0
		}
		inter := float64((x1 - x0) * (y1 - y0))
		aa := float64((a[2] - a[0]) * (a[3] - a[1]))
		bb := float64((b[2] - b[0]) * (b[3] - b[1]))
		return inter / (aa + bb - inter)
	}

	used := make([]bool, len(shapes))
	replaced := make(map[int]model.Shape) // i -> replacement (j dropped)
	dropped := make(map[int]bool)
	for i := 1; i < len(shapes); i++ {
		if !gs[i].ok || used[i] {
			continue
		}
		for j := i + 1; j < len(shapes); j++ {
			if !gs[j].ok || used[j] || gs[i].kind != gs[j].kind {
				continue
			}
			dc := 0.0
			for k := 0; k < 3; k++ {
				if d := math.Abs(float64(gs[i].col[k] - gs[j].col[k])); d > dc {
					dc = d
				}
			}
			if dc > mergeColorTol || math.Abs(float64(gs[i].col[3]-gs[j].col[3])) > mergeAlphaTol {
				continue
			}
			ov := iou(gs[i].bbox, gs[j].bbox)
			if ov < mergeIoUMin {
				continue
			}
			m, ok := mergeFit(gs[i].kind, gs[i].p, gs[j].p, gs[i].col, gs[j].col, ov, w, h)
			if !ok {
				continue
			}
			replaced[i] = m.ToShape(shapes[i].Score)
			dropped[j] = true
			used[i], used[j] = true, true
			break
		}
	}
	if len(replaced) == 0 {
		return shapes, 0
	}
	out := make([]model.Shape, 0, len(shapes)-len(dropped))
	for i, s := range shapes {
		if dropped[i] {
			continue
		}
		if m, ok := replaced[i]; ok {
			out = append(out, m)
			continue
		}
		out = append(out, s)
	}
	return out, len(dropped)
}

// mergeFit builds the replacement candidate for a pair: the covariance ellipse of the pair's
// combined coverage (momentEllipse over a strided union-bbox frame), carried back to the pair's
// kind (rect half-extents = √3·σ for a uniform box; ellipse/glow/disk keep the moment axes;
// triangle pairs become the moment ellipse — the e2e gate rejects bad silhouette swaps). Color is
// area-weighted; alpha blends toward the stacked 1−(1−a₁)(1−a₂) by the overlap fraction.
func mergeFit(kind model.ShapeKind, p1, p2 [6]float32, c1, c2 [4]float32, overlap float64, w, h int) (model.Candidate, bool) {
	b1, b2 := bboxOf4(kind, p1, w, h), bboxOf4(kind, p2, w, h)
	x0, y0 := mini(b1[0], b2[0]), mini(b1[1], b2[1])
	x1, y1 := maxi(b1[2], b2[2]), maxi(b1[3], b2[3])
	bw, bh := x1-x0+1, y1-y0+1
	if bw < 2 || bh < 2 {
		return model.Candidate{}, false
	}
	step := 1
	for bw/step > mergeMaxFrame || bh/step > mergeMaxFrame {
		step++
	}
	fw, fh := (bw+step-1)/step, (bh+step-1)/step
	frame := make([]float32, fw*fh)
	for fy := 0; fy < fh; fy++ {
		for fx := 0; fx < fw; fx++ {
			x, y := x0+fx*step, y0+fy*step
			cov := raster.Coverage(kind, p1, x, y) + raster.Coverage(kind, p2, x, y)
			if cov > 1 {
				cov = 1
			}
			frame[fy*fw+fx] = float32(cov)
		}
	}
	cx, cy, rx, ry, theta, ok := momentEllipse(frame, fw, fh)
	if !ok || rx < 1 || ry < 0.5 {
		return model.Candidate{}, false
	}
	cx = float32(x0) + cx*float32(step)
	cy = float32(y0) + cy*float32(step)
	rx *= float32(step)
	ry *= float32(step)

	a1 := float64(area(kind, p1))
	a2 := float64(area(kind, p2))
	tw := a1 + a2
	var col model.RGBA
	col.R = float32((a1*float64(c1[0]) + a2*float64(c2[0])) / tw)
	col.G = float32((a1*float64(c1[1]) + a2*float64(c2[1])) / tw)
	col.B = float32((a1*float64(c1[2]) + a2*float64(c2[2])) / tw)
	al1, al2 := float64(c1[3]), float64(c2[3])
	stacked := 1 - (1-al1)*(1-al2)
	flat := math.Max(al1, al2)
	col.A = float32((1-overlap)*flat + overlap*stacked)
	if col.A > 1 {
		col.A = 1
	}

	out := model.Candidate{Kind: kind, Color: col}
	switch kind {
	case model.KindRectangle:
		// The moment σ of a uniform box maps to half-extents via √3·σ; momentEllipse returns
		// momentSemiAxis·σ (=2σ), so rescale.
		out.P = [6]float32{cx, cy, rx * float32(math.Sqrt(3)/2), ry * float32(math.Sqrt(3)/2), theta, 0}
	case model.KindTriangle:
		out.Kind = model.KindEllipse // no honest triangle from moments — let the gate judge the ellipse
		out.P = [6]float32{cx, cy, rx, ry, theta, 0}
	default: // ellipse / glow / disk keep the moment axes
		out.P = [6]float32{cx, cy, rx, ry, theta, 0}
	}
	return out, true
}

func bboxOf4(kind model.ShapeKind, p [6]float32, w, h int) [4]int {
	x0, y0, x1, y1 := raster.BBox(kind, p, w, h)
	return [4]int{x0, y0, x1, y1}
}

// area returns the analytic footprint of a primitive (loose for triangles — bbox/2).
func area(kind model.ShapeKind, p [6]float32) float32 {
	switch kind {
	case model.KindTriangle:
		return float32(math.Abs(float64((p[2]-p[0])*(p[5]-p[1])-(p[4]-p[0])*(p[3]-p[1]))) / 2)
	case model.KindRectangle:
		return 4 * p[2] * p[3]
	default:
		return float32(math.Pi) * p[2] * p[3]
	}
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func mini(a, b int) int {
	if a < b {
		return a
	}
	return b
}
