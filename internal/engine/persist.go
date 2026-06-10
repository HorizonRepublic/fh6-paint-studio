package engine

// Persistent-error sampling — opt-in EXPERIMENT (Options.PersistGain > 0), the Image-GS idea:
// spawn primitives where the error PERSISTS, not just where it is high right now. The instantaneous
// error grid can't tell a cell ten shapes failed to close from a fresh one, so a small high-error
// detail (a saturated iris on a busy face) loses the importance lottery to big soft regions forever.
// Track per-cell stagnation across grid refreshes (one per accepted shape) and multiply the SAMPLING
// weight — never the error itself: the accept gate, knee and progress all stay on the raw grid.
//
//	weight(cell) = err(cell) · (1 + gain · min(stagnation, persistCap))
//
// stagnation rises by 1 each refresh the cell's error fails to drop ≥1% and HALVES when something
// finally improves it (smooth release, no oscillation). Both the host sampler and the device search
// consume the weighted grid (it feeds sampGrid), so the GPU path gets the bias with zero kernel work.

const (
	persistDropEps = 0.99 // a refresh counts as progress when err < last·0.99 (1% drop)
	persistCap     = 16   // stagnation saturates: max upweight = 1 + gain·16
)

type persistCtx struct {
	gain  float32
	last  []float32 // cell error at the previous refresh
	stag  []float32 // per-cell stagnation counter
	wgrid []float32 // weighted-grid scratch (reused)
}

func newPersistCtx(gain float64, cells int) *persistCtx {
	return &persistCtx{
		gain: float32(gain),
		last: make([]float32, cells), stag: make([]float32, cells), wgrid: make([]float32, cells),
	}
}

// update advances the stagnation counters against a fresh raw error grid.
func (p *persistCtx) update(grid []float32) {
	for i, e := range grid {
		if e <= 0 {
			p.stag[i] = 0
		} else if e < p.last[i]*persistDropEps {
			p.stag[i] *= 0.5
		} else if p.stag[i] < persistCap {
			p.stag[i]++
		}
		p.last[i] = e
	}
}

// apply returns the sampling grid with the persistence upweight folded in. src may already carry
// the detail-grid blend; the raw error grid stays untouched.
func (p *persistCtx) apply(src []float32) []float32 {
	for i, e := range src {
		p.wgrid[i] = e * (1 + p.gain*p.stag[i])
	}
	return p.wgrid
}
