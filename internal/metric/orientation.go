package metric

import "math"

// OrientationMap returns, per pixel, the dominant edge orientation in degrees
// [0,180) — the direction *along* the local edge (the long axis a shape should
// follow). Computed from the smoothed structure tensor of the target luminance,
// which is sign-insensitive (opposite gradients give the same orientation),
// making it stable for seeding elongated shapes along hair strands, folds, etc.
// len = w*h.
func OrientationMap(target []float32, w, h int) []float32 {
	return orientationTensor(target, w, h, nil)
}

// OrientationCoherenceMap returns the same orientation field plus the per-pixel COHERENCE of the
// structure tensor — how strongly the neighbourhood prefers that direction. Both come from one pass
// over the same tensor, so asking for coherence costs nothing beyond the second output buffer.
func OrientationCoherenceMap(target []float32, w, h int) (orient, coherence []float32) {
	coherence = make([]float32, w*h)
	return orientationTensor(target, w, h, coherence), coherence
}

func orientationTensor(target []float32, w, h int, coh []float32) []float32 {
	// Every loop here writes one output per pixel and reads only the input, so a row-band split
	// reproduces the serial order exactly — same values, same order, byte-identical. This map is
	// built unconditionally on every run in serial float64 (Sobel, then a 3x3 box, then an Atan2
	// per pixel), which at the 4096 fit cap is tens of millions of transcendentals on one core.
	lum := make([]float32, w*h)
	heRows(h, func(y0, y1 int) {
		for i := y0 * w; i < y1*w; i++ {
			lum[i] = Luma(target[i*4], target[i*4+1], target[i*4+2])
		}
	})
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
	heRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				gx := (at(x+1, y-1) + 2*at(x+1, y) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x-1, y) + at(x-1, y+1))
				gy := (at(x-1, y+1) + 2*at(x, y+1) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x, y-1) + at(x+1, y-1))
				i := y*w + x
				jxx[i] = gx * gx
				jyy[i] = gy * gy
				jxy[i] = gx * gy
			}
		}
	})
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
	heRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				i := y*w + x
				sxx := box(jxx, x, y)
				syy := box(jyy, x, y)
				sxy := box(jxy, x, y)
				// Eigenvector of the smaller eigenvalue = along-edge direction.
				ang := 0.5 * math.Atan2(2*sxy, sxx-syy) // direction of max change (gradient)
				along := ang*rad2deg + 90               // rotate 90° -> along the edge
				// atan2 returns (-pi,pi], so `along` is within (-90, 270] and one compare-subtract
				// lands it in [0,180) exactly where math.Mod did — Mod is a full remainder with
				// argument reduction, called once per pixel for a range this loop already knows.
				if along >= 180 {
					along -= 180
				} else if along < 0 {
					along += 180
				}
				out[i] = float32(along)
				if coh != nil {
					coh[i] = float32(tensorCoherence(sxx, syy, sxy))
				}
			}
		}
	})
	return out
}

// tensorCoherence is (λ₁−λ₂)/(λ₁+λ₂) of the smoothed structure tensor: 0 where the local
// neighbourhood has no preferred direction (flat, or an isotropic corner), 1 where it is a clean
// straight edge. The closed form avoids solving for the eigenvalues themselves.
//
// This is the number the ORIENTATION alone cannot supply. An angle is defined everywhere, including
// in flat regions where it is pure noise, so seeding every candidate along it says nothing about
// whether the region is actually anisotropic. Approximation theory is explicit that the n^-2 rate
// belongs to elements matched to a locally ANISOTROPIC structure, and our measured slope is -0.98 —
// isotropic — which is what makes "how elongated, and how confidently" the missing input rather than
// "which way".
func tensorCoherence(sxx, syy, sxy float64) float64 {
	tr := sxx + syy
	if tr <= 1e-12 {
		return 0
	}
	d := math.Hypot(sxx-syy, 2*sxy) // λ₁−λ₂
	c := d / tr
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}
