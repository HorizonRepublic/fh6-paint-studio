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
