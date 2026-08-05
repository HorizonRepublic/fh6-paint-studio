package metric

import "testing"

// The gate map must be high at a real colour boundary and fall to ~0 in the smooth interior — the
// discrimination HardEdgeMap loses when one line crosses its 12px cell.
func TestBoundaryHardMap_HighAtEdgeZeroInside(t *testing.T) {
	w, h := 64, 64
	img := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(0.1)
			if x >= w/2 {
				v = 0.8
			}
			i := (y*w + x) * 4
			img[i], img[i+1], img[i+2], img[i+3] = v, v, v, 1
		}
	}
	// The pre-blur turns a synthetic hard step into a couple of transition columns, which survive as
	// their own thin regions — harmless here, the point is that the two flats do not share a label.
	seg := Segment(img, w, h, 2, 64)
	if seg.LabelAt(8, 32) == seg.LabelAt(55, 32) {
		t.Fatal("the two flats share a label")
	}
	m := BoundaryHardMap(seg, w, h, 0.12, 3)
	if v := m[32*w+31]; v < 0.9 {
		t.Fatalf("boundary should saturate, got %v", v)
	}
	if v := m[32*w+8]; v > 0.05 {
		t.Fatalf("interior should be ~0, got %v", v)
	}
	for i, v := range m {
		if v < 0 || v > 1 {
			t.Fatalf("value %v out of range at %d", v, i)
		}
	}
}

// A low-contrast boundary is not the kind of edge a rect or triangle earns its rim on, so it must
// score below a high-contrast one.
func TestBoundaryHardMap_WeightsByContrast(t *testing.T) {
	w, h := 96, 32
	img := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(0.30)
			switch {
			case x >= 2*w/3:
				v = 0.90 // strong step
			case x >= w/3:
				v = 0.36 // weak step
			}
			i := (y*w + x) * 4
			img[i], img[i+1], img[i+2], img[i+3] = v, v, v, 1
		}
	}
	seg := Segment(img, w, h, 0.5, 32)
	m := BoundaryHardMap(seg, w, h, 0.12, 3)
	peak := func(cx int) float32 {
		var best float32
		for x := cx - 3; x <= cx+3; x++ {
			if v := m[16*w+x]; v > best {
				best = v
			}
		}
		return best
	}
	weak, strong := peak(w/3), peak(2*w/3)
	if weak >= strong {
		t.Fatalf("weak boundary %v should score below strong %v", weak, strong)
	}
}

// Decay must be monotone with distance: the gate keeps an apron around structure, not a step.
func TestBoundaryHardMap_DecaysWithDistance(t *testing.T) {
	w, h := 64, 16
	img := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(0.1)
			if x >= w/2 {
				v = 0.9
			}
			i := (y*w + x) * 4
			img[i], img[i+1], img[i+2], img[i+3] = v, v, v, 1
		}
	}
	seg := Segment(img, w, h, 2, 16)
	m := BoundaryHardMap(seg, w, h, 0.12, 3)
	prev := float32(2)
	for d := 0; d < 10; d++ {
		v := m[8*w+(w/2-1-d)]
		if v > prev {
			t.Fatalf("score rose at distance %d: %v after %v", d, v, prev)
		}
		prev = v
	}
}
