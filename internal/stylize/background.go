package stylize

// BackgroundMask returns a per-pixel mask of the image BACKGROUND: light pixels (luma ≥ lumaThresh)
// that are connected to the image border. This is the white/near-white margin around a figure (anime on
// white) plus its faint anti-aliased halo — NOT light areas inside the figure (skin highlights), which are
// not border-connected. The fill and ink engines skip regions/lines that fall mostly in here, so the
// background stays clean (no halo, no stray edge lines) and the budget is spent on the figure.
func BackgroundMask(src *SrcImage, lumaThresh float64) []bool {
	w, h := src.W, src.H
	n := w * h
	bg := make([]bool, n)
	// "background light" = near-WHITE: high luma AND low chroma. The chroma gate keeps coloured light
	// backgrounds (a cream room, a pale sky) OUT of the mask, so suppression only fires on a true white
	// margin (anime on white) and never eats figure detail on busy/coloured scenes.
	light := func(i int) bool {
		p := src.Pix[i]
		luma := float64(0.299*p.R + 0.587*p.G + 0.114*p.B)
		hi := p.R
		if p.G > hi {
			hi = p.G
		}
		if p.B > hi {
			hi = p.B
		}
		lo := p.R
		if p.G < lo {
			lo = p.G
		}
		if p.B < lo {
			lo = p.B
		}
		return luma >= lumaThresh && float64(hi-lo) < 0.10
	}
	stack := make([]int, 0, 1024)
	push := func(i int) {
		if i >= 0 && i < n && !bg[i] && light(i) {
			bg[i] = true
			stack = append(stack, i)
		}
	}
	for x := 0; x < w; x++ {
		push(x)
		push((h-1)*w + x)
	}
	for y := 0; y < h; y++ {
		push(y * w)
		push(y*w + w - 1)
	}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x, y := i%w, i/w
		if x > 0 {
			push(i - 1)
		}
		if x < w-1 {
			push(i + 1)
		}
		if y > 0 {
			push(i - w)
		}
		if y < h-1 {
			push(i + w)
		}
	}
	return bg
}
