package metric

import (
	"math"
)

// GradientStats summarizes how much of an image is SMOOTH SHADING (gradients/ramps) versus flat
// fills versus hard-edged line-work — the signal a per-image adaptive tuner uses to decide how hard
// to push the gradient-primitive machinery (ShadePre / SmoothBase / glow-swap). Computed on a 12px
// cell grid over opaque pixels only.
type GradientStats struct {
	SmoothFrac float64 // fraction of opaque cells that are NOT hard-edged (HardEdgeMap mean < 0.1)
	RampFrac   float64 // fraction of opaque cells that are smooth AND carry a gentle non-zero luma slope (a real ramp, not a flat fill) — the "gradientness" number
	FlatFrac   float64 // fraction of opaque cells that are smooth AND ~flat (near-zero slope)
	HardFrac   float64 // fraction of opaque cells that are hard-edged line-work/wedges
}

// RampMap returns, per pixel, how much the local region is a SMOOTH SHADING RAMP in [0,1] — smooth
// (low HardEdgeMap) AND carrying a gentle non-zero luma slope (a real gradient, not a flat fill and
// not line-work). It is the "where the gradient is" signal: buildWeightMap boosts the shape budget
// in these cells so gradient zones get enough shapes to render smoothly (few facets there is exactly
// what makes them stand out). Cell-based (12px) + box-smoothed + bilinearly upsampled, len w*h,
// mirroring HardEdgeMap so the two compose cleanly.
func RampMap(target []float32, w, h int) []float32 {
	hard := HardEdgeMap(target, w, h)
	const (
		cell    = 12
		hardTau = 0.14  // above this = line-work, ramp score 0
		slopeLo = 0.004 // luma slope below this = flat fill (no ramp)
		slopeHi = 0.030 // slope at/above this = full ramp strength (steeper = a hard edge, capped)
	)
	// Three math.Pow per pixel, once per run, on one core. Per-pixel independent, so a row-band
	// split reproduces the serial values exactly.
	luma := make([]float32, w*h)
	heRows(h, func(y0, y1 int) {
		for i := y0 * w; i < y1*w; i++ {
			luma[i] = 0.2126*encSRGB(target[i*4+0]) + 0.7152*encSRGB(target[i*4+1]) + 0.0722*encSRGB(target[i*4+2])
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
		return float64(luma[y*w+x])
	}
	cw, ch := (w+cell-1)/cell, (h+cell-1)/cell
	cHard := make([]float64, cw*ch)
	cSlope := make([]float64, cw*ch)
	cN := make([]float64, cw*ch)
	cellRows(h, cell, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				ci := (y/cell)*cw + x/cell
				cN[ci]++
				cHard[ci] += float64(hard[y*w+x])
				gx := (at(x+1, y) - at(x-1, y)) * 0.5
				gy := (at(x, y+1) - at(x, y-1)) * 0.5
				cSlope[ci] += math.Hypot(gx, gy)
			}
		}
	})
	cell01 := make([]float64, cw*ch)
	for ci := range cN {
		if cN[ci] <= 0 {
			continue
		}
		if cHard[ci]/cN[ci] >= hardTau {
			continue
		}
		slope := cSlope[ci] / cN[ci]
		if slope < slopeLo {
			continue
		}
		r := (slope - slopeLo) / (slopeHi - slopeLo)
		if r > 1 {
			r = 1
		}
		cell01[ci] = r
	}
	// 3x3 box smooth (no cell-boundary steps) then bilinear upsample to per-pixel.
	sm := make([]float64, cw*ch)
	for cy := 0; cy < ch; cy++ {
		for cx := 0; cx < cw; cx++ {
			var s, n float64
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					x, y := cx+dx, cy+dy
					if x < 0 || y < 0 || x >= cw || y >= ch {
						continue
					}
					s += cell01[y*cw+x]
					n++
				}
			}
			sm[cy*cw+cx] = s / n
		}
	}
	out := make([]float32, w*h)
	for y := 0; y < h; y++ {
		fy := (float64(y) - cell/2) / cell
		cy0 := int(math.Floor(fy))
		ty := fy - float64(cy0)
		cy1 := cy0 + 1
		if cy0 < 0 {
			cy0, cy1, ty = 0, 0, 0
		}
		if cy1 >= ch {
			cy1 = ch - 1
			if cy0 > cy1 {
				cy0 = cy1
			}
		}
		for x := 0; x < w; x++ {
			fx := (float64(x) - cell/2) / cell
			cx0 := int(math.Floor(fx))
			tx := fx - float64(cx0)
			cx1 := cx0 + 1
			if cx0 < 0 {
				cx0, cx1, tx = 0, 0, 0
			}
			if cx1 >= cw {
				cx1 = cw - 1
				if cx0 > cx1 {
					cx0 = cx1
				}
			}
			v00 := sm[cy0*cw+cx0]
			v01 := sm[cy0*cw+cx1]
			v10 := sm[cy1*cw+cx0]
			v11 := sm[cy1*cw+cx1]
			top := v00 + (v01-v00)*tx
			bot := v10 + (v11-v10)*tx
			out[y*w+x] = float32(top + (bot-top)*ty)
		}
	}
	return out
}

