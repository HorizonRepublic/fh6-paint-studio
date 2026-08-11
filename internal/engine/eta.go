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
// usually means progress has stalled inside a phase while the clock keeps running, and following it
// makes the countdown climb — so it is resisted. A falling one means work genuinely finished, and a
// countdown that lingers above the truth is the more annoying error — so it is followed quickly.
const (
	etaSmoothUp   = 0.12
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

	etaEMA float64 // smoothed estimate; the raw one steps whenever a phase boundary lands

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
func passWeight(p pass, opt Options) float64 {
	polish := 18.0
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
		// Each round prunes, regrows and re-polishes, but the re-polish is a SHORT one from an
		// already-converged stack — measured at about a quarter of the main polish per round, not a
		// half. Overstating it here was worth a systematic 50% overestimate in the countdown.
		return float64(opt.LooRefit) * (polish/4 + 1)
	case globalColorPass:
		return 22 * float64(opt.GlobalColorIters) / 100
	case annealPass:
		return float64(opt.AnnealIters) * polish / 10
	case artifactFixPass, zswapPass, softSwapPass, standoutPass:
		return 2
	}
	return 1
}

// newETA plans the run: the greedy loop, then every pass that is actually enabled. A pass that will
// not run must not hold weight, or the bar stalls at the end waiting for work that never comes.
func newETA(opt Options, start time.Time, emit func(PhaseProgress)) *etaTracker {
	if emit == nil {
		return nil
	}
	t := &etaTracker{start: start, emit: emit}
	if opt.SmoothBase {
		t.phases = append(t.phases, etaPhase{"Claiming smooth regions…", 3})
	}
	if opt.ShadePrepass {
		t.phases = append(t.phases, etaPhase{"Claiming shading…", 1})
	}
	if opt.GlyphPrepass {
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
	t.last = now
	w := t.doneW
	name := ""
	frac := t.frac
	if t.idx < len(t.phases) {
		// Whichever says more: the phase's own counter, or the share of its expected duration that
		// has passed. The time term is capped below 1 — only the phase actually ending may claim the
		// last of it, or the bar would sit full while work continues.
		if t.phaseExpect > 0 {
			if tf := float64(now.Sub(t.phaseStart)) / float64(t.phaseExpect); tf > frac {
				frac = math.Min(tf, etaPhaseTimeCap)
			}
		}
		w += frac * t.phases[t.idx].weight
		name = t.phases[t.idx].name
	}
	done := w / t.total
	elapsed := now.Sub(t.start)
	var eta time.Duration
	if done > 0.01 && elapsed >= etaMinElapsed {
		raw := float64(elapsed) * (1 - done) / done
		// Smoothed: a phase boundary steps the estimate, and the run's own pace varies. Following the
		// raw value makes the number jitter; an average that still converges within a few updates
		// reads as a countdown rather than a guess being retaken.
		switch {
		case t.etaEMA <= 0, raw < t.etaEMA*0.5:
			// Either the first reading, or the smoothed one is stale by more than half — which is what
			// the end of a phase looks like. Smoothing a number that is now plainly wrong just keeps
			// showing the wrong number.
			t.etaEMA = raw
		case raw < t.etaEMA:
			t.etaEMA += etaSmoothDown * (raw - t.etaEMA)
		default:
			t.etaEMA += etaSmoothUp * (raw - t.etaEMA)
		}
		eta = time.Duration(t.etaEMA)
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
