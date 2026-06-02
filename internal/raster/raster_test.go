package raster

import "testing"

func TestEllipseBBoxAxisAligned(t *testing.T) {
	// center (50,40), rx=10, ry=5, theta=0
	xmin, ymin, xmax, ymax := EllipseBBox([6]float32{50, 40, 10, 5, 0, 0}, 100, 100)
	if xmin > 40 || xmax < 60 || ymin > 35 || ymax < 45 {
		t.Fatalf("bbox = (%d,%d,%d,%d) does not enclose ellipse", xmin, ymin, xmax, ymax)
	}
	if xmin < 38 || xmax > 62 || ymin < 33 || ymax > 47 {
		t.Fatalf("bbox = (%d,%d,%d,%d) is too large", xmin, ymin, xmax, ymax)
	}
}

func TestEllipseInside(t *testing.T) {
	p := [6]float32{50, 40, 10, 5, 0, 0}
	if !EllipseInside(p, 50, 40) {
		t.Fatal("center should be inside")
	}
	if EllipseInside(p, 50, 50) {
		t.Fatal("point 10px below center (ry=5) should be outside")
	}
}

func TestEllipseInsideRotated90(t *testing.T) {
	p := [6]float32{50, 40, 10, 5, 90, 0} // major axis (10) now vertical
	if !EllipseInside(p, 50, 48) {
		t.Fatal("point 8px below center should be inside when rotated 90° (vertical major axis)")
	}
	if EllipseInside(p, 58, 40) {
		t.Fatal("point 8px right of center should be outside when rotated 90°")
	}
}
