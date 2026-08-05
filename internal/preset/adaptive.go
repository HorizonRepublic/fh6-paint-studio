package preset

import (
	"fmt"
	"os"

	"fh6-paint-studio/internal/metric"
)

// Content-adaptive knobs: values derived from the TARGET at load time and applied ON TOP of whatever
// preset is active, instead of being baked into the bundle.
//
// A preset is one set of numbers chosen for a whole class of pictures, which is the right shape only
// for knobs whose best value is constant across that class. Measurement says most are not: scanning
// one knob at a time over the bank, almost every knob helps some pictures and hurts others, and the
// class it was tuned for is too coarse to separate them. Keeping those knobs in the bundle forces one
// compromise on every picture; deriving them here lets each picture get its own.
//
// The layer is deliberately separate from the preset tables so a rule can be measured, pinned and
// reverted without touching the bundle underneath it, and so the studio — which has no flags — can
// still A/B it through an env pin.
//
// Rules earn their place by MEASUREMENT, never by plausibility: at least three images across two
// seeds, and the effect has to clear the seed noise floor, which on this engine is of the same order
// as most single-seed knob effects. A rule that cannot beat that floor does not belong here.

// Adaptive carries the knob values a target's own statistics ask for. A zero field means "no opinion,
// keep whatever the preset chose" — rules opt in one field at a time.
type Adaptive struct {
	SaliencyQuota float64 // 0 = no opinion
}

// NO RULE SHIPS HERE YET, and that is a result rather than an omission.
//
// The first candidate was the saliency quota keyed on flatness. On one seed it looked like exactly
// the sign flip this layer exists for: the reserve paid off on a 93%-flat frame and cost quality on a
// busy one. Across four seeds it dissolved — 2 wins and 2 losses, paired mean +0.7% — because the
// baseline on that flat frame moves 11.2% from the SEED ALONE, several times the effect being
// chased. The busy side did replicate (4/4 worse, baseline seed spread only 1.5%), which merely
// confirms the preset's existing 0.15 is right there.
//
// So the bar for anything added below: the effect must be paired per seed and must clear that frame's
// own seed spread, which on flat content is an order of magnitude larger than on busy content. A
// single-seed scan cannot establish a rule here, and 3-7% single-seed knob effects are exactly the
// size of the noise.
//
// Until a rule earns its place, a candidate can still be injected through the env pin and A/B'd
// without a rebuild — including from the studio, which has no flags.

// AdaptiveKnobs derives knob values from the target's content statistics. FH6_ADAPT_QUOTA
// ("flatTau,quota") injects a candidate quota rule for lab A/Bs; nothing fires without it.
func AdaptiveKnobs(cs metric.ContentStats) Adaptive {
	var a Adaptive
	if s := os.Getenv("FH6_ADAPT_QUOTA"); s != "" {
		var tau, q float64
		if n, err := fmt.Sscanf(s, "%f,%f", &tau, &q); n == 2 && err == nil && cs.FlatFrac >= tau {
			a.SaliencyQuota = q
		}
	}
	return a
}

// Apply overlays the derived values onto a preset's choices. Fields the rules have no opinion on are
// left exactly as the preset set them, so adding a rule can never silently disturb an unrelated knob.
func (a Adaptive) Apply(quota *float64) {
	if a.SaliencyQuota > 0 && quota != nil {
		*quota = a.SaliencyQuota
	}
}
