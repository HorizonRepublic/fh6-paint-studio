package shape

import (
	"sort"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/stylize"
)

// Quantize reduces the image to k colours by classic median-cut: repeatedly split the colour box with
// the widest channel range at its median, then average each box. Deterministic (no RNG). Returns the
// palette plus a per-pixel palette index. Shared by the Fill (region fills) and Stroke (contours)
// engines. A perceptual k-means upgrade can register alongside later.
func Quantize(src *stylize.SrcImage, k int) (palette []model.RGBA, idx []int) {
	if k < 1 {
		k = 1
	}
	step := 1
	if n := len(src.Pix); n > 24000 {
		step = n / 24000
	}
	var samp [][3]float32
	for i := 0; i < len(src.Pix); i += step {
		p := src.Pix[i]
		samp = append(samp, [3]float32{p.R, p.G, p.B})
	}
	boxes := [][][3]float32{samp}
	for len(boxes) < k {
		bi, brange := -1, float32(-1)
		for i, bx := range boxes {
			if len(bx) < 2 {
				continue
			}
			if _, rng := widestAxis(bx); rng > brange {
				brange, bi = rng, i
			}
		}
		if bi < 0 {
			break
		}
		ax, _ := widestAxis(boxes[bi])
		bx := boxes[bi]
		sort.Slice(bx, func(a, b int) bool { return bx[a][ax] < bx[b][ax] })
		mid := len(bx) / 2
		boxes[bi] = bx[:mid]
		boxes = append(boxes, bx[mid:])
	}
	palette = make([]model.RGBA, len(boxes))
	for i, bx := range boxes {
		var r, g, b float32
		for _, p := range bx {
			r, g, b = r+p[0], g+p[1], b+p[2]
		}
		n := float32(len(bx))
		if n == 0 {
			n = 1
		}
		palette[i] = model.RGBA{R: r / n, G: g / n, B: b / n, A: 1}
	}
	idx = make([]int, len(src.Pix))
	for i, p := range src.Pix {
		best, bd := 0, float32(1e9)
		for j, c := range palette {
			dr, dg, db := p.R-c.R, p.G-c.G, p.B-c.B
			if d := dr*dr + dg*dg + db*db; d < bd {
				bd, best = d, j
			}
		}
		idx[i] = best
	}
	return palette, idx
}

func widestAxis(bx [][3]float32) (axis int, rng float32) {
	lo := [3]float32{1e9, 1e9, 1e9}
	hi := [3]float32{-1e9, -1e9, -1e9}
	for _, p := range bx {
		for c := 0; c < 3; c++ {
			if p[c] < lo[c] {
				lo[c] = p[c]
			}
			if p[c] > hi[c] {
				hi[c] = p[c]
			}
		}
	}
	for c := 0; c < 3; c++ {
		if r := hi[c] - lo[c]; r > rng {
			rng, axis = r, c
		}
	}
	return axis, rng
}
