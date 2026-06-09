// Package stroke also hosts the "ink" engine: it extracts the drawing's ACTUAL ink lines with XDoG,
// thins them to centerlines, and lays calibrated dictionary arcs + thin rects along them — the
// hand-drawn line layer (vs. the older "stroke" engine that outlines colour-region boundaries).
package stroke

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
	"fh6-paint-studio/internal/stylize/shape"
)

// InkConfig holds the XDoG line-extraction + arc-fitting knobs (eye-tuned).
type InkConfig struct {
	Sigma        float64 `json:"sigma"`      // XDoG base Gaussian (line scale, px)
	K            float64 `json:"k"`          // second-Gaussian ratio (~1.6)
	Tau          float64 `json:"tau"`        // XDoG inhibition (~0.98)
	Eps          float64 `json:"eps"`        // XDoG ink threshold
	Phi          float64 `json:"phi"`        // XDoG edge sharpness
	PreSmooth    float64 `json:"preSmooth"`  // light DT smooth before XDoG (spatial σ; 0=off) — drops fine texture
	MinLen       int     `json:"minLen"`     // drop skeleton branches shorter than this (px)
	LineSmooth   int     `json:"lineSmooth"` // centerline smoothing passes (removes pixel jaggies → arcs fit)
	Simplify     float64 `json:"simplify"`   // polyline simplify tolerance (px)
	Darken       float64 `json:"darken"`     // ink = sampled dark colour × this
	Width        float64 `json:"width"`      // min stroke half-width (px)
	MaxWidth     float64 `json:"maxWidth"`   // max half-width; >Width = vary line weight by local ink thickness (DT)
	Arcs         bool    `json:"arcs"`       // fit dictionary arcs to curved runs
	ArcTol       float64 `json:"arcTol"`
	MinSweep     float64 `json:"minSweep"`
	Budget       int     `json:"budget"`
	Method       string  `json:"method"`       // line extractor: "xdog" (default) or "fdog" (coherent, connected)
	SigmaM       float64 `json:"sigmaM"`       // fdog: along-flow coherence length (px); bigger = longer connected lines
	Thresh       float64 `json:"thresh"`       // fdog: binarization (higher = more/fainter lines)
	ETFIters     int     `json:"etfIters"`     // fdog: edge-tangent-flow refinement passes
	DedupOverlap float64 `json:"dedupOverlap"` // skip a polyline already ≥ this fraction covered by earlier-drawn ink (0=off); frees budget from redundant retraces/parallel branches
	SuppressBg   bool    `json:"suppressBg"`   // skip lines sitting mostly in the border-connected light background (no stray edge marks in the white margin)
	BgLuma       float64 `json:"bgLuma"`       // luma threshold for "background light" (used by SuppressBg)
	StitchTurn   float64 `json:"stitchTurn"`   // through-junction stitch: max bend (deg from straight) a stroke may take crossing a node (0=off); merges fragmented branches into long coherent strokes
}

func inkDefaults() InkConfig {
	return InkConfig{Sigma: 1.0, K: 1.6, Tau: 0.985, Eps: 0.0, Phi: 60, PreSmooth: 6,
		MinLen: 10, LineSmooth: 2, Simplify: 1.6, Darken: 0.3, Width: 1.0, MaxWidth: 2.6,
		Arcs: true, ArcTol: 1.6, MinSweep: 35, Budget: 0,
		Method: "xdog", SigmaM: 3.0, Thresh: 0.5, ETFIters: 3, DedupOverlap: 0,
		SuppressBg: true, BgLuma: 0.86, StitchTurn: 40}
}

type inkEngine struct{ cfg InkConfig }

func (e *inkEngine) Name() string { return "ink" }

