package shape

import "fh6-paint-studio/internal/model"

// CoverBlocks fills a region with axis-aligned rectangles via breadth-first binary subdivision: a box
// that is at least fillFrac in-region (or has shrunk to minBlock) is emitted as one solid rect;
// otherwise it is split along its longer axis. BFS places the largest blocks first, so if the budget
// (maxShapes) runs out only fine detail is missing. Rectangles tile without gaps → solid flat fills,
// and every in-region pixel is guaranteed under some rect (boundary cells emit at minBlock).
func CoverBlocks(r *Region, maxShapes, minBlock int, fillFrac float64) []model.Shape {
	w, h := r.BW, r.BH
	ps := make([]int, (w+1)*(h+1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := 0
			if r.Mask[y*w+x] {
				v = 1
			}
			ps[(y+1)*(w+1)+(x+1)] = v + ps[y*(w+1)+(x+1)] + ps[(y+1)*(w+1)+x] - ps[y*(w+1)+x]
		}
	}
	count := func(x, y, bw, bh int) int {
		x2, y2 := x+bw, y+bh
		return ps[y2*(w+1)+x2] - ps[y*(w+1)+x2] - ps[y2*(w+1)+x] + ps[y*(w+1)+x]
	}
	col := []int{C255(r.Color.R), C255(r.Color.G), C255(r.Color.B), 255}
	var shapes []model.Shape
	type box struct{ x, y, bw, bh int }
	queue := []box{{0, 0, w, h}}
	for head := 0; head < len(queue) && len(shapes) < maxShapes; head++ {
		b := queue[head]
		cnt := count(b.x, b.y, b.bw, b.bh)
		if cnt == 0 {
			continue
		}
		frac := float64(cnt) / float64(b.bw*b.bh)
		atMin := b.bw <= minBlock && b.bh <= minBlock
		if frac >= fillFrac || atMin {
			shapes = append(shapes, model.Shape{Type: model.TypeRotatedRectangle, Color: col,
				Data: []float64{float64(r.X0+b.x) + float64(b.bw)/2, float64(r.Y0+b.y) + float64(b.bh)/2,
					float64(b.bw) / 2, float64(b.bh) / 2, 0}})
			continue
		}
		if b.bw >= b.bh {
			hw := b.bw / 2
			queue = append(queue, box{b.x, b.y, hw, b.bh}, box{b.x + hw, b.y, b.bw - hw, b.bh})
		} else {
			hh := b.bh / 2
			queue = append(queue, box{b.x, b.y, b.bw, hh}, box{b.x, b.y + hh, b.bw, b.bh - hh})
		}
	}
	return shapes
}

// distanceTransform returns, per in-mask pixel, an approximate Euclidean distance to the nearest
// out-of-mask pixel (chamfer 3-4, two passes). The bbox boundary counts as outside, so a region that
// fills its bbox still has a finite distance (never an unbounded peak).
func distanceTransform(mask []bool, w, h int) []float64 {
	const inf = 1e9
	const d1, d2 = 1.0, 1.41421356
	d := make([]float64, w*h)
	for i := range d {
		if mask[i] {
			d[i] = inf
		}
	}
	at := func(x, y int) float64 {
		if x < 0 || y < 0 || x >= w || y >= h {
			return 0
		}
		return d[y*w+x]
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if d[i] == 0 {
				continue
			}
			if v := at(x-1, y) + d1; v < d[i] {
				d[i] = v
			}
			if v := at(x, y-1) + d1; v < d[i] {
				d[i] = v
			}
			if v := at(x-1, y-1) + d2; v < d[i] {
				d[i] = v
			}
			if v := at(x+1, y-1) + d2; v < d[i] {
				d[i] = v
			}
		}
	}
	for y := h - 1; y >= 0; y-- {
		for x := w - 1; x >= 0; x-- {
			i := y*w + x
			if d[i] == 0 {
				continue
			}
			if v := at(x+1, y) + d1; v < d[i] {
				d[i] = v
			}
			if v := at(x, y+1) + d1; v < d[i] {
				d[i] = v
			}
			if v := at(x+1, y+1) + d2; v < d[i] {
				d[i] = v
			}
			if v := at(x-1, y+1) + d2; v < d[i] {
				d[i] = v
			}
		}
	}
	return d
}

// CoverInscribe fills a region with inscribed circles (medial-axis packing). Kept as an alternative
// cover strategy; CoverBlocks gives denser flat fills, but circle packing suits soft/organic masks.
func CoverInscribe(r *Region, maxShapes int, minR, covThresh float64) []model.Shape {
	w, h := r.BW, r.BH
	mask := make([]bool, len(r.Mask))
	copy(mask, r.Mask)
	col := []int{C255(r.Color.R), C255(r.Color.G), C255(r.Color.B), 255}
	covered := 0
	var shapes []model.Shape
	for len(shapes) < maxShapes {
		d := distanceTransform(mask, w, h)
		bi, bd := -1, 0.0
		for i, v := range d {
			if v > bd {
				bd, bi = v, i
			}
		}
		if bi < 0 || bd < minR {
			break
		}
		cx, cy := bi%w, bi/w
		rad := bd
		shapes = append(shapes, model.Shape{Type: model.TypeRotatedEllipse, Color: col,
			Data: []float64{float64(r.X0+cx) + 0.5, float64(r.Y0+cy) + 0.5, rad, rad, 0}})
		r0 := int(rad) + 1
		for yy := cy - r0; yy <= cy+r0; yy++ {
			if yy < 0 || yy >= h {
				continue
			}
			for xx := cx - r0; xx <= cx+r0; xx++ {
				if xx < 0 || xx >= w {
					continue
				}
				dx, dy := float64(xx-cx), float64(yy-cy)
				if dx*dx+dy*dy <= rad*rad {
					if j := yy*w + xx; mask[j] {
						mask[j] = false
						covered++
					}
				}
			}
		}
		if covThresh > 0 && r.Area > 0 && float64(covered)/float64(r.Area) >= covThresh {
			break
		}
	}
	return shapes
}
