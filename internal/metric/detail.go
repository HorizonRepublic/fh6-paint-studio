package metric

import "math"

// DetailGrid returns a per-cell "detail" measure of the TARGET image at gw×gh grid
// resolution: the mean Sobel edge magnitude in each cell, normalized so the busiest
// cell = 1 and a perfectly flat cell = 0. len = gw*gh, row-major (cell index = gy*gw+gx).
//
// It feeds DETAIL-WEIGHTED SAMPLING: late candidate centres are biased toward
// intrinsically detailed regions (faces, linework, ornaments) so fine detail keeps
// earning budget instead of late shapes piling into already-solved smooth areas
// (which leaves angular faceting). The measure is a property of the TARGET only
// (not the residual), so it stays a
// stable bias across the whole run. Returns nil for non-positive dimensions.
func DetailGrid(target []float32, w, h, gw, gh int) []float32 {
	if w <= 0 || h <= 0 || gw <= 0 || gh <= 0 || len(target) < w*h*4 {
		return nil
	}
	lum := make([]float32, w*h)
	for i := 0; i < w*h; i++ {
		lum[i] = 0.299*target[i*4] + 0.587*target[i*4+1] + 0.114*target[i*4+2]
	}
	at := func(x, y int) float32 {
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
		return lum[y*w+x]
	}
	sum := make([]float32, gw*gh)
	cnt := make([]int, gw*gh)
	for y := 0; y < h; y++ {
		cy := y * gh / h
		for x := 0; x < w; x++ {
			gx := (at(x+1, y-1) + 2*at(x+1, y) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x-1, y) + at(x-1, y+1))
			gy := (at(x-1, y+1) + 2*at(x, y+1) + at(x+1, y+1)) - (at(x-1, y-1) + 2*at(x, y-1) + at(x+1, y-1))
			m := float32(math.Hypot(float64(gx), float64(gy)))
			ci := cy*gw + x*gw/w
			sum[ci] += m
			cnt[ci]++
		}
	}
	out := make([]float32, gw*gh)
	var maxc float32
	for i := range out {
		if cnt[i] > 0 {
			out[i] = sum[i] / float32(cnt[i])
		}
		if out[i] > maxc {
			maxc = out[i]
		}
	}
	if maxc > 0 {
		for i := range out {
			out[i] /= maxc
		}
	}
	return out
}
