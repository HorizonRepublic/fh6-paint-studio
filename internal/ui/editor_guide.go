package ui

import (
	"image"
	"image/color"
	"strconv"

	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// canvas backdrop / measurement-guide modes, cycled by the toolbar button.
const (
	guideChecker = iota // transparency checkerboard (default)
	guideGrid           // solid backdrop + alignment grid
	guideRuler          // grid + numbered edge rulers
	canvasGuideModes
)

func guideModeKey(m int) string {
	switch m {
	case guideGrid:
		return "editor.guide_grid"
	case guideRuler:
		return "editor.guide_ruler"
	default:
		return "editor.guide_checker"
	}
}

// drawCanvasBackdrop fills behind the rendered art: the transparency checker, or a flat panel when a
// measurement guide is on (so the grid/ruler lines read cleanly).
func (s *AppState) drawCanvasBackdrop(gtx C, rect image.Rectangle) {
	if s.canvasGuide == guideChecker {
		drawCheckerboard(gtx, rect)
		return
	}
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0x23, G: 0x26, B: 0x2e, A: 0xff}, clip.Rect(rect).Op())
}

// niceStep returns the smallest "nice" number (1, 2 or 5 × 10ᵏ) that is at least v.
func niceStep(v float64) float64 {
	if v <= 1 {
		return 1
	}
	pow := 1.0
	for v >= 10 {
		v /= 10
		pow *= 10
	}
	switch {
	case v <= 2:
		return 2 * pow
	case v <= 5:
		return 5 * pow
	default:
		return 10 * pow
	}
}

// drawCanvasGuide overlays the alignment grid (and, in ruler mode, numbered edge rulers) over the art so
// users can lay out a livery precisely. The grid step is a round number of image pixels chosen so the
// on-screen spacing stays readable at any zoom; every fifth line is a major line.
func (s *AppState) drawCanvasGuide(gtx C, vp, rect image.Rectangle) {
	if s.canvasGuide == guideChecker || rect.Dx() <= 0 || s.EditW <= 0 {
		return
	}
	th := s.Th
	scale := float64(rect.Dx()) / float64(s.EditW) // screen px per image px
	step := niceStep(float64(gtx.Dp(38)) / scale)
	minor := color.NRGBA{R: th.Border.R, G: th.Border.G, B: th.Border.B, A: 70}
	major := color.NRGBA{R: th.Border.R, G: th.Border.G, B: th.Border.B, A: 140}

	clipv := clip.Rect(vp).Push(gtx.Ops)
	for k, ix := 0, 0.0; ix <= float64(s.EditW)+0.5; k, ix = k+1, ix+step {
		sx := rect.Min.X + int(ix*scale+0.5)
		col := minor
		if k%5 == 0 {
			col = major
		}
		paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(sx, rect.Min.Y, sx+1, rect.Max.Y)).Op())
	}
	for k, iy := 0, 0.0; iy <= float64(s.EditH)+0.5; k, iy = k+1, iy+step {
		sy := rect.Min.Y + int(iy*scale+0.5)
		col := minor
		if k%5 == 0 {
			col = major
		}
		paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(rect.Min.X, sy, rect.Max.X, sy+1)).Op())
	}
	clipv.Pop()

	if s.canvasGuide == guideRuler {
		s.drawRulers(gtx, vp, rect, step, scale)
	}
}

// drawRulers paints the top and left ruler bands with ticks at every grid line; major lines get a longer
// tick, and the top ruler is labelled with the image-pixel coordinate.
func (s *AppState) drawRulers(gtx C, vp, rect image.Rectangle, step, scale float64) {
	th := s.Th
	band := gtx.Dp(16)
	bg := color.NRGBA{R: 0x18, G: 0x1b, B: 0x22, A: 0xe6}
	tick := color.NRGBA{R: th.TextDim.R, G: th.TextDim.G, B: th.TextDim.B, A: 0xff}
	paint.FillShape(gtx.Ops, bg, clip.Rect(image.Rect(vp.Min.X, vp.Min.Y, vp.Max.X, vp.Min.Y+band)).Op())
	paint.FillShape(gtx.Ops, bg, clip.Rect(image.Rect(vp.Min.X, vp.Min.Y, vp.Min.X+band, vp.Max.Y)).Op())

	for k, ix := 0, 0.0; ix <= float64(s.EditW)+0.5; k, ix = k+1, ix+step {
		sx := rect.Min.X + int(ix*scale+0.5)
		if sx < vp.Min.X+band || sx > vp.Max.X {
			continue
		}
		h := band / 2
		if k%5 == 0 {
			h = band
			s.rulerLabel(gtx, sx+2, vp.Min.Y+1, strconv.Itoa(int(ix+0.5)))
		}
		paint.FillShape(gtx.Ops, tick, clip.Rect(image.Rect(sx, vp.Min.Y+band-h, sx+1, vp.Min.Y+band)).Op())
	}
	for k, iy := 0, 0.0; iy <= float64(s.EditH)+0.5; k, iy = k+1, iy+step {
		sy := rect.Min.Y + int(iy*scale+0.5)
		if sy < vp.Min.Y+band || sy > vp.Max.Y {
			continue
		}
		w := band / 2
		if k%5 == 0 {
			w = band
		}
		paint.FillShape(gtx.Ops, tick, clip.Rect(image.Rect(vp.Min.X+band-w, sy, vp.Min.X+band, sy+1)).Op())
	}
}

func (s *AppState) rulerLabel(gtx C, x, y int, txt string) {
	defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()
	s.Th.Lbl(gtx, 9, txt, s.Th.TextDim)
}
