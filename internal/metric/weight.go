package metric

import "math"

// WeightBase is the minimum per-pixel weight so flat regions still contribute.
const WeightBase = 0.15

// WeightMap returns a per-pixel importance weight in [WeightBase, 1] derived from
// the target's edge strength (Sobel gradient magnitude on luminance). Edges and
// fine detail approach 1; flat regions get the small baseline. len = w*h.
func WeightMap(target []float32, w, h int) []float32 {
	lum := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		lum[i] = Luma(target[i*4], target[i*4+1], target[i*4+2])
	}
	at := func(x, y int) float32 {
		if x < 0 {
			x = 0
		} else if x >= w {
			x = w - 1
		}
		if y < 0 {
			y = 0
		} else if y >= h {
			y = h - 1
		}
		return lum[y*w+x]
	}
	mag := make([]float32, w*h)
	var maxMag float32
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			gx := (at(x+1, y-1) + 2*at(x+1, y) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x-1, y) + at(x-1, y+1))
			gy := (at(x-1, y+1) + 2*at(x, y+1) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x, y-1) + at(x+1, y-1))
			m := float32(math.Hypot(float64(gx), float64(gy)))
			mag[y*w+x] = m
			if m > maxMag {
				maxMag = m
			}
		}
	}
	out := make([]float32, w*h)
	for i := range out {
		var norm float32
		if maxMag > 0 {
			norm = mag[i] / maxMag
		}
		out[i] = WeightBase + (1-WeightBase)*norm
	}
	return out
}

// WeightMapV2 is a richer importance map that fixes the two structural
// weaknesses of WeightMap for CRISP CONTOURS:
//
//   - ABSOLUTE (not max-normalized) edge gain with a wide [0.55,5.25] clamp, so a black
//     outline keeps its high weight even when the image has another high-contrast region
//     (the relative Sobel map dilutes contour weight on busy images -> rough outlines).
//   - 3x3 MAX-DILATION (decay 0.92): widens each 1-2px ink line's high weight into its
//     neighbour ring, so a flat-fill shape that CROSSES the outline pays for it instead of
//     smearing/breaking it (the textbook cause of broken contours).
//
// The weight feeds the optimal-color solve, the per-shape ΔSSE (which shape wins), the
// error grid AND the candidate-center sampler — so this one map makes the engine select
// shapes that hug contours AND place more candidates on them. The backend math is linear
// in the weight (ratios for color, multiply for ΔSSE), so the wider range needs NO backend
// change — only RELATIVE weights matter. Best for flat/line-art/cutout content; on smooth
// content a strong edge term can drift flat-fill colors slightly dark.
//
// The linework (ink) term is gated on the luma EDGE rather than on saturation, because
// pure-black outlines have ~zero saturation and a dark×saturated formula would give them no
// boost — edge-gating makes black contours fire.
func WeightMapV2(target []float32, w, h int) []float32 {
	n := w * h
	if n <= 0 || len(target) < n*4 {
		return nil
	}
	luma := make([]float32, n)
	alpha := make([]float32, n)
	for i := 0; i < n; i++ {
		p := i * 4
		luma[i] = Luma(target[p], target[p+1], target[p+2])
		alpha[i] = clamp01f(target[p+3])
	}
	imp := make([]float32, n)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			p := i * 4
			R, G, B := target[p], target[p+1], target[p+2]
			sat := maxf(maxf(R, G), B) - minf(minf(R, G), B)
			a := alpha[i]
			var gx, gy, agx, agy float32
			if x > 0 {
				gx = absf(luma[i] - luma[i-1])
				agx = absf(a - alpha[i-1])
			}
			if y > 0 {
				gy = absf(luma[i] - luma[i-w])
				agy = absf(a - alpha[i-w])
			}
			lumaEdge := maxf(gx, gy)
			alphaEdge := maxf(agx, agy)
			darkness := clamp01f((0.48 - luma[i]) / 0.48)
			linework := darkness * clamp01f(lumaEdge*6) * a // EDGE-gated ink term (fires on black outlines)
			highlights := clamp01f((luma[i]-0.78)/0.22) * clamp01f(sat*1.15) * a
			vis := float32(1)
			if a <= 0.02 {
				vis = 0.55
			}
			imp[i] = (1 +
				clipf(lumaEdge*9, 0, 2.6) +
				clipf(alphaEdge*7.5, 0, 2.8) +
				clipf(sat*0.55, 0, 0.75)*a +
				linework*1.6 +
				highlights*0.70) * vis
		}
	}
	// 3x3 max-dilation (decay 0.92) then clamp [0.55,5.25].
	out := make([]float32, n)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			m := imp[i]
			for dy := -1; dy <= 1; dy++ {
				yy := y + dy
				if yy < 0 || yy >= h {
					continue
				}
				for dx := -1; dx <= 1; dx++ {
					xx := x + dx
					if (dx == 0 && dy == 0) || xx < 0 || xx >= w {
						continue
					}
					if nv := imp[yy*w+xx] * 0.92; nv > m {
						m = nv
					}
				}
			}
			out[i] = clipf(m, 0.55, 5.25)
		}
	}
	return out
}

func clamp01f(v float32) float32 { return clipf(v, 0, 1) }
func clipf(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
func maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
func minf(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}
