package engine

import (
	"os"
	"sort"
	"strconv"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// Batch placement. The greedy scores ~50k candidates to place ONE shape, and the coarse filter
// already re-scores its kpart survivors at the full sample budget — so every round throws away a
// fully-priced pool to keep a single winner.
//
// Under alpha-over in linear light two candidates whose bounding boxes do not intersect have
// EXACTLY additive ΔSSE: each touches only the pixels inside its own box, and each one's colour
// was solved from the canvas under that box. Placing both therefore lowers the error by exactly
// the sum of their scores — this is not an approximation that needs a gate, it is an identity.
// So a maximal independent set over the survivor pool places several shapes for the price of one
// search, and the win concentrates in the late rounds, which dominate the shape count.
//
// What the extras do NOT get is the hill-climb refinement the round winner gets, so they are
// slightly cheaper shapes. batchGain keeps that honest by only accepting an extra whose gain is a
// real fraction of the winner's.
type batchSearcher interface {
	SetBatch(on bool)
	SearchSurvivors(cands []model.Candidate, raw, adj []float32) int
}

// batchDefaultGain: an extra must recover at least this fraction of the round winner's gain.
// Below it the shape is a marginal one the next round would have found anyway, and taking it now
// only spends budget earlier.
const batchDefaultGain = 0.5

// batchEnv is the per-run batch state, nil when batch placement is off.
type batchEnv struct {
	be     batchSearcher
	k      int     // max shapes per round INCLUDING the round winner
	gain   float32 // fraction of the winner's gain an extra must clear
	refine bool    // FH6_BATCHMUT=1: hill-climb the extras too
	cands  []model.Candidate
	raw    []float32
	adj    []float32
	boxes  []box // accepted boxes this round
	out    []model.Candidate
	score  []float32
}

type box struct{ x0, y0, x1, y1 int }

func (a box) hits(b box) bool {
	return a.x0 <= b.x1 && b.x0 <= a.x1 && a.y0 <= b.y1 && b.y0 <= a.y1
}

// batchKFromEnv resolves the knob. FH6_BATCHK=<n> sets the per-round cap (1 or unset = off, the
// shipped single-placement greedy); FH6_BATCHGAIN=<f> overrides the acceptance fraction.
func batchKFromEnv() (int, float32) {
	k := 0
	if v := os.Getenv("FH6_BATCHK"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			k = n
		}
	}
	g := float32(batchDefaultGain)
	if v := os.Getenv("FH6_BATCHGAIN"); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil && f >= 0 {
			g = float32(f)
		}
	}
	return k, g
}

// newBatchEnv arms the backend's survivor export and sizes the scratch. pool is the survivor
// count the coarse filter keeps (CoarseK); a backend without the export returns nil.
func newBatchEnv(be interface{}, k, pool int, gain float32) *batchEnv {
	if k < 2 || pool < 2 {
		return nil
	}
	bs, ok := be.(batchSearcher)
	if !ok {
		return nil
	}
	bs.SetBatch(true)
	return &batchEnv{
		be:     bs,
		k:      k,
		gain:   gain,
		refine: os.Getenv("FH6_BATCHMUT") == "1",
		cands:  make([]model.Candidate, pool),
		raw:    make([]float32, pool),
		adj:    make([]float32, pool),
		boxes:  make([]box, 0, k),
		out:    make([]model.Candidate, 0, k),
		score:  make([]float32, 0, k),
	}
}

// batchApply places the round winner and its extras. ApplyBatch is one submit for the whole set;
// a backend without it falls back to the same sequence of single applies.
func (r *run) batchApply(winner model.Candidate, extras []model.Candidate) {
	all := make([]model.Candidate, 0, len(extras)+1)
	all = append(all, winner)
	all = append(all, extras...)
	if ab, ok := r.be.(interface{ ApplyBatch([]model.Candidate) bool }); ok {
		if ab.ApplyBatch(all) {
			return
		}
	}
	for _, c := range all {
		_ = r.be.Apply(c)
	}
}

func (b *batchEnv) close() {
	if b != nil && b.be != nil {
		b.be.SetBatch(false)
	}
}

