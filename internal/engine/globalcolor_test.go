package engine

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

// gcMakeShapes builds three OVERLAPPING semi-transparent ellipses over a background rect. Overlap
// is the whole point: with a diagonal Gram (nothing overlapping) the per-layer greedy solve is
// already optimal and a joint solve would have nothing to add.
func gcMakeShapes(cols [][3]float64, alpha float32) []model.Shape {
	out := []model.Shape{
		model.Candidate{
			Kind:  model.KindRectangle,
			P:     [6]float32{20, 20, 20, 20, 0, 0},
			Color: model.RGBA{R: 0.5, G: 0.5, B: 0.5, A: 1},
		}.ToShape(0),
	}
	centres := [][2]float32{{16, 18}, {22, 20}, {19, 24}}
	for i, c := range cols {
		out = append(out, model.Candidate{
			Kind:  model.KindEllipse,
			P:     [6]float32{centres[i][0], centres[i][1], 11, 9, float32(20 * i), 0},
			Color: model.RGBA{R: float32(c[0]), G: float32(c[1]), B: float32(c[2]), A: alpha},
		}.ToShape(0))
	}
	return out
}

// TestGlobalColorRecoversAKnownStack is the correctness gate: render a stack with known colours,
// hand the solver the SAME geometry with the colours wiped to grey, and require it to find the
// colours back. Anything less means the affine model or the transmittance sweep is wrong.
func TestGlobalColorRecoversAKnownStack(t *testing.T) {
	// LINEAR LIGHT ON: this is the configuration the studio and CLI actually run in, and it is the
	// one where the colour byte encoding is NOT a plain scale. A test at the default hid a real
	// gamma bug here once, because F2B and DecChan happen to be inverses only when it is off.
	defer func(prev bool) { model.LinearLight = prev }(model.LinearLight)
	model.LinearLight = true

	const w, h = 40, 40
	bg := model.RGBA{R: 0.5, G: 0.5, B: 0.5, A: 1}
	truth := [][3]float64{{0.9, 0.15, 0.1}, {0.1, 0.8, 0.2}, {0.15, 0.2, 0.85}}
	want := gcMakeShapes(truth, 0.6)

	target := renderStackTo(want, bg, w, h)
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1
	}

	wiped := gcMakeShapes([][3]float64{{0.5, 0.5, 0.5}, {0.5, 0.5, 0.5}, {0.5, 0.5, 0.5}}, 0.6)
	got, before, after, ok := globalColorSolve(wiped, target, weight, w, h, bg, false, 400)
	if !ok {
		t.Fatal("solver refused a well-formed stack")
	}
	if after >= before {
		t.Fatalf("convex objective did not decrease: before=%.6g after=%.6g", before, after)
	}
	if after > before*0.02 {
		t.Errorf("residual too high: before=%.6g after=%.6g (want <2%% of before)", before, after)
	}
	for i := range truth {
		for ch := 0; ch < 3; ch++ {
			g := model.DecChan(got[i+1].Color[ch])
			if math.Abs(float64(g)-truth[i][ch]) > 0.05 {
				t.Errorf("layer %d channel %d = %.3f, want %.3f", i, ch, g, truth[i][ch])
			}
		}
	}
}

// TestGlobalColorNeverLosesToTheInput pins the property the caller's gate relies on: FISTA on a
// convex box-constrained quadratic must not end above where it started. A step size larger than
// 1/L is the way this breaks, which is why the Lipschitz estimate is measured rather than guessed.
func TestGlobalColorNeverLosesToTheInput(t *testing.T) {
	const w, h = 40, 40
	bg := model.RGBA{R: 0.2, G: 0.3, B: 0.4, A: 1}
	shapes := gcMakeShapes([][3]float64{{0.8, 0.2, 0.2}, {0.2, 0.7, 0.3}, {0.3, 0.3, 0.9}}, 0.45)

	// A target unrelated to the stack: the solve cannot reach it, so any step-size error shows up
	// as an increase instead of being masked by an easy descent.
	target := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := (y*w + x) * 4
			target[p+0] = float32(x) / w
			target[p+1] = float32(y) / h
			target[p+2] = 0.25
			target[p+3] = 1
		}
	}
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1 + float32(i%17) // non-uniform, like the perceptual weight
	}

	_, before, after, ok := globalColorSolve(shapes, target, weight, w, h, bg, false, 200)
	if !ok {
		t.Fatal("solver refused a well-formed stack")
	}
	if after > before {
		t.Errorf("solve increased the objective: before=%.6g after=%.6g", before, after)
	}
}

// renderStackTo composites a stack the same way the solver's forward pass does, so the test's
// ground truth comes from the shipped raster rather than from a second implementation of `over`.
func renderStackTo(shapes []model.Shape, bg model.RGBA, w, h int) []float32 {
	gl := gcBuild(shapes[1:], w, h)
	out := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		out[i*4+0], out[i*4+1], out[i*4+2], out[i*4+3] = bg.R, bg.G, bg.B, 1
	}
	c := make([]float64, len(gl)*3)
	for i, s := range shapes[1:] {
		c[i*3+0] = float64(model.DecChan(s.Color[0]))
		c[i*3+1] = float64(model.DecChan(s.Color[1]))
		c[i*3+2] = float64(model.DecChan(s.Color[2]))
	}
	gcComposite(gl, c, out, w)
	return out
}

