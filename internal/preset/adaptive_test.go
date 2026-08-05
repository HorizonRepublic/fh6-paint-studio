package preset

import (
	"testing"

	"fh6-paint-studio/internal/metric"
)

// Nothing may fire without an explicit pin: the layer exists, but no rule has yet survived seed
// replication, and a plausible-looking default here would cost quality on every picture.
func TestAdaptiveShipsNoRuleByDefault(t *testing.T) {
	for _, flat := range []float64{0.10, 0.50, 0.85, 0.93, 0.99} {
		if a := AdaptiveKnobs(metric.ContentStats{FlatFrac: flat}); a != (Adaptive{}) {
			t.Errorf("FlatFrac %.2f produced %+v, want no opinion", flat, a)
		}
	}
}

func TestAdaptiveLeavesUntouchedWhatItHasNoOpinionOn(t *testing.T) {
	quota := 0.15
	AdaptiveKnobs(metric.ContentStats{FlatFrac: 0.95}).Apply(&quota)
	if quota != 0.15 {
		t.Errorf("quota = %v, want the preset's 0.15 left alone", quota)
	}
}

// The pin is how a candidate gets A/B'd from the studio, which has no command line.
func TestAdaptiveQuotaEnvPinFiresOnlyAboveItsThreshold(t *testing.T) {
	t.Setenv("FH6_ADAPT_QUOTA", "0.90,0.45")
	if a := AdaptiveKnobs(metric.ContentStats{FlatFrac: 0.50}); a.SaliencyQuota != 0 {
		t.Errorf("below the pinned threshold the rule fired: %v", a.SaliencyQuota)
	}
	quota := 0.15
	AdaptiveKnobs(metric.ContentStats{FlatFrac: 0.93}).Apply(&quota)
	if quota != 0.45 {
		t.Errorf("quota = %v, want the pinned 0.45", quota)
	}
}

func TestAdaptiveIgnoresAMalformedPin(t *testing.T) {
	t.Setenv("FH6_ADAPT_QUOTA", "nonsense")
	if a := AdaptiveKnobs(metric.ContentStats{FlatFrac: 0.95}); a != (Adaptive{}) {
		t.Errorf("malformed pin produced %+v, want no opinion", a)
	}
}
