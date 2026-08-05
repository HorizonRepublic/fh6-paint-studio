package imageio

import (
	"fh6-paint-studio/internal/model"
)

// The base rectangle is coloured with the frame MEAN, which is the single colour minimising squared
// error over every pixel. That objective is wrong for a layer that is about to be painted over: what
// the base is worth is the number of pixels it gets EXACTLY right, because those are the pixels no
// later shape has to buy back. On a subject over a plain background the mean sits between the two
// and satisfies neither, so the greedy spends hundreds of low-alpha shapes nudging the background
// from "nearly right" to right — measured at 66% of a 1000-shape budget on a plain white backdrop.
//
// When one colour genuinely dominates the frame, starting there costs nothing (the base rect is
// spent either way) and hands that whole area to the budget for free. Gated on coverage rather than
// used unconditionally: for a picture with no dominant colour the mean really is the better start,
// and a mode chosen from a thin plurality would be a worse one.
//
// DominantBGFrac is the coverage a colour must reach to take the base. 0 disables, restoring the
// mean. Package-level like model.LinearLight, which this pipeline already configures the same way.
var DominantBGFrac float64

// dominantColor returns the mean of the largest colour bucket among opaque pixels, if that bucket
// covers at least minFrac of them. Bucketing is coarse (32 levels per channel) so that a slightly
// noisy or gently dithered backdrop still lands in one bin; the returned colour is the exact mean
// of the pixels that fell in it, not the bin centre, so nothing is lost to quantisation.
func dominantColor(px []float32, w, h int, minFrac float64) (model.RGBA, bool) {
	const bins = 32
	type acc struct {
		n          int
		sr, sg, sb float64
	}
	hist := make(map[int]*acc)
	opaque := 0
	for i := 0; i < w*h; i++ {
		if px[i*4+3] < 0.5 {
			continue
		}
		opaque++
		r, g, b := px[i*4+0], px[i*4+1], px[i*4+2]
		key := quant(r)*bins*bins + quant(g)*bins + quant(b)
		a := hist[key]
		if a == nil {
			a = &acc{}
			hist[key] = a
		}
		a.n++
		a.sr, a.sg, a.sb = a.sr+float64(r), a.sg+float64(g), a.sb+float64(b)
	}
	if opaque == 0 {
		return model.RGBA{}, false
	}
	var best *acc
	for _, a := range hist {
		if best == nil || a.n > best.n {
			best = a
		}
	}
	if best == nil || float64(best.n) < minFrac*float64(opaque) {
		return model.RGBA{}, false
	}
	inv := 1 / float64(best.n)
	return model.RGBA{
		R: float32(best.sr * inv), G: float32(best.sg * inv), B: float32(best.sb * inv), A: 1,
	}, true
}

// quant maps a working-space channel to a bucket index. The working space is linear when
// model.LinearLight is set, where the same absolute step spans far more perceptual distance in the
// shadows; bucketing on the sRGB-encoded value keeps the bins perceptually even in both modes, so a
// dark backdrop is not split across bins that a light one would survive.
func quant(v float32) int {
	const bins = 32
	if model.LinearLight {
		v = model.LinearToSRGB(v)
	}
	i := int(v * bins)
	if i < 0 {
		i = 0
	}
	if i >= bins {
		i = bins - 1
	}
	return i
}
