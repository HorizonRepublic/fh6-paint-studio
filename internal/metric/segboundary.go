package metric

import (
	"math"

	"fh6-paint-studio/internal/model"
)

// BoundaryHardMap turns a segmentation into the per-pixel "hard-edged structure" score the shape
// gates key on — the same contract as HardEdgeMap (len w*h, [0,1], 1 = structure), from a different
// question. HardEdgeMap measures Sobel edge DENSITY in a 12px cell, so a single line crossing an
// otherwise smooth cell saturates it: measured against segmentation boundaries on three of the
// owner's generations it reads 0.67 mean over region INTERIORS versus 0.79 on real boundaries
// (img_9), which is barely any discrimination at all. Every anti-artifact mechanism keys on that
// map — the kind gate, the glow swap, the region-weighted polish terms — so all three fire in the
// wrong places at once, in exactly the smooth zones the owner keeps pointing at.
//
// Here a pixel scores high only near an ACTUAL label boundary, weighted by the colour contrast
// across it: a boundary between two nearly-identical regions is not the kind of edge that earns a
// rect or a triangle. The score decays with distance so the gate keeps an apron around structure
// (a corner needs candidates that reach past the corner itself), computed as a max-decay
// propagation: two raster passes carrying max(v_neighbour * decay^step).
func BoundaryHardMap(seg *Segments, w, h int, contrastFull, falloff float64) []float32 {
	if seg == nil || w <= 0 || h <= 0 || len(seg.Label) < w*h {
		return nil
	}
	if contrastFull <= 0 {
		contrastFull = 0.12
	}
	if falloff <= 0 {
		falloff = 3
	}
	out := make([]float32, w*h)
	meanDist := func(a, b int32) float64 {
		if a < 0 || b < 0 || int(a)*3+2 >= len(seg.Mean) || int(b)*3+2 >= len(seg.Mean) {
			return 0
		}
		var s float64
		for c := 0; c < 3; c++ {
			d := float64(model.LinearToSRGB(seg.Mean[int(a)*3+c]) - model.LinearToSRGB(seg.Mean[int(b)*3+c]))
			s += d * d
		}
		return math.Sqrt(s)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			l := seg.Label[i]
			var best float64
			chk := func(j int) {
				if seg.Label[j] != l {
					if c := meanDist(l, seg.Label[j]) / contrastFull; c > best {
						best = c
					}
				}
			}
			if x > 0 {
				chk(i - 1)
			}
			if x+1 < w {
				chk(i + 1)
			}
			if y > 0 {
				chk(i - w)
			}
			if y+1 < h {
				chk(i + w)
			}
			if best > 1 {
				best = 1
			}
			out[i] = float32(best)
		}
	}
	dOrtho := float32(math.Exp(-1 / falloff))
	dDiag := float32(math.Exp(-math.Sqrt2 / falloff))
	relax := func(i, j int, d float32) {
		if v := out[j] * d; v > out[i] {
			out[i] = v
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if x > 0 {
				relax(i, i-1, dOrtho)
			}
			if y > 0 {
				relax(i, i-w, dOrtho)
				if x > 0 {
					relax(i, i-w-1, dDiag)
				}
				if x+1 < w {
					relax(i, i-w+1, dDiag)
				}
			}
		}
	}
	for y := h - 1; y >= 0; y-- {
		for x := w - 1; x >= 0; x-- {
			i := y*w + x
			if x+1 < w {
				relax(i, i+1, dOrtho)
			}
			if y+1 < h {
				relax(i, i+w, dOrtho)
				if x+1 < w {
					relax(i, i+w+1, dDiag)
				}
				if x > 0 {
					relax(i, i+w-1, dDiag)
				}
			}
		}
	}
	return out
}
