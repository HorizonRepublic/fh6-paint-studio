package ui

import (
	"fmt"
	"image"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// statusBar is the bottom bar: status text on the left, Export + Inject on the right.
func (s *AppState) statusBar(gtx C) D {
	th := s.Th
	return layout.Background{}.Layout(gtx,
		func(gtx C) D {
			sz := gtx.Constraints.Min
			paint.FillShape(gtx.Ops, th.Surface, clip.Rect{Max: sz}.Op())
			paint.FillShape(gtx.Ops, th.Border, clip.Rect{Max: image.Pt(sz.X, 1)}.Op())
			return D{Size: sz}
		},
		func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.UniformInset(10).Layout(gtx, func(gtx C) D {
				// Inject / Export / FH6-layers moved into the Library tab; admin moved to the top bar.
				// The status bar = the status line + the (persisted) "sound on finish" preference.
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D { return th.Dim(gtx, s.statusText()) }),
					layout.Rigid(func(gtx C) D {
						cb := material.CheckBox(th.M, &s.SoundOn, "Sound on finish")
						cb.Color, cb.IconColor, cb.TextSize = th.TextDim, th.Accent, 13
						return cb.Layout(gtx)
					}),
				)
			})
		},
	)
}

// adminButton is the UAC indicator shown when the app is not elevated; clicking it relaunches
// the app as administrator (handled in the main loop). FH6 memory injection usually needs admin.
func (s *AppState) adminButton(gtx C) D {
	th := s.Th
	return material.Clickable(gtx, &s.ElevateBtn, func(gtx C) D {
		return layout.Background{}.Layout(gtx,
			func(gtx C) D {
				fillRRect(gtx, th.SurfaceHi, gtx.Constraints.Min, 8)
				return D{Size: gtx.Constraints.Min}
			},
			func(gtx C) D {
				return layout.UniformInset(8).Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							if s.Shield != nil {
								sz := gtx.Dp(16)
								gtx.Constraints = layout.Exact(image.Pt(sz, sz))
								return widget.Image{Src: s.ShieldOp, Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
							}
							return th.Lbl(gtx, 13, "!", th.Bad)
						}),
						layout.Rigid(GapH(6).Layout),
						layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 13, "Run as admin", th.Text) }),
					)
				})
			},
		)
	})
}

func (s *AppState) statusText() string {
	if s.Toast != "" {
		return s.Toast
	}
	switch s.Phase {
	case PhaseRunning:
		return "Generating…"
	case PhaseDone:
		return fmt.Sprintf("Done — %s shapes in %s", group(s.Stats.Shapes), fmtDur(s.Stats.Elapsed))
	case PhaseError:
		return "Error — see log"
	default:
		if s.Source == nil {
			return "Ready — open an image to start"
		}
		return "Ready"
	}
}
