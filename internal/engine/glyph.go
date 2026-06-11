package engine

import (
	"math"
	"sort"
	"sync"

	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"
)

// The glyph proposer (Options.GlyphDict, default off) offers the dictionary's mask words as
// candidates in the greedy per-shape search. One residual blob is sampled per step and described by
// a rotation-aware radial signature (angular bins of mass + mean radius); every bank word carries
// the same signature, so matching is a circular-shift comparison that yields the best word, its
// rotation AND its mirror in one pass — shape-aware, unlike a bare moment fit. The top matches are
// placed (centroid on centroid, RMS-radius scale, rotation from the matched shift) and the exact
// backend ΔSSE competes against the primitive winner, so a glyph is only accepted when it genuinely
// beats ellipse/rect/triangle on the same residual. Geometry stays polish-frozen (colour-only).

// deviceMaskEvaluator gates the proposer to backends that can actually score mask kinds.
type deviceMaskEvaluator interface{ MasksOnDevice() bool }

const (
	glyphBins = 24 // angular bins of the radial signature (15° each)
	glyphTopK = 12 // words evaluated per step (3 rotation variants each)
)

// glyphSig is the rotation-aware shape signature: per angular bin the mass fraction and the
// mass-weighted mean radius (normalized by the overall RMS radius).
type glyphSig struct {
	mass [glyphBins]float64
	rad  [glyphBins]float64
	rms  float64 // RMS radius in source units (px for blobs, native units for words)
}

type glyphWord struct {
	kind             model.ShapeKind
	nativeW, nativeH float64
	cx, cy           float64     // coverage centroid in native units (origin = mask centre)
	sig              [2]glyphSig // 0 = as stored, 1 = x-mirrored
}

var (
	glyphOnce  sync.Once
	glyphCache []glyphWord
)

// buildSig accumulates the signature from weighted points (already centred).
type sigAcc struct {
	m   [glyphBins]float64
	mr  [glyphBins]float64
	mr2 float64
	mt  float64
}

func (a *sigAcc) add(x, y, w float64) {
	r := math.Hypot(x, y)
	th := math.Atan2(y, x) // [-π, π]
	b := int((th + math.Pi) / (2 * math.Pi) * glyphBins)
	if b >= glyphBins {
		b = glyphBins - 1
	}
	if b < 0 {
		b = 0
	}
	a.m[b] += w
	a.mr[b] += w * r
	a.mr2 += w * r * r
	a.mt += w
}

func (a *sigAcc) sig() (glyphSig, bool) {
	var s glyphSig
	if a.mt <= 1e-9 || a.mr2 <= 1e-12 {
		return s, false
	}
	s.rms = math.Sqrt(a.mr2 / a.mt)
	for b := 0; b < glyphBins; b++ {
		s.mass[b] = a.m[b] / a.mt
		if a.m[b] > 1e-12 {
			s.rad[b] = a.mr[b] / a.m[b] / s.rms
		}
	}
	return s, true
}

func glyphBank() []glyphWord {
	glyphOnce.Do(func() {
		for _, e := range maskbank.All() {
			// centroid in native units (origin at mask centre, y-down like the raster)
			var mt, mx, my float64
			for y := 0; y < e.H; y++ {
				for x := 0; x < e.W; x++ {
					w := float64(e.Cov[y*e.W+x])
					if w <= 0 {
						continue
					}
					px := (float64(x)+0.5)/float64(e.W) - 0.5
					py := (float64(y)+0.5)/float64(e.H) - 0.5
					mt += w
					mx += w * px * float64(e.NativeW)
					my += w * py * float64(e.NativeH)
				}
			}
			if mt <= 1e-9 {
				continue
			}
			cx, cy := mx/mt, my/mt
			var acc [2]sigAcc
			for y := 0; y < e.H; y++ {
				for x := 0; x < e.W; x++ {
					w := float64(e.Cov[y*e.W+x])
					if w <= 0 {
						continue
					}
					px := ((float64(x)+0.5)/float64(e.W) - 0.5) * float64(e.NativeW)
					py := ((float64(y)+0.5)/float64(e.H) - 0.5) * float64(e.NativeH)
					acc[0].add(px-cx, py-cy, w)
					acc[1].add(-(px - cx), py-cy, w)
				}
			}
			s0, ok0 := acc[0].sig()
			s1, ok1 := acc[1].sig()
			if !ok0 || !ok1 {
				continue
			}
			glyphCache = append(glyphCache, glyphWord{
				kind: e.Kind, nativeW: float64(e.NativeW), nativeH: float64(e.NativeH),
				cx: cx, cy: cy, sig: [2]glyphSig{s0, s1},
			})
		}
	})
	return glyphCache
}

// sigDist is the signature distance at a given circular shift: mass mismatch + radius mismatch
// weighted by the shared mass.
func sigDist(a, b *glyphSig, shift int) float64 {
	var d float64
	for i := 0; i < glyphBins; i++ {
		j := (i + shift) % glyphBins
		dm := a.mass[i] - b.mass[j]
		dr := a.rad[i] - b.rad[j]
		d += dm*dm + 0.5*dr*dr*(a.mass[i]+b.mass[j])/2
	}
	return d
}