// extras picks the additional winners for this round: survivors whose box misses the winner's box
// and every already-accepted one, ranked by the same adjusted score the device's argmin used.
// winner must be the shape actually being placed (post hill-climb) — its box is what the extras
// are tested against, so a mutation that moved it is accounted for.
//
// minGain mirrors the loop's own low-contrast gate (Options.MinShapeGain); 0 disables it.
func (b *batchEnv) extras(winner model.Candidate, winnerScore float32, w, h int, minGain float64) ([]model.Candidate, []float32) {
	b.out = b.out[:0]
	b.score = b.score[:0]
	n := b.be.SearchSurvivors(b.cands, b.raw, b.adj)
	if n < 2 {
		return nil, nil
	}
	b.boxes = b.boxes[:0]
	x0, y0, x1, y1 := raster.BBox(winner.Kind, winner.P, w, h)
	b.boxes = append(b.boxes, box{x0, y0, x1, y1})

	// Only the strongest handful can survive the disjointness test anyway, so rank a bounded
	// shortlist instead of sorting the whole pool once per placed shape.
	limit := 8 * b.k
	if limit > n {
		limit = n
	}
	idx := make([]int, 0, n)
	cut := winnerScore * b.gain // both negative: an extra must be <= this to qualify
	for i := 0; i < n; i++ {
		if b.raw[i] < 0 && b.raw[i] <= cut && b.adj[i] < 0 {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return nil, nil
	}
	sort.Slice(idx, func(p, q int) bool {
		if b.adj[idx[p]] != b.adj[idx[q]] {
			return b.adj[idx[p]] < b.adj[idx[q]]
		}
		return idx[p] < idx[q]
	})
	if len(idx) > limit {
		idx = idx[:limit]
	}
	for _, i := range idx {
		if len(b.out)+1 >= b.k {
			break
		}
		c := b.cands[i]
		if model.IsMask(c.Kind) {
			continue // words are not scored by the device eval; never trust their slot here
		}
		if minGain > 0 {
			if area := candidateArea(c, w, h); area > 0 && -float64(b.raw[i])/area < minGain {
				continue
			}
		}
		bx0, by0, bx1, by1 := raster.BBox(c.Kind, c.P, w, h)
		if bx1 < bx0 || by1 < by0 {
			continue
		}
		nb := box{bx0, by0, bx1, by1}
		clash := false
		for _, ob := range b.boxes {
			if nb.hits(ob) {
				clash = true
				break
			}
		}
		if clash {
			continue
		}
		b.boxes = append(b.boxes, nb)
		b.out = append(b.out, c)
		b.score = append(b.score, b.raw[i])
	}
	return b.out, b.score
}

// refineExtras runs the same on-device hill climb the round winner gets on each extra. A mutated
// shape can move out of its old box, so anything that would now overlap an accepted box keeps its
// unrefined form — the additive-ΔSSE argument only holds while the boxes stay disjoint.
// FH6_BATCHMUT=1; the point of measuring it is whether the extras' quality gap IS the missing
// hill climb, since refining them gives back the mutate time the batch had saved.
func (r *run) refineExtras(extras []model.Candidate, scores []float32) {
	if r.devMutate == nil || r.rounds < 1 {
		return
	}
	for i := range extras {
		if model.IsMask(extras[i].Kind) {
			continue
		}
		mb, ms, ok := r.devMutate.SearchMutate(r.rng.Int63(), extras[i], scores[i], r.rounds, r.perRound,
			r.moveStep, r.radiusStep, r.allowAlpha, r.alphaMin, r.opt.CompactPenalty, len(r.shapes)-1, r.opt.CanvasPad)
		if !ok {
			r.devMutate = nil
			return
		}
		if ms >= scores[i] {
			continue
		}
		x0, y0, x1, y1 := raster.BBox(mb.Kind, mb.P, r.w, r.h)
		nb := box{x0, y0, x1, y1}
		clash := false
		for j, ob := range r.batch.boxes {
			if j != i+1 && nb.hits(ob) {
				clash = true
				break
			}
		}
		if clash {
			continue
		}
		r.batch.boxes[i+1] = nb
		extras[i], scores[i] = mb, ms
	}
}
