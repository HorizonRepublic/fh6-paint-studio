package engine

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// grey builds a w*h*4 RGBA canvas whose R=G=B=luma[i], alpha=1 — so luma() of a pixel
// returns exactly the supplied value (the Sobel helpers run on luma).
func greyCanvas(luma []float32, w, h int) []float32 {
	c := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		c[i*4+0], c[i*4+1], c[i*4+2], c[i*4+3] = luma[i], luma[i], luma[i], 1
	}
	return c
}

func TestFalseEdgeMap_StepVsFlat(t *testing.T) {
	w, h := 6, 4
	// recon: a vertical step — left columns dark (0), right columns bright (1).
	rl := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x >= 3 {
				rl[y*w+x] = 1
			}
		}
	}
	recon := greyCanvas(rl, w, h)
	// target: flat grey — NO edge anywhere.
	tl := make([]float32, w*h)
	for i := range tl {
		tl[i] = 0.5
	}
	target := greyCanvas(tl, w, h)

	fe, f0, gtot := falseEdgeMap(recon, target, w, h)
	if f0 <= 0 || gtot <= 0 {
		t.Fatalf("expected positive energies, got F0=%v Gtot=%v", f0, gtot)
	}
	// The false edge must be concentrated at the step (x=2/3 boundary), ~zero at the far-left flat column.
	feAtStep := fe[1*w+2] + fe[1*w+3]
	feAtFlat := fe[1*w+0]
	if feAtStep <= feAtFlat {
		t.Fatalf("false edge should peak at the step: step=%v flat=%v", feAtStep, feAtFlat)
	}
}

func TestFalseEdgeMap_MatchingIsZero(t *testing.T) {
	w, h := 6, 4
	// recon == target == the same step => recon has no edge the target lacks.
	rl := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x >= 3 {
				rl[y*w+x] = 1
			}
		}
	}
	canvas := greyCanvas(rl, w, h)
	fe, f0, _ := falseEdgeMap(canvas, canvas, w, h)
	if f0 > 1e-4 {
		t.Fatalf("matching recon/target must give ~zero false-edge energy, got F0=%v", f0)
	}
	for i, v := range fe {
		if v > 1e-4 {
			t.Fatalf("fe[%d]=%v expected ~0 when recon==target", i, v)
		}
	}
}

func TestShapeStandoutSalience(t *testing.T) {
	w, h := 16, 16
	// A centered rectangle shape (Data = cx,cy,halfW,halfH,angle).
	rect := model.Shape{Type: model.TypeRotatedRectangle, Data: []float64{8, 8, 5, 4, 0}, Color: []int{120, 120, 120, 255}}
	shapes := []model.Shape{{Type: model.TypeRectangle, Data: []float64{8, 8, 8, 8, 0}, Color: []int{0, 0, 0, 255}}, rect}

	// fe all ones => the rectangle's rim sits on false-edge => positive salience.
	feOnes := make([]float32, w*h)
	for i := range feOnes {
		feOnes[i] = 1
	}
	sOnes := shapeStandoutSalience(shapes, feOnes, w, h)
	if sOnes[1] <= 0 {
		t.Fatalf("salience with fe=1 should be positive, got %v", sOnes[1])
	}
	// fe all zeros => no false edge => zero salience.
	feZero := make([]float32, w*h)
	sZero := shapeStandoutSalience(shapes, feZero, w, h)
	if sZero[1] != 0 {
		t.Fatalf("salience with fe=0 should be zero, got %v", sZero[1])
	}
	// Background (index 0) is never scored.
	if sOnes[0] != 0 {
		t.Fatalf("background salience must be 0, got %v", sOnes[0])
	}
}

func TestLocalMeanColorTarget_Uniform(t *testing.T) {
	w, h := 8, 8
	// Uniform target {0.2,0.4,0.6}; weight 1 => the mean over any non-empty footprint is that color.
	target := make([]float32, w*h*4)
	weight := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		target[i*4+0], target[i*4+1], target[i*4+2], target[i*4+3] = 0.2, 0.4, 0.6, 1
		weight[i] = 1
	}
	rect := model.Shape{Type: model.TypeRotatedRectangle, Data: []float64{4, 4, 3, 3, 0}, Color: []int{0, 0, 0, 128}}
	// sanity: the shape covers at least one pixel.
	kind := model.KindFromType(rect.Type)
	p := model.ParamsFromShape(rect)
	if !raster.Inside(kind, p, 4, 4) {
		t.Fatalf("test rect should cover its center")
	}
	r, g, b, ok := localMeanColorTarget(rect, target, weight, w, h)
	if !ok {
		t.Fatalf("expected a valid mean color")
	}
	wantR, wantG, wantB := model.EncByte(0.2), model.EncByte(0.4), model.EncByte(0.6)
	if absInt(r-wantR) > 1 || absInt(g-wantG) > 1 || absInt(b-wantB) > 1 {
		t.Fatalf("mean color = (%d,%d,%d) want (%d,%d,%d)", r, g, b, wantR, wantG, wantB)
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// guard against accidental NaN from the Sobel helper on degenerate input.
func TestSobelMagAt_NoNaN(t *testing.T) {
	w, h := 3, 3
	lum := []float32{0, 0, 0, 0, 1, 0, 0, 0, 0}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if m := sobelMagAt(lum, w, h, x, y); math.IsNaN(float64(m)) {
				t.Fatalf("NaN at %d,%d", x, y)
			}
		}
	}
}
