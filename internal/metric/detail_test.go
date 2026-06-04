package metric

import "testing"

// makeRGBA builds a w*h*4 straight-alpha float image from a per-pixel luma fn.
func makeRGBA(w, h int, lum func(x, y int) float32) []float32 {
	px := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := lum(x, y)
			i := (y*w + x) * 4
			px[i], px[i+1], px[i+2], px[i+3] = v, v, v, 1
		}
	}
	return px
}

func TestDetailGridFlatIsZero(t *testing.T) {
	w, h := 32, 32
	px := makeRGBA(w, h, func(x, y int) float32 { return 0.5 }) // uniform -> no edges
	g := DetailGrid(px, w, h, 4, 4)
	if g == nil {
		t.Fatal("nil grid")
	}
	for i, v := range g {
		if v != 0 {
			t.Fatalf("flat image cell %d should be 0, got %v", i, v)
		}
	}
}

func TestDetailGridEdgeCellIsHottest(t *testing.T) {
	w, h := 40, 40
	// A single vertical step edge at x=20: left black, right white. Only the columns
	// straddling x=20 carry gradient; the rest are flat.
	px := makeRGBA(w, h, func(x, y int) float32 {
		if x < 20 {
			return 0
		}
		return 1
	})
	gw, gh := 4, 4
	g := DetailGrid(px, w, h, gw, gh)
	// The busiest cell must be normalized to 1.
	var maxv float32
	for _, v := range g {
		if v > maxv {
			maxv = v
		}
	}
	if maxv != 1 {
		t.Fatalf("expected a normalized max cell = 1, got %v", maxv)
	}
	// The two middle columns (gx=1,2 span x∈[10,30), straddling the x=20 edge) must be
	// hotter than the outer columns (gx=0 spans [0,10), flat black; gx=3 spans [30,40), flat white).
	row := 0 // any row; the edge is vertical so all rows behave the same
	mid := g[row*gw+1] + g[row*gw+2]
	outer := g[row*gw+0] + g[row*gw+3]
	if mid <= outer {
		t.Fatalf("edge-straddling cells (%v) should exceed flat cells (%v)", mid, outer)
	}
	if g[row*gw+0] != 0 || g[row*gw+3] != 0 {
		t.Fatalf("flat outer cells should be 0, got %v and %v", g[row*gw+0], g[row*gw+3])
	}
}

func TestDetailGridGuards(t *testing.T) {
	if DetailGrid(nil, 0, 0, 4, 4) != nil {
		t.Fatal("expected nil for zero dims")
	}
	px := makeRGBA(8, 8, func(x, y int) float32 { return 0.2 })
	if DetailGrid(px, 8, 8, 0, 4) != nil {
		t.Fatal("expected nil for zero grid width")
	}
}
