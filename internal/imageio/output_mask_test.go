package imageio

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

// scaleParams' default branch zeros slot 5, which would destroy a mask's skew under SSAA. The mask
// branch must scale position+extents (slots 0-3) and carry rot+skew (slots 4-5) through untouched.
func TestScaleParamsMaskPreservesSkew(t *testing.T) {
	k := model.RegisterMaskWord(0x1234, 100, 100)
	p := [6]float32{10, 20, 30, 40, 17, 0.5}
	got := scaleParams(k, p, 2)
	want := [6]float32{20, 40, 60, 80, 17, 0.5}
	if got != want {
		t.Errorf("scaleParams(mask) = %v want %v", got, want)
	}
}

// End-to-end render-faithful check: a dictionary word (circle mask) placed via the affine flows through
// RenderFH6's generic Coverage path and composites in linear light — centre = shape colour, far = bg.
func TestRenderFH6MaskCircle(t *testing.T) {
	const W, H = 200, 200
	shapes := []model.Shape{
		{Type: model.TypeRotatedRectangle, Color: []int{0, 0, 0, 255}, Data: []float64{100, 100, 200, 200, 0}}, // black bg
		{Type: 0x0066, Color: []int{255, 0, 0, 255}, Data: []float64{100, 100, 100, 100, 0, 0}},                // red circle mask, footprint 100px
	}
	out := RenderFH6(shapes, false, W, H, 1)
	at := func(x, y int) (r, g, b float32) { i := (y*W + x) * 4; return out[i], out[i+1], out[i+2] }
	if r, g, b := at(100, 100); r < 0.9 || g > 0.1 || b > 0.1 {
		t.Errorf("centre = (%.2f,%.2f,%.2f) want red", r, g, b)
	}
	if r, _, _ := at(100, 145); r < 0.5 { // inside the silhouette -> mostly red
		t.Errorf("inside-edge r=%.2f want >0.5", r)
	}
	if r, g, b := at(100, 158); r > 0.1 || g > 0.1 || b > 0.1 { // beyond the footprint -> black bg
		t.Errorf("outside = (%.2f,%.2f,%.2f) want black", r, g, b)
	}
}
