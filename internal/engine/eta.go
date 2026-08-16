package engine

import (
	"math"
	"sync"
	"time"
)

// Run-wide progress with a time estimate.
//
// The greedy loop could always be estimated — it places a known number of shapes, so shapes/second
// gives the remainder. Everything after it could not: the polish and the post-passes reported a name
// and nothing else, which is most of the wall time on a photo run and all of the time the progress
// bar looked frozen.
//
// The estimate deliberately does NOT carry per-machine constants. Phase weights below are a PRIOR for
// how the work divides, measured on the bench; the seconds come from THIS run's own elapsed time
// (eta = elapsed·(1−done)/done). A card half as fast makes every phase twice as slow, elapsed grows
// with it, and the estimate follows on its own — nothing to calibrate, nothing to ship per GPU. The
// prior only has to be roughly right about the SHAPE of the split, and it self-corrects as phases
// complete: by the time the greedy is done its real cost is known, and the remainder is rescaled.
type PhaseProgress struct {
	Phase     string        // human name of the running phase
	PhaseFrac float64       // 0..1 within the phase (0 when the phase reports no progress of its own)
	Overall   float64       // 0..1 across the whole run
	ETA       time.Duration // estimated time remaining; 0 while there is not enough signal yet
}

// etaMinElapsed is how much of a run must have passed before an estimate is worth showing. The first
// second is dominated by setup and the largest shapes, so extrapolating from it produces a wild
// number that then visibly collapses — worse than showing nothing.
const etaMinElapsed = 1500 * time.Millisecond

// etaEmitEvery throttles emission; a phase that reports per-iteration would otherwise fire thousands
// of events at the UI.
const etaEmitEvery = 250 * time.Millisecond

// etaPhaseTimeCap bounds how far the time interpolation may carry a phase on its own. Only the phase
// ending is allowed to complete it: a bar that reaches the end of a step while the step is still
// running is a worse lie than one that is slightly behind.
const etaPhaseTimeCap = 0.95

// Smoothing is ASYMMETRIC, because the two directions mean different things. A rising raw estimate
// usually means progress has stalled inside a phase while the clock keeps running — the deadline is
// allowed to slip, but never as fast as the clock runs, so the countdown keeps visibly ticking (at
// worst at half speed) instead of freezing on one number. A falling one means work genuinely
// finished, and a countdown that lingers above the truth is the more annoying error — so it is
// followed quickly.
const (
	etaSlipFrac   = 0.5 // max deadline slip per publish, as a fraction of wall time since the last
	etaSmoothDown = 0.50
)

type etaPhase struct {
	name   string
	weight float64
}

type etaTracker struct {
	mu     sync.Mutex
	phases []etaPhase
	total  float64
	idx    int     // index of the running phase, len(phases) once finished
	doneW  float64 // summed weight of completed phases
	frac   float64 // progress inside the running phase
	start  time.Time
	last   time.Time
	emit   func(PhaseProgress)
	done   chan struct{}
	seq    uint64 // snapshot order, assigned under mu

	// Per-phase time fallback. A phase's own counter is not always a clean 0..1 ramp: the polish
	// extends its budget mid-flight (the fine phase raises the denominator), stops early when it
	// converges, and runs several sweeps inside one phase (two back-fit branches, a re-polish per LOO
	// round). Progress may only move forward, so a restarted counter leaves the bar STOPPED until it
	// climbs past its old mark — and a stalled bar with a live clock makes the estimate creep upward
	// and then collapse. Interpolating the phase over its expected duration keeps it moving.
	phaseStart  time.Time
	phaseExpect time.Duration

	// The countdown is a DEADLINE, not a smoothed duration. Re-deriving a duration every tick
	// froze the number whenever a phase stalled: raw = elapsed·(1−done)/done grows exactly as fast
	// as the clock at constant done, so the display neither fell nor rose — "the timer does not
	// tick". A deadline ticks down by itself between updates; new readings only MOVE it.
	etaDeadline time.Time

	// emitMu serialises delivery. The snapshot is taken under mu and handed off OUTSIDE it, so the
	// callback (a UI pump, an IPC write) can never stall the engine's hot loop behind the heartbeat.
	// The cost of that is two goroutines racing to deliver: without ordering here, an older snapshot
	// can land after a newer one and the bar visibly jumps backwards.
	emitMu  sync.Mutex
	emitted uint64
}

// greedyWeight is the shape-placing loop, which dominates a default run.
const greedyWeight = 60

