package ui

import (
	"math"
	"testing"
	"time"
)

// The displayed ETA must be a smooth countdown: bursty per-tick rates (the raw rate×remaining
// estimate wobbles ±5-10s) may not yank the display around, and with a steady true rate the
// display must tick DOWN monotonically (within the convergence wobble of the first ticks).
func TestETASmoothCountdown(t *testing.T) {
	var s RunStats
	total := 2000
	elapsed := time.Duration(0)
	shapes := 0
	// Alternate slow/fast ticks around a 10ms/shape true rate: 25 shapes per tick,
	// tick durations 150ms / 350ms (inst rate 6ms vs 14ms — a ±40% burst pattern).
	var prev time.Duration
	var maxJumpUp time.Duration
	for i := 0; i < 60; i++ {
		dt := 150 * time.Millisecond
		if i%2 == 1 {
			dt = 350 * time.Millisecond
		}
		elapsed += dt
		shapes += 25
		s.UpdateETA(shapes, total, elapsed)
		if i > 5 && prev > 0 {
			if up := s.ETA - prev; up > maxJumpUp {
				maxJumpUp = up
			}
		}
		prev = s.ETA
	}
	// Raw estimates differ by seconds between adjacent ticks; the display may creep up only a
	// fraction of that while converging.
	if maxJumpUp > 900*time.Millisecond {
		t.Errorf("displayed ETA jumped up by %v in one tick, want ≤0.9s (smooth countdown)", maxJumpUp)
	}
	// Sanity: after convergence the ETA is near the true remaining time (10ms/shape × remaining).
	trueETA := time.Duration(float64(total-shapes) * 0.01 * float64(time.Second))
	if d := s.ETA - trueETA; math.Abs(d.Seconds()) > 2 {
		t.Errorf("converged ETA %v vs true %v (off by %v)", s.ETA, trueETA, d)
	}

	// A genuine 2× slowdown: the display must keep tracking the true remaining time (the raw
	// estimate rises as the rate EMA catches up; the countdown may lag but not by much).
	for i := 0; i < 15; i++ {
		elapsed += 500 * time.Millisecond // 20ms/shape now
		shapes += 25
		s.UpdateETA(shapes, total, elapsed)
	}
	trueETA = time.Duration(float64(total-shapes) * 0.02 * float64(time.Second))
	if d := s.ETA - trueETA; math.Abs(d.Seconds()) > 2 {
		t.Errorf("post-slowdown ETA %v vs true %v (off by %v)", s.ETA, trueETA, d)
	}
}
