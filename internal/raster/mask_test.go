package raster

import (
	"math"
	"testing"
)

func approxMask(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// TestMaskSampleUVBilinear checks the bilinear UV sampler on a 2x2 checker
// (TL=0 TR=1 / BL=1 BR=0, row-major with v=0 at the top row). Texel centres sit
// at u,v=(i+0.5)/n; the box centre is the mean of the four; UV outside [0,1] -> 0.
func TestMaskSampleUVBilinear(t *testing.T) {
	m := &maskTex{w: 2, h: 2, cov: []float32{0, 1, 1, 0}}
	cases := []struct{ u, v, want float64 }{
		{0.25, 0.25, 0}, // TL texel centre
		{0.75, 0.25, 1}, // TR
		{0.25, 0.75, 1}, // BL
		{0.75, 0.75, 0}, // BR
		{0.5, 0.5, 0.5}, // centre = mean of the four
		{-0.1, 0.5, 0},  // outside UV
		{1.1, 0.5, 0},   // outside UV
	}
	for i, c := range cases {
		if got := m.sampleUV(c.u, c.v); !approxMask(got, c.want, 1e-6) {
			t.Errorf("case %d sampleUV(%.2f,%.2f)=%.4f want %.4f", i, c.u, c.v, got, c.want)
		}
	}
}

// leftHalfMask is 1 on the UV left half (u<0.5), 0 on the right — a directional
// probe so the inverse affine's sign/orientation is unambiguous.
func leftHalfMask() *maskTex {
	const n = 256
	cov := make([]float32, n*n)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if (float64(x)+0.5)/n < 0.5 {
				cov[y*n+x] = 1
			}
		}
	}
	return &maskTex{w: n, h: n, cov: cov}
}

// TestMaskCoverageAffineRot0: P=[cx,cy,Hx,Hy,rot,skew], Hx,Hy = full screen extents
// so n=(d/H) in [-0.5,0.5]. pos at .5 makes pixel-centre offsets integer.
func TestMaskCoverageAffineRot0(t *testing.T) {
	m := leftHalfMask()
	p := [6]float32{100.5, 100.5, 200, 200, 0, 0}
	if got := maskCoverage(m, p, 60, 100); !approxMask(got, 1, 1e-6) { // d=(-40,0) -> u=0.3 (left)
		t.Errorf("left pixel cov=%.3f want 1", got)
	}
	if got := maskCoverage(m, p, 150, 100); !approxMask(got, 0, 1e-6) { // d=(50,0) -> u=0.75 (right)
		t.Errorf("right pixel cov=%.3f want 0", got)
	}
}

// TestMaskCoverageAffineRot90: rot maps the local left half to up-screen.
func TestMaskCoverageAffineRot90(t *testing.T) {
	m := leftHalfMask()
	p := [6]float32{100.5, 100.5, 200, 200, 90, 0}
	if got := maskCoverage(m, p, 100, 60); !approxMask(got, 1, 1e-6) { // above centre
		t.Errorf("above-centre cov=%.3f want 1 (rot90 maps left->up)", got)
	}
	if got := maskCoverage(m, p, 100, 150); !approxMask(got, 0, 1e-6) { // below centre
		t.Errorf("below-centre cov=%.3f want 0", got)
	}
}

// TestMaskCoverageSkew: horizontal shear K^{-1}.x = kx - skew*ky shifts a centre-column
// pixel (which would be the u=0.5 boundary at skew=0) onto the left half.
func TestMaskCoverageSkew(t *testing.T) {
	m := leftHalfMask()
	p := [6]float32{100.5, 100.5, 200, 200, 0, 1}
	if got := maskCoverage(m, p, 100, 140); !approxMask(got, 1, 1e-6) { // d=(0,40) -> sx=-40 -> u=0.3
		t.Errorf("skewed pixel cov=%.3f want 1", got)
	}
}
