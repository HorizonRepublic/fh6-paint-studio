package shape

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

func triArea(a, b, c [2]float64) float64 {
	return math.Abs(cross2(b[0]-a[0], b[1]-a[1], c[0]-a[0], c[1]-a[1])) / 2
}

func polyArea(p [][2]float64) float64 { return math.Abs(signedArea(p)) }

func TestEarClipSquare(t *testing.T) {
	sq := [][2]float64{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	tris := earClip(sq)
	if len(tris) != 2 {
		t.Fatalf("square should give 2 triangles, got %d", len(tris))
	}
	var area float64
	for _, tr := range tris {
		area += triArea(sq[tr[0]], sq[tr[1]], sq[tr[2]])
	}
	if math.Abs(area-100) > 1e-6 {
		t.Errorf("triangle area sum = %.3f, want 100", area)
	}
}

func TestEarClipConcave(t *testing.T) {
	// L-shape (concave), area = 100 - 25 = ... actually a 10x10 with a 5x5 bite: area 75.
	l := [][2]float64{{0, 0}, {10, 0}, {10, 5}, {5, 5}, {5, 10}, {0, 10}}
	tris := earClip(l)
	if len(tris) != len(l)-2 {
		t.Errorf("polygon of %d verts should give %d triangles, got %d", len(l), len(l)-2, len(tris))
	}
	var area float64
	for _, tr := range tris {
		area += triArea(l[tr[0]], l[tr[1]], l[tr[2]])
	}
	if math.Abs(area-polyArea(l)) > 1e-6 {
		t.Errorf("concave triangulation area %.3f != polygon area %.3f", area, polyArea(l))
	}
}

func TestCoverTrianglesDiskRegion(t *testing.T) {
	const w, h = 40, 40
	mask := make([]bool, w*h)
	cx, cy, r := 20.0, 20.0, 15.0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if math.Hypot(float64(x)-cx, float64(y)-cy) <= r {
				mask[y*w+x] = true
			}
		}
	}
	reg := &Region{Color: model.RGBA{R: 0.5, G: 0.2, B: 0.7, A: 1}, X0: 0, Y0: 0, BW: w, BH: h, Mask: mask}
	tris := CoverTriangles(reg, 200, 1.5, 1)
	if len(tris) < 3 {
		t.Fatalf("disk region should triangulate to several triangles, got %d", len(tris))
	}
	for _, s := range tris {
		if s.Type != model.TypeTriangle || len(s.Data) != 6 {
			t.Fatalf("expected triangle shapes with 6 data, got type=%d len=%d", s.Type, len(s.Data))
		}
	}
}

func TestCoverTrianglesBudget(t *testing.T) {
	const w, h = 60, 60
	mask := make([]bool, w*h)
	for i := range mask {
		mask[i] = true // full square → many boundary points before simplify
	}
	reg := &Region{Color: model.RGBA{A: 1}, BW: w, BH: h, Mask: mask}
	tris := CoverTriangles(reg, 8, 0.5, 1) // tight budget forces eps up
	if len(tris) > 8 {
		t.Errorf("budget 8 exceeded: got %d triangles", len(tris))
	}
}