// passWeight is the prior cost of one post-greedy pass RELATIVE to the greedy loop, as a wall-clock
// segment — a pass that re-polishes internally is credited with that re-polish here, because the
// progress bar cares about elapsed time, not about which bucket the time is billed to.
// The constants below were re-measured 2026-08-16 AFTER the perf campaign, which sped the phases
// up UNEVENLY (evaluate −80%, polish −55%, skewrefine −45%…) and left the old prior parking the
// bar in the low eighties and then jumping to 100. Bench: img_10 anime @2000/1000 — greedy 3.9s,
// main polish 1.4s, LOO refit incl. its warm re-polishes 6.5s (a re-polish now costs ≈ the main
// polish, ~2 re-polishes per configured round), globalcolor 1.9s, skewrefine 1.8s, smoothbase 1.0s.
func passWeight(p pass, opt Options) float64 {
	polish := 27.0
	if opt.PolishOpts.Iters > 0 {
		polish *= float64(opt.PolishOpts.Iters) / 250
	}
	switch p.(type) {
	case backfitPolishPass:
		return 8 + 2*polish // polishes both branches
	case backfitPass:
		return 8
	case softSwapPolishPass:
		return polish + 2
	case polishPass:
		return polish
	case looRefitPass:
		// Each configured round prunes, regrows and re-polishes ~twice; a warm re-polish costs
		// about one main polish since the backward slicing (it used to be 0.4×). This pass is the
		// single largest post-greedy segment now — underweighting it stalls the bar mid-run.
		return float64(opt.LooRefit) * (2.3*polish + 1)
	case globalColorPass:
		return 30 * float64(opt.GlobalColorIters) / 100
	case annealPass:
		return float64(opt.AnnealIters) * polish / 10
	case skewRefinePass:
		// A pattern search over every geometry parameter of every shape, repeated in rounds until
		// the returns dry up. Re-measured 2026-08-16: ~11% of the smoke run ≈ half the greedy.
		return 28
	case artifactFixPass, zswapPass, softSwapPass, standoutPass:
		return 2
	}
	return 1
}

// newETA plans the run: the greedy loop, then every pass that is actually enabled. A pass that will
// not run must not hold weight, or the bar stalls at the end waiting for work that never comes.
// masks = the run's own masksReady verdict. The shade and word pre-passes announce themselves only
// when the backend can score mask words, so listing them on the option alone left the plan one
// phase longer than the run — and a plan the run never finishes entering is a bar that never
// reaches 100%.
func newETA(opt Options, masks bool, start time.Time, emit func(PhaseProgress)) *etaTracker {
	if emit == nil {
		return nil
	}
	t := &etaTracker{start: start, emit: emit}
	if opt.SmoothBase {
		t.phases = append(t.phases, etaPhase{"Claiming smooth regions…", 15})
	}
	if opt.ShadePrepass && masks {
		t.phases = append(t.phases, etaPhase{"Claiming shading…", 4})
	}
	if opt.GlyphPrepass && masks {
		t.phases = append(t.phases, etaPhase{"Claiming words…", 1})
	}
	t.phases = append(t.phases, etaPhase{"Placing shapes…", greedyWeight})
	for _, p := range postPasses() {
		if p.enabled(opt) {
			t.phases = append(t.phases, etaPhase{"", passWeight(p, opt)})
		}
	}
	for _, p := range t.phases {
		t.total += p.weight
	}
	if t.total <= 0 {
		t.total = 1
	}
	// Heartbeat. Only the loops with a countable unit report progress; a pass like the colour re-solve
	// runs for seconds in one call and would otherwise leave the estimate frozen at whatever it read
	// when the phase started — the exact complaint the bar had after the greedy. Re-publishing on a
	// timer keeps the countdown moving through every silent pass without threading a callback into
	// each one.
	done := make(chan struct{})
	t.done = done
	go func() {
		tick := time.NewTicker(etaEmitEvery * 2)
		defer tick.Stop()
		for {
			select {
			case <-done: // the captured channel, not t.done: stop() clears the field
				return
			case <-tick.C:
				t.publish(false)
			}
		}
	}()
	return t
}

// stop ends the heartbeat. Safe to call more than once.
func (t *etaTracker) stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done != nil {
		close(t.done)
		t.done = nil
	}
}

// enter advances to the next phase. Passes announce themselves through setStatus, so the tracker
// follows the same call and needs no second wiring; a repeated announcement of the SAME name (a pass
// that reports per round) does not advance.
func (t *etaTracker) enter(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.idx < len(t.phases) && t.phases[t.idx].name == name {
		t.mu.Unlock()
		return
	}
	if t.idx < len(t.phases) {
		t.doneW += t.phases[t.idx].weight
		t.idx++
	}
	if t.idx < len(t.phases) {
		t.phases[t.idx].name = name
	}
	t.frac = 0
	// How long this phase should take, from the pace the run has shown so far: the weights say how
	// the work divides, and the elapsed time says what the division is worth on this machine.
	t.phaseStart = time.Now()
	t.phaseExpect = 0
	if done := t.doneW / t.total; done > 0.05 && t.idx < len(t.phases) {
		totalExpect := float64(t.phaseStart.Sub(t.start)) / done
		t.phaseExpect = time.Duration(totalExpect * t.phases[t.idx].weight / t.total)
	}
	t.mu.Unlock()
	t.publish(true)
}

