package engine

import "sort"

// Saliency quota (Options.SaliencyQuota, opt-in): the reserved tail of the shape budget samples
// ONLY inside the most detailed target cells. The persistent-error experiment proved a sampling
// BIAS cannot move the per-shape argmax (it only buys candidates; the pick stays a global argmax),
// so the quota zeroes everything else instead — within the masked grid the argmax IS the salient
// region. The accept gate, knee and progress stay on the raw grid.

// salientFrac is the detail-mass quantile that counts as salient: cells in the top fraction by
// detail value. ~15% concentrates on eyes/faces/linework without collapsing to a single cell.
const salientFrac = 0.15

// salientMask lazily builds the boolean cell mask from the detail grid's value quantile.
func (r *run) salientMask() []bool {
	if r.salient != nil {
		return r.salient
	}
	vals := make([]float64, 0, len(r.detailGrid))
	for _, v := range r.detailGrid {
		if v > 0 {
			vals = append(vals, float64(v))
		}
	}
	r.salient = make([]bool, len(r.detailGrid))
	if len(vals) == 0 {
		return r.salient
	}
	sort.Float64s(vals)
	thr := vals[int(float64(len(vals))*(1-salientFrac))]
	for i, v := range r.detailGrid {
		r.salient[i] = float64(v) >= thr && v > 0
	}
	return r.salient
}

// applySalient returns sampGrid with non-salient cells zeroed (fresh slice; inputs untouched).
// Falls back to the unmasked grid if the salient region carries no residual error (nothing left
// to fix there — the quota would otherwise sample uniformly at random).
func (r *run) applySalient(sampGrid []float32) []float32 {
	mask := r.salientMask()
	out := make([]float32, len(sampGrid))
	var mass float64
	for i, v := range sampGrid {
		if mask[i] && v > 0 {
			out[i] = v
			mass += float64(v)
		}
	}
	if mass <= 1e-9 {
		return sampGrid
	}
	return out
}
