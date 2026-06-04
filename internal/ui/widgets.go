package ui

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// ---- fills -----------------------------------------------------------------

// fillRRect fills a rounded rect of size sz at the origin with col.
func fillRRect(gtx C, col color.NRGBA, sz image.Point, radius int) {
	rr := clip.UniformRRect(image.Rectangle{Max: sz}, radius)
	paint.FillShape(gtx.Ops, col, rr.Op(gtx.Ops))
}

// borderRRect draws a rounded rect with a width-thick border in `border`, the interior filled `inner`.
// It needs no op.Offset — the inner fill is an inset rounded rect (UniformRRect accepts a non-zero Min).
func borderRRect(gtx C, border, inner color.NRGBA, sz image.Point, radius, width int) {
	fillRRect(gtx, border, sz, radius)
	in := clip.UniformRRect(image.Rect(width, width, sz.X-width, sz.Y-width), radius)
	paint.FillShape(gtx.Ops, inner, in.Op(gtx.Ops))
}

// ---- card ------------------------------------------------------------------

// Card draws a rounded, hairline-bordered surface panel wrapping w (with inner Pad).
func (t *Theme) Card(gtx C, w layout.Widget) D {
	return t.CardBg(gtx, t.Surface, t.Pad, w)
}

// CardBg is Card with an explicit surface color and inner padding.
func (t *Theme) CardBg(gtx C, bg color.NRGBA, pad unit.Dp, w layout.Widget) D {
	const radius = 10
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			sz := gtx.Constraints.Min
			fillRRect(gtx, t.Border, sz, radius) // 1px border underlay
			in := image.Rectangle{Min: image.Pt(1, 1), Max: image.Pt(sz.X-1, sz.Y-1)}
			rr := clip.UniformRRect(in, radius-1)
			paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
			return D{Size: sz}
		}),
		layout.Stacked(func(gtx C) D {
			return layout.UniformInset(pad).Layout(gtx, w)
		}),
	)
}

// ---- text ------------------------------------------------------------------

// Lbl is a colored label at a given size.
func (t *Theme) Lbl(gtx C, size unit.Sp, txt string, col color.NRGBA) D {
	l := material.Label(t.M, size, txt)
	l.Color = col
	return l.Layout(gtx)
}

// Title is a bold section heading.
func (t *Theme) Title(gtx C, txt string) D {
	l := material.Label(t.M, 16, txt)
	l.Color = t.Text
	l.Font.Weight = font.Bold
	return l.Layout(gtx)
}

// Body is normal primary text; Dim is muted secondary text.
func (t *Theme) Body(gtx C, txt string) D { return t.Lbl(gtx, 14, txt, t.Text) }
func (t *Theme) Dim(gtx C, txt string) D  { return t.Lbl(gtx, 13, txt, t.TextDim) }

// ---- buttons ---------------------------------------------------------------

// PrimaryButton is the teal CTA; dimmed when !enabled.
func (t *Theme) PrimaryButton(gtx C, btn *widget.Clickable, label string, enabled bool) D {
	b := material.Button(t.M, btn, label)
	b.CornerRadius = 8
	b.Inset = layout.Inset{Top: 11, Bottom: 11, Left: 16, Right: 16}
	if enabled {
		b.Background, b.Color = t.Accent, t.OnAccent
	} else {
		b.Background, b.Color = t.AccentDim, t.TextDim
	}
	return b.Layout(gtx)
}

// DangerButton is a red destructive button (e.g. Stop).
func (t *Theme) DangerButton(gtx C, btn *widget.Clickable, label string) D {
	b := material.Button(t.M, btn, label)
	b.CornerRadius = 8
	b.Inset = layout.Inset{Top: 11, Bottom: 11, Left: 16, Right: 16}
	b.Background, b.Color = t.Bad, t.Text
	return b.Layout(gtx)
}

// AccentButton is a teal CTA at the SecondaryButton (compact) size — for emphasis where a full-size
// PrimaryButton would dominate, e.g. an inline Save sitting beside other compact row buttons.
func (t *Theme) AccentButton(gtx C, btn *widget.Clickable, label string) D {
	b := material.Button(t.M, btn, label)
	b.CornerRadius = 8
	b.Inset = layout.Inset{Top: 9, Bottom: 9, Left: 14, Right: 14}
	b.Background, b.Color = t.Accent, t.OnAccent
	return b.Layout(gtx)
}

// SecondaryButton is a low-emphasis surface button.
func (t *Theme) SecondaryButton(gtx C, btn *widget.Clickable, label string, enabled bool) D {
	b := material.Button(t.M, btn, label)
	b.CornerRadius = 8
	b.Inset = layout.Inset{Top: 9, Bottom: 9, Left: 14, Right: 14}
	b.Background = t.SurfaceHi
	if enabled {
		b.Color = t.Text
	} else {
		b.Color = t.TextDim
	}
	return b.Layout(gtx)
}

// StatusPill is a non-interactive compact pill (the SecondaryButton height) showing a coloured status
// label — used in place of a row button for a transient inject result (tick: Injected / cross: Failed) so the
// row keeps its layout while the result lingers.
func (t *Theme) StatusPill(gtx C, label string, fg color.NRGBA) D {
	return layout.Background{}.Layout(gtx,
		func(gtx C) D {
			fillRRect(gtx, t.SurfaceHi, gtx.Constraints.Min, 8)
			return D{Size: gtx.Constraints.Min}
		},
		func(gtx C) D {
			return layout.Inset{Top: 9, Bottom: 9, Left: 14, Right: 14}.Layout(gtx, func(gtx C) D {
				return t.Lbl(gtx, 14, label, fg)
			})
		},
	)
}