func (e *inkEngine) Generate(ctx *stylize.Context) ([]model.Shape, error) {
	src := ctx.Orig
	if src == nil {
		src = ctx.Src
	}
	if e.cfg.PreSmooth > 0 { // light edge-preserving smooth: drop fine texture (hair strands), keep real ink
		src = stylize.Smooth(src, stylize.SmoothConfig{Method: "dt", Spatial: e.cfg.PreSmooth, Range: 0.4, Iters: 2})
	}
	var mask []bool
	if e.cfg.Method == "fdog" {
		fp := defaultFDoG()
		fp.SigmaC, fp.K = e.cfg.Sigma, e.cfg.K
		if e.cfg.SigmaM > 0 {
			fp.SigmaM = e.cfg.SigmaM
		}
		if e.cfg.Thresh > 0 {
			fp.Thresh = e.cfg.Thresh
		}
		if e.cfg.ETFIters > 0 {
			fp.ETFIters = e.cfg.ETFIters
		}
		mask = fdogMask(src, fp)
	} else {
		mask = xdogMask(src, xdogParams{Sigma: e.cfg.Sigma, K: e.cfg.K, Tau: e.cfg.Tau, Eps: e.cfg.Eps, Phi: e.cfg.Phi})
	}
	dt := inkDT(mask, src.W, src.H) // line half-width ≈ distance transform at the centerline
	skel := zhangSuenThin(mask, src.W, src.H)
	var polys [][][2]float64
	rawPolys := 0
	if e.cfg.StitchTurn > 0 {
		// Trace with a tiny floor so the stitcher has the full branch material, bridge near-straight
		// junctions into long coherent strokes, then drop the leftover spurs by MinLen.
		polys = traceSkeleton(skel, src.W, src.H, 2)
		rawPolys = len(polys)
		polys = stitchThroughJunctions(polys, e.cfg.StitchTurn)
		// Drop only tiny specks post-stitch (not the full MinLen): longest-first + the freed budget keep
		// the major strokes first, and the surviving short strokes add fine detail (lashes, mouth corners).
		polys = dropShortPolys(polys, 4)
	} else {
		polys = traceSkeleton(skel, src.W, src.H, e.cfg.MinLen)
		rawPolys = len(polys)
	}
	// Longest lines first so the major contours (eyes, face, big locks) win the budget before wisps.
	sort.Slice(polys, func(a, b int) bool { return polyLen(polys[a]) > polyLen(polys[b]) })
	budget := e.cfg.Budget
	if budget <= 0 || budget > ctx.Budget {
		budget = ctx.Budget
	}
	ink := inkSample(src, e.cfg.Darken)
	scfg := Config{Arcs: e.cfg.Arcs, ArcTol: e.cfg.ArcTol, MinSweep: e.cfg.MinSweep}
	var inked []bool
	if e.cfg.DedupOverlap > 0 {
		inked = make([]bool, src.W*src.H)
	}
	var bg []bool
	if e.cfg.SuppressBg {
		bg = stylize.BackgroundMask(src, e.cfg.BgLuma)
	}
	var shapes []model.Shape
	skipped := 0
	for _, poly := range polys {
		if len(shapes) >= budget {
			break
		}
		if bg != nil && polyInBg(poly, bg, src.W, src.H) > 0.7 {
			continue // a faint edge line in the light background → drop it (clean margin, free budget)
		}
		hw := branchHalfWidth(dt, poly, src.W, e.cfg.Width, e.cfg.MaxWidth)
		sp := simplify(smoothPolyline(poly, e.cfg.LineSmooth), e.cfg.Simplify)
		if len(sp) < 2 {
			continue
		}
		// Coverage dedup: a polyline already mostly painted by earlier (longer) lines is a redundant
		// retrace / parallel branch — skip it, freeing its budget for un-drawn lines.
		if inked != nil {
			if polyInkedFraction(sp, inked, src.W, src.H, hw) >= e.cfg.DedupOverlap {
				skipped++
				continue
			}
			stampPoly(sp, inked, src.W, src.H, hw)
		}
		emitOutline(sp, 0, 0, hw, ink, scfg, &shapes, budget)
	}
	if os.Getenv("INK_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[ink] raw=%d stitched=%d shapes=%d dedup-skipped=%d\n", rawPolys, len(polys), len(shapes), skipped)
	}
	return shapes, nil
}

// polyInBg returns the fraction of a polyline's points that fall in the background mask.
func polyInBg(poly [][2]float64, bg []bool, w, h int) float64 {
	if len(poly) == 0 {
		return 0
	}
	in := 0
	for _, p := range poly {
		x, y := int(p[0]), int(p[1])
		if x >= 0 && y >= 0 && x < w && y < h && bg[y*w+x] {
			in++
		}
	}
	return float64(in) / float64(len(poly))
}

