package stylize

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

func chromaVar(p model.RGBA) float64 { // simple chroma magnitude proxy
	o1 := float64(p.R - p.G)
	o2 := float64(p.B) - 0.5*float64(p.R+p.G)
	return o1*o1 + o2*o2
}

// TestChromaSaliencyHighOnColourDetail: a flat-grey left half vs a red/green checkerboard right half —
// saliency must be far higher on the colourful-detail side.
func TestChromaSaliencyHighOnColourDetail(t *testing.T) {
	w, h := 40, 20
	pix := make([]model.RGBA, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				pix[y*w+x] = model.RGBA{R: 0.5, G: 0.5, B: 0.5, A: 1}
			} else if (x+y)%2 == 0 {
				pix[y*w+x] = model.RGBA{R: 0.9, G: 0.1, B: 0.1, A: 1}
			} else {
				pix[y*w+x] = model.RGBA{R: 0.1, G: 0.9, B: 0.1, A: 1}
			}
		}
	}
	sal := chromaVarianceSaliency(pix, w, h, 3)
	var left, right float64
	for y := 2; y < h-2; y++ {
		for x := 2; x < w/2-2; x++ {
			left += sal[y*w+x]
		}
		for x := w/2 + 2; x < w-2; x++ {
			right += sal[y*w+x]
		}
	}
	if right <= left*5 {
		t.Errorf("saliency not concentrated on colour detail: left=%.3f right=%.3f", left, right)
	}
}

// TestAdaptiveDTSparesColourDetail: a grey field with a small colour-textured patch (like an eye). After
// the same strong smooth, the adaptive DT must retain MORE of the patch's chroma than the plain DT.
func TestAdaptiveDTSparesColourDetail(t *testing.T) {
	w, h := 48, 48
	mk := func() []model.RGBA {
		pix := make([]model.RGBA, w*h)
		for i := range pix {
			pix[i] = model.RGBA{R: 0.5, G: 0.5, B: 0.5, A: 1}
		}
		for y := 20; y < 28; y++ { // a colour-textured patch
			for x := 20; x < 28; x++ {
				if (x+y)%2 == 0 {
					pix[y*w+x] = model.RGBA{R: 0.85, G: 0.15, B: 0.5, A: 1}
				} else {
					pix[y*w+x] = model.RGBA{R: 0.15, G: 0.3, B: 0.85, A: 1}
				}
			}
		}
		return pix
	}
	plain := domainTransformRF(mk(), w, h, 32, 0.9, 4)
	adapt := domainTransformRFAdaptive(mk(), w, h, 32, 0.9, 4, 8)
	var cp, ca float64
	for y := 20; y < 28; y++ {
		for x := 20; x < 28; x++ {
			cp += chromaVar(plain[y*w+x])
			ca += chromaVar(adapt[y*w+x])
		}
	}
	if ca <= cp {
		t.Errorf("adaptive DT did not spare colour: plainChroma=%.4f adaptChroma=%.4f", cp, ca)
	}
}
