package engine

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// renderStack composites a shape list the way skewRefine's inner loop does, so a test can build a
// target out of shapes and then ask the pass to recover one of them.
func renderStack(shapes []model.Shape, w, h int) []float32 {
	buf := make([]float32, w*h*4)
	for _, s := range shapes {
		k := model.KindFromType(s.Type)
		p := model.ParamsFromShape(s)
		pr := raster.Prep(k, p)
		col := [4]float32{model.DecChan(s.Color[0]), model.DecChan(s.Color[1]), model.DecChan(s.Color[2]), float32(s.Color[3]) / 255}
		x0, y0, x1, y1 := raster.BBox(k, p, w, h)
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				a := float32(pr.Coverage(x, y)) * col[3]
				if a <= 0 {
					continue
				}
				i := (y*w + x) * 4
				inv := 1 - a
				buf[i+0] = buf[i+0]*inv + col[0]*a
				buf[i+1] = buf[i+1]*inv + col[1]*a
				buf[i+2] = buf[i+2]*inv + col[2]*a
				buf[i+3] = buf[i+3]*inv + a
			}
		}
	}
	return buf
}

func shape(typ int, col []int, data ...float64) model.Shape {
	return model.Shape{Type: typ, Color: col, Data: data}
}

// TestSkewRefineRecoversAKnownShear builds a target whose foreground IS a parallelogram, hands the
// pass the same rectangle unsheared, and expects the shear back. Without this the pass could ship
// doing nothing at all and every A/B would read as "no effect" rather than "not wired".
func TestSkewRefineRecoversAKnownShear(t *testing.T) {
	const w, h = 220, 160
	const trueSkew = 0.6
	bg := shape(model.TypeRotatedRectangle, []int{20, 20, 20, 255}, w/2, h/2, w, h, 0)
	fgTrue := shape(model.TypeRotatedRectangle, []int{230, 40, 40, 255}, 110, 80, 45, 34, 15, trueSkew)
	target := renderStack([]model.Shape{bg, fgTrue}, w, h)

	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1
	}
	start := []model.Shape{bg, shape(model.TypeRotatedRectangle, []int{230, 40, 40, 255}, 110, 80, 45, 34, 15)}

	out, changed := localRefine(start, target, weight, w, h, false)
	if changed != 1 {
		t.Fatalf("changed %d shapes, want exactly the one rectangle", changed)
	}
	if len(out[1].Data) < 6 {
		t.Fatalf("the refined rectangle exported %d fields — the shear was dropped on the way out", len(out[1].Data))
	}
	got := out[1].Data[5]
	if math.Abs(got-trueSkew) > 0.2 {
		t.Errorf("recovered shear %.3f, want about %.3f", got, trueSkew)
	}
	if e0, e1 := stackErr(start, target, weight, w, h), stackErr(out, target, weight, w, h); e1 >= e0 {
		t.Errorf("error did not fall: %.1f -> %.1f", e0, e1)
	}
}

// TestSkewRefineLeavesAnUnshearedFitAlone is the other half: on a target the stack already matches,
// the pass must keep its hands off. A pass that banks noise-level "wins" spends a data field per
// shape and moves the geometry for nothing.
func TestSkewRefineLeavesAnUnshearedFitAlone(t *testing.T) {
	const w, h = 220, 160
	bg := shape(model.TypeRotatedRectangle, []int{20, 20, 20, 255}, w/2, h/2, w, h, 0)
	fg := shape(model.TypeRotatedRectangle, []int{230, 40, 40, 255}, 110, 80, 45, 34, 15)
	stack := []model.Shape{bg, fg}
	target := renderStack(stack, w, h)
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1
	}
	if _, changed := localRefine(stack, target, weight, w, h, false); changed != 0 {
		t.Errorf("%d shapes sheared against an exact fit — the accept floor is not holding", changed)
	}
}

// TestSkewRefineSkipsRedundantKinds pins the maths claim the pass rests on: shearing an ellipse
// produces another rotated ellipse, so offering the DOF to the radial kinds costs time and buys
// nothing. If someone widens eligibility, this says why not.
func TestSkewRefineSkipsRedundantKinds(t *testing.T) {
	for _, k := range []model.ShapeKind{model.KindEllipse, model.KindGlow, model.KindDisk, model.KindTriangle, model.KindLine} {
		if skewEligible(k) {
			t.Errorf("kind %v is eligible for the shear refine but gains no new shape from it", k)
		}
	}
	if !skewEligible(model.KindRectangle) {
		t.Error("the rectangle must be eligible — a sheared rectangle is a parallelogram")
	}
}

