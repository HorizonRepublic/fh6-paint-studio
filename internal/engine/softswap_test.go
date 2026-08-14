package engine

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

// A uniform rect's covariance is (w²/12, h²/12), so the moment-matched ellipse must come back with
// semi-axes ≈ halfW·2/√3 and the rect's own rotation.
func TestMomentEllipseOfShape_Rect(t *testing.T) {
	w, h := 64, 64
	halfW, halfH := 12.0, 6.0
	p := [6]float32{32, 32, float32(halfW), float32(halfH), 30, 0}
	cx, cy, a, b, deg, ok := momentEllipseOfShape(model.KindRectangle, p, w, h)
	if !ok {
		t.Fatal("fit failed")
	}
	if math.Abs(cx-32) > 0.6 || math.Abs(cy-32) > 0.6 {
		t.Fatalf("centroid (%.2f,%.2f) want (32,32)", cx, cy)
	}
	wantA, wantB := halfW*2/math.Sqrt(3), halfH*2/math.Sqrt(3)
	if math.Abs(a-wantA) > 0.8 || math.Abs(b-wantB) > 0.8 {
		t.Fatalf("axes (%.2f,%.2f) want (%.2f,%.2f)", a, b, wantA, wantB)
	}
	if d := math.Abs(deg - 30); d > 3 && math.Abs(d-180) > 3 {
		t.Fatalf("rotation %.1f° want ~30°", deg)
	}
}

// A standout rectangle over a smooth ramp (its rim draws edges the target lacks) must get swapped
// for a soft-edged kind within the gate; the gate itself must hold on the returned error.
func TestSoftSwapReplacesStandoutRect(t *testing.T) {
	w, h := 48, 48
	target := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(x) / float32(w-1) // smooth horizontal ramp — no hard edges anywhere
			p := (y*w + x) * 4
			target[p+0], target[p+1], target[p+2], target[p+3] = v, v, v, 1
		}
	}
	be := newTestBackend(t, target, w, h, 8)
	bg := bgFromTarget(target, w, h)
	bgShape := model.Shape{Type: model.TypeRectangle, Data: []float64{float64(w) / 2, float64(h) / 2, float64(w) / 2, float64(h) / 2, 0},
		Color: []int{model.EncByte(bg.R), model.EncByte(bg.G), model.EncByte(bg.B), 255}}
	// A dark opaque rect in the middle of the ramp: its rim is a hard step the smooth target
	// does not have anywhere.
	rect := model.Shape{Type: model.TypeRotatedRectangle, Data: []float64{24, 24, 8, 5, 20},
		Color: []int{60, 60, 60, 255}}
	shapes := []model.Shape{bgShape, bgShape, rect}

	initCanvas := backgroundCanvas(bg, w, h)
	finalErr := renderExcept(be, initCanvas, shapes, -1)

	opt := Options{SoftSwapTol: 0.05}
	swapped, err := softSwapStandouts(be, shapes, finalErr, initCanvas, opt, w, h)
	if err > finalErr*(1+opt.SoftSwapTol)+1e-6 {
		t.Fatalf("gate violated: %.4f > %.4f", err, finalErr*(1+opt.SoftSwapTol))
	}
	k := model.KindFromType(swapped[2].Type)
	if k == model.KindRectangle {
		t.Fatalf("standout rect was not swapped (kind still rectangle)")
	}
	if k != model.KindEllipse && k != model.KindGlow && k != model.KindDisk {
		t.Fatalf("unexpected replacement kind %v", k)
	}
	if len(swapped) != len(shapes) {
		t.Fatalf("swap must preserve shape count: %d != %d", len(swapped), len(shapes))
	}
}

// The pre-polish variant is gated end-to-end: same seed, so a run with it can never finish more
// than tol worse than the polish-only baseline (and on refusal ships the identical baseline).
func TestSoftSwapPrePolishGate(t *testing.T) {
	w, h := 48, 48
	target := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(x+y) / float32(w+h-2)
			p := (y*w + x) * 4
			target[p+0], target[p+1], target[p+2], target[p+3] = v, v*0.8, 1-v, 1
		}
	}
	opts := func(pre bool) Options {
		return Options{
			Width: w, Height: h, Background: bgFromTarget(target, w, h),
			StopAt: 24, RandomSamples: 200, MutatedSamples: 100, Seed: 1,
			Kinds:  []model.ShapeKind{model.KindEllipse, model.KindRectangle},
			Polish: true, SoftSwapTol: 0.05, SoftSwapPre: pre,
		}
	}
	base := Run(newTestBackend(t, target, w, h, 8), opts(false))
	pre := Run(newTestBackend(t, target, w, h, 8), opts(true))
	if pre.FinalError > base.FinalError*(1+0.05)+1e-6 {
		t.Fatalf("pre-polish gate violated: %.4f > %.4f", pre.FinalError, base.FinalError*1.05)
	}
}

// With the pass disabled (tol 0) the input must pass through untouched.
func TestSoftSwapDisabledIsNoop(t *testing.T) {
	w, h := 16, 16
	target := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		target[i*4+3] = 1
	}
	be := newTestBackend(t, target, w, h, 8)
	shapes := []model.Shape{{}, {}, {Type: model.TypeRotatedRectangle, Data: []float64{8, 8, 4, 4, 0}, Color: []int{10, 10, 10, 255}}}
	out, err := softSwapStandouts(be, shapes, 123.0, nil, Options{}, w, h)
	if err != 123.0 || len(out) != 3 || out[2].Type != model.TypeRotatedRectangle {
		t.Fatal("disabled pass must be a no-op")
	}
}
