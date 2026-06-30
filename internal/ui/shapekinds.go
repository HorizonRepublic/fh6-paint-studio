package ui

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget/material"

	"fh6-paint-studio/internal/i18n"
)

// shapeChipKinds is the display order of the "Used shapes" icon row — circle, square, triangle — each
// mapped to its engine kind name. The order differs from preset.KindNames on purpose: this is the UI
// presentation; KindsSel stays the single source of truth.
var shapeChipKinds = []string{"ellipse", "rectangle", "triangle"}

// toggleShapeKind flips one kind in the shared KindsSel selection, refusing to untick the last one —
// the engine needs at least one primitive, else it silently falls back to ellipse-only.
func (s *AppState) toggleShapeKind(name string) {
	if s.KindsSel.IsOn(name) {
		if s.KindsSel.OnCount() > 1 {
			s.KindsSel.SetOn(name, false)
		}
		return
	}
	s.KindsSel.SetOn(name, true)
}

// shapeKindsRow is the friendly "Used shapes" picker at the top of Advanced settings. It is a second
// view on KindsSel (the expert weight fields drive the same state), so the default — all on — leaves
// generation byte-identical. Advanced hides itself for gaussian, which ignores shape kinds.
func (s *AppState) shapeKindsRow(gtx C) D {
	th := s.Th
	for i := range s.ShapeChips {
		if s.ShapeChips[i].Clicked(gtx) {
			s.toggleShapeKind(shapeChipKinds[i])
		}
	}
	header := layout.Rigid(func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return th.Dim(gtx, i18n.T("shapes.used")) }),
			layout.Rigid(GapH(6).Layout),
			layout.Rigid(func(gtx C) D {
				return s.KindsHint.Layout(gtx, th,
					i18n.T("hint.used_shapes"))
			}),
		)
	})
	chips := make([]layout.FlexChild, 0, 2*len(s.ShapeChips)-1)
	for i := range s.ShapeChips {
		if i > 0 {
			chips = append(chips, layout.Rigid(GapH(8).Layout))
		}
		i := i
		chips = append(chips, layout.Flexed(1, func(gtx C) D { return s.shapeChip(gtx, i) }))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		header,
		layout.Rigid(GapV(6).Layout),
		layout.Rigid(func(gtx C) D { return layout.Flex{}.Layout(gtx, chips...) }),
	)
}

// shapeChip is one selectable tile: filled accent icon + accent border when its kind is on, a muted
// outline icon on a plain tile when off. The whole tile is the click target.
func (s *AppState) shapeChip(gtx C, i int) D {
	th := s.Th
	name := shapeChipKinds[i]
	on := s.KindsSel.IsOn(name)
	return material.Clickable(gtx, &s.ShapeChips[i], func(gtx C) D {
		return layout.Background{}.Layout(gtx,
			func(gtx C) D {
				sz := gtx.Constraints.Min
				if on {
					borderRRect(gtx, th.Accent, th.SurfaceHi, sz, 8, gtx.Dp(2))
				} else {
					fillRRect(gtx, th.SurfaceHi, sz, 8)
				}
				return D{Size: sz}
			},
			func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.UniformInset(11).Layout(gtx, func(gtx C) D {
					col := th.TextDim
					if on {
						col = th.Accent
					}
					return layout.Center.Layout(gtx, func(gtx C) D { return drawShapeIcon(gtx, name, col, on) })
				})
			},
		)
	})
}

// drawShapeIcon paints one primitive glyph centred in an 18dp box: solid when filled, a stroked
// outline otherwise. Shapes are inset by the stroke half-width so the outline is never clipped.
func drawShapeIcon(gtx C, kind string, col color.NRGBA, filled bool) D {
	s := gtx.Dp(18)
	sw := float32(gtx.Dp(2))
	pad := gtx.Dp(1)
	switch kind {
	case "ellipse":
		e := clip.Ellipse{Min: image.Pt(pad, pad), Max: image.Pt(s-pad, s-pad)}
		if filled {
			paint.FillShape(gtx.Ops, col, e.Op(gtx.Ops))
		} else {
			paint.FillShape(gtx.Ops, col, clip.Stroke{Path: e.Path(gtx.Ops), Width: sw}.Op())
		}
	case "rectangle":
		r := clip.UniformRRect(image.Rect(pad, pad, s-pad, s-pad), gtx.Dp(2))
		if filled {
			paint.FillShape(gtx.Ops, col, r.Op(gtx.Ops))
		} else {
			paint.FillShape(gtx.Ops, col, clip.Stroke{Path: r.Path(gtx.Ops), Width: sw}.Op())
		}
	case "triangle":
		fs, fp := float32(s), float32(pad)
		var p clip.Path
		p.Begin(gtx.Ops)
		p.MoveTo(f32.Pt(fs/2, fp))
		p.LineTo(f32.Pt(fs-fp, fs-fp))
		p.LineTo(f32.Pt(fp, fs-fp))
		p.Close()
		spec := p.End()
		if filled {
			paint.FillShape(gtx.Ops, col, clip.Outline{Path: spec}.Op())
		} else {
			paint.FillShape(gtx.Ops, col, clip.Stroke{Path: spec, Width: sw}.Op())
		}
	}
	return D{Size: image.Pt(s, s)}
}

// drawArrowIcon draws a curved undo/redo arrow — a loop over the top whose head swoops down one side, in
// an 18dp box. left=true is undo (head curls down the left), left=false is redo (mirrored to the right).
// drawArrowIcon draws a filled curved undo/redo arrow (the Material "undo" glyph) in an 18dp box.
// left=true is undo; left=false mirrors it horizontally into redo.
func drawArrowIcon(gtx C, left bool, col color.NRGBA) D {
	box := gtx.Dp(18)
	sc := float64(box) / 24 // the path is authored on a 24×24 grid
	tf := func(x, y float64) f32.Point {
		if !left {
			x = 24 - x
		}
		return f32.Pt(float32(x*sc), float32(y*sc))
	}
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(tf(12.5, 8))
	p.CubeTo(tf(9.85, 8), tf(7.45, 8.99), tf(5.6, 10.6))
	p.LineTo(tf(2, 7))
	p.LineTo(tf(2, 16))
	p.LineTo(tf(11, 16))
	p.LineTo(tf(7.38, 12.38))
	p.CubeTo(tf(8.77, 11.22), tf(10.54, 10.5), tf(12.5, 10.5))
	p.CubeTo(tf(16.04, 10.5), tf(19.05, 12.81), tf(20.1, 16))
	p.LineTo(tf(22.47, 15.22))
	p.CubeTo(tf(21.08, 11.03), tf(17.15, 8), tf(12.5, 8))
	p.Close()
	paint.FillShape(gtx.Ops, col, clip.Outline{Path: p.End()}.Op())
	return D{Size: image.Pt(box, box)}
}
