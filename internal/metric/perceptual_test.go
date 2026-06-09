package metric

import (
	"math"
	"testing"
)

// fill makes a w*h sRGB RGBA buffer of a constant colour.
func fillBuf(w, h int, r, g, b float32) []float32 {
	buf := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		buf[i*4], buf[i*4+1], buf[i*4+2], buf[i*4+3] = r, g, b, 1
	}
	return buf
}

func TestDeltaE76Identical(t *testing.T) {
	a := fillBuf(16, 16, 0.4, 0.6, 0.2)
	mean, p95 := DeltaE76(a, a, 16, 16)
	if mean > 1e-9 || p95 > 1e-9 {
		t.Errorf("ΔE of identical buffers = %.4f/%.4f, want 0", mean, p95)
	}
}

func TestDeltaE76BlackWhite(t *testing.T) {
	a := fillBuf(8, 8, 0, 0, 0)
	b := fillBuf(8, 8, 1, 1, 1)
	mean, _ := DeltaE76(a, b, 8, 8)
	// black↔white is L*=0 vs 100 → ΔE = 100.
	if math.Abs(mean-100) > 0.5 {
		t.Errorf("ΔE black↔white = %.2f, want ≈100", mean)
	}
}

func TestSSIMIdentical(t *testing.T) {
	a := fillBuf(32, 32, 0.5, 0.5, 0.5)
	if s := SSIM(a, a, 32, 32); math.Abs(s-1) > 1e-6 {
		t.Errorf("SSIM of identical buffers = %.4f, want 1", s)
	}
}

func TestFalseEdgesFlatRenderIsZero(t *testing.T) {
	// Smooth source + flat render → no spurious edges.
	src := fillBuf(32, 32, 0.5, 0.5, 0.5)
	render := fillBuf(32, 32, 0.4, 0.4, 0.4)
	if fe := FalseEdges(src, render, 32, 32, 0.02); fe > 1e-6 {
		t.Errorf("FalseEdges of two flat buffers = %.4f, want 0", fe)
	}
}

func TestFalseEdgesCatchesBanding(t *testing.T) {
	// Smooth (flat) source, but a render with a hard step in the middle = banding the metric must catch.
	w, h := 32, 32
	src := fillBuf(w, h, 0.5, 0.5, 0.5)
	render := fillBuf(w, h, 0.5, 0.5, 0.5)
	for y := 0; y < h; y++ {
		for x := w / 2; x < w; x++ {
			render[(y*w+x)*4] = 0.9
			render[(y*w+x)*4+1] = 0.9
			render[(y*w+x)*4+2] = 0.9
		}
	}
	if fe := FalseEdges(src, render, w, h, 0.02); fe <= 0 {
		t.Errorf("FalseEdges failed to catch a hard step: %.4f", fe)
	}
}
