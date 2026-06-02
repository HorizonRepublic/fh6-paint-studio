package metric

import (
	"math"
	"testing"
)

func TestBoundaryDistanceEdgeIsZero(t *testing.T) {
	w, h := 40, 8
	// Vertical luma step at x=20 → the two columns straddling it are edge pixels (dist 0).
	px := makeRGBA(w, h, func(x, y int) float32 {
		if x < 20 {
			return 0
		}
		return 1
	})
	d := BoundaryDistance(px, w, h, 0.18)
	if d == nil {
		t.Fatal("nil field")
	}
	// Some pixel near x=20 must be 0 (on the edge).
	var minNearEdge float32 = 1e9
	for y := 0; y < h; y++ {
		if d[y*w+19] < minNearEdge {
			minNearEdge = d[y*w+19]
		}
		if d[y*w+20] < minNearEdge {
			minNearEdge = d[y*w+20]
		}
	}
	if minNearEdge != 0 {
		t.Fatalf("a pixel adjacent to the step edge should have distance 0, got %v", minNearEdge)
	}
	// A pixel far from the edge (x=0) should have distance ≈ its pixel offset to the edge (~19),
	// and must be much larger than near the edge.
	far := d[0*w+0]
	if far < 10 {
		t.Fatalf("pixel far from edge should have large distance, got %v", far)
	}
	// Monotonic-ish: x=0 farther than x=15 (both left of the edge).
	if d[15] >= d[0] {
		t.Fatalf("distance should grow with offset from the edge: d[15]=%v d[0]=%v", d[15], d[0])
	}
}

func TestBoundaryDistanceFlatImageIsLarge(t *testing.T) {
	w, h := 24, 24
	px := makeRGBA(w, h, func(x, y int) float32 { return 0.5 }) // no edges at all
	d := BoundaryDistance(px, w, h, 0.18)
	// With no boundary, every pixel stays at the "infinite" (huge) seed scaled by /3.
	for i, v := range d {
		if v < 1e6 {
			t.Fatalf("flat image cell %d should stay ~infinite distance, got %v", i, v)
		}
	}
}

func TestBoundaryDistanceCutoutSilhouette(t *testing.T) {
	w, h := 24, 24
	// A centered opaque disc on a transparent field — the silhouette is an alpha boundary,
	// even though luma is uniform inside the disc.
	cx, cy, r := 12.0, 12.0, 7.0
	px := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			if math.Hypot(float64(x)-cx, float64(y)-cy) <= r {
				px[i], px[i+1], px[i+2], px[i+3] = 0.5, 0.5, 0.5, 1
			} // else stays 0,0,0,0 (transparent)
		}
	}
	d := BoundaryDistance(px, w, h, 0.18)
	// The disc CENTRE is ~r from the silhouette, so its distance must be > 0 but finite (< inf).
	center := d[12*w+12]
	if center <= 0 || center > 1e6 {
		t.Fatalf("disc centre should have a finite positive distance to the silhouette, got %v", center)
	}
	// A pixel right at the silhouette edge (~x=5,y=12) should be near 0.
	edge := d[12*w+5]
	if edge > 2 {
		t.Fatalf("a pixel on the silhouette should have ~0 distance, got %v", edge)
	}
}

func TestBoundaryDistanceGuards(t *testing.T) {
	if BoundaryDistance(nil, 0, 0, 0.18) != nil {
		t.Fatal("expected nil for zero dims")
	}
	px := makeRGBA(8, 8, func(x, y int) float32 { return 0.2 })
	if BoundaryDistance(px, 8, 8, 0) != nil {
		t.Fatal("expected nil for non-positive edgeThresh")
	}
}