// glyphPropose samples one residual blob, signature-matches the dictionary against it, and exact-
// scores the best placements. ok=false when the blob is degenerate or no candidate evaluates.
func (r *run) glyphPropose(progress float32, sampGrid []float32, penalty func(model.Candidate) float32) (model.Candidate, float32, bool) {
	words := glyphBank()
	if len(words) == 0 {
		return model.Candidate{}, 0, false
	}
	w, h := r.w, r.h
	px, py := r.sampler.Sample(r.rng)
	px = clampF(px, 0, float32(w-1))
	py = clampF(py, 0, float32(h-1))
	cR := annealMaxR(w, h, progress)

	// blob signature from the error-grid cells within cR of the sample point
	sx := float64(w) / float64(r.gw)
	sy := float64(h) / float64(r.gh)
	gx0 := int((float64(px) - float64(cR)) / sx)
	gx1 := int((float64(px) + float64(cR)) / sx)
	gy0 := int((float64(py) - float64(cR)) / sy)
	gy1 := int((float64(py) + float64(cR)) / sy)
	if gx0 < 0 {
		gx0 = 0
	}
	if gy0 < 0 {
		gy0 = 0
	}
	if gx1 > r.gw-1 {
		gx1 = r.gw - 1
	}
	if gy1 > r.gh-1 {
		gy1 = r.gh - 1
	}
	var mt, mx, my float64
	for gy := gy0; gy <= gy1; gy++ {
		for gx := gx0; gx <= gx1; gx++ {
			wv := float64(sampGrid[gy*r.gw+gx])
			if wv <= 0 {
				continue
			}
			mt += wv
			mx += wv * (float64(gx) + 0.5) * sx
			my += wv * (float64(gy) + 0.5) * sy
		}
	}
	if mt <= 1e-9 {
		return model.Candidate{}, 0, false
	}
	bcx, bcy := mx/mt, my/mt
	var acc sigAcc
	for gy := gy0; gy <= gy1; gy++ {
		for gx := gx0; gx <= gx1; gx++ {
			wv := float64(sampGrid[gy*r.gw+gx])
			if wv <= 0 {
				continue
			}
			acc.add((float64(gx)+0.5)*sx-bcx, (float64(gy)+0.5)*sy-bcy, wv)
		}
	}
	blob, ok := acc.sig()
	if !ok || blob.rms < 2 {
		return model.Candidate{}, 0, false
	}

	// match every word: best (shift, mirror) per word, then take the global top-K
	type match struct {
		wi, shift, mirror int
		d                 float64
	}
	best := make([]match, 0, len(words))
	for wi := range words {
		m := match{wi: wi, d: math.Inf(1)}
		for mir := 0; mir < 2; mir++ {
			sg := &words[wi].sig[mir]
			for sh := 0; sh < glyphBins; sh++ {
				if d := sigDist(&blob, sg, sh); d < m.d {
					m.d, m.shift, m.mirror = d, sh, mir
				}
			}
		}
		best = append(best, m)
	}
	sort.Slice(best, func(a, b int) bool { return best[a].d < best[b].d })

	alpha := float32(1)
	if r.allowAlpha {
		alpha = maxF(0.85, r.alphaMin)
	}
	maxExt := float64(cR) * 3
	cands := make([]model.Candidate, 0, glyphTopK*3)
	for k := 0; k < glyphTopK && k < len(best); k++ {
		m := best[k]
		gw := &words[m.wi]
		s := blob.rms / gw.sig[m.mirror].rms
		hx, hy := s*gw.nativeW, s*gw.nativeH
		if hx < 2 || hy < 2 || hx > maxExt || hy > maxExt {
			continue
		}
		mir := 1.0
		ccx := gw.cx
		if m.mirror == 1 {
			mir = -1
			ccx = -gw.cx
		}
		for _, dsh := range [3]int{0, -1, 1} {
			// sigDist matches blob bin i against word bin i+shift, so a blob rotated by θ
			// is found at shift = -θ·bins/360: the placement rotation negates the shift
			sh := ((glyphBins-(m.shift+dsh))%glyphBins + glyphBins) % glyphBins
			rot := float64(sh) * (360.0 / glyphBins)
			rad := rot * math.Pi / 180
			c, sn := math.Cos(rad), math.Sin(rad)
			ox, oy := s*ccx, s*gw.cy
			cands = append(cands, model.Candidate{
				Kind:  gw.kind,
				Color: model.RGBA{A: alpha},
				P: [6]float32{
					float32(bcx - (c*ox - sn*oy)), float32(bcy - (sn*ox + c*oy)),
					float32(mir * hx), float32(hy), float32(rot), 0,
				},
			})
		}
	}
	if len(cands) == 0 {
		return model.Candidate{}, 0, false
	}
	bestC, score := pickBest(r.be, cands, penalty)
	return bestC, score, true
}
