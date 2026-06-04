package metric

import "math"

// OrientationMap returns, per pixel, the dominant edge orientation in degrees
// [0,180) — the direction *along* the local edge (the long axis a shape should
// follow). Computed from the smoothed structure tensor of the target luminance,
// which is sign-insensitive (opposite gradients give the same orientation),
// making it stable for seeding elongated shapes along hair strands, folds, etc.
// len = w*h.
func OrientationMap(target []float32, w, h int) []float32 {
	lum := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		lum[i] = Luma(target[i*4], target[i*4+1], target[i*4+2])
	}
	at := func(x, y int) float64 {
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
		return float64(lum[y*w+x])
	}
	// Per-pixel structure-tensor components from Sobel gradients.
	jxx := make([]float64, w*h)
	jyy := make([]float64, w*h)
	jxy := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			gx := (at(x+1, y-1) + 2*at(x+1, y) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x-1, y) + at(x-1, y+1))
			gy := (at(x-1, y+1) + 2*at(x, y+1) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x, y-1) + at(x+1, y-1))
			i := y*w + x
			jxx[i] = gx * gx
			jyy[i] = gy * gy
			jxy[i] = gx * gy
		}
	}
	// Smooth the tensor with a 3x3 box so orientation is locally coherent.
	box := func(src []float64, x, y int) float64 {
		var s float64
		for dy := -1; dy <= 1; dy++ {
			yy := y + dy
			if yy < 0 {
				yy = 0
			} else if yy >= h {
				yy = h - 1
			}
			for dx := -1; dx <= 1; dx++ {
				xx := x + dx
				if xx < 0 {
					xx = 0
				} else if xx >= w {
					xx = w - 1
				}
				s += src[yy*w+xx]
			}
		}
		return s / 9
	}
	out := make([]float32, w*h)
	const rad2deg = 180 / math.Pi
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			sxx := box(jxx, x, y)
			syy := box(jyy, x, y)
			sxy := box(jxy, x, y)
			// Eigenvector of the smaller eigenvalue = along-edge direction.
			ang := 0.5 * math.Atan2(2*sxy, sxx-syy) // direction of max change (gradient)
			along := ang*rad2deg + 90               // rotate 90° -> along the edge
			along = math.Mod(along, 180)
			if along < 0 {
				along += 180
			}
			out[i] = float32(along)
		}
	}
	return out
}
