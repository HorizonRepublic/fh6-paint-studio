package stroke

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
)

func countMasks(shapes []model.Shape) int {
	n := 0
	for _, s := range shapes {
		if model.IsMask(model.KindFromType(s.Type)) {
			n++
		}
	}
	return n
}

// arcLoop returns points along a circular arc (centre c, radius r, a0→a1 rad).
func arcLoop(cx, cy, r, a0, a1 float64, n int) [][2]float64 {
	var loop [][2]float64
	for k := 0; k <= n; k++ {
		a := a0 + (a1-a0)*float64(k)/float64(n)
		loop = append(loop, [2]float64{cx + r*math.Cos(a), cy + r*math.Sin(a)})
	}
	return loop
}

func TestEmitOutlineStraightStaysRects(t *testing.T) {
	loop := [][2]float64{{10, 10}, {30, 10}, {50, 10}, {70, 10}}
	var out []model.Shape
	emitOutline(loop, 0, 0, 1.0, []int{0, 0, 0, 255}, Defaults(), &out, 1000)
	if m := countMasks(out); m != 0 {
		t.Errorf("straight contour should place no arcs, got %d", m)
	}
	if len(out) == 0 {
		t.Error("straight contour should still place rects")
	}
}

func TestEmitOutlinePlacesArc(t *testing.T) {
	loop := arcLoop(80, 80, 50, math.Pi, math.Pi*1.5, 8) // 90° arc, third quadrant
	var out []model.Shape
	emitOutline(loop, 0, 0, 1.0, []int{255, 0, 0, 255}, Defaults(), &out, 1000)
	if countMasks(out) == 0 {
		t.Fatalf("a clean 90° arc should place a dictionary arc, got %d shapes (no mask)", len(out))
	}
}

// TestArcRendersOnContour is the end-to-end convergence check: placement math + the renderer's mask
// transform must agree, so the ink lands ON the arc and the enclosed centre stays background.
func TestArcRendersOnContour(t *testing.T) {
	const W, H = 160, 160
	cx, cy, r := 80.0, 80.0, 50.0
	loop := arcLoop(cx, cy, r, math.Pi, math.Pi*1.5, 10) // 180°..270°
	var out []model.Shape
	emitOutline(loop, 0, 0, 1.2, []int{220, 20, 20, 255}, Defaults(), &out, 1000)
	if countMasks(out) == 0 {
		t.Fatal("expected an arc word for the 90° contour")
	}
	shapes := append([]model.Shape{{Type: model.TypeRotatedRectangle, Color: []int{255, 255, 255, 255}, Data: []float64{0, 0, 0, 0, 0, 0}}}, out...)
	buf := imageio.RenderFH6(shapes, false, W, H, 2)
	red := func(x, y int) bool {
		q := (y*W + x) * 4
		return buf[q] > 0.5 && buf[q+1] < 0.45 && buf[q+2] < 0.45
	}
	// arc midpoint (225°) — search a small window for ink on the stroke
	mx, my := int(cx+r*math.Cos(math.Pi*1.25)+0.5), int(cy+r*math.Sin(math.Pi*1.25)+0.5)
	found := false
	for dy := -7; dy <= 7 && !found; dy++ {
		for dx := -7; dx <= 7 && !found; dx++ {
			if x, y := mx+dx, my+dy; x >= 0 && x < W && y >= 0 && y < H && red(x, y) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("no ink near the arc midpoint (%d,%d) — placement/renderer disagree", mx, my)
	}
	// the enclosed centre must stay white (a thin stroke, not a filled disk)
	ci := (int(cy)*W + int(cx)) * 4
	if buf[ci] < 0.8 || buf[ci+1] < 0.8 {
		t.Errorf("arc centre is not background (R%.2f G%.2f) — stroke is filling, not outlining", buf[ci], buf[ci+1])
	}
}