func stackErr(shapes []model.Shape, target, weight []float32, w, h int) float64 {
	buf := renderStack(shapes, w, h)
	var e float64
	for i := 0; i < w*h; i++ {
		wt := float64(weight[i])
		for c := 0; c < 3; c++ {
			d := float64(buf[i*4+c]) - float64(target[i*4+c])
			e += wt * d * d
		}
	}
	return e
}

// TestGeomRefineRecoversADisplacedShape is the wide mode's version of the same question. The stack is
// handed a rectangle that is off-centre, too small and turned the wrong way; the pass has to walk it
// back. This is what separates a coordinate search from a shear search — no single parameter fixes it.
func TestGeomRefineRecoversADisplacedShape(t *testing.T) {
	const w, h = 240, 180
	bg := shape(model.TypeRotatedRectangle, []int{25, 25, 30, 255}, w/2, h/2, w, h, 0)
	truth := shape(model.TypeRotatedRectangle, []int{220, 60, 50, 255}, 120, 90, 46, 30, 22)
	target := renderStack([]model.Shape{bg, truth}, w, h)
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1
	}
	start := []model.Shape{bg, shape(model.TypeRotatedRectangle, []int{220, 60, 50, 255}, 112, 84, 39, 25, 14)}

	e0 := stackErr(start, target, weight, w, h)
	out, changed := localRefine(start, target, weight, w, h, true)
	if changed != 1 {
		t.Fatalf("changed %d shapes, want 1", changed)
	}
	e1 := stackErr(out, target, weight, w, h)
	if e1 >= e0 {
		t.Fatalf("error did not fall: %.1f -> %.1f", e0, e1)
	}
	// It should close most of the gap, not merely twitch. A pass that recovers a few percent of a
	// plainly wrong placement is not doing the job its cost implies.
	if got := (e0 - e1) / e0; got < 0.5 {
		t.Errorf("recovered only %.1f%% of the error; the search is not reaching the optimum", got*100)
	}
	d := out[1].Data
	t.Logf("recovered [%.1f %.1f %.1f %.1f %.1f] from [112 84 39 25 14], truth [120 90 46 30 22]",
		d[0], d[1], d[2], d[3], d[4])
}

// TestGeomRefineLeavesAnExactFitAlone guards the accept floor in the wide mode, where there are five
// or six chances per shape to bank a rounding-level win instead of one.
func TestGeomRefineLeavesAnExactFitAlone(t *testing.T) {
	const w, h = 240, 180
	bg := shape(model.TypeRotatedRectangle, []int{25, 25, 30, 255}, w/2, h/2, w, h, 0)
	fg := shape(model.TypeRotatedEllipse, []int{220, 60, 50, 255}, 120, 90, 40, 26, 18)
	stack := []model.Shape{bg, fg}
	target := renderStack(stack, w, h)
	weight := make([]float32, w*h)
	for i := range weight {
		weight[i] = 1
	}
	if _, changed := localRefine(stack, target, weight, w, h, true); changed != 0 {
		t.Errorf("%d shapes moved against an exact fit", changed)
	}
}

// TestSearchWindowContainsEveryTrial is the guard on the one assumption the whole pass rests on: all
// trials are scored over ONE fixed window, so every position the search can reach must render inside
// it. If it does not, a trial is charged nothing for coverage it pushed out of view and "move away"
// becomes the cheapest improvement there is.
func TestSearchWindowContainsEveryTrial(t *testing.T) {
	const w, h = 400, 300
	cases := []struct {
		kind model.ShapeKind
		p    [6]float32
	}{
		{model.KindRectangle, [6]float32{200, 150, 40, 25, 20, 0}},
		{model.KindEllipse, [6]float32{200, 150, 55, 18, -35, 0}},
		{model.KindGlow, [6]float32{200, 150, 60, 40, 10, 0}},
		{model.KindTriangle, [6]float32{160, 120, 250, 140, 190, 210}},
		{model.KindLine, [6]float32{150, 130, 260, 190, 4, 0}},
	}
	for _, c := range cases {
		axes := refineAxes(c.kind, c.p, true)
		win := searchWindow(c.kind, c.p, axes, w, h)
		// Drive every axis to both bounds at once — a harsher corner than the search can actually
		// reach, since it moves one parameter at a time.
		for _, pick := range []func(refineAxis) float64{
			func(a refineAxis) float64 { return a.lo },
			func(a refineAxis) float64 { return a.hi },
		} {
			q := c.p
			for _, a := range axes {
				q[a.slot] = float32(pick(a))
			}
			x0, y0, x1, y1 := raster.BBox(c.kind, q, w, h)
			if x0 < win[0] || y0 < win[1] || x1 > win[2] || y1 > win[3] {
				t.Errorf("%v: extreme trial box [%d %d %d %d] escapes the search window [%d %d %d %d]",
					c.kind, x0, y0, x1, y1, win[0], win[1], win[2], win[3])
			}
		}
	}
}
