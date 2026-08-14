package engine

import "testing"

// TestPolishAlphaFloorUnsetKeepsHistoricalBound guards the compatibility half of the alpha-floor
// fix: a caller that threads no floor must still get the 0.05 bound, so every path that has not
// been measured with a floor stays bit-identical.
func TestPolishAlphaFloorUnsetKeepsHistoricalBound(t *testing.T) {
	if got := (PolishOptions{}).alphaFloor(); got != 0.05 {
		t.Errorf("unset alphaFloor() = %v, want the historical 0.05", got)
	}
	if got := (PolishOptions{AlphaMin: 0.3}).alphaFloor(); got != 0.3 {
		t.Errorf("alphaFloor() = %v, want the threaded 0.3", got)
	}
}