// setFrac reports progress inside the running phase. Anything with a countable unit of work calls it
// — placed shapes, polish iterations, colour-solve iterations — and the phases that have no such unit
// simply hold at 0 and are carried by their weight alone.
//
// Progress inside a phase only ever moves FORWARD. One phase can contain several sweeps of the same
// counter: the back-fit trio polishes two branches to compare them, and each LOO round re-polishes
// from scratch, so the iteration counter restarts at 1 partway through. Taking that literally sent
// the bar backwards, which is the one thing a progress bar must never do.
func (t *etaTracker) setFrac(f float64) {
	if t == nil {
		return
	}
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	t.mu.Lock()
	if f <= t.frac {
		t.mu.Unlock()
		return
	}
	t.frac = f
	t.mu.Unlock()
	t.publish(false)
}

func (t *etaTracker) publish(force bool) {
	t.mu.Lock()
	now := time.Now()
	if !force && now.Sub(t.last) < etaEmitEvery {
		t.mu.Unlock()
		return
	}
	dt := now.Sub(t.last) // wall time since the previous reading, for the slip limit below
	if dt < 0 || dt > time.Second {
		dt = time.Second
	}
	t.last = now
	w := t.doneW
	name := ""
	frac := t.frac
	if t.idx < len(t.phases) {
		// Whichever says more: the phase's own counter, or the share of its expected duration that
		// has passed. The time term is capped below 1 — only the phase actually ending may claim the
		// last of it, or the bar would sit full while work continues. It may only RAISE the counter:
		// clamping an overrun time term to the cap used to pull a phase that had already counted to
		// 100% back down to 95%.
		if t.phaseExpect > 0 {
			if tf := float64(now.Sub(t.phaseStart)) / float64(t.phaseExpect); tf > frac {
				if c := math.Min(tf, etaPhaseTimeCap); c > frac {
					frac = c
				}
			}
		}
		w += frac * t.phases[t.idx].weight
		name = t.phases[t.idx].name
	}
	done := w / t.total
	elapsed := now.Sub(t.start)
	var eta time.Duration
	if done > 0.01 && elapsed >= etaMinElapsed {
		raw := time.Duration(float64(elapsed) * (1 - done) / done)
		rawEnd := now.Add(raw)
		remaining := t.etaDeadline.Sub(now)
		switch {
		case remaining <= 0 && raw > 2*time.Second:
			// Escape hatch: a deadline that fell into the past can never recover through the
			// branches below (the snap compares against a negative half, and the slip cap moves
			// slower than the clock) — the countdown would show 0:00 for the rest of the run.
			// A substantially non-zero raw estimate means the run is clearly not about to end,
			// so re-anchor on it.
			t.etaDeadline = rawEnd
		case t.etaDeadline.IsZero(), raw < remaining/2:
			// Either the first reading, or the held deadline is stale by more than half — which is
			// what the end of a phase looks like. Smoothing a number that is now plainly wrong just
			// keeps showing the wrong number.
			t.etaDeadline = rawEnd
		case rawEnd.Before(t.etaDeadline):
			// Work finished faster than expected: follow quickly. A countdown that lingers above
			// the truth is the more annoying error.
			t.etaDeadline = t.etaDeadline.Add(time.Duration(etaSmoothDown * float64(rawEnd.Sub(t.etaDeadline))))
		default:
			// The estimate is slipping later — progress stalled while the clock runs. Let the
			// deadline slip by at most a fraction of the wall time since the last reading, so the
			// countdown keeps ticking down (at worst at 1−etaSlipFrac speed) instead of freezing.
			slip := rawEnd.Sub(t.etaDeadline)
			if lim := time.Duration(etaSlipFrac * float64(dt)); slip > lim {
				slip = lim
			}
			t.etaDeadline = t.etaDeadline.Add(slip)
		}
		if eta = t.etaDeadline.Sub(now); eta < 0 {
			eta = 0
		}
	}
	t.seq++
	seq := t.seq
	t.mu.Unlock()
	t.emitMu.Lock()
	defer t.emitMu.Unlock()
	if seq <= t.emitted {
		return // a newer snapshot already went out; this one would move the bar backwards
	}
	t.emitted = seq
	t.emit(PhaseProgress{Phase: name, PhaseFrac: frac, Overall: done, ETA: eta})
}
