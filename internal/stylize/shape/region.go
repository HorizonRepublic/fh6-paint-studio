// Package shape holds the stylizer's shared raster primitives — region segmentation and the cover
// strategies that turn a pixel mask into FH6 shapes. Both the Fill and Stroke engines compose these
// (engines stay decoupled; the geometry utilities live here, once).
package shape

import "fh6-paint-studio/internal/model"

// Region is one connected pixel area: a colour plus a bbox-local boolean mask (memory stays
// proportional to the bboxes, not regions×canvas).
type Region struct {
	Color  model.RGBA // sRGB [0,1]
	X0, Y0 int        // bbox origin in canvas coords
	BW, BH int        // bbox dimensions
	Mask   []bool     // BW*BH, true = pixel belongs to this region
	Area   int        // number of true pixels
}

// Segment finds 4-connected components of equal index and returns them as Regions, dropping any whose
// area is below minArea (noise speckle). palette[idx[p]] gives a region's colour.
func Segment(w, h int, idx []int, palette []model.RGBA, minArea int) []Region {
	visited := make([]bool, w*h)
	var regions []Region
	var stack, pix []int
	for start := 0; start < w*h; start++ {
		if visited[start] {
			continue
		}
		ci := idx[start]
		stack = append(stack[:0], start)
		pix = pix[:0]
		visited[start] = true
		x0, y0, x1, y1 := w, h, -1, -1
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			pix = append(pix, p)
			px, py := p%w, p/w
			if px < x0 {
				x0 = px
			}
			if px > x1 {
				x1 = px
			}
			if py < y0 {
				y0 = py
			}
			if py > y1 {
				y1 = py
			}
			if px > 0 && !visited[p-1] && idx[p-1] == ci {
				visited[p-1] = true
				stack = append(stack, p-1)
			}
			if px < w-1 && !visited[p+1] && idx[p+1] == ci {
				visited[p+1] = true
				stack = append(stack, p+1)
			}
			if py > 0 && !visited[p-w] && idx[p-w] == ci {
				visited[p-w] = true
				stack = append(stack, p-w)
			}
			if py < h-1 && !visited[p+w] && idx[p+w] == ci {
				visited[p+w] = true
				stack = append(stack, p+w)
			}
		}
		if len(pix) < minArea {
			continue
		}
		bw, bh := x1-x0+1, y1-y0+1
		mask := make([]bool, bw*bh)
		for _, p := range pix {
			px, py := p%w, p/w
			mask[(py-y0)*bw+(px-x0)] = true
		}
		regions = append(regions, Region{Color: palette[ci], X0: x0, Y0: y0, BW: bw, BH: bh, Mask: mask, Area: len(pix)})
	}
	return regions
}

// C255 converts a 0..1 channel to a clamped 0..255 byte.
func C255(v float32) int {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return int(v*255 + 0.5)
}
