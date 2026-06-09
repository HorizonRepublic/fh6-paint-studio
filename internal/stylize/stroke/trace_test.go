package stroke

import "testing"

func TestTraceAndSimplifyRect(t *testing.T) {
	w, h := 10, 6
	mask := make([]bool, w*h)
	for i := range mask {
		mask[i] = true
	}
	loop := traceBoundary(mask, w, h)
	if len(loop) < 20 {
		t.Fatalf("boundary loop too short: %d (want ~perimeter)", len(loop))
	}
	simp := simplify(loop, 1.5)
	if len(simp) < 4 || len(simp) > 7 {
		t.Errorf("rect should simplify to ~4 corners, got %d points", len(simp))
	}
}

func TestTraceEmpty(t *testing.T) {
	if loop := traceBoundary(make([]bool, 9), 3, 3); loop != nil {
		t.Errorf("empty mask should trace to nil, got %d points", len(loop))
	}
}
