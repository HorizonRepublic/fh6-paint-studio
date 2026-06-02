package raster

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestRectInsideOutsideBBox(t *testing.T) {
	p := [6]float32{50, 50, 10, 4, 0, 0} // center, halfW, halfH, theta
	if !Inside(model.KindRectangle, p, 50, 50) {
		t.Fatal("center should be inside rect")
	}
	if Inside(model.KindRectangle, p, 50, 60) {
		t.Fatal("10px below center (halfH=4) should be outside rect")
	}
	xmin, ymin, xmax, ymax := BBox(model.KindRectangle, p, 100, 100)
	if xmin > 40 || xmax < 60 || ymin > 46 || ymax < 54 {
		t.Fatalf("rect bbox (%d,%d,%d,%d) does not enclose", xmin, ymin, xmax, ymax)
	}
}

func TestTriangleInsideOutsideBBox(t *testing.T) {
	p := [6]float32{10, 10, 90, 10, 50, 90} // verts (10,10)(90,10)(50,90)
	if !Inside(model.KindTriangle, p, 50, 40) {
		t.Fatal("near-centroid point should be inside triangle")
	}
	if Inside(model.KindTriangle, p, 10, 80) {
		t.Fatal("bottom-left point should be outside triangle")
	}
	xmin, ymin, xmax, ymax := BBox(model.KindTriangle, p, 100, 100)
	if xmin > 10 || xmax < 90 || ymin > 10 || ymax < 90 {
		t.Fatalf("triangle bbox (%d,%d,%d,%d) does not enclose verts", xmin, ymin, xmax, ymax)
	}
}

func TestLineInsideOutsideBBox(t *testing.T) {
	p := [6]float32{10, 50, 90, 50, 3, 0} // segment (10,50)-(90,50), halfWidth 3
	if !Inside(model.KindLine, p, 50, 51) {
		t.Fatal("1px off the line (halfWidth=3) should be inside")
	}
	if Inside(model.KindLine, p, 50, 60) {
		t.Fatal("10px off the line should be outside")
	}
	xmin, ymin, xmax, ymax := BBox(model.KindLine, p, 100, 100)
	if xmin > 10 || xmax < 90 || ymin > 47 || ymax < 53 {
		t.Fatalf("line bbox (%d,%d,%d,%d) does not enclose", xmin, ymin, xmax, ymax)
	}
}

func TestDispatchEllipseDefault(t *testing.T) {
	p := [6]float32{50, 40, 10, 5, 0, 0}
	if !Inside(model.KindEllipse, p, 50, 40) {
		t.Fatal("ellipse dispatch should match EllipseInside")
	}
	if Inside(model.KindEllipse, p, 50, 50) {
		t.Fatal("ellipse dispatch: 10px below center (ry=5) should be outside")
	}
}
