package cpu

import (
	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// evalGradient scores a radial-gradient candidate (KindGlow/KindDisk). Unlike the hard kinds the
// composited alpha VARIES per pixel: a(x) = A · cov(x), where cov is the baked falloff (raster.
// Coverage) and A = cand.Color.A. The optimal RGB under a weighted least-squares with per-pixel a is
//
//	O_c = clamp01( (Σ w·a·r_c + Σ w·a²·s_c) / Σ w·a² ),   r = target − canvas
//	ΔSSE_c = 2·Σ w·a·r·s + Σ w·a²·s² − 2·O_c·(Σ w·a·r + Σ w·a²·s) + O_c²·Σ w·a²
//
// (the ΔSSE form is exact for the clamped O, reducing to −Sae²/Saa at the unclamped optimum). The
// alpha channel has a fixed contribution O_A=1, so its ΔSSE is accumulated directly. With progressive
// sampling the sums scale by step² (O is a ratio -> invariant), matching the hard-kind path.
func (c *CPU) evalGradient(cand model.Candidate) backend.EvalResult {
	A := float64(cand.Color.A)
	if A < 1e-3 {
		A = 1e-3
	}
	if A > 1 {
		A = 1
	}
	xMin, yMin, xMax, yMax := raster.BBox(cand.Kind, cand.P, c.w, c.h)
	step := c.sampleStep(xMin, yMin, xMax, yMax)

	var saa float64                    // Σ w·a²  (shared across channels — a is per-pixel, not per-channel)
	var sarR, sarG, sarB float64       // Σ w·a·r
	var saasR, saasG, saasB float64    // Σ w·a²·s
	var sarsR, sarsG, sarsB float64    // Σ w·a·r·s
	var saassR, saassG, saassB float64 // Σ w·a²·s²
	var dA float64                     // alpha-channel ΔSSE (O_A=1, accumulated directly)
	var W float64                      // Σ w over covered opaque pixels
	var saaT float64                   // Σ w·a² over covered TRANSPARENT pixels (spill weight)
	var n, nt int                      // covered opaque / transparent (overhang) pixel counts

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
			if c.target[p+3] < 0.5 { // transparent target pixel = overhang/spill
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
			W += wgt
			n++
		}
	}
	if n == 0 || saa <= 0 {
		return backend.EvalResult{Score: rejected}
	}
	if nt > 0 && float64(nt) > 1.5*float64(n) {
		return backend.EvalResult{Score: rejected}
	}

	oR := clamp01((sarR + saasR) / saa)
	oG := clamp01((sarG + saasG) / saa)
	oB := clamp01((sarB + saasB) / saa)
	totalDelta := dA
	totalDelta += 2*sarsR + saassR - 2*oR*(sarR+saasR) + oR*oR*saa
	totalDelta += 2*sarsG + saassG - 2*oG*(sarG+saasG) + oG*oG*saa
	totalDelta += 2*sarsB + saassB - 2*oB*(sarB+saasB) + oB*oB*saa

	// Spill penalty: gradient energy landing on transparent background (raises alpha where the target
	// wants 0). saaT is the Σ w·a² over those pixels; inert for opaque images (nt==0).
	if nt > 0 {
		spillFrac := float64(nt) / float64(n+nt)
		totalDelta += saaT * (1 + 2*spillFrac) * (oR*oR + oG*oG + oB*oB + 1)
	}

	return backend.EvalResult{
		Score: float32(totalDelta * float64(step*step)),
		Color: model.RGBA{R: float32(oR), G: float32(oG), B: float32(oB), A: cand.Color.A},
	}
}

// applyGradient composites a gradient candidate with its per-pixel alpha a(x)=A·cov(x).
func (c *CPU) applyGradient(cand model.Candidate) {
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
}