// TestGlobalColorUncachedMatchesCached guards the memory cap: layers past the cache budget are
// evaluated on the fly instead of from a stored opacity map, and the two paths must agree exactly.
// A silent divergence here would be invisible in normal runs (the budget is generous) and would
// only appear on the large stacks of a weak machine, which is the case the cap exists for.
func TestGlobalColorUncachedMatchesCached(t *testing.T) {
	defer func(prev bool) { model.LinearLight = prev }(model.LinearLight)
	model.LinearLight = true

	const w, h = 40, 40
	bg := model.RGBA{R: 0.5, G: 0.5, B: 0.5, A: 1}
	truth := [][3]float64{{0.9, 0.15, 0.1}, {0.1, 0.8, 0.2}, {0.15, 0.2, 0.85}}
	target := renderStackTo(gcMakeShapes(truth, 0.6), bg, w, h)
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1
	}
	wiped := func() []model.Shape {
		return gcMakeShapes([][3]float64{{0.5, 0.5, 0.5}, {0.5, 0.5, 0.5}, {0.5, 0.5, 0.5}}, 0.6)
	}

	cached, _, wantErr, ok := globalColorSolve(wiped(), target, weight, w, h, bg, false, 200)
	if !ok {
		t.Fatal("solver refused a well-formed stack")
	}
	defer func(prev int) { gcCacheBudget = prev }(gcCacheBudget)
	gcCacheBudget = 0 // nothing fits: every layer takes the on-the-fly path
	live, _, gotErr, ok := globalColorSolve(wiped(), target, weight, w, h, bg, false, 200)
	if !ok {
		t.Fatal("solver refused the same stack with the cache disabled")
	}
	if math.Abs(gotErr-wantErr) > 1e-9 {
		t.Errorf("uncached objective %.9g != cached %.9g", gotErr, wantErr)
	}
	for i := range cached {
		for ch := 0; ch < 3; ch++ {
			if cached[i].Color[ch] != live[i].Color[ch] {
				t.Fatalf("shape %d channel %d: uncached %d != cached %d", i, ch, live[i].Color[ch], cached[i].Color[ch])
			}
		}
	}
}

// TestGlobalAlphaMatchesABruteForceScan gates the maths of the closed-form alpha directly, which a
// reconstruction test cannot: with the colours free the solve can absorb a wrong alpha by shifting
// colour, so "it recovered the image" says nothing about whether the alpha optimum is right. Here the
// colours are held fixed and the sweep's answer is compared against a fine scan of the same
// objective. Agreement means the affine-in-alpha split and its least-squares are correct.
func TestGlobalAlphaMatchesABruteForceScan(t *testing.T) {
	defer func(prev bool) { model.LinearLight = prev }(model.LinearLight)
	model.LinearLight = true

	const w, h = 36, 36
	bg := model.RGBA{R: 0.25, G: 0.4, B: 0.55, A: 1}
	cols := [][3]float64{{0.9, 0.15, 0.1}, {0.1, 0.8, 0.2}, {0.15, 0.2, 0.85}}
	target := renderStackTo(gcMakeShapes(cols, 0.7), bg, w, h)
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1 + float32(i%13)/13 // non-uniform, like the perceptual weight
	}
	base := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		base[i*4+0], base[i*4+1], base[i*4+2], base[i*4+3] = bg.R, bg.G, bg.B, 1
	}
	flat := make([]float64, len(cols)*3)
	for i, c := range cols {
		flat[i*3+0], flat[i*3+1], flat[i*3+2] = c[0], c[1], c[2]
	}

	// Objective as a function of ONE layer's alpha, everything else held at the starting stack.
	sse := func(layer int, a float32) float64 {
		gl := gcBuild(gcMakeShapes(cols, 0.4)[1:], w, h)
		l := &gl[layer]
		sc := a / float32(l.alpha)
		for j := range l.a {
			l.a[j] *= sc
		}
		l.alpha = float64(a)
		render := make([]float32, w*h*4)
		copy(render, base)
		gcComposite(gl, flat, render, w)
		var e float64
		for i := 0; i < w*h; i++ {
			for ch := 0; ch < 3; ch++ {
				d := float64(render[i*4+ch] - target[i*4+ch])
				e += float64(weight[i]) * d * d
			}
		}
		return e
	}

	gl := gcBuild(gcMakeShapes(cols, 0.4)[1:], w, h)
	vcache := make([][]float32, len(gl))
	for i := range gl {
		vcache[i] = make([]float32, (gl[i].x1-gl[i].x0+1)*(gl[i].y1-gl[i].y0+1)*3)
	}
	trans := make([]float32, w*h)
	gcAlphaSweep(gl, flat, base, target, weight, vcache, trans, w, h, 0.05)

	// The topmost layer is the one the sweep visits first, so its answer is computed against exactly
	// the stack the scan uses; lower layers see alphas the sweep has already moved.
	top := len(gl) - 1
	var bestA float32
	bestE := math.Inf(1)
	for a := 0.05; a <= 1.0001; a += 0.005 {
		if e := sse(top, float32(a)); e < bestE {
			bestE, bestA = e, float32(a)
		}
	}
	got := gl[top].alpha
	if math.Abs(got-float64(bestA)) > 0.01 {
		t.Errorf("closed-form alpha = %.4f, brute-force optimum = %.4f", got, bestA)
	}
}

