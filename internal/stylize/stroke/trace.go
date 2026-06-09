package stroke

import "math"

// traceBoundary returns the outer boundary of a bbox-local mask as an ordered loop of pixel points
// (Moore-neighbour tracing, clockwise). The result is at pixel resolution; simplify it before placing
// strokes. Returns nil for an empty mask.
func traceBoundary(mask []bool, w, h int) [][2]float64 {
	get := func(x, y int) bool { return x >= 0 && y >= 0 && x < w && y < h && mask[y*w+x] }
	sx, sy := -1, -1
	for y := 0; y < h && sy < 0; y++ {
		for x := 0; x < w; x++ {
			if get(x, y) {
				sx, sy = x, y
				break
			}
		}
	}
	if sx < 0 {
		return nil
	}
	// clockwise neighbour offsets: N, NE, E, SE, S, SW, W, NW
	dirs := [8][2]int{{0, -1}, {1, -1}, {1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}}
	out := [][2]float64{{float64(sx), float64(sy)}}
	px, py, btd := sx, sy, 6 // entered the start from the west
	for iter := 0; iter < 8*w*h+10; iter++ {
		found := false
		for k := 1; k <= 8; k++ {
			d := (btd + k) & 7
			nx, ny := px+dirs[d][0], py+dirs[d][1]
			if get(nx, ny) {
				if nx == sx && ny == sy {
					return out
				}
				out = append(out, [2]float64{float64(nx), float64(ny)})
				btd = (d + 4) & 7
				px, py = nx, ny
				found = true
				break
			}
		}
		if !found {
			break // isolated pixel
		}
	}
	return out
}

// simplify reduces a polyline with Douglas–Peucker at tolerance eps (perpendicular distance, px).
func simplify(pts [][2]float64, eps float64) [][2]float64 {
	if len(pts) < 3 {
		return pts
	}
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	var rec func(lo, hi int)
	rec = func(lo, hi int) {
		if hi <= lo+1 {
			return
		}
		ax, ay := pts[lo][0], pts[lo][1]
		bx, by := pts[hi][0], pts[hi][1]
		dx, dy := bx-ax, by-ay
		ll := math.Hypot(dx, dy)
		maxD, maxI := -1.0, -1
		for i := lo + 1; i < hi; i++ {
			var d float64
			if ll == 0 {
				d = math.Hypot(pts[i][0]-ax, pts[i][1]-ay)
			} else {
				d = math.Abs((pts[i][0]-ax)*dy-(pts[i][1]-ay)*dx) / ll
			}
			if d > maxD {
				maxD, maxI = d, i
			}
		}
		if maxD > eps {
			keep[maxI] = true
			rec(lo, maxI)
			rec(maxI, hi)
		}
	}
	rec(0, len(pts)-1)
	var out [][2]float64
	for i, k := range keep {
		if k {
			out = append(out, pts[i])
		}
	}
	return out
}