// GradientFraction computes GradientStats for an RGBA float target (len w*h*4). Reuses HardEdgeMap
// for the hard/smooth split, then classifies each smooth cell as ramp vs flat by its mean intra-
// cell luma-gradient magnitude (a linear ramp has a small but steady slope; a flat fill ~0).
func GradientFraction(target []float32, w, h int) GradientStats {
	hard := HardEdgeMap(target, w, h)
	const (
		cell      = 12
		smoothTau = 0.10  // matches engine.smoothTau
		rampLo    = 0.004 // per-px luma slope: below = flat fill; above = a real shading ramp
	)
	// Per-pixel luma slope (central difference on sRGB luma) and opacity.
	luma := make([]float32, w*h)
	heRows(h, func(y0, y1 int) {
		for i := y0 * w; i < y1*w; i++ {
			r := encSRGB(target[i*4+0])
			g := encSRGB(target[i*4+1])
			b := encSRGB(target[i*4+2])
			luma[i] = 0.2126*r + 0.7152*g + 0.0722*b
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
		return float64(luma[y*w+x])
	}
	cw, ch := (w+cell-1)/cell, (h+cell-1)/cell
	cHard := make([]float64, cw*ch)
	cSlope := make([]float64, cw*ch)
	cOpaque := make([]float64, cw*ch)
	cN := make([]float64, cw*ch)
	cellRows(h, cell, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < w; x++ {
				ci := (y/cell)*cw + x/cell
				cN[ci]++
				cHard[ci] += float64(hard[y*w+x])
				if target[(y*w+x)*4+3] >= 0.5 {
					cOpaque[ci]++
				}
				gx := (at(x+1, y) - at(x-1, y)) * 0.5
				gy := (at(x, y+1) - at(x, y-1)) * 0.5
				cSlope[ci] += math.Hypot(gx, gy)
			}
		}
	})
	var smooth, ramp, flat, hardC, opaqueCells float64
	for ci := range cN {
		if cN[ci] <= 0 || cOpaque[ci] < 0.5*cN[ci] {
			continue // skip mostly-transparent cells (cutout background)
		}
		opaqueCells++
		mh := cHard[ci] / cN[ci]
		slope := cSlope[ci] / cN[ci]
		if mh >= smoothTau {
			hardC++
			continue
		}
		smooth++
		if slope >= rampLo {
			ramp++
		} else {
			flat++
		}
	}
	if opaqueCells <= 0 {
		return GradientStats{}
	}
	return GradientStats{
		SmoothFrac: smooth / opaqueCells,
		RampFrac:   ramp / opaqueCells,
		FlatFrac:   flat / opaqueCells,
		HardFrac:   hardC / opaqueCells,
	}
}
