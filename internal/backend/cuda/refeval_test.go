//go:build cuda

package cuda

import (
	"math"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// refeval_test.go — the pure-Go golden reference for Evaluate/Apply/ErrorGrid, salvaged from the
// deleted internal/backend/cpu when the runtime CPU backend was dropped (owner decision
// 2026-07-19: CUDA = the golden backend, Vulkan = the cross-vendor port). TEST-ONLY: it keeps the
// analytic eval math verifiable against an independent implementation without maintaining a full
// runtime backend. The polish reference lives separately in engine.PolishStepProbe (host Go).

type refBE struct {
	w, h         int
	target       []float32
	canvas       []float32
	weight       []float32
	sampleBudget int
	alphaGrid    []float64
}

const refDefaultSampleBudget = 4000

func newRef(target, weight []float32, w, h int) *refBE {
	canvas := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		canvas[i*4+3] = 1
	}
	if weight == nil {
		weight = make([]float32, w*h)
		for i := range weight {
			weight[i] = 1
		}
	}
	return &refBE{
		w: w, h: h,
		target:       append([]float32(nil), target...),
		canvas:       canvas,
		weight:       weight,
		sampleBudget: refDefaultSampleBudget,
	}
}

func (c *refBE) Reset(canvas []float32)   { copy(c.canvas, canvas) }
func (c *refBE) ReadCanvas(dst []float32) { copy(dst, c.canvas) }
func (c *refBE) SetSampleBudget(n int)    { c.sampleBudget = n }
func (c *refBE) SetAlphaGrid(vals []float32) {
	c.alphaGrid = c.alphaGrid[:0]
	for _, v := range vals {
		c.alphaGrid = append(c.alphaGrid, float64(v))
	}
}

func (c *refBE) Evaluate(cands []model.Candidate) []backend.EvalResult {
	out := make([]backend.EvalResult, len(cands))
	for i, cand := range cands {
		out[i] = c.evalShape(cand)
	}
	return out
}

func (c *refBE) evalShape(cand model.Candidate) backend.EvalResult {
	if raster.IsGradient(cand.Kind) {
		return c.evalGradient(cand)
	}
	a := float64(cand.Color.A)
	if a < 1e-3 {
		a = 1e-3
	}
	if a > 1 {
		a = 1
	}
	xMin, yMin, xMax, yMax := raster.BBox(cand.Kind, cand.P, c.w, c.h)
	step := c.sampleStep(xMin, yMin, xMax, yMax)
	var n, nt int
	var W float64
	var sTR, sTG, sTB, sTA float64
	var sCR, sCG, sCB, sCA float64
	var sCR2, sCG2, sCB2, sCA2 float64
	var sTCR, sTCG, sTCB, sTCA float64
	for y := yMin; y <= yMax; y += step {
		for x := xMin; x <= xMax; x += step {
			if !raster.Inside(cand.Kind, cand.P, x, y) {
				continue
			}
			idx := y*c.w + x
			p := idx * 4
			if c.target[p+3] < 0.5 {
				nt++
				continue
			}
			wgt := float64(c.weight[idx])
			tr, tg, tb, ta := float64(c.target[p]), float64(c.target[p+1]), float64(c.target[p+2]), float64(c.target[p+3])
			sr, sg, sb, sa := float64(c.canvas[p]), float64(c.canvas[p+1]), float64(c.canvas[p+2]), float64(c.canvas[p+3])
			sTR, sTG, sTB, sTA = sTR+wgt*tr, sTG+wgt*tg, sTB+wgt*tb, sTA+wgt*ta
			sCR, sCG, sCB, sCA = sCR+wgt*sr, sCG+wgt*sg, sCB+wgt*sb, sCA+wgt*sa
			sCR2, sCG2, sCB2, sCA2 = sCR2+wgt*sr*sr, sCG2+wgt*sg*sg, sCB2+wgt*sb*sb, sCA2+wgt*sa*sa
			sTCR, sTCG, sTCB, sTCA = sTCR+wgt*tr*sr, sTCG+wgt*tg*sg, sTCB+wgt*tb*sb, sTCA+wgt*ta*sa
			W += wgt
			n++
		}
	}
	if n == 0 || W <= 0 {
		return backend.EvalResult{Score: rejected}
	}
	if nt > 0 && float64(nt) > 1.5*float64(n) {
		return backend.EvalResult{Score: rejected}
	}
	invW := 1.0 / W
	solve := func(a float64) (oR, oG, oB, totalDelta float64) {
		invA := 1.0 - a
		oR = refClamp01((sTR*invW - (sCR*invW)*invA) / a)
		oG = refClamp01((sTG*invW - (sCG*invW)*invA) / a)
		oB = refClamp01((sTB*invW - (sCB*invW)*invA) / a)
		a2, twoA := a*a, 2*a
		dR := a2*(W*oR*oR-2*oR*sCR+sCR2) - twoA*(oR*sTR-oR*sCR-sTCR+sCR2)
		dG := a2*(W*oG*oG-2*oG*sCG+sCG2) - twoA*(oG*sTG-oG*sCG-sTCG+sCG2)
		dB := a2*(W*oB*oB-2*oB*sCB+sCB2) - twoA*(oB*sTB-oB*sCB-sTCB+sCB2)
		dA := a2*(W-2*sCA+sCA2) - twoA*(sTA-sCA-sTCA+sCA2)
		totalDelta = dR + dG + dB + dA
		if nt > 0 {
			spillFrac := float64(nt) / float64(n+nt)
			totalDelta += a2 * float64(nt) * (1 + 2*spillFrac) * (oR*oR + oG*oG + oB*oB + 1)
		}
		return oR, oG, oB, totalDelta
	}
	oR, oG, oB, totalDelta := solve(a)
	outA := cand.Color.A
	for _, ga := range c.alphaGrid {
		if ga < 1e-3 {
			ga = 1e-3
		}
		if ga > 1 {
			ga = 1
		}
		gR, gG, gB, gd := solve(ga)
		if gd < totalDelta {
			oR, oG, oB, totalDelta, outA = gR, gG, gB, gd, float32(ga)
		}
	}
	return backend.EvalResult{
		Score: float32(totalDelta * float64(step*step)),
		Color: model.RGBA{R: float32(oR), G: float32(oG), B: float32(oB), A: outA},
	}
}

