package ui

import (
	"image"

	"fh6-paint-studio/internal/i18n"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

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
						layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 13, i18n.T("status.run_as_admin"), th.Text) }),
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
		return i18n.T("status.generating")
	case PhaseDone:
		return i18n.T("status.done", group(s.Stats.Shapes), fmtDur(s.Stats.Elapsed))
	case PhaseError:
		return i18n.T("status.error")
	default:
		if s.Source == nil {
			return i18n.T("status.ready_open")
		}
		return i18n.T("status.ready")
	}
}
