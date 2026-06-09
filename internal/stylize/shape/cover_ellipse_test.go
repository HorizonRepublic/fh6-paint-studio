package shape

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

func diskRegion(r float64) *Region {
	w, h := int(2*r)+4, int(2*r)+4
	mask := make([]bool, w*h)
	cx, cy := float64(w)/2, float64(h)/2
	area := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if math.Hypot(float64(x)-cx, float64(y)-cy) <= r {
				mask[y*w+x] = true
				area++
			}
		}
	}
	return &Region{Color: model.RGBA{R: 0.5, A: 1}, BW: w, BH: h, Mask: mask, Area: area}
}

func TestFitEllipseRoundHighIoU(t *testing.T) {
	_, _, rx, ry, _, iou := FitEllipse(diskRegion(15))
	if iou < 0.90 {
		t.Errorf("disk IoU = %.3f, want ≥0.90", iou)
	}
	if math.Abs(rx-ry) > 0.2*rx {
		t.Errorf("disk should fit a near-circular ellipse, got rx=%.1f ry=%.1f", rx, ry)
	}
}

func TestFitEllipseLShapeLowIoU(t *testing.T) {
	// concave L-shape: an ellipse cannot cover it well → low IoU → fill falls back to triangulation.
	w, h := 20, 20
	mask := make([]bool, w*h)
	area := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < 8 || y >= 12 { // L
				mask[y*w+x] = true
				area++
			}
		}
	}
	r := &Region{Color: model.RGBA{A: 1}, BW: w, BH: h, Mask: mask, Area: area}
	if _, _, _, _, _, iou := FitEllipse(r); iou >= 0.85 {
		t.Errorf("L-shape IoU = %.3f, should be well below the 0.85 ellipse gate", iou)
	}
}

func TestFitEllipseStraightSliverElongated(t *testing.T) {
	// a thin horizontal bar: the fit ellipse should be elongated (rx >> ry) and cover it reasonably.
	w, h := 40, 8
	mask := make([]bool, w*h)
	area := 0
	for y := 2; y < 5; y++ {
		for x := 2; x < 38; x++ {
			mask[y*w+x] = true
			area++
		}
	}
	r := &Region{Color: model.RGBA{A: 1}, BW: w, BH: h, Mask: mask, Area: area}
	_, _, rx, ry, _, iou := FitEllipse(r)
	if rx <= ry {
		t.Errorf("sliver ellipse not elongated: rx=%.1f ry=%.1f", rx, ry)
	}
	if iou < 0.6 {
		t.Errorf("sliver IoU = %.3f, want a reasonable cover", iou)
	}
}