func (c *refBE) evalGradient(cand model.Candidate) backend.EvalResult {
	A := float64(cand.Color.A)
	if A < 1e-3 {
		A = 1e-3
	}
	if A > 1 {
		A = 1
	}
	xMin, yMin, xMax, yMax := raster.BBox(cand.Kind, cand.P, c.w, c.h)
	step := c.sampleStep(xMin, yMin, xMax, yMax)

	var saa float64
	var sarR, sarG, sarB float64
	var saasR, saasG, saasB float64
	var sarsR, sarsG, sarsB float64
	var saassR, saassG, saassB float64
	var dA float64
	var saaT float64
	var n, nt int

	for y := yMin; y <= yMax; y += step {
		for x := xMin; x <= xMax; x += step {
			cov := raster.Coverage(cand.Kind, cand.P, x, y)
			if cov <= 0 {
				continue
			}
			idx := y*c.w + x
			p := idx * 4
			a := A * cov
			wgt := float64(c.weight[idx])
			wa := wgt * a
			wa2 := wa * a
			if c.target[p+3] < 0.5 {
				nt++
				saaT += wa2
				continue
			}
			tr, tg, tb, ta := float64(c.target[p]), float64(c.target[p+1]), float64(c.target[p+2]), float64(c.target[p+3])
			sr, sg, sb, sa := float64(c.canvas[p]), float64(c.canvas[p+1]), float64(c.canvas[p+2]), float64(c.canvas[p+3])
			rr, rg, rb := tr-sr, tg-sg, tb-sb
			saa += wa2
			sarR, sarG, sarB = sarR+wa*rr, sarG+wa*rg, sarB+wa*rb
			saasR, saasG, saasB = saasR+wa2*sr, saasG+wa2*sg, saasB+wa2*sb
			sarsR, sarsG, sarsB = sarsR+wa*rr*sr, sarsG+wa*rg*sg, sarsB+wa*rb*sb
			saassR, saassG, saassB = saassR+wa2*sr*sr, saassG+wa2*sg*sg, saassB+wa2*sb*sb
			oneMinusSa := 1 - sa
			dA += wgt * (a*a*oneMinusSa*oneMinusSa - 2*a*oneMinusSa*(ta-sa))
			n++
		}
	}
	if n == 0 || saa <= 0 {
		return backend.EvalResult{Score: rejected}
	}
	if nt > 0 && float64(nt) > 1.5*float64(n) {
		return backend.EvalResult{Score: rejected}
	}

	oR := refClamp01((sarR + saasR) / saa)
	oG := refClamp01((sarG + saasG) / saa)
	oB := refClamp01((sarB + saasB) / saa)
	totalDelta := dA
	totalDelta += 2*sarsR + saassR - 2*oR*(sarR+saasR) + oR*oR*saa
	totalDelta += 2*sarsG + saassG - 2*oG*(sarG+saasG) + oG*oG*saa
	totalDelta += 2*sarsB + saassB - 2*oB*(sarB+saasB) + oB*oB*saa
	if nt > 0 {
		spillFrac := float64(nt) / float64(n+nt)
		totalDelta += saaT * (1 + 2*spillFrac) * (oR*oR + oG*oG + oB*oB + 1)
	}
	return backend.EvalResult{
		Score: float32(totalDelta * float64(step*step)),
		Color: model.RGBA{R: float32(oR), G: float32(oG), B: float32(oB), A: cand.Color.A},
	}
}

