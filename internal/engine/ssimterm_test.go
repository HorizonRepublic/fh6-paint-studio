package engine

import (
	"math"
	"math/rand"
	"testing"
)

// SSIM is maximal (S=1, term=0) exactly at render==target; any luma perturbation must raise the
// term and the adjoint must vanish at the maximum. This pins the windowed-moment plumbing
// (means, variances, covariance) independently of the FD gradient test.
func TestSSIMTermIdentity(t *testing.T) {
	w, h := 20, 14
	rng := rand.New(rand.NewSource(7))
	img := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		img[i*4+0] = rng.Float32()
		img[i*4+1] = rng.Float32()
		img[i*4+2] = rng.Float32()
		img[i*4+3] = 1
	}
	ss := newSSIMState(img, w, h)
	if ss == nil {
		t.Fatal("state nil for a canvas larger than a window")
	}
	if f0 := ss.total(img, w, h); math.Abs(f0) > 1e-9 {
		t.Errorf("identity total = %g, want 0", f0)
	}
	if f0 := ss.adjoint(img, w, h); math.Abs(f0) > 1e-9 {
		t.Errorf("identity adjoint energy = %g, want 0", f0)
	}
	for i, a := range ss.adj {
		if math.Abs(a) > 1e-9 {
			t.Fatalf("identity adj[%d] = %g, want ~0 (gradient must vanish at the SSIM maximum)", i, a)
		}
	}

	// A contrast crush (luma pulled toward the mean) leaves means intact but kills variance —
	// the structural error SSE undercharges and this term exists to catch.
	crushed := make([]float32, len(img))
	copy(crushed, img)
	for i := 0; i < w*h; i++ {
		for c := 0; c < 3; c++ {
			crushed[i*4+c] = 0.5*crushed[i*4+c] + 0.25
		}
	}
	if f0 := ss.total(crushed, w, h); f0 <= 0 {
		t.Errorf("contrast-crushed total = %g, want > 0", f0)
	}

	// total and adjoint must agree on the energy (same forward, two code paths).
	ft, fa := ss.total(crushed, w, h), ss.adjoint(crushed, w, h)
	if math.Abs(ft-fa) > 1e-9*math.Max(1, math.Abs(ft)) {
		t.Errorf("total=%g adjoint=%g diverge", ft, fa)
	}
}

func TestSSIMTermTinyCanvas(t *testing.T) {
	img := make([]float32, 4*4*4)
	if ss := newSSIMState(img, 4, 4); ss != nil {
		t.Error("canvas smaller than a window must yield a nil state (term degrades to 0)")
	}
}
