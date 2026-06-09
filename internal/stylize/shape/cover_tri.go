package shape

import (
	"math"

	"fh6-paint-studio/internal/model"
)

// CoverTriangles fills a region with TRIANGLES whose edges follow its boundary, instead of axis-aligned
// blocks: trace the region's outer contour → Douglas-Peucker simplify → ear-clip triangulate → emit one
// FH6 triangle per ear. Triangle edges run along the (diagonal/curved) boundary, so there is no
// stair-step. The vertex count (≈ triangles+2) is bounded to maxShapes by raising the simplify tolerance.
// Holes are ignored on purpose: an enclosed region is a separate, later (smaller-area) fill that
// overdraws this one, so its colour wins. eps0 is the starting simplify tolerance (px).
func CoverTriangles(r *Region, maxShapes int, eps0 float64, dilate int) []model.Shape {
	if maxShapes < 1 {
		return nil
	}
	// Pad + dilate the mask so neighbouring regions overlap at shared edges (no background slivers).
	if dilate < 0 {
		dilate = 0
	}
	pad := dilate
	if pad < 1 {
		pad = 1
	}
	pw, ph := r.BW+2*pad, r.BH+2*pad
	dm := make([]bool, pw*ph)
	for y := 0; y < r.BH; y++ {
		for x := 0; x < r.BW; x++ {
			if !r.Mask[y*r.BW+x] {
				continue
			}
			for dy := -dilate; dy <= dilate; dy++ {
				for dx := -dilate; dx <= dilate; dx++ {
					dm[(y+pad+dy)*pw+(x+pad+dx)] = true
				}
			}
		}
	}
	loop := traceContour(dm, pw, ph)
	if len(loop) < 3 {
		return nil
	}
	eps := eps0
	var poly [][2]float64
	for {
		poly = douglasPeucker(loop, eps)
		if len(poly) >= 3 && len(poly)-2 <= maxShapes {
			break
		}
		if len(poly) < 3 || eps > float64(r.BW+r.BH) {
			if len(poly) < 3 {
				return nil
			}
			break
		}
		eps *= 1.4
	}
	tris := earClip(poly)
	col := []int{C255(r.Color.R), C255(r.Color.G), C255(r.Color.B), 255}
	ox, oy := float64(r.X0-pad), float64(r.Y0-pad) // mask was padded by pad px
	shapes := make([]model.Shape, 0, len(tris))
	for _, t := range tris {
		if len(shapes) >= maxShapes {
			break
		}
		a, b, c := poly[t[0]], poly[t[1]], poly[t[2]]
		shapes = append(shapes, model.Shape{Type: model.TypeTriangle, Color: col,
			Data: []float64{ox + a[0], oy + a[1], ox + b[0], oy + b[1], ox + c[0], oy + c[1]}})
	}
	return shapes
}

// traceContour returns the outer boundary of a bbox-local mask as an ordered loop (Moore-neighbour,
// clockwise), at pixel resolution. Returns nil for an empty mask. (Same method as the stroke tracer,
// kept here so the shape package is self-contained.)
func traceContour(mask []bool, w, h int) [][2]float64 {
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
	dirs := [8][2]int{{0, -1}, {1, -1}, {1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}}
	out := [][2]float64{{float64(sx), float64(sy)}}
	px, py, btd := sx, sy, 6
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
			break
		}
	}
	return out
}

// douglasPeucker simplifies a closed pixel loop to a polygon at perpendicular tolerance eps.
func douglasPeucker(pts [][2]float64, eps float64) [][2]float64 {
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
		ax, ay, bx, by := pts[lo][0], pts[lo][1], pts[hi][0], pts[hi][1]
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

// earClip triangulates a simple polygon (Eberly, "Triangulation by Ear Clipping"). Returns index
// triples into poly. Robust to concavity; holes must be elided by the caller.
func earClip(poly [][2]float64) [][3]int {
	n := len(poly)
	if n < 3 {
		return nil
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	if signedArea(poly) < 0 { // make CCW (interior on the left), so convex ⇔ cross > 0
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			idx[i], idx[j] = idx[j], idx[i]
		}
	}
	var tris [][3]int
	guard := 0
	for len(idx) > 3 && guard < 4*n+16 {
		guard++
		m := len(idx)
		ear := -1
		for i := 0; i < m; i++ {
			ip, ic, in := idx[(i-1+m)%m], idx[i], idx[(i+1)%m]
			a, b, c := poly[ip], poly[ic], poly[in]
			if cross2(b[0]-a[0], b[1]-a[1], c[0]-b[0], c[1]-b[1]) <= 0 {
				continue // reflex / collinear
			}
			isEar := true
			for j := 0; j < m; j++ {
				k := idx[j]
				if k == ip || k == ic || k == in {
					continue
				}
				if pointInTri(poly[k], a, b, c) {
					isEar = false
					break
				}
			}
			if isEar {
				ear = i
				break
			}
		}
		if ear < 0 { // no ear (numerical / degenerate): clip the first vertex to make progress
			ear = 0
		}
		ip, ic, in := idx[(ear-1+m)%m], idx[ear], idx[(ear+1)%m]
		tris = append(tris, [3]int{ip, ic, in})
		idx = append(idx[:ear], idx[ear+1:]...)
		guard = 0
	}
	if len(idx) == 3 {
		tris = append(tris, [3]int{idx[0], idx[1], idx[2]})
	}
	return tris
}

func signedArea(p [][2]float64) float64 {
	a := 0.0
	for i := 0; i < len(p); i++ {
		j := (i + 1) % len(p)
		a += p[i][0]*p[j][1] - p[j][0]*p[i][1]
	}
	return a / 2
}

func cross2(ax, ay, bx, by float64) float64 { return ax*by - ay*bx }

func pointInTri(p, a, b, c [2]float64) bool {
	d1 := cross2(b[0]-a[0], b[1]-a[1], p[0]-a[0], p[1]-a[1])
	d2 := cross2(c[0]-b[0], c[1]-b[1], p[0]-b[0], p[1]-b[1])
	d3 := cross2(a[0]-c[0], a[1]-c[1], p[0]-c[0], p[1]-c[1])
	hasNeg := d1 < 0 || d2 < 0 || d3 < 0
	hasPos := d1 > 0 || d2 > 0 || d3 > 0
	return !(hasNeg && hasPos)
}
