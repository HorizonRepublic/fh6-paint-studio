package metric

import (
	"math"
	"os"
	"strconv"
	"strings"

	"fh6-paint-studio/internal/model"
)

// Calibration of HardEdgeMap. edgeTau is the Sobel magnitude of a real drawn edge (~0.09 sRGB step;
// shading stays below); densSat is the edge-pixel density at which a cell reads as fully structured;
// cohFloor is what a corner/wedge (two orientations, low coherence) keeps.
//
// densSat is the load-bearing one and it decides where the gates that suppress standouts apply:
// at 0.08 a 12x12 cell crossed by ONE 12px edge already saturates to "fully structured", so a face's
// smooth neck (a jawline and a few hair strands within the 3x3 cell smoothing) reads 0.62 mean /
// 0.72 median — which simultaneously (a) lets rect/tri candidates into smooth skin, (b) puts the
// deep-smooth glow swap out of reach (needs < tau), and (c) damps the region-weighted FE/EAGLE
// terms to 1-0.62. All three anti-standout mechanisms go quiet in exactly the zone the owner keeps
// pointing at. FH6_HARDMAP="edgeTau,densSat,cohFloor" overrides for lab A/Bs.
var hardEdgeTau, hardDensSat, hardCohFloor = func() (float64, float64, float64) {
	tau, dens, coh := 0.35, 0.08, 0.4
	if s := os.Getenv("FH6_HARDMAP"); s != "" {
		p := strings.Split(s, ",")
		dst := []*float64{&tau, &dens, &coh}
		for i := 0; i < len(p) && i < 3; i++ {
			if v, err := strconv.ParseFloat(strings.TrimSpace(p[i]), 64); err == nil {
				*dst[i] = v
			}
		}
	}
	return tau, dens, coh
}()

// HardEdgeMap returns, per pixel, how much the local target neighbourhood is HARD-EDGED STRUCTURE
// (line-work, spikes/wedges, geometric borders) in [0,1] вЂ” the regions where hard-cornered shape
// kinds (rectangle/triangle) earn their keep. Smooth shading scores ~0: a rect/tri placed there
// draws straight rim edges the target does not have (the "standout" artifact).
//
// Cell-based (12 px): per cell, the score = edge-pixel density (Sobel luma magnitude over a hard
// threshold, saturating at ~8% of the cell) modulated by the doubled-angle orientation coherence of
// those edge pixels (a straight segment is fully coherent; a corner/wedge splits into two
// orientations and keeps a floor, since corners are triangle territory). The cell grid is box-3x3
// smoothed and bilinearly upsampled, so the gate has no cell-boundary steps. len = w*h.
func HardEdgeMap(target []float32, w, h int) []float32 {
	const cell = 12
	edgeTau, densSat, cohFloor := hardEdgeTau, hardDensSat, hardCohFloor
	// Perceptual per-channel planes: sRGB-encode each channel so shadow edges keep their visual
	// contrast (linear light crushes darks вЂ” dark-on-dark line-work vanished from a linear-luma
	// map), and keep the channels separate so chroma-only edges (same luma) still register.
	chans := [3][]float32{make([]float32, w*h), make([]float32, w*h), make([]float32, w*h)}
	for i := 0; i < w*h; i++ {
		for c := 0; c < 3; c++ {
			chans[c][i] = model.LinearToSRGB(target[i*4+c])
		}
	}
	at := func(pl []float32, x, y int) float64 {
		if x < 0 {
			x = 0
		} else if x >= w {
			x = w - 1
		}
		if y < 0 {
			y = 0
		} else if y >= h {
			y = h - 1
		}
		return float64(pl[y*w+x])
	}
	cw, ch := (w+cell-1)/cell, (h+cell-1)/cell
	edgeN := make([]float64, cw*ch)
	pxN := make([]float64, cw*ch)
	vx := make([]float64, cw*ch)
	vy := make([]float64, cw*ch)
	msum := make([]float64, cw*ch)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var m, gxb, gyb float64
			for c := 0; c < 3; c++ {
				pl := chans[c]
				gx := (at(pl, x+1, y-1) + 2*at(pl, x+1, y) + at(pl, x+1, y+1)) - (at(pl, x-1, y-1) + 2*at(pl, x-1, y) + at(pl, x-1, y+1))
				gy := (at(pl, x-1, y+1) + 2*at(pl, x, y+1) + at(pl, x+1, y+1)) - (at(pl, x-1, y-1) + 2*at(pl, x, y-1) + at(pl, x+1, y-1))
				if mm := math.Hypot(gx, gy); mm > m {
					m, gxb, gyb = mm, gx, gy
				}
			}
			c := (y/cell)*cw + x/cell
			pxN[c]++
			if m < edgeTau {
				continue
			}
			edgeN[c]++
			th2 := 2 * math.Atan2(gyb, gxb) // doubled angle: opposite gradients = same orientation
			vx[c] += m * math.Cos(th2)
			vy[c] += m * math.Sin(th2)
			msum[c] += m
		}
	}
	cellH := make([]float64, cw*ch)
	for c := range cellH {
		if pxN[c] <= 0 {
			continue
		}
		dens := edgeN[c] / pxN[c] / densSat
		if dens > 1 {
			dens = 1
		}
		coh := 0.0
		if msum[c] > 0 {
			coh = math.Hypot(vx[c], vy[c]) / msum[c]
		}
		cellH[c] = dens * (cohFloor + (1-cohFloor)*coh)
	}
	// 3x3 box smooth so a single busy cell doesn't flicker the gate.
	sm := make([]float64, cw*ch)
	for cy := 0; cy < ch; cy++ {
		for cx := 0; cx < cw; cx++ {
			var s float64
			var n float64
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					x, y := cx+dx, cy+dy
					if x < 0 || y < 0 || x >= cw || y >= ch {
						continue
					}
					s += cellH[y*cw+x]
					n++
				}
			}
			sm[cy*cw+cx] = s / n
		}
	}
	// Bilinear upsample cell centres -> per pixel.
	out := make([]float32, w*h)
	for y := 0; y < h; y++ {
		fy := (float64(y) - cell/2) / cell
		cy0 := int(math.Floor(fy))
		ty := fy - float64(cy0)
		cy1 := cy0 + 1
		if cy0 < 0 {
			cy0, cy1, ty = 0, 0, 0
		}
		if cy1 >= ch {
			cy1 = ch - 1
			if cy0 > cy1 {
				cy0 = cy1
			}
		}
		for x := 0; x < w; x++ {
			fx := (float64(x) - cell/2) / cell
			cx0 := int(math.Floor(fx))
			tx := fx - float64(cx0)
			cx1 := cx0 + 1
			if cx0 < 0 {
				cx0, cx1, tx = 0, 0, 0
			}
			if cx1 >= cw {
				cx1 = cw - 1
				if cx0 > cx1 {
					cx0 = cx1
				}
			}
			v := (1-ty)*((1-tx)*sm[cy0*cw+cx0]+tx*sm[cy0*cw+cx1]) +
				ty*((1-tx)*sm[cy1*cw+cx0]+tx*sm[cy1*cw+cx1])
			out[y*w+x] = float32(v)
		}
	}
	return out
}
