package ui

import (
	"image"
	"path/filepath"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"fh6-paint-studio/internal/i18n"
	"fh6-paint-studio/internal/preset"
)

// sourcePanel is the left column: a scrollable Source + Settings stack with the primary
// Generate / Stop action PINNED at the bottom (so configure -> generate is one column and the
// action is always visible even when the settings scroll). While a generation is running the
// scrollable settings are disabled (inert + dimmed) so they can't change mid-run, but the
// Stop button below stays active.
func (s *AppState) sourcePanel(gtx C) D {
	th := s.Th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx C) D {
			g := gtx
			if s.Phase == PhaseRunning {
				g = g.Disabled()
			}
			items := []layout.Widget{
				func(gtx C) D { return th.Card(gtx, s.sourceCard) },
				GapV(th.Gap).Layout,
				func(gtx C) D { return th.Card(gtx, s.settingsCard) },
			}
			return material.List(th.M, &s.LeftScroll).Layout(g, len(items), func(gtx C, i int) D {
				return items[i](gtx)
			})
		}),
		layout.Rigid(GapV(th.Gap).Layout),
		layout.Rigid(s.generateButton),
	)
}

// generateButton is the primary action pinned at the bottom of the left column: Generate (or
// "Generate again" once a run finished), swapping to a red Stop while a run is active.
func (s *AppState) generateButton(gtx C) D {
	th := s.Th
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	if s.Phase == PhaseRunning {
		return th.DangerButton(gtx, &s.CancelBtn, i18n.T("common.stop"))
	}
	label := i18n.T("common.generate")
	if s.Phase == PhaseDone {
		label = i18n.T("common.generate_again")
	}
	return th.PrimaryButton(gtx, &s.GenBtn, label, s.Source != nil)
}

func (s *AppState) sourceCard(gtx C) D {
	th := s.Th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Title(gtx, i18n.T("source.title")) }),
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(s.thumbnail),
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.SecondaryButton(gtx, &s.OpenBtn, i18n.T("source.open"), true)
		}),
		layout.Rigid(GapV(6).Layout),
		layout.Rigid(func(gtx C) D {
			name := i18n.T("source.no_image")
			if s.ImgPath != "" {
				name = filepath.Base(s.ImgPath)
			}
			return th.Dim(gtx, name)
		}),
		layout.Rigid(s.recentList),
	)
}

// recentList shows the recently-opened images as clickable filenames (click to reopen). The clicks are
// handled in the main loop (RecentBtns parallel to Recent). Hidden when empty.
func (s *AppState) recentList(gtx C) D {
	th := s.Th
	if len(s.Recent) == 0 {
		return D{}
	}
	children := []layout.FlexChild{
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 11, i18n.T("source.recent"), th.TextDim) }),
		layout.Rigid(GapV(4).Layout),
	}
	for i := range s.Recent {
		if i >= len(s.RecentBtns) || i >= 6 {
			break
		}
		i := i
		name := filepath.Base(s.Recent[i])
		children = append(children, layout.Rigid(func(gtx C) D {
			return material.Clickable(gtx, &s.RecentBtns[i], func(gtx C) D {
				return layout.Inset{Top: 2, Bottom: 2}.Layout(gtx, func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return th.Lbl(gtx, 13, name, th.Text)
				})
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// thumbnail draws the loaded source fit inside a rounded surface box, or a placeholder.
func (s *AppState) thumbnail(gtx C) D {
	th := s.Th
	w := gtx.Constraints.Max.X
	h := gtx.Dp(130)
	sz := image.Pt(w, h)
	fillRRect(gtx, th.SurfaceHi, sz, 8)
	gtx.Constraints = layout.Exact(sz)
	if s.Source != nil {
		defer clip.UniformRRect(image.Rectangle{Max: sz}, 8).Push(gtx.Ops).Pop()
		widget.Image{Src: s.SourceOp, Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
		return D{Size: sz}
	}
	layout.Center.Layout(gtx, func(gtx C) D { return th.Dim(gtx, i18n.T("source.thumb_placeholder")) })
	return D{Size: sz}
}

func (s *AppState) settingsCard(gtx C) D {
	s.syncSettings()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		// Step 1 — pick the content style (the only required choice).
		layout.Rigid(func(gtx C) D { return s.stepHeader(gtx, "1", i18n.T("common.step_what")) }),
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(s.presetCardsSection),
		layout.Rigid(GapV(16).Layout),
		// Step 2 — the two creative dials (detail amount + line/fill for hybrids).
		layout.Rigid(func(gtx C) D { return s.stepHeader(gtx, "2", i18n.T("adjust.title")) }),
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(s.budgetRow),
		layout.Rigid(func(gtx C) D {
			if !preset.IsHybridMode(s.baseMode) { // the Artist line/fill dial only applies to the hybrids
				return D{}
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(GapV(12).Layout),
				layout.Rigid(s.artistBlock),
			)
		}),
		layout.Rigid(GapV(16).Layout),
		// Everything technical, behind one disclosure.
		layout.Rigid(s.advancedSection),
	)
}

// stepHeader is a numbered section heading (accent chip + title) — the visual 1·2 spine that walks a
// first-time user down the panel.
func (s *AppState) stepHeader(gtx C, num, title string) D {
	th := s.Th
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Background{}.Layout(gtx,
				func(gtx C) D { fillRRect(gtx, th.Accent, gtx.Constraints.Min, 5); return D{Size: gtx.Constraints.Min} },
				func(gtx C) D {
					return layout.Inset{Top: 1, Bottom: 1, Left: 6, Right: 6}.Layout(gtx, func(gtx C) D {
						return th.Lbl(gtx, 13, num, th.OnAccent)
					})
				},
			)
		}),
		layout.Rigid(GapH(8).Layout),
		layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 15, title, th.Text) }),
	)
}

