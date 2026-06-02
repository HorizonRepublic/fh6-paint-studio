package metric

import "testing"

func TestWeightMapHighAtEdge(t *testing.T) {
	w, h := 8, 8
	target := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(0)
			if x >= w/2 {
				v = 1 // sharp vertical edge at x=4
			}
			p := (y*w + x) * 4
			target[p], target[p+1], target[p+2], target[p+3] = v, v, v, 1
		}
	}
	wm := WeightMap(target, w, h)
	if len(wm) != w*h {
		t.Fatalf("len=%d want %d", len(wm), w*h)
	}
	edge := wm[4*w+4] // at the boundary
	flat := wm[4*w+0] // far left, flat
	if edge <= flat {
		t.Fatalf("edge weight %v should exceed flat weight %v", edge, flat)
	}
	if flat < WeightBase-1e-6 {
		t.Fatalf("flat weight %v below base %v", flat, WeightBase)
	}
}

func TestWeightMapV2EdgeAndBounds(t *testing.T) {
	w, h := 8, 8
	target := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(1) // white background
			if x == 4 {
				v = 0 // a 1px BLACK vertical ink line at x=4
			}
			p := (y*w + x) * 4
			target[p], target[p+1], target[p+2], target[p+3] = v, v, v, 1
		}
	}
	wm := WeightMapV2(target, w, h)
	if len(wm) != w*h {
		t.Fatalf("len=%d want %d", len(wm), w*h)
	}
	// All weights within the clamp range [0.55, 5.25].
	for i, v := range wm {
		if v < 0.55-1e-4 || v > 5.25+1e-4 {
			t.Fatalf("weight[%d]=%v out of [0.55,5.25]", i, v)
		}
	}
	onLine := wm[3*w+4] // on the black ink line (high: edge + linework)
	flat := wm[3*w+0]   // far flat-white corner (baseline ~0.55)
	if onLine <= flat {
		t.Fatalf("ink-line weight %v should exceed flat weight %v", onLine, flat)
	}
	// 3x3 dilation must spread the line's high weight to its immediate neighbour.
	neighbour := wm[3*w+3] // one px left of the line
	if neighbour <= flat {
		t.Fatalf("dilated neighbour weight %v should exceed flat %v (dilation failed)", neighbour, flat)
	}
}

func TestWeightMapFlatImageAllBase(t *testing.T) {
	w, h := 5, 5
	target := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		target[i*4], target[i*4+1], target[i*4+2], target[i*4+3] = 0.4, 0.4, 0.4, 1
	}
	wm := WeightMap(target, w, h)
	for i, v := range wm {
		if v < WeightBase-1e-6 || v > WeightBase+1e-6 {
			t.Fatalf("flat image weight[%d]=%v want %v", i, v, WeightBase)
		}
	}
}