// TestGlobalAlphaRespectsTheFloor pins that the sweep cannot re-open the hole the polish used to
// leave: a stack whose shapes would be cheapest at alpha 0 must still come back above the floor.
func TestGlobalAlphaRespectsTheFloor(t *testing.T) {
	defer func(prev bool) { model.LinearLight = prev }(model.LinearLight)
	model.LinearLight = true

	const w, h = 40, 40
	bg := model.RGBA{R: 0.2, G: 0.5, B: 0.7, A: 1}
	target := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		target[i*4+0], target[i*4+1], target[i*4+2], target[i*4+3] = bg.R, bg.G, bg.B, 1
	}
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1
	}
	const floor = 0.3
	got, _, _, ok := globalColorAlphaSolve(
		gcMakeShapes([][3]float64{{0.9, 0.1, 0.1}, {0.1, 0.9, 0.1}, {0.1, 0.1, 0.9}}, 0.6),
		target, weight, w, h, bg, false, 200, 4, floor)
	if !ok {
		t.Fatal("solver refused a well-formed stack")
	}
	for i := 1; i < len(got); i++ {
		if a := float64(got[i].Color[3]) / 255; a < floor-1.0/255 {
			t.Errorf("layer %d alpha = %.3f, below the floor %.2f", i-1, a, floor)
		}
	}
}

// The sweep runs top-down, and the layer below the first one it moves is where the composite it
// reconstructs stops matching the transmittance it uses. The existing scan only checks the TOP
// layer, which is exactly the one that cannot expose it — nothing above it has moved yet.
func TestGlobalAlphaSecondLayerMatchesABruteForceScan(t *testing.T) {
	defer func(prev bool) { model.LinearLight = prev }(model.LinearLight)
	model.LinearLight = true

	const w, h = 36, 36
	bg := model.RGBA{R: 0.25, G: 0.4, B: 0.55, A: 1}
	cols := [][3]float64{{0.9, 0.15, 0.1}, {0.1, 0.8, 0.2}, {0.15, 0.2, 0.85}}
	target := renderStackTo(gcMakeShapes(cols, 0.7), bg, w, h)
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1 + float32(i%13)/13
	}
	base := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		base[i*4+0], base[i*4+1], base[i*4+2], base[i*4+3] = bg.R, bg.G, bg.B, 1
	}
	flat := make([]float64, len(cols)*3)
	for i, c := range cols {
		flat[i*3+0], flat[i*3+1], flat[i*3+2] = c[0], c[1], c[2]
	}

	gl := gcBuild(gcMakeShapes(cols, 0.4)[1:], w, h)
	vcache := make([][]float32, len(gl))
	for i := range gl {
		vcache[i] = make([]float32, (gl[i].x1-gl[i].x0+1)*(gl[i].y1-gl[i].y0+1)*3)
	}
	trans := make([]float32, w*h)
	gcAlphaSweep(gl, flat, base, target, weight, vcache, trans, w, h, 0.05)

	top, second := len(gl)-1, len(gl)-2
	if second < 0 {
		t.Skip("need at least two layers")
	}
	topAlpha := gl[top].alpha

	// Scan the second layer against the stack the sweep actually saw: the top layer at the alpha the
	// sweep gave it, everything below still at the entry alpha.
	setAlpha := func(l *gcLayer, a float64) {
		sc := float32(a / l.alpha)
		for j := range l.a {
			l.a[j] *= sc
		}
		l.alpha = a
	}
	sse := func(a float64) float64 {
		g := gcBuild(gcMakeShapes(cols, 0.4)[1:], w, h)
		setAlpha(&g[top], topAlpha)
		setAlpha(&g[second], a)
		render := make([]float32, w*h*4)
		copy(render, base)
		gcComposite(g, flat, render, w)
		var e float64
		for i := 0; i < w*h; i++ {
			for ch := 0; ch < 3; ch++ {
				d := float64(render[i*4+ch] - target[i*4+ch])
				e += float64(weight[i]) * d * d
			}
		}
		return e
	}
	var bestA, bestE = 0.0, math.Inf(1)
	for a := 0.05; a <= 1.0001; a += 0.005 {
		if e := sse(a); e < bestE {
			bestE, bestA = e, a
		}
	}
	if got := gl[second].alpha; math.Abs(got-bestA) > 0.01 {
		t.Errorf("second layer: closed-form alpha = %.4f, brute-force optimum = %.4f", got, bestA)
	}
}