// artistBlock is the semi-expert "Artist" tier shown for the hybrid presets: ONE friendly knob for the
// line/fill balance (the single artistic decision), set apart from the technical Expert panel. The total
// shape budget above is split into FDoG ink lines + geometrize fill by this ratio.
func (s *AppState) artistBlock(gtx C) D {
	th := s.Th
	pct := int(s.InkRatioValue()*100 + 0.5)
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return th.CardBg(gtx, th.SurfaceHi, 10, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 13, i18n.T("adjust.artist"), th.Accent) }),
					layout.Rigid(GapH(6).Layout),
					layout.Rigid(func(gtx C) D {
						return s.InkHint.Layout(gtx, th, i18n.T("hint.artist"))
					}),
					layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
					layout.Rigid(func(gtx C) D { return th.Dim(gtx, i18n.T("adjust.lines_pct", pct)) }),
				)
			}),
			layout.Rigid(GapV(6).Layout),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return th.Dim(gtx, i18n.T("adjust.fill")) }),
					layout.Flexed(1, func(gtx C) D {
						sl := material.Slider(th.M, &s.InkRatio)
						sl.Color = th.Accent
						return layout.UniformInset(6).Layout(gtx, sl.Layout)
					}),
					layout.Rigid(func(gtx C) D { return th.Dim(gtx, i18n.T("adjust.lines")) }),
				)
			}),
			layout.Rigid(GapV(6).Layout),
			layout.Rigid(func(gtx C) D {
				l := material.Label(th.M, 11, i18n.T("adjust.artist_explain"))
				l.Color = th.TextDim
				return l.Layout(gtx)
			}),
		)
	})
}