// polyInkedFraction samples the polyline (≈1px steps) and returns the fraction of samples whose footprint
// (radius hw) is already inked.
func polyInkedFraction(sp [][2]float64, inked []bool, w, h int, hw float64) float64 {
	total, hit := 0, 0
	r := int(hw)
	if r < 1 {
		r = 1
	}
	at := func(x, y int) bool {
		if x < 0 || y < 0 || x >= w || y >= h {
			return false
		}
		return inked[y*w+x]
	}
	for i := 1; i < len(sp); i++ {
		ax, ay, bx, by := sp[i-1][0], sp[i-1][1], sp[i][0], sp[i][1]
		steps := int(math.Hypot(bx-ax, by-ay)) + 1
		for s := 0; s <= steps; s++ {
			t := float64(s) / float64(steps)
			x, y := int(ax+(bx-ax)*t), int(ay+(by-ay)*t)
			total++
			if at(x, y) || at(x+r, y) || at(x-r, y) || at(x, y+r) || at(x, y-r) {
				hit++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hit) / float64(total)
}

// stampPoly marks the polyline's footprint (radius hw) into the inked mask.
func stampPoly(sp [][2]float64, inked []bool, w, h int, hw float64) {
	r := int(hw)
	if r < 1 {
		r = 1
	}
	for i := 1; i < len(sp); i++ {
		ax, ay, bx, by := sp[i-1][0], sp[i-1][1], sp[i][0], sp[i][1]
		steps := int(math.Hypot(bx-ax, by-ay)) + 1
		for s := 0; s <= steps; s++ {
			t := float64(s) / float64(steps)
			cx, cy := int(ax+(bx-ax)*t), int(ay+(by-ay)*t)
			for dy := -r; dy <= r; dy++ {
				for dx := -r; dx <= r; dx++ {
					x, y := cx+dx, cy+dy
					if x >= 0 && y >= 0 && x < w && y < h {
						inked[y*w+x] = true
					}
				}
			}
		}
	}
}

// inkSample returns the mean colour of the dark (ink) pixels, darkened — a single ink tone for the
// whole drawing (anime ink is near-uniform). Falls back to a neutral dark if no dark pixels exist.
func inkSample(src *stylize.SrcImage, darken float64) []int {
	var sr, sg, sb float64
	n := 0
	for _, p := range src.Pix {
		if 0.299*float64(p.R)+0.587*float64(p.G)+0.114*float64(p.B) < 0.3 {
			sr += float64(p.R)
			sg += float64(p.G)
			sb += float64(p.B)
			n++
		}
	}
	if n == 0 {
		return []int{40, 32, 44, 255}
	}
	d := float32(darken)
	return []int{shape.C255(float32(sr/float64(n)) * d), shape.C255(float32(sg/float64(n)) * d), shape.C255(float32(sb/float64(n)) * d), 255}
}

func polyLen(p [][2]float64) float64 {
	var l float64
	for i := 1; i < len(p); i++ {
		l += math.Hypot(p[i][0]-p[i-1][0], p[i][1]-p[i-1][1])
	}
	return l
}

// smoothPolyline applies a [0.25,0.5,0.25] binomial smooth (fixed endpoints) to remove the pixel
// staircase of a traced centerline, so arc fitting sees a clean curve.
func smoothPolyline(poly [][2]float64, passes int) [][2]float64 {
	if passes <= 0 || len(poly) < 3 {
		return poly
	}
	cur := poly
	for p := 0; p < passes; p++ {
		out := make([][2]float64, len(cur))
		out[0], out[len(cur)-1] = cur[0], cur[len(cur)-1]
		for i := 1; i < len(cur)-1; i++ {
			out[i] = [2]float64{
				0.25*cur[i-1][0] + 0.5*cur[i][0] + 0.25*cur[i+1][0],
				0.25*cur[i-1][1] + 0.5*cur[i][1] + 0.25*cur[i+1][1],
			}
		}
		cur = out
	}
	return cur
}

// inkDT is the chamfer distance transform of the ink mask: for each ink pixel, the distance to the
// nearest non-ink pixel. At a thinned centerline this ≈ half the local stroke width.
func inkDT(mask []bool, w, h int) []float32 {
	const inf = 1e9
	d := make([]float32, w*h)
	for i := range d {
		if mask[i] {
			d[i] = inf
		}
	}
	at := func(x, y int) float32 {
		if x < 0 || y < 0 || x >= w || y >= h {
			return 0
		}
		return d[y*w+x]
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if d[i] == 0 {
				continue
			}
			for _, c := range [4][3]float32{{-1, 0, 1}, {0, -1, 1}, {-1, -1, 1.41421356}, {1, -1, 1.41421356}} {
				if v := at(x+int(c[0]), y+int(c[1])) + c[2]; v < d[i] {
					d[i] = v
				}
			}
		}
	}
	for y := h - 1; y >= 0; y-- {
		for x := w - 1; x >= 0; x-- {
			i := y*w + x
			if d[i] == 0 {
				continue
			}
			for _, c := range [4][3]float32{{1, 0, 1}, {0, 1, 1}, {1, 1, 1.41421356}, {-1, 1, 1.41421356}} {
				if v := at(x+int(c[0]), y+int(c[1])) + c[2]; v < d[i] {
					d[i] = v
				}
			}
		}
	}
	return d
}

// branchHalfWidth returns the median DT along a branch (≈ its half stroke width), clamped to
// [minHW,maxHW]. maxHW<=minHW disables the variation (uniform minHW).
func branchHalfWidth(dt []float32, poly [][2]float64, w int, minHW, maxHW float64) float64 {
	if maxHW <= minHW || len(poly) == 0 {
		return minHW
	}
	vals := make([]float64, 0, len(poly))
	for _, p := range poly {
		x, y := int(p[0]), int(p[1])
		if i := y*w + x; x >= 0 && y >= 0 && i >= 0 && i < len(dt) {
			vals = append(vals, float64(dt[i]))
		}
	}
	if len(vals) == 0 {
		return minHW
	}
	sort.Float64s(vals)
	hw := vals[len(vals)/2]
	if hw < minHW {
		hw = minHW
	}
	if hw > maxHW {
		hw = maxHW
	}
	return hw
}

func init() {
	stylize.RegisterEngine("ink", func(cfg json.RawMessage) (stylize.Engine, error) {
		c := inkDefaults()
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &c); err != nil {
				return nil, err
			}
		}
		return &inkEngine{cfg: c}, nil
	})
}
