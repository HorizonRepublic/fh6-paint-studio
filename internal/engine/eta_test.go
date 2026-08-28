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
	lean := newETA(base, true, time.Now(), func(PhaseProgress) {})
	defer lean.stop()
	if got := len(lean.phases); got != 1 {
		t.Fatalf("plain run planned %d phases, want just the greedy loop", got)
	}

	rich := Options{StopAt: 100, SmoothBase: true, Polish: true, GlobalColorIters: 100, LooRefit: 2,
		PolishOpts: PolishOptions{Iters: 250}}
	tr := newETA(rich, true, time.Now(), func(PhaseProgress) {})
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
	tr := newETA(opt, true, time.Now().Add(-10*time.Second), func(p PhaseProgress) { last = p; seen++ })
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

// TestETACountsDownWhileStalled pins the deadline behaviour the countdown was rebuilt for: when a
// phase stops reporting progress, the displayed remaining time must keep FALLING between readings
// (the old duration-EMA froze — raw = elapsed·(1−done)/done grows exactly as fast as the clock at
// constant done, so the number never moved).
func TestETACountsDownWhileStalled(t *testing.T) {
	opt := Options{StopAt: 100, Polish: true, PolishOpts: PolishOptions{Iters: 250}}
	var last PhaseProgress
	tr := newETA(opt, true, time.Now().Add(-20*time.Second), func(p PhaseProgress) { last = p })
	defer tr.stop()
	tr.enter("Placing shapes…")
	tr.frac = 0.9
	tr.publish(true)
	first := last.ETA
	if first <= 0 {
		t.Fatalf("no estimate after 20s elapsed at 90%% of the greedy")
	}
	// The phase stalls: no frac movement, only the clock. Each reading must come back lower.
	prev := first
	for i := 0; i < 3; i++ {
		time.Sleep(300 * time.Millisecond)
		tr.publish(true)
		if last.ETA >= prev {
			t.Fatalf("reading %d: ETA froze or rose while stalled: %v -> %v", i, prev, last.ETA)
		}
		prev = last.ETA
	}
}

// TestETAWithoutCallbackIsInert guards the default path: no callback means no tracker, no heartbeat
// goroutine, and every method still safe to call on the nil it returns.
func TestETAWithoutCallbackIsInert(t *testing.T) {
	tr := newETA(Options{StopAt: 10}, true, time.Now(), nil)
	if tr != nil {
		t.Fatal("a run with no OnPhase built a tracker")
	}
	tr.enter("x")
	tr.setFrac(0.5)
	tr.stop()
}