func (c *refBE) Apply(cand model.Candidate) {
	if raster.IsGradient(cand.Kind) {
		A := cand.Color.A
		if A < 0 {
			A = 0
		}
		if A > 1 {
			A = 1
		}
		xMin, yMin, xMax, yMax := raster.BBox(cand.Kind, cand.P, c.w, c.h)
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				cov := raster.Coverage(cand.Kind, cand.P, x, y)
				if cov <= 0 {
					continue
				}
				a := A * float32(cov)
				invA := 1 - a
				p := (y*c.w + x) * 4
				c.canvas[p+0] = c.canvas[p+0]*invA + cand.Color.R*a
				c.canvas[p+1] = c.canvas[p+1]*invA + cand.Color.G*a
				c.canvas[p+2] = c.canvas[p+2]*invA + cand.Color.B*a
				c.canvas[p+3] = c.canvas[p+3]*invA + a
			}
		}
		return
	}
	a := cand.Color.A
	if a < 0 {
		a = 0
	}
	if a > 1 {
		a = 1
	}
	invA := 1 - a
	xMin, yMin, xMax, yMax := raster.BBox(cand.Kind, cand.P, c.w, c.h)
	for y := yMin; y <= yMax; y++ {
		for x := xMin; x <= xMax; x++ {
			if !raster.Inside(cand.Kind, cand.P, x, y) {
				continue
			}
			p := (y*c.w + x) * 4
			c.canvas[p+0] = c.canvas[p+0]*invA + cand.Color.R*a
			c.canvas[p+1] = c.canvas[p+1]*invA + cand.Color.G*a
			c.canvas[p+2] = c.canvas[p+2]*invA + cand.Color.B*a
			c.canvas[p+3] = c.canvas[p+3]*invA + a
		}
	}
}

// ErrorGrid mirrors the device gridKernel cell partition (gw = gh = grid).
func (c *refBE) ErrorGrid(grid int) []float32 {
	gw, gh := grid, grid
	out := make([]float32, gw*gh)
	for gy := 0; gy < gh; gy++ {
		y0, y1 := gy*c.h/gh, (gy+1)*c.h/gh
		for gx := 0; gx < gw; gx++ {
			x0, x1 := gx*c.w/gw, (gx+1)*c.w/gw
			var sum float64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					idx := y*c.w + x
					wgt := float64(c.weight[idx])
					p := idx * 4
					for k := 0; k < 4; k++ {
						d := float64(c.target[p+k] - c.canvas[p+k])
						sum += wgt * d * d
					}
				}
			}
			out[gy*gw+gx] = float32(sum)
		}
	}
	return out
}

func (c *refBE) sampleStep(xMin, yMin, xMax, yMax int) int {
	target := c.sampleBudget
	if target < 1 {
		target = refDefaultSampleBudget
	}
	area := (xMax - xMin + 1) * (yMax - yMin + 1)
	if area <= target {
		return 1
	}
	step := int(math.Sqrt(float64(area) / float64(target)))
	if step < 1 {
		return 1
	}
	return step
}

func refClamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
