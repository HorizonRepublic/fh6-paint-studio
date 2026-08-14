package engine

import (
	"testing"
	"time"
)

// TestETAPlanCountsOnlyEnabledPasses pins the property the countdown depends on: weight is reserved
// for work that will actually run. A pass that is switched off but still holds weight leaves the bar
// short of 100% at the end, waiting for something that never starts.
func TestETAPlanCountsOnlyEnabledPasses(t *testing.T) {
	base := Options{StopAt: 100}
	lean := newETA(base, time.Now(), func(PhaseProgress) {})
	defer lean.stop()
	if got := len(lean.phases); got != 1 {
		t.Fatalf("plain run planned %d phases, want just the greedy loop", got)
	}

	rich := Options{StopAt: 100, SmoothBase: true, Polish: true, GlobalColorIters: 100, LooRefit: 2,
		PolishOpts: PolishOptions{Iters: 250}}
	tr := newETA(rich, time.Now(), func(PhaseProgress) {})
	defer tr.stop()
	if len(tr.phases) != 5 { // smooth base, greedy, polish, LOO refit, colour re-solve
		t.Fatalf("configured run planned %d phases, want 5: %+v", len(tr.phases), tr.phases)
	}
	if tr.total <= greedyWeight {
		t.Fatalf("total weight %.1f does not exceed the greedy loop alone", tr.total)
	}
}

// TestETAReachesTheEnd walks a run the way the engine does — enter each phase, report progress inside
// it — and checks the estimate is monotone, bounded, and actually lands on 100%.
func TestETAReachesTheEnd(t *testing.T) {
	opt := Options{StopAt: 100, Polish: true, GlobalColorIters: 100, PolishOpts: PolishOptions{Iters: 250}}
	var last PhaseProgress
	seen := 0
	tr := newETA(opt, time.Now().Add(-10*time.Second), func(p PhaseProgress) { last = p; seen++ })
	defer tr.stop()

	prev := -1.0
	// Distinct labels per phase, as the passes have: a repeated label is how the tracker recognises
	// the same pass reporting twice, so reusing one here would (correctly) refuse to advance.
	for i, ph := range append([]etaPhase(nil), tr.phases...) {
		name := ph.name
		if name == "" {
			name = "pass" + string(rune('A'+i))
		}
		tr.enter(name)
		for _, f := range []float64{0, 0.5, 1} {
			tr.frac = f // set directly: setFrac throttles, and the test wants every step observed
			tr.publish(true)
			if last.Overall < prev {
				t.Fatalf("phase %d frac %.1f: overall went backwards %.3f -> %.3f", i, f, prev, last.Overall)
			}
			prev = last.Overall
			if last.Overall > 1.0001 {
				t.Fatalf("overall exceeded 1: %.4f", last.Overall)
			}
		}
	}
	if last.Overall < 0.999 {
		t.Fatalf("run finished at overall %.4f, want ~1", last.Overall)
	}
	if seen == 0 {
		t.Fatal("no progress was emitted")
	}
	// Ten seconds of elapsed time with the run complete leaves nothing to wait for.
	if last.ETA > time.Second {
		t.Fatalf("ETA at the end is %v, want ~0", last.ETA)
	}
}

// TestETAWithoutCallbackIsInert guards the default path: no callback means no tracker, no heartbeat
// goroutine, and every method still safe to call on the nil it returns.
func TestETAWithoutCallbackIsInert(t *testing.T) {
	tr := newETA(Options{StopAt: 10}, time.Now(), nil)
	if tr != nil {
		t.Fatal("a run with no OnPhase built a tracker")
	}
	tr.enter("x")
	tr.setFrac(0.5)
	tr.stop()
}
