package metric

import "math"

// BoundaryDistance returns a per-pixel field (len w*h) giving the distance in PIXELS
// from each pixel to the nearest strong TARGET boundary. A boundary is either a
// LUMINANCE edge (Sobel magnitude ≥ edgeThresh of the image max) or, for cutout
// images, the ALPHA SILHOUETTE (an opaque pixel adjacent to a transparent one).
//
// It drives BOUNDARY-AWARE RADIUS: a candidate centred far from any boundary may be
// large, but one near a boundary is capped so it can't balloon ACROSS the edge —
// keeping shapes from spilling over color regions / the object silhouette (the cause
// of fringe halos on flat/logo/cutout and translucent "veil" overshoot on organic).
//
// Distance is approximated with a two-pass chamfer transform (Borgefors 3-4: cost 3
// orthogonal, 4 diagonal, divided by 3 to recover ~pixel units) — O(w*h), exact enough
// for a radius cap. edgeThresh in (0,1]; ~0.18 keeps only genuine contours. Returns nil
// for non-positive dimensions.
func BoundaryDistance(target []float32, w, h int, edgeThresh float32) []float32 {
	if w <= 0 || h <= 0 || len(target) < w*h*4 || edgeThresh <= 0 {
		return nil
	}
	lum := make([]float32, w*h)
	hasAlpha := false
	for i := 0; i < w*h; i++ {
		lum[i] = 0.299*target[i*4] + 0.587*target[i*4+1] + 0.114*target[i*4+2]
		if target[i*4+3] < 0.999 {
			hasAlpha = true
		}
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
	// Edge magnitude + its max for relative thresholding.
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
	alphaA := func(x, y int) float32 {
		if x < 0 || x >= w || y < 0 || y >= h {
			return 0
		}
		return target[(y*w+x)*4+3]
	}
	const inf = float32(1e9)
	dist := make([]float32, w*h)
	thr := edgeThresh * maxMag
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			edge := maxMag > 0 && mag[i] >= thr
			if !edge && hasAlpha {
				// Silhouette: an opaque pixel with a transparent 4-neighbour (or vice versa).
				a := alphaA(x, y)
				if (a >= 0.5) != (alphaA(x-1, y) >= 0.5) || (a >= 0.5) != (alphaA(x+1, y) >= 0.5) ||
					(a >= 0.5) != (alphaA(x, y-1) >= 0.5) || (a >= 0.5) != (alphaA(x, y+1) >= 0.5) {
					edge = true
				}
			}
			if edge {
				dist[i] = 0
			} else {
				dist[i] = inf
			}
		}
	}
	const c1, c2 = float32(3), float32(4)
	relax := func(i int, d, cost float32) {
		if v := d + cost; v < dist[i] {
			dist[i] = v
		}
	}
	// Forward pass (top-left → bottom-right).
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if dist[i] == 0 {
				continue
			}
			if x > 0 {
				relax(i, dist[i-1], c1)
			}
			if y > 0 {
				relax(i, dist[i-w], c1)
				if x > 0 {
					relax(i, dist[i-w-1], c2)
				}
				if x < w-1 {
					relax(i, dist[i-w+1], c2)
				}
			}
		}
	}
	// Backward pass (bottom-right → top-left).
	for y := h - 1; y >= 0; y-- {
		for x := w - 1; x >= 0; x-- {
			i := y*w + x
			if dist[i] == 0 {
				continue
			}
			if x < w-1 {
				relax(i, dist[i+1], c1)
			}
			if y < h-1 {
				relax(i, dist[i+w], c1)
				if x < w-1 {
					relax(i, dist[i+w+1], c2)
				}
				if x > 0 {
					relax(i, dist[i+w-1], c2)
				}
			}
		}
	}
	for i := range dist {
		dist[i] /= 3 // chamfer 3-4 → ~pixel units
	}
	return dist
}
