package engine

import "testing"

func TestBoundaryRadiusCap(t *testing.T) {
	// Before start: no cap regardless of distance.
	if got := boundaryRadiusCap(100, 5, 16, 0.3, 0.42); got != 100 {
		t.Fatalf("before start must return maxR unchanged, got %v", got)
	}
	// start>=1 guard: no cap.
	if got := boundaryRadiusCap(100, 5, 16, 1.0, 1.0); got != 100 {
		t.Fatalf("start>=1 must return maxR, got %v", got)
	}
	// Far from any boundary (dist+padding >= maxR): no cap.
	if got := boundaryRadiusCap(100, 200, 16, 1.0, 0.42); got != 100 {
		t.Fatalf("far-from-boundary must return maxR, got %v", got)
	}
	// At progress 1, near a boundary: full cap = dist+padding.
	if got := boundaryRadiusCap(100, 4, 16, 1.0, 0.42); got != 20 {
		t.Fatalf("at progress 1 cap must be dist+padding=20, got %v", got)
	}
	// Mid-ramp (progress 0.71, start 0.42 → mix=0.5): lerp 100→20 = 60.
	got := boundaryRadiusCap(100, 4, 16, 0.71, 0.42)
	if got < 59.5 || got > 60.5 {
		t.Fatalf("mid-ramp cap must be ~60 (lerp maxR=100→lim=20 at mix~0.5), got %v", got)
	}
	// Cap never exceeds maxR and never below lim at full ramp.
	if got := boundaryRadiusCap(50, 1, 4, 1.0, 0.42); got != 5 {
		t.Fatalf("full cap = dist+padding = 5, got %v", got)
	}
}
