package vulkan

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/model"
)

// Device-side finite differences on the polish gradient. The analytic gradient comes from
// PolishReadGrad; the numeric one comes from re-running PolishForward + PolishLoss on the device
// with one parameter nudged. Both sides are the shader, so the shader is checked against itself —
// there is no host SDF anywhere in this file.

// polishScene is the parameter state the FD walk perturbs, in the wire layout PolishUpload wants.
type polishScene struct {
	g     *Vulkan
	w, h  int
	n     int
	P     []float64
	col   []float64
	kinds []int32
	bbx   []int32
	off   []int64
	total int64
	tau   float64
}

func (s *polishScene) upload() { s.g.PolishUpload(s.P, s.col, s.kinds, s.bbx, s.off, s.total) }

func (s *polishScene) loss() float64 {
	s.upload()
	s.g.PolishForward(s.tau, s.bbx)
	return s.g.PolishLoss()
}

func TestPolishGradientFD(t *testing.T) {
	const w, h = 20, 18
	rng := rand.New(rand.NewSource(1))
	target, weight := makeTarget(rng, w, h, false)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Skipf("vulkan unavailable: %v", err)
	}
	defer gpu.Close()

	base := flatCanvas(w, h, 0.25)

	type shape struct {
		name string
		kind model.ShapeKind
		P    [6]float64
		col  [4]float64
		geo  []int // geometry slots that carry a real degree of freedom for this kind
	}
	shapes := []shape{
		{"ellipse", model.KindEllipse, [6]float64{8.3, 7.4, 5, 3, 20, 0}, [4]float64{0.7, 0.2, 0.5, 0.8}, []int{0, 1, 2, 3, 4}},
		// Non-zero skew in slot 5: the rectangle's parallelogram DOF shipped with no test of its
		// own, and at skew 0 the term vanishes from the chain, so the check has to shear the box.
		{"rect+skew", model.KindRectangle, [6]float64{10.4, 8.6, 4, 5, 35, 0.35}, [4]float64{0.5, 0.5, 0.3, 0.7}, []int{0, 1, 2, 3, 4, 5}},
		// Fractional vertices on purpose: a triangle's bbox edge IS a vertex coord, so an integer
		// coord would let the FD ±eps straddle the floor/ceil boundary and read a render
		// discontinuity as gradient.
		{"triangle", model.KindTriangle, [6]float64{5.3, 4.4, 15.6, 6.2, 9.1, 14.7}, [4]float64{0.4, 0.35, 0.6, 0.7}, []int{0, 1, 2, 3, 4, 5}},
		// A large glow so its smooth interior dominates the difference.
		{"glow", model.KindGlow, [6]float64{9.4, 8.6, 10, 9, 15, 0}, [4]float64{0.6, 0.3, 0.45, 0.85}, []int{0, 1, 2, 3, 4}},
	}
	n := len(shapes)

	sc := &polishScene{g: gpu, w: w, h: h, n: n, tau: 1.2,
		P: make([]float64, n*6), col: make([]float64, n*4),
		kinds: make([]int32, n), bbx: make([]int32, n*4), off: make([]int64, n)}
	for i, s := range shapes {
		copy(sc.P[i*6:], s.P[:])
		copy(sc.col[i*4:], s.col[:])
		sc.kinds[i] = int32(s.kind)
		// Full-frame bbox for every shape. The engine ships a tau-expanded per-shape box, but that
		// box MOVES with the parameters, and a bbox that changes under the ±eps nudge is a render
		// discontinuity the FD would read as gradient. A fixed box removes the trap entirely and
		// costs nothing on a 20x18 scene.
		sc.bbx[i*4+0], sc.bbx[i*4+1], sc.bbx[i*4+2], sc.bbx[i*4+3] = 0, 0, w-1, h-1
		sc.off[i] = int64(i) * int64(w*h*4)
	}
	sc.total = int64(n) * int64(w*h*4)

	gpu.PolishSetup(base, n)
	defer gpu.PolishFree()
	// The optional loss terms are DLL globals that outlive a polish, so another test in this
	// process could leave one armed. Clear them: this test checks the plain SSE gradient.
	gpu.PolishSetSTE(false) // straight-through is a deliberate surrogate gradient, not FD-checkable
	gpu.PolishSetOKLab(false)
	gpu.PolishSetFalseEdge(0)
	gpu.PolishSetSSIM(0)
	gpu.PolishSetEagle(0)
	gpu.PolishSetLostDetail(0)
	gpu.PolishSetTermWeight(nil)

	sc.upload()
	gpu.PolishForward(sc.tau, sc.bbx)
	gpu.PolishBackward(sc.tau, sc.bbx)
	ana := make([]float64, n*10)
	gpu.PolishReadGrad(ana)

	// check compares the analytic gradient of one parameter with a central difference of the device
	// loss. p addresses the parameter in the scene; f32 marks the geometry slots, which the device
	// stores as float32 — the perturbed values are rounded through float32 here so the denominator
	// is the step the shader actually saw, not the one asked for.
	live := 0 // slots whose gradient is big enough that the comparison could have failed
	check := func(name string, p *float64, analytic, eps float64, f32 bool) {
		t.Helper()
		if math.Abs(analytic) > 0.05 {
			live++
		}
		save := *p
		up, dn := save+eps, save-eps
		if f32 {
			up, dn = float64(float32(up)), float64(float32(dn))
		}
		*p = up
		lp := sc.loss()
		*p = dn
		lm := sc.loss()
		*p = save
		num := (lp - lm) / (up - dn)
		den := math.Max(1, math.Max(math.Abs(num), math.Abs(analytic)))
		// The forward render buffer is float32, which puts a ~1e-2 relative noise floor under the
		// central difference; the analytic gradient is the accurate one. A real derivation error is
		// large and systematic, so it still shows.
		if math.Abs(num-analytic)/den > 1.5e-2 {
			t.Errorf("%s: analytic=%.6f numeric=%.6f (rel %.4f)", name, analytic, num, math.Abs(num-analytic)/den)
		}
	}

	for i, s := range shapes {
		for _, k := range s.geo {
			check(fmt.Sprintf("%s P%d", s.name, k), &sc.P[i*6+k], ana[i*10+k], 5e-3, true)
		}
		for c := 0; c < 4; c++ {
			check(fmt.Sprintf("%s col%d", s.name, c), &sc.col[i*4+c], ana[i*10+6+c], 1e-3, false)
		}
	}

	// The tolerance is relative to max(1, …), so a scene where every gradient is ~0 would pass
	// without comparing anything. Most slots must carry real signal.
	if want := len(shapes) * 5; live < want {
		t.Errorf("only %d gradient slots exceeded 0.05 — the scene is too flat to test (want ≥ %d)", live, want)
	}
	// Same trap one slot down: a zeroed skew gradient would agree with a forward that stopped
	// responding to skew, so pin that slot 5 is actually driven.
	if skew := ana[1*10+5]; math.Abs(skew) < 1e-3 {
		t.Errorf("rectangle skew gradient is %.3g — slot 5 is not being driven", skew)
	}
}
