package stroke

import "testing"

// TestInkDedupCoverage verifies the coverage helpers: an already-stamped path reads as fully inked, a
// distant path as not — the basis for skipping redundant retraces.
func TestInkDedupCoverage(t *testing.T) {
	w, h := 40, 40
	inked := make([]bool, w*h)
	line := [][2]float64{{5, 20}, {35, 20}}

	if f := polyInkedFraction(line, inked, w, h, 1); f > 0.05 {
		t.Errorf("unstamped line fraction = %.2f, want ~0", f)
	}
	stampPoly(line, inked, w, h, 1)
	if f := polyInkedFraction(line, inked, w, h, 1); f < 0.9 {
		t.Errorf("stamped line fraction = %.2f, want ~1 (redundant retrace)", f)
	}
	far := [][2]float64{{5, 5}, {35, 5}} // a parallel line 15px away
	if f := polyInkedFraction(far, inked, w, h, 1); f > 0.1 {
		t.Errorf("distant line fraction = %.2f, want ~0 (not redundant)", f)
	}
}
