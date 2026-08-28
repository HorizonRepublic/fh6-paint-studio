package preset

import "testing"

// A default that reaches the CLI and not the studio has cost this repo real time before. The studio
// goes through ModeDefaultsFor, so that is where the rule has to hold — testing the flag plumbing in
// cmd/fh6paint would prove nothing about the other consumer.
//
// Flat is off on its own measurement, not by omission: 6 paired runs over 3 flat images cleared the
// bar on 1, with three of the six bit-identical — flat shapes are large and evenly coloured, so a
// local refine finds almost nothing and the whole pass rolls back. It never made flat worse; it just
// costs 5% of the wall for a win on a minority of images.
func TestGeomRefineDefaults(t *testing.T) {
	// The pin is the only way to A/B the pass from the studio, so it gets exported for whole
	// sessions — and this test asserts the DEFAULT. Clear it, or the documented way to run the
	// control arm fails the suite.
	t.Setenv("FH6_GEOMREFINE", "")
	for _, tc := range []struct {
		mode string
		want bool
	}{{"anime", true}, {"photo", true}, {"flat", false}} {
		md := ModeDefaultsFor(tc.mode, 0, false)
		if md.GeomRefine != tc.want {
			t.Errorf("%s: GeomRefine = %v, want %v — the pass measured 27/27, mean -3.6 percent",
				tc.mode, md.GeomRefine, tc.want)
		}
	}
}