// BusyPill is a compact pill with an animated spinner + label, shown on a row's button while its
// inject is in flight (the spinner self-animates via material.Loader).
func (t *Theme) BusyPill(gtx C, label string) D {
	return layout.Background{}.Layout(gtx,
		func(gtx C) D {
			fillRRect(gtx, t.SurfaceHi, gtx.Constraints.Min, 8)
			return D{Size: gtx.Constraints.Min}
		},
		func(gtx C) D {
			return layout.Inset{Top: 9, Bottom: 9, Left: 14, Right: 14}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						sz := gtx.Dp(16)
						gtx.Constraints = layout.Exact(image.Pt(sz, sz))
						l := material.Loader(t.M)
						l.Color = t.Accent
						return l.Layout(gtx)
					}),
					layout.Rigid(GapH(8).Layout),
					layout.Rigid(func(gtx C) D { return t.Lbl(gtx, 14, label, t.TextDim) }),
				)
			})
		},
	)
}

// ---- progress --------------------------------------------------------------

// Progress is a rounded progress bar filling frac (0..1) of the available width.
func (t *Theme) Progress(gtx C, frac float64) D {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	w := gtx.Constraints.Max.X
	h := gtx.Dp(10)
	r := h / 2
	track := clip.UniformRRect(image.Rect(0, 0, w, h), r)
	paint.FillShape(gtx.Ops, t.SurfaceHi, track.Op(gtx.Ops))
	if fw := int(float64(w) * frac); fw > 0 {
		if fw < h {
			fw = h
		}
		fill := clip.UniformRRect(image.Rect(0, 0, fw, h), r)
		paint.FillShape(gtx.Ops, t.Accent, fill.Op(gtx.Ops))
	}
	return D{Size: image.Pt(w, h)}
}

// ProgressIndeterminate draws a segment sweeping across the track — for the post-greedy phases
// (polish/standout) that have no shape counter, so the bar shows live activity
// instead of sitting stuck at 100%. phase in [0,1) positions the segment; the caller animates it
// (e.g. from elapsed time, with periodic invalidation).
func (t *Theme) ProgressIndeterminate(gtx C, phase float64) D {
	w := gtx.Constraints.Max.X
	h := gtx.Dp(10)
	r := h / 2
	paint.FillShape(gtx.Ops, t.SurfaceHi, clip.UniformRRect(image.Rect(0, 0, w, h), r).Op(gtx.Ops))
	seg := w / 3
	if seg < h {
		seg = h
	}
	travel := w + seg
	x0 := int(phase*float64(travel)) - seg
	x1 := x0 + seg
	if x0 < 0 {
		x0 = 0
	}
	if x1 > w {
		x1 = w
	}
	if x1 > x0 {
		paint.FillShape(gtx.Ops, t.Accent, clip.UniformRRect(image.Rect(x0, 0, x1, h), r).Op(gtx.Ops))
	}
	return D{Size: image.Pt(w, h)}
}

// ---- sparkline -------------------------------------------------------------

// Sparkline draws data as an accent polyline over a faint baseline, full available width.
func (t *Theme) Sparkline(gtx C, data []float64, height unit.Dp) D {
	w := gtx.Constraints.Max.X
	h := gtx.Dp(height)
	sz := image.Pt(w, h)

	// faint baseline
	base := clip.Rect(image.Rect(0, h-1, w, h))
	paint.FillShape(gtx.Ops, t.Border, base.Op())

	if len(data) >= 2 && w > 1 {
		min, max := data[0], data[0]
		for _, v := range data {
			if v < min {
				min = v
			}
			if v > max {
				max = v
			}
		}
		span := max - min
		var p clip.Path
		p.Begin(gtx.Ops)
		for i, v := range data {
			x := float32(i) / float32(len(data)-1) * float32(w)
			norm := 0.5
			if span > 0 {
				norm = (v - min) / span
			}
			// higher value -> higher on screen (top); error decreasing trends downward
			y := float32(h) - 2 - float32(norm)*float32(h-4)
			if i == 0 {
				p.MoveTo(f32.Pt(x, y))
			} else {
				p.LineTo(f32.Pt(x, y))
			}
		}
		paint.FillShape(gtx.Ops, t.Accent, clip.Stroke{Path: p.End(), Width: float32(gtx.Dp(2))}.Op())
	}
	return D{Size: sz}
}

// ---- misc ------------------------------------------------------------------

// Divider is a 1px horizontal rule.
func (t *Theme) Divider(gtx C) D {
	w := gtx.Constraints.Max.X
	paint.FillShape(gtx.Ops, t.Border, clip.Rect(image.Rect(0, 0, w, 1)).Op())
	return D{Size: image.Pt(w, 1)}
}

// GapV is vertical whitespace of n dp.
func GapV(n unit.Dp) layout.Spacer { return layout.Spacer{Height: n} }

// GapH is horizontal whitespace of n dp.
func GapH(n unit.Dp) layout.Spacer { return layout.Spacer{Width: n} }
