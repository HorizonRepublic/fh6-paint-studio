package metric

import "testing"

func rgba(lum []float32, w, h int) []float32 {
	c := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		c[i*4], c[i*4+1], c[i*4+2], c[i*4+3] = lum[i], lum[i], lum[i], 1
	}
	return c
}

// Smooth shading must gate hard kinds out (~0); a drawn line must keep them (high near the line).
func TestHardEdgeMap_RampVsLine(t *testing.T) {
	w, h := 96, 96
	ramp := make([]float32, w*h)
	line := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ramp[y*w+x] = float32(x) / float32(w-1)
			line[y*w+x] = 1
			if x >= 46 && x <= 49 {
				line[y*w+x] = 0 // vertical 4px black bar on white
			}
		}
	}
	hr := HardEdgeMap(rgba(ramp, w, h), w, h)
	hl := HardEdgeMap(rgba(line, w, h), w, h)

	if v := hr[48*w+48]; v > 0.05 {
		t.Fatalf("smooth ramp centre should be ~0, got %v", v)
	}
	if v := hl[48*w+48]; v < 0.5 {
		t.Fatalf("line-work centre should be structured, got %v", v)
	}
	if v := hl[48*w+8]; v > 0.15 {
		t.Fatalf("far from the line should stay low, got %v", v)
	}
	for i, v := range hl {
		if v < 0 || v > 1 {
			t.Fatalf("out of range at %d: %v", i, v)
		}
	}
}
