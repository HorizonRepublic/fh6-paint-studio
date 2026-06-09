package ui

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/widget/material"
)

// Hint is a small "(?)" help icon that shows a wrapped tooltip while hovered.
type Hint struct {
	hovered bool
}

// Layout draws the icon and, while hovered, a deferred tooltip card with help text.
func (h *Hint) Layout(gtx C, th *Theme, help string) D {
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: h, Kinds: pointer.Enter | pointer.Leave})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok {
			switch pe.Kind {
			case pointer.Enter:
				h.hovered = true
			case pointer.Leave:
				h.hovered = false
			}
		}
	}

	dims := th.Lbl(gtx, 13, "(?)", th.TextDim)

	area := clip.Rect{Max: dims.Size}.Push(gtx.Ops)
	event.Op(gtx.Ops, h)
	area.Pop()

	if h.hovered {
		macro := op.Record(gtx.Ops)
		off := op.Offset(image.Pt(0, dims.Size.Y+4)).Push(gtx.Ops)
		drawTooltip(gtx, th, help)
		off.Pop()
		op.Defer(gtx.Ops, macro.Stop())
	}
	return dims
}

func drawTooltip(gtx C, th *Theme, text string) D {
	gtx.Constraints.Min = image.Point{}
	gtx.Constraints.Max.X = gtx.Dp(260)
	return th.CardBg(gtx, th.SurfaceHi, 8, func(gtx C) D {
		l := material.Label(th.M, 12, text)
		l.Color = th.Text
		return l.Layout(gtx)
	})
}
