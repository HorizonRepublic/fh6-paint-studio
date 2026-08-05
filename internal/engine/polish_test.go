package engine

import (
	"math"
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/model"
)

// TestPolishGradientFD verifies the hand-derived analytic gradients in
// polishBackward against central finite differences of the full soft-render
// weighted-SSE loss. This is the correctness gate for the differentiable polish.
func TestPolishGradientFD(t *testing.T) {
	w, h := 18, 16
	rng := rand.New(rand.NewSource(1))
	target := make([]float32, w*h*4)
	weight := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		target[i*4+0] = rng.Float32()
		target[i*4+1] = rng.Float32()
		target[i*4+2] = rng.Float32()
		target[i*4+3] = rng.Float32()
		weight[i] = 0.3 + 0.7*rng.Float32()
	}
	base := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		base[i*4+0], base[i*4+1], base[i*4+2], base[i*4+3] = 0.2, 0.3, 0.25, 1
	}
	ps := []pshape{
		{kind: model.KindEllipse, P: [6]float64{8, 7, 5, 3, 20}, col: [4]float64{0.7, 0.2, 0.5, 0.8}, optGeo: true},
		{kind: model.KindRectangle, P: [6]float64{10, 8, 4, 5, 35}, col: [4]float64{0.5, 0.5, 0.3, 0.7}, optGeo: true},
		{kind: model.KindEllipse, P: [6]float64{11, 9, 4, 6, 70}, col: [4]float64{0.2, 0.6, 0.4, 0.6}, optGeo: true},
		// Fractional vertices on purpose: the triangle bbox edge IS a vertex coord, so an
		// integer coord would make the FD ±eps straddle the floor/ceil bbox boundary (a render
		// discontinuity -> spurious numeric gradient). Off-integer keeps the bbox stable.
		{kind: model.KindTriangle, P: [6]float64{5.3, 4.4, 13.6, 6.2, 9.1, 12.7}, col: [4]float64{0.4, 0.35, 0.6, 0.7}, optGeo: true},
		// Trainable glow (gaussian splat): a large footprint so the interior pixels (smooth coverage)
		// dominate the FD; geometry params cx,cy,rx,ry,θ (slot 5 = 0) are checked like any optGeo shape.
		{kind: model.KindGlow, P: [6]float64{9, 8, 10, 9, 15}, col: [4]float64{0.6, 0.3, 0.45, 0.85}, optGeo: true},
	}
	render := make([]float32, w*h*4)
	dC := make([]float64, w*h*4)
	below := make([][]float32, len(ps))
	bbx := make([][4]int, len(ps))
	tau := 1.2

	cases := []struct {
		name       string
		oklab      bool
		feLambda   float64
		ssimLambda float64
		ldLambda   float64
	}{{"sse", false, 0, 0, 0}, {"oklab", true, 0, 0, 0}, {"false-edge", false, 0.5, 0, 0},
		{"ssim", false, 0, 0.5, 0}, {"lost-detail", false, 0, 0, 0.5},
		{"false-edge+lost-detail", false, 0.5, 0, 0.5}}
	for _, tc := range cases {
		tc := tc
		// Analytic gradients at the unperturbed params (soft mode — the FD check validates the
		// true soft-render gradient; STE's gradient is a deliberate surrogate, not FD-checkable).
		// The oklab pass additionally validates the OKLab Jacobian chain (okLabPixelDC); the
		// false-edge pass validates the Sobel-adjoint chain (feState.adjoint + the dC luma seed);
		// the ssim pass validates the window-moment chain (ssimState.adjoint, same dC seed).
		oklab := tc.oklab
		var fe *feState
		var feAdj []float64
		if tc.feLambda > 0 {
			fe = newFEState(target, w, h, nil)
			feAdj = fe.adj
		}
		var ssim *ssimState
		var ssimAdj []float64
		if tc.ssimLambda > 0 {
			ssim = newSSIMState(target, w, h)
			ssimAdj = ssim.adj
		}
		// lost-detail: the MIRROR of false-edge. The combined case matters on its own — the two
		// terms scatter opposite signs through the same Sobel stencil, so a sign slip in either
		// cancels in isolation and only shows up when both are live.
		var ld *ldState
		var ldAdj []float64
		if tc.ldLambda > 0 {
			ld = newLDState(target, w, h, nil)
			ldAdj = ld.adj
		}
		polishForward(ps, base, render, below, bbx, w, h, tau, false)
		if fe != nil {
			fe.adjoint(render, w, h)
		}
		if ssim != nil {
			ssim.adjoint(render, w, h)
		}
		if ld != nil {
			ld.adjoint(render, w, h)
		}
		polishBackward(ps, base, render, target, weight, below, bbx, dC, w, h, tau, false, oklab, feAdj, tc.feLambda, ssimAdj, tc.ssimLambda, nil, 0, ldAdj, tc.ldLambda)
		ana := make([][10]float64, len(ps))
		for i := range ps {
			ana[i] = ps[i].grad
		}

		lossAt := func() float64 {
			polishForward(ps, base, render, below, bbx, w, h, tau, false)
			l := polishLoss(render, target, weight, w, h, oklab)
			if fe != nil {
				l += tc.feLambda * fe.total(render, w, h)
			}
			if ssim != nil {
				l += tc.ssimLambda * ssim.total(render, w, h)
			}
			if ld != nil {
				l += tc.ldLambda * ld.total(render, w, h)
			}
			return l
		}
		eps := 1e-4
		check := func(name string, get func() *float64, analytic float64) {
			p := get()
			save := *p
			*p = save + eps
			lp := lossAt()
			*p = save - eps
			lm := lossAt()
			*p = save
			num := (lp - lm) / (2 * eps)
			den := math.Max(1, math.Max(math.Abs(num), math.Abs(analytic)))
			// Tolerance ~1.5e-2: the forward render buffer is float32, so the central
			// difference has a ~1e-2 relative noise floor; the analytic gradient (float64)
			// is the accurate one. This still catches any real derivation error (those
			// show up as large, systematic mismatches).
			if math.Abs(num-analytic)/den > 1.5e-2 {
				t.Errorf("%s: analytic=%.6f numeric=%.6f (rel %.4f)", name, analytic, num, math.Abs(num-analytic)/den)
			}
		}

		for si := range ps {
			for k := 0; k < 6; k++ { // 6 geo slots (ellipse/rect leave slot 5 = 0; triangle uses all 6)
				k := k
				si := si
				check(
					labelf("shape", si, "P", k),
					func() *float64 { return &ps[si].P[k] },
					ana[si][k],
				)
			}
			for c := 0; c < 4; c++ {
				c := c
				si := si
				check(
					labelf("shape", si, "col", c),
					func() *float64 { return &ps[si].col[c] },
					ana[si][6+c],
				)
			}
		}
	}
}

