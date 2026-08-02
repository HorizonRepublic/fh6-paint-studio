package engine

import (
	"math"
	"math/rand"
	"testing"
)

func eagleRandImg(rng *rand.Rand, w, h int) []float32 {
	img := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		img[i*4+0] = rng.Float32()
		img[i*4+1] = rng.Float32()
		img[i*4+2] = rng.Float32()
		img[i*4+3] = 1
	}
	return img
}

// At render==target the term is exactly 0 and the (sub)gradient vanishes; a hard synthetic rim on a
// smooth target must raise it; total and adjoint must agree on the energy.
func TestEagleTermIdentityAndRim(t *testing.T) {
	w, h := 24, 18
	rng := rand.New(rand.NewSource(3))
	img := eagleRandImg(rng, w, h)
	e := newEagleState(img, w, h, nil)
	if e == nil {
		t.Fatal("state nil for a valid canvas")
	}
	if f := e.total(img, w, h); math.Abs(f) > 1e-12 {
		t.Errorf("identity total = %g, want 0", f)
	}
	if f := e.adjoint(img, w, h); math.Abs(f) > 1e-12 {
		t.Errorf("identity adjoint energy = %g, want 0", f)
	}
	for i, a := range e.adj {
		if math.Abs(a) > 1e-12 {
			t.Fatalf("identity adj[%d] = %g, want 0", i, a)
		}
	}

	// Smooth ramp target; render adds a hard-edged square — a gradient-structure signature the
	// target lacks anywhere.
	smooth := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(x+y) / float32(w+h)
			p := (y*w + x) * 4
			smooth[p], smooth[p+1], smooth[p+2], smooth[p+3] = v, v, v, 1
		}
	}
	es := newEagleState(smooth, w, h, nil)
	rim := make([]float32, len(smooth))
	copy(rim, smooth)
	for y := 6; y < 12; y++ {
		for x := 8; x < 16; x++ {
			p := (y*w + x) * 4
			rim[p], rim[p+1], rim[p+2] = 0.9, 0.9, 0.9
		}
	}
	ft := es.total(rim, w, h)
	if ft <= 0 {
		t.Fatalf("hard rim on smooth target must have positive energy, got %g", ft)
	}
	if fa := es.adjoint(rim, w, h); math.Abs(ft-fa) > 1e-9*math.Max(1, ft) {
		t.Fatalf("total=%g adjoint=%g diverge", ft, fa)
	}
}

// Finite-difference check of dEagle/dchannel through the luma chain, incl. border pixels (the
// zero-padded ops make the adjoint exact everywhere).
func TestEagleTermFD(t *testing.T) {
	w, h := 20, 16
	rng := rand.New(rand.NewSource(11))
	target := eagleRandImg(rng, w, h)
	render := eagleRandImg(rng, w, h)
	e := newEagleState(target, w, h, nil)
	e.adjoint(render, w, h)
	adj := make([]float64, len(e.adj))
	copy(adj, e.adj)

	pts := [][2]int{{5, 5}, {12, 9}, {0, 0}, {w - 1, h - 1}, {10, 0}, {0, 7}, {3, 14}}
	eps := float32(1e-4)
	checked := 0
	for _, pt := range pts {
		idx := pt[1]*w + pt[0]
		for c := 0; c < 3; c++ {
			lw := [3]float64{feLumaR, feLumaG, feLumaB}[c]
			want := adj[idx] * lw
			orig := render[idx*4+c]
			render[idx*4+c] = orig + eps
			fp := e.total(render, w, h)
			render[idx*4+c] = orig - eps
			fm := e.total(render, w, h)
			render[idx*4+c] = orig
			got := (fp - fm) / (2 * float64(eps))
			// |·| kinks: skip points where the sign pattern is unstable (tiny derivative).
			if math.Abs(want) < 1e-4 && math.Abs(got) < 1e-4 {
				continue
			}
			if diff := math.Abs(got - want); diff > 5e-2*math.Max(1, math.Abs(want)) {
				t.Errorf("FD mismatch at (%d,%d) ch%d: analytic %g vs FD %g", pt[0], pt[1], c, want, got)
			}
			checked++
		}
	}
	if checked < 6 {
		t.Fatalf("too few FD points survived the kink filter: %d", checked)
	}
}