// advancedSection is the single "Advanced settings" disclosure — the one place every technical control
// lives (the old "Custom (advanced)" + "Expert mode" two-step is collapsed into it). Opening it engages
// the expert overrides; collapsed, the chosen preset's tuned defaults are used as-is.
func (s *AppState) advancedSection(gtx C) D {
	th := s.Th
	// Gaussian is a NICHE mode that bypasses the greedy entirely (soft glows trained jointly), so none
	// of the greedy/polish toggles apply to it — show a short explanation instead of dead controls.
	if s.baseMode == "gaussian" || s.Mode.Value() == "gaussian" {
		l := material.Label(th.M, 12, i18n.T("hint.gaussian"))
		l.Color = th.TextDim
		return l.Layout(gtx)
	}
	if s.AdvClick.Clicked(gtx) {
		s.AdvOpen = !s.AdvOpen
	}
	s.Expert.Value = s.AdvOpen // opening Advanced engages the expert overrides; closed = preset defaults
	arrow := "▸"
	if s.AdvOpen {
		arrow = "▾"
	}
	head := func(gtx C) D {
		return material.Clickable(gtx, &s.AdvClick, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.Lbl(gtx, 13, arrow+"  "+i18n.T("advanced.settings"), th.TextDim)
		})
	}
	if !s.AdvOpen {
		return head(gtx)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(head),
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(s.shapeKindsRow),
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(func(gtx C) D {
			return s.toggleRow(gtx, &s.SourceRes, i18n.T("advanced.source_res"), &s.SourceResHint,
				i18n.T("hint.source_res"))
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			return s.toggleRow(gtx, &s.KeepInside, i18n.T("advanced.keep_inside"), &s.KeepInsideHint,
				i18n.T("hint.keep_inside"))
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			return s.toggleRow(gtx, &s.Mono, i18n.T("advanced.mono"), &s.MonoHint,
				i18n.T("hint.mono"))
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			return s.toggleRow(gtx, &s.Economy, i18n.T("advanced.economy"), &s.EconomyHint,
				i18n.T("hint.economy"))
		}),
		layout.Rigid(GapV(12).Layout),
		layout.Rigid(s.expertBlock),
	)
}

// budgetRow is the shape-budget control: a label + manual number entry over a full-width slider.
func (s *AppState) budgetRow(gtx C) D {
	th := s.Th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return th.Dim(gtx, i18n.T("adjust.detail")) }),
				layout.Rigid(GapH(6).Layout),
				layout.Rigid(func(gtx C) D {
					return s.BudgetHint.Layout(gtx, th, i18n.T("hint.budget"))
				}),
				layout.Flexed(1, func(gtx C) D { return D{} }),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(70), gtx.Dp(70)
					return th.editorBox(gtx, &s.BudgetEd, "1000")
				}),
			)
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			sl := material.Slider(th.M, &s.Budget)
			sl.Color = th.Accent
			return sl.Layout(gtx)
		}),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 11, i18n.T("adjust.simpler"), th.TextDim) }),
				layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
				layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 11, i18n.T("adjust.more_detail"), th.TextDim) }),
			)
		}),
	)
}

// toggleRow is a checkbox followed by a hover help hint.
func (s *AppState) toggleRow(gtx C, b *widget.Bool, label string, h *Hint, help string) D {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return s.checkbox(gtx, b, label) }),
		layout.Rigid(GapH(6).Layout),
		layout.Rigid(func(gtx C) D { return h.Layout(gtx, s.Th, help) }),
	)
}

// fieldHint stacks a label (with a help hint) above a control.
func (s *AppState) fieldHint(gtx C, label string, h *Hint, help string, w layout.Widget) D {
	th := s.Th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return th.Dim(gtx, label) }),
				layout.Rigid(GapH(6).Layout),
				layout.Rigid(func(gtx C) D { return h.Layout(gtx, th, help) }),
			)
		}),
		layout.Rigid(GapV(4).Layout),
		layout.Rigid(w),
	)
}

func (s *AppState) checkbox(gtx C, b *widget.Bool, label string) D {
	cb := material.CheckBox(s.Th.M, b, label)
	cb.Color = s.Th.Text
	cb.IconColor = s.Th.Accent
	cb.TextSize = 13
	return cb.Layout(gtx)
}

// editorBox wraps a single-line editor in a rounded surface box.
func (t *Theme) editorBox(gtx C, ed *widget.Editor, hint string) D {
	return t.editorBoxErr(gtx, ed, hint, false)
}

// editorBoxErr is editorBox with an optional red error border (e.g. a required field left empty).
func (t *Theme) editorBoxErr(gtx C, ed *widget.Editor, hint string, errState bool) D {
	return layout.Background{}.Layout(gtx,
		func(gtx C) D {
			sz := gtx.Constraints.Min
			if errState {
				borderRRect(gtx, t.Bad, t.SurfaceHi, sz, 8, 2)
			} else {
				fillRRect(gtx, t.SurfaceHi, sz, 8)
			}
			return D{Size: sz}
		},
		func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.UniformInset(9).Layout(gtx, func(gtx C) D {
				e := material.Editor(t.M, ed, hint)
				e.Color = t.Text
				e.HintColor = t.TextDim
				return e.Layout(gtx)
			})
		},
	)
}