func labelf(a string, i int, b string, j int) string {
	return a + itoa(i) + "." + b + itoa(j)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

// TestPolishHonoursAlphaFloor pins the fix for the floor the descent used to ignore: the greedy
// places candidates above the preset's organic alpha floor, and the polish optimised alpha freely
// down to a hard-coded 0.05. The target here is the background itself, so every shape is pure cost
// and the descent's cheapest move is to fade them all out - exactly the case the floor must survive.
func TestPolishHonoursAlphaFloor(t *testing.T) {
	w, h := 24, 20
	bg := model.RGBA{R: 0.2, G: 0.5, B: 0.7, A: 1}
	target := make([]float32, w*h*4)
	weight := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		target[i*4+0], target[i*4+1], target[i*4+2], target[i*4+3] = bg.R, bg.G, bg.B, 1
		weight[i] = 1
	}
	var shapes []model.Shape
	for i := 0; i < 4; i++ {
		c := model.Candidate{
			Kind:  model.KindEllipse,
			P:     [6]float32{float32(4 + 5*i), 10, 4, 3, 0, 0},
			Color: model.RGBA{R: 0.9, G: 0.1, B: 0.1, A: 0.6},
		}
		shapes = append(shapes, c.ToShape(0))
	}
	const floor = 0.3
	pr := Polish(shapes, target, weight, w, h, bg, false, PolishOptions{
		Iters: 150, Tau0: 2.0, Tau1: 0.15,
		LRPos: 0.5, LRRad: 0.5, LRAng: 0.5, LRColor: 0.01, LRAlpha: 0.05,
		GradClip: 8, AlphaMin: floor,
	})
	for i, s := range pr.Shapes {
		if a := float64(s.Color[3]) / 255; a < floor-1.0/255 {
			t.Errorf("shape %d alpha = %.4f, below the floor %.2f the polish was given", i, a, floor)
		}
	}
}

// TestPolishAlphaFloorUnsetKeepsHistoricalBound guards the compatibility half of the same fix: a
// caller that threads no floor must still get the 0.05 bound, so every path that has not been
// measured with a floor stays bit-identical.
func TestPolishAlphaFloorUnsetKeepsHistoricalBound(t *testing.T) {
	if got := (PolishOptions{}).alphaFloor(); got != 0.05 {
		t.Errorf("unset alphaFloor() = %v, want the historical 0.05", got)
	}
	if got := (PolishOptions{AlphaMin: 0.3}).alphaFloor(); got != 0.3 {
		t.Errorf("alphaFloor() = %v, want the threaded 0.3", got)
	}
}
