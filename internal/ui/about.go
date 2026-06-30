package ui

import (
	"image"
	"image/color"
	"strings"

	"fh6-paint-studio/internal/i18n"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

func (s *AppState) aboutChip(gtx C) D {
	th := s.Th
	label := "v" + strings.TrimPrefix(s.Version, "v")
	if s.Version == "" || s.Version == "dev" {
		label = i18n.T("about.dev")
	}
	return material.Clickable(gtx, &s.AboutBtn, func(gtx C) D {
		return layout.Inset{Left: 8, Right: 8, Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 13, label, th.TextDim) }),
				layout.Rigid(func(gtx C) D {
					if !s.HasUpdateBadge() {
						return D{}
					}
					return layout.Inset{Left: 6}.Layout(gtx, func(gtx C) D {
						d := gtx.Dp(7)
						paint.FillShape(gtx.Ops, th.Accent, clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
						return D{Size: image.Pt(d, d)}
					})
				}),
			)
		})
	})
}

func (s *AppState) aboutOverlay(gtx C) D {
	th := s.Th
	sz := gtx.Constraints.Max
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			return s.AboutClose.Layout(gtx, func(gtx C) D { // scrim: click outside the card to dismiss
				paint.FillShape(gtx.Ops, color.NRGBA{A: 200}, clip.Rect{Max: sz}.Op())
				return D{Size: sz}
			})
		}),
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min = sz
			return layout.Center.Layout(gtx, func(gtx C) D {
				w := gtx.Dp(460)
				if w > sz.X-32 {
					w = sz.X - 32
				}
				gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
				gtx.Constraints.Max.Y = sz.Y - 64
				return s.AboutCardSink.Layout(gtx, func(gtx C) D {
					return th.Card(gtx, s.aboutCard)
				})
			})
		}),
	)
}

func (s *AppState) aboutCard(gtx C) D {
	th := s.Th
	ver := "v" + strings.TrimPrefix(s.Version, "v")
	if s.Version == "" || s.Version == "dev" {
		ver = i18n.T("about.dev_build")
	}
	gap := func(n unit.Dp) layout.FlexChild { return layout.Rigid(GapV(n).Layout) }

	rows := []layout.FlexChild{
		layout.Rigid(func(gtx C) D { return th.Title(gtx, i18n.T("app.title")) }),
		layout.Rigid(func(gtx C) D { return th.Dim(gtx, ver+"  ·  "+s.BackendLabel) }),
		gap(12),
	}

	if s.Update != nil {
		lines := strings.Split(s.Update.Notes, "\n")
		rows = append(rows,
			layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 14, i18n.T("about.update_available")+s.Update.Version, th.Accent) }),
			gap(6),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Max.Y = gtx.Dp(220)
				return material.List(th.M, &s.AboutList).Layout(gtx, len(lines), func(gtx C, i int) D {
					return th.Lbl(gtx, 13, lines[i], th.TextDim)
				})
			}),
			gap(10),
			layout.Rigid(func(gtx C) D { return th.PrimaryButton(gtx, &s.DownloadBtn, i18n.T("about.download"), true) }),
			gap(12),
		)
	} else if s.UpdateCheckEnabled {
		status := i18n.T("about.up_to_date")
		if s.UpdateStatus != "" {
			status = s.UpdateStatus
		}
		rows = append(rows,
			layout.Rigid(func(gtx C) D { return th.Dim(gtx, status) }),
			gap(12),
		)
	}

	links := []layout.FlexChild{
		layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.GitHubBtn, i18n.T("about.github"), true) }),
		layout.Rigid(GapH(8).Layout),
		layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.NexusBtn, i18n.T("about.nexusmods"), true) }),
	}
	if s.UpdateCheckEnabled {
		links = append(links,
			layout.Rigid(GapH(8).Layout),
			layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.CheckNowBtn, i18n.T("about.check_now"), true) }),
		)
	}
	rows = append(rows,
		layout.Rigid(func(gtx C) D { return layout.Flex{}.Layout(gtx, links...) }),
		gap(12),
	)
	if s.UpdateCheckEnabled {
		rows = append(rows,
			layout.Rigid(func(gtx C) D {
				cb := material.CheckBox(th.M, &s.AutoUpdate, i18n.T("about.auto_update"))
				cb.Color, cb.IconColor, cb.TextSize = th.TextDim, th.Accent, 13
				return cb.Layout(gtx)
			}),
			gap(10),
		)
	}
	rows = append(rows,
		layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 12, i18n.T("about.license"), th.TextDim) }),
	)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}
