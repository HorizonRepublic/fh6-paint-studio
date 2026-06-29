package ui

import (
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget/material"
)

// rgbToHSV converts an opaque colour to hue (0..360), saturation and value (both 0..1).
func rgbToHSV(c color.NRGBA) (h, sat, v float64) {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	mx := math.Max(r, math.Max(g, b))
	mn := math.Min(r, math.Min(g, b))
	v = mx
	d := mx - mn
	if mx > 0 {
		sat = d / mx
	}
	if d == 0 {
		return 0, sat, v
	}
	switch mx {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h, sat, v
}

// colorWheelImage renders an HSV disc: angle = hue, radius = saturation, at brightness v. Pixels outside
// the disc are transparent, with a 1.5px soft edge so the rim isn't jagged.
func colorWheelImage(size int, v float64) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	r := float64(size) / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-r, float64(y)+0.5-r
			dist := math.Hypot(dx, dy)
			if dist > r {
				continue
			}
			h := math.Atan2(dy, dx) * 180 / math.Pi
			if h < 0 {
				h += 360
			}
			sat := dist / r
			if sat > 1 {
				sat = 1
			}
			col := hsvToRGB(h, sat, v)
			a := 1.0
			if e := r - dist; e < 1.5 {
				if a = e / 1.5; a < 0 {
					a = 0
				}
			}
			p := (y*size + x) * 4
			img.Pix[p+0] = col.R
			img.Pix[p+1] = col.G
			img.Pix[p+2] = col.B
			img.Pix[p+3] = uint8(a*255 + 0.5)
		}
	}
	return img
}

// colorWheel lays out the interactive HSV disc (hue + saturation); brightness comes from the value slider.
// Dragging anywhere on the disc sets the hue/saturation of the selected shape. The disc is cached and only
// re-rendered when its size or the value bucket changes.
func (s *AppState) colorWheel(gtx C) D {
	side := gtx.Dp(150)
	if mx := gtx.Constraints.Max.X; mx > 0 && side > mx {
		side = mx
	}
	gtx.Constraints = layout.Exact(image.Pt(side, side))

	vb := int(s.pickV*20 + 0.5)
	if vb < 0 {
		vb = 0
	}
	if vb > 20 {
		vb = 20
	}
	if !s.colorWheelBuilt || s.colorWheelSize != side || s.colorWheelV != vb {
		s.colorWheelOp = paint.NewImageOp(colorWheelImage(side, float64(vb)/20))
		s.colorWheelSize, s.colorWheelV, s.colorWheelBuilt = side, vb, true
	}
	s.colorWheelOp.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)

	// cursor ring at (hue angle, saturation radius)
	r := float64(side) / 2
	ang := s.pickH * 2 * math.Pi
	s.drawWheelCursor(gtx, int(r+s.pickS*r*math.Cos(ang)+0.5), int(r+s.pickS*r*math.Sin(ang)+0.5))

	area := clip.Rect(image.Rectangle{Max: image.Pt(side, side)}).Push(gtx.Ops)
	event.Op(gtx.Ops, &s.colorWheelTag)
	pointer.CursorCrosshair.Add(gtx.Ops)
	area.Pop()
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.colorWheelTag, Kinds: pointer.Press | pointer.Drag | pointer.Release})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press, pointer.Drag:
			s.setWheelFromPoint(pe.Position, side)
		case pointer.Release:
			if s.selValid() {
				s.pushRecentColor(colorFromShape(s.EditShapes[s.EditSel]))
			}
		}
	}
	return D{Size: image.Pt(side, side)}
}

// drawWheelCursor draws the dark-outlined white ring marking the current hue/saturation on the disc.
func (s *AppState) drawWheelCursor(gtx C, x, y int) {
	rr := gtx.Dp(6)
	ring := image.Rect(x-rr, y-rr, x+rr, y+rr)
	drawRectBorderW(gtx, ring, color.NRGBA{A: 200}, 3)
	drawRectBorderW(gtx, ring, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, 2)
}

// setWheelFromPoint maps a disc-local pointer position to hue (angle) + saturation (radius) and applies it.
func (s *AppState) setWheelFromPoint(p f32.Point, side int) {
	r := float64(side) / 2
	dx, dy := float64(p.X)-r, float64(p.Y)-r
	ang := math.Atan2(dy, dx)
	if ang < 0 {
		ang += 2 * math.Pi
	}
	sat := math.Hypot(dx, dy) / r
	if sat > 1 {
		sat = 1
	}
	s.pickH = ang / (2 * math.Pi)
	s.pickS = sat
	s.applyHSV()
}

// applyHSV writes pickH/pickS/pickV (keeping the existing alpha) to the selected shape and syncs the RGB
// sliders so the two representations stay in step.
func (s *AppState) applyHSV() {
	if !s.selValid() {
		return
	}
	c := hsvToRGB(s.pickH*360, s.pickS, s.pickV)
	sh := &s.EditShapes[s.EditSel]
	if len(sh.Color) < 4 {
		nc := make([]int, 4)
		nc[3] = 255
		copy(nc, sh.Color)
		sh.Color = nc
	}
	sh.Color[0], sh.Color[1], sh.Color[2] = int(c.R), int(c.G), int(c.B)
	s.pickR.Value, s.pickG.Value, s.pickB.Value = float32(c.R)/255, float32(c.G)/255, float32(c.B)/255
	s.markEditDirty()
}

// handleColorPicker reacts to the brightness (value) slider; the disc itself is handled in colorWheel
// during layout.
func (s *AppState) handleColorPicker(gtx C) {
	if !s.selValid() {
		return
	}
	if math.Abs(float64(s.pickVf.Value)-s.pickV) > 0.0015 {
		s.pickV = float64(s.pickVf.Value)
		s.applyHSV()
	}
}

// brightnessSlider is the value (V) slider beneath the disc.
func (s *AppState) brightnessSlider(gtx C) D {
	th := s.Th
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(14)
			return th.Lbl(gtx, 12, "V", th.TextDim)
		}),
		layout.Rigid(GapH(6).Layout),
		layout.Flexed(1, func(gtx C) D {
			sl := material.Slider(th.M, &s.pickVf)
			sl.Color = th.Accent
			return sl.Layout(gtx)
		}),
	)
}
