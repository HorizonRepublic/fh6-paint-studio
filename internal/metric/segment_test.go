package metric

import (
	"math/rand"
	"testing"
)

// Two flat halves separated by a hard edge must land in two regions, and noise inside a half must
// not split it — that is the containment property the whole pass rests on.
func TestSegment_SplitsAtTheEdgeNotAtNoise(t *testing.T) {
	w, h := 96, 96
	rng := rand.New(rand.NewSource(1))
	img := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			base := float32(0.15)
			if x >= w/2 {
				base = 0.75
			}
			n := float32(rng.NormFloat64()) * 0.01
			i := (y*w + x) * 4
			img[i], img[i+1], img[i+2], img[i+3] = base+n, base+n, base+n, 1
		}
	}
	seg := Segment(img, w, h, 300, 64)
	if seg == nil {
		t.Fatal("nil segmentation")
	}
	if seg.N < 2 || seg.N > 6 {
		t.Fatalf("want a handful of regions, got %d", seg.N)
	}
	if seg.LabelAt(10, 48) == seg.LabelAt(85, 48) {
		t.Fatal("the two halves share a label")
	}
	for _, x := range []int{5, 20, 40} {
		if seg.LabelAt(x, 10) != seg.LabelAt(x, 80) {
			t.Fatalf("noise split the left half at x=%d", x)
		}
	}
}

// A smooth ramp has no edge to split on: it must come back as one region, whatever k. This is the
// case that separates a label map from a Sobel threshold — the ramp is all gradient and no boundary.
func TestSegment_RampStaysWhole(t *testing.T) {
	w, h := 64, 64
	img := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(x) / float32(w-1)
			i := (y*w + x) * 4
			img[i], img[i+1], img[i+2], img[i+3] = v, v, v, 1
		}
	}
	seg := Segment(img, w, h, 300, 64)
	if seg.N != 1 {
		t.Fatalf("a pure ramp should be one region, got %d", seg.N)
	}
}

// Labels must be dense and the bookkeeping self-consistent; the generators index Mean/Size by label.
func TestSegment_DenseLabelsAndSizes(t *testing.T) {
	w, h := 48, 32
	rng := rand.New(rand.NewSource(7))
	img := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		v := float32(rng.Float64())
		img[i*4], img[i*4+1], img[i*4+2], img[i*4+3] = v, v*0.5, 1-v, 1
	}
	seg := Segment(img, w, h, 50, 16)
	var total int32
	for l := 0; l < seg.N; l++ {
		if seg.Size[l] <= 0 {
			t.Fatalf("region %d is empty", l)
		}
		total += seg.Size[l]
	}
	if int(total) != w*h {
		t.Fatalf("sizes sum to %d, want %d", total, w*h)
	}
	for _, l := range seg.Label {
		if l < 0 || int(l) >= seg.N {
			t.Fatalf("label %d out of range [0,%d)", l, seg.N)
		}
	}
}

// Same input, same labels: candidate generation reads this map, so a run-to-run reshuffle would
// silently behave like a seed change.
func TestSegment_Deterministic(t *testing.T) {
	w, h := 40, 40
	rng := rand.New(rand.NewSource(3))
	img := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		v := float32(rng.Float64())
		img[i*4], img[i*4+1], img[i*4+2], img[i*4+3] = v, v, v, 1
	}
	a := Segment(img, w, h, 100, 8)
	b := Segment(img, w, h, 100, 8)
	if a.N != b.N {
		t.Fatalf("region count differs: %d vs %d", a.N, b.N)
	}
	for i := range a.Label {
		if a.Label[i] != b.Label[i] {
			t.Fatalf("label %d differs: %d vs %d", i, a.Label[i], b.Label[i])
		}
	}
}
