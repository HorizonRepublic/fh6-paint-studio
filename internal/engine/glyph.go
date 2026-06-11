package engine

import (
	"math"
	"sync"

	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"
)

// The glyph proposer (Options.GlyphDict, default off) offers the dictionary's mask words as
// candidates in the greedy per-shape search: one residual blob is moment-fitted (the same fit the
// NEXTGEN seed uses) and every bank word is placed onto it by aligning moment ellipses — centroid on
// centroid, principal axes scaled and rotated, mirrored and 180°-flipped variants. The exact
// backend ΔSSE then competes against the primitive winner, so a glyph is only ever accepted when it
// genuinely beats ellipse/rect/triangle on the same residual. Geometry stays polish-frozen
// (colour-only), matching the mask render contract.

// deviceMaskEvaluator gates the proposer to backends that can actually score mask kinds.
type deviceMaskEvaluator interface{ MasksOnDevice() bool }

type glyphWord struct {
	kind   model.ShapeKind
	cu, cv float64 // coverage centroid in UV
	ru, rv float64 // moment semi-axes in UV units (same momentSemiAxis convention as the blob fit)
	th     float64 // principal angle (deg, y-down)
}

var (
	glyphOnce  sync.Once
	glyphCache []glyphWord
)

func glyphBank() []glyphWord {
	glyphOnce.Do(func() {
		for _, e := range maskbank.All() {
			cx, cy, rx, ry, th, ok := momentEllipse(e.Cov, e.W, e.H)
			if !ok {
				continue
			}
			glyphCache = append(glyphCache, glyphWord{
				kind: e.Kind,
				cu:   float64(cx) / float64(e.W), cv: float64(cy) / float64(e.H),
				ru: math.Max(1e-4, float64(rx)/float64(e.W)), rv: math.Max(1e-4, float64(ry)/float64(e.H)),
				th: float64(th),
			})
		}
	})
	return glyphCache
}

// glyphPropose fits one residual blob and scores every bank word against it. ok=false when the
// blob fit fails or no candidate evaluates.
func (r *run) glyphPropose(progress float32, sampGrid []float32, penalty func(model.Candidate) float32) (model.Candidate, float32, bool) {
	words := glyphBank()
	if len(words) == 0 {
		return model.Candidate{}, 0, false
	}
	w, h := r.w, r.h
	px, py := r.sampler.Sample(r.rng)
	cR := annealMaxR(w, h, progress)
	bcx, bcy, brx, bry, bth, ok := momentSeedFromGrid(sampGrid, r.gw, r.gh, w, h,
		clampF(px, 0, float32(w-1)), clampF(py, 0, float32(h-1)), cR)
	if !ok {
		return model.Candidate{}, 0, false
	}
	alpha := float32(1)
	if r.allowAlpha {
		alpha = maxF(0.85, r.alphaMin)
	}
	maxExt := float64(cR) * 3
	cands := make([]model.Candidate, 0, len(words)*4)
	for _, g := range words {
		// Principal-axis alignment is approximate (the placement affine scales along the mask's UV
		// axes, not its principal frame): assign blob major/minor to the nearer UV axis, rotate the
		// rest. The exact eval + the hill-climb refine absorb the residual mismatch.
		var hx, hy float64
		if math.Abs(g.th) <= 45 {
			hx, hy = float64(brx)/g.ru, float64(bry)/g.rv
		} else {
			hx, hy = float64(bry)/g.ru, float64(brx)/g.rv
		}
		if hx < 2 || hy < 2 || hx > maxExt || hy > maxExt {
			continue
		}
		for _, m := range [2]float64{1, -1} {
			rot := float64(bth) - g.th
			if m < 0 {
				rot = float64(bth) + g.th
			}
			for _, extra := range [2]float64{0, 180} {
				rad := (rot + extra) * math.Pi / 180
				c, s := math.Cos(rad), math.Sin(rad)
				du := (g.cu - 0.5) * hx * m
				dv := (g.cv - 0.5) * hy
				cands = append(cands, model.Candidate{
					Kind:  g.kind,
					Color: model.RGBA{A: alpha},
					P: [6]float32{
						float32(float64(bcx) - (c*du - s*dv)),
						float32(float64(bcy) - (s*du + c*dv)),
						float32(m * hx), float32(hy), float32(rot + extra), 0,
					},
				})
			}
		}
	}
	if len(cands) == 0 {
		return model.Candidate{}, 0, false
	}
	best, score := pickBest(r.be, cands, penalty)
	return best, score, true
}
