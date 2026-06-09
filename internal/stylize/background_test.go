package stylize

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestBackgroundMaskBorderConnectedWhiteOnly(t *testing.T) {
	w, h := 20, 20
	pix := make([]model.RGBA, w*h)
	for i := range pix {
		pix[i] = model.RGBA{R: 1, G: 1, B: 1, A: 1} // white field
	}
	// a dark figure blob in the centre, with a white "eye" hole inside it (an interior light region)
	for y := 6; y < 14; y++ {
		for x := 6; x < 14; x++ {
			pix[y*w+x] = model.RGBA{R: 0.1, G: 0.1, B: 0.1, A: 1}
		}
	}
	pix[10*w+10] = model.RGBA{R: 1, G: 1, B: 1, A: 1} // interior white speck (NOT border-connected)

	bg := BackgroundMask(&SrcImage{W: w, H: h, Pix: pix}, 0.86)
	if !bg[0] || !bg[w-1] {
		t.Error("border white pixels should be background")
	}
	if bg[6*w+6] {
		t.Error("dark figure pixel should not be background")
	}
	if bg[10*w+10] {
		t.Error("interior white speck (not border-connected) must NOT be background")
	}
}

func TestBackgroundMaskSkipsColouredLight(t *testing.T) {
	w, h := 12, 12
	pix := make([]model.RGBA, w*h)
	for i := range pix {
		pix[i] = model.RGBA{R: 0.95, G: 0.80, B: 0.80, A: 1} // light PINK border (high luma, chroma 0.15)
	}
	bg := BackgroundMask(&SrcImage{W: w, H: h, Pix: pix}, 0.80)
	for _, b := range bg {
		if b {
			t.Fatal("coloured-light background should not be flagged (chroma gate)")
		}
	}
}
