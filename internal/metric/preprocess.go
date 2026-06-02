package metric

import "math"

// Target preprocessing for FLAT/vector content. The idea: clean the target the
// shape generator fits — band the luminance (hard at
// edges, soft in gradients) and optionally quantize color — so shapes lock onto
// crisp contours and exact flat fills instead of chasing compression/AA noise.
// Operates on straight-alpha RGBA float in [0,1], len w*h*4. Returns a NEW buffer.

// LumaBands applies edge-weighted luminance banding: quantize perceptual luma into
// `levels` bands, blend MORE toward the bands at edges (band_weight 0.16..0.50) and
// keep source luma in smooth regions, then a slight contrast lift. Chroma (hue) is
// preserved by scaling RGB by the luma ratio. Perceptual luma is used as a close,
// dependency-free approximation to banding in LAB-L.
func LumaBands(px []float32, w, h int) []float32 {
	n := w * h
	lum := make([]float32, n)
	for i := 0; i < n; i++ {
		lum[i] = 0.2126*px[i*4] + 0.7152*px[i*4+1] + 0.0722*px[i*4+2]
	}
	// 3x3 gaussian-ish blur of luma (for a stable edge estimate).
	blur := boxBlurLuma(lum, w, h)
	const levels = 64.0
	const step = 1.0 / levels
	out := make([]float32, len(px))
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
		return blur[y*w+x]
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			// Sobel gradient magnitude on blurred luma (in 0..1; *255 to work in
			// 0..255-luma threshold units).
			gx := (at(x+1, y-1) + 2*at(x+1, y) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x-1, y) + at(x-1, y+1))
			gy := (at(x-1, y+1) + 2*at(x, y+1) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x, y-1) + at(x+1, y-1))
			edge := float32(math.Sqrt(float64(gx*gx+gy*gy))) * 255
			en := clamp01f((edge - 3) / 18)
			bw := 0.16 + en*0.34
			L := lum[i]
			lq := float32(math.Floor(float64(L/step)))*step + step*0.5
			lo := lq*bw + L*(1-bw)
			lo = (lo-0.5)*1.005 + 0.5
			lo = clamp01f(lo)
			scale := float32(1)
			if L > 1e-4 {
				scale = lo / L
			}
			out[i*4+0] = clamp01f(px[i*4+0] * scale)
			out[i*4+1] = clamp01f(px[i*4+1] * scale)
			out[i*4+2] = clamp01f(px[i*4+2] * scale)
			out[i*4+3] = px[i*4+3]
		}
	}
	return out
}

func boxBlurLuma(lum []float32, w, h int) []float32 {
	out := make([]float32, len(lum))
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
	// 3x3 gaussian kernel [1 2 1;2 4 2;1 2 1]/16.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := at(x-1, y-1) + 2*at(x, y-1) + at(x+1, y-1) +
				2*at(x-1, y) + 4*at(x, y) + 2*at(x+1, y) +
				at(x-1, y+1) + 2*at(x, y+1) + at(x+1, y+1)
			out[y*w+x] = s / 16
		}
	}
	return out
}

// Posterize quantizes each RGB channel to `levels` evenly-spaced values (alpha
// untouched). levels<2 is a no-op. Used for flat/logo content to snap broad color
// regions to exact constants so shapes are not wasted modelling compression noise.
func Posterize(px []float32, w, h, levels int) []float32 {
	out := make([]float32, len(px))
	copy(out, px)
	if levels < 2 {
		return out
	}
	q := float32(levels - 1)
	for i := 0; i < w*h; i++ {
		for c := 0; c < 3; c++ {
			v := out[i*4+c]
			out[i*4+c] = float32(math.Round(float64(v*q))) / q
		}
	}
	return out
}
