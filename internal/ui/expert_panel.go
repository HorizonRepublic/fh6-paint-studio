package ui

import (
	"fmt"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"fh6-paint-studio/internal/i18n"
)

// expertBlock is the unlocked generator panel shown when Expert mode is on. Controls are split into
// collapsible sub-sections so opening Expert lays out only the open groups (cheaper, smoother) instead
// of every control at once. Each control carries a concrete value (filled from the preset) and a hover
// hint stating what it does and whether it moves render time or quality; dependent controls grey out
// when their parent toggle is off.
func (s *AppState) expertBlock(gtx C) D {
	th := s.Th

	gap := func(n unit.Dp) layout.FlexChild { return layout.Rigid(GapV(n).Layout) }
	tog := func(b *widget.Bool, label string, h *Hint, help string) layout.FlexChild {
		return layout.Rigid(func(gtx C) D { return s.toggleRow(gtx, b, label, h, help) })
	}
	slider := func(label string, h *Hint, help string, sl *widget.Float, lo, hi float32, format string, offAtZero, disabled bool) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			return s.sliderRow(gtx, label, h, help, sl, lo, hi, format, offAtZero, disabled)
		})
	}
	editor := func(label string, h *Hint, help string, ed *widget.Editor, placeholder string, disabled bool) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			g := gtx
			if disabled {
				g = g.Disabled()
			}
			return s.fieldHint(g, label, h, help, func(gtx C) D { return th.editorBox(gtx, ed, placeholder) })
		})
	}
	vbox := func(children ...layout.FlexChild) layout.Widget {
		return func(gtx C) D { return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...) }
	}

	polishOff := !s.PolishOn.Value
	alphaOff := !s.Alpha.Value
	weightedOff := !s.WeightedOn.Value

	smoothness := vbox(
		tog(&s.PolishOn, i18n.T("expert.joint_polish"), &s.PolishHint,
			i18n.T("hint.polish")),
		gap(8),
		editor(i18n.T("expert.polish_iterations"), &s.PolishItersHint,
			i18n.T("hint.polish_iters"),
			&s.PolishItersEd, i18n.T("common.auto"), polishOff),
		gap(8),
		slider(i18n.T("expert.edge_softness"), &s.TauHint,
			i18n.T("hint.edge_softness"),
			&s.TauSlider, tauLo, tauHi, "%.3f", false, polishOff),
		gap(10),
		tog(&s.Alpha, i18n.T("expert.semi_transparency"), &s.AlphaHint,
			i18n.T("hint.alpha")),
		gap(8),
		slider(i18n.T("expert.transparency_floor"), &s.AlphaMinHint,
			i18n.T("hint.alpha_floor"),
			&s.AlphaMinSlider, alphaFloorLo, alphaFloorHi, "%.2f", false, alphaOff),
	)

	edges := vbox(
		tog(&s.WeightedOn, i18n.T("expert.edge_weighting"), &s.WeightedHint,
			i18n.T("hint.edge_weighting")),
		gap(8),
		slider(i18n.T("expert.edge_weight_strength"), &s.WeightStrHint,
			i18n.T("hint.edge_weight_strength"),
			&s.WeightStrSlider, weightStrLo, weightStrHi, "%.2f", false, weightedOff),
		gap(10),
		tog(&s.Boundary, i18n.T("expert.boundary_radius"), &s.BoundaryHint,
			i18n.T("hint.boundary")),
		gap(8),
		tog(&s.Backfit, i18n.T("expert.backfitting"), &s.BackfitHint,
			i18n.T("hint.backfit")),
		gap(8),
		tog(&s.CompactOn, i18n.T("expert.compact_bias"), &s.CompactHint,
			i18n.T("hint.compact")),
		gap(8),
		editor(i18n.T("expert.sliver_aspect_cap"), &s.AspectHint,
			i18n.T("hint.aspect"),
			&s.AspectEd, i18n.T("common.auto"), false),
		gap(8),
		layout.Rigid(s.kindWeightsBlock),
		gap(8),
		slider(i18n.T("expert.standout_suppression"), &s.StandoutHint,
			i18n.T("hint.standout"),
			&s.StandoutSlider, standoutLo, standoutHi, "%.4f", true, false),
	)

	search := vbox(
		layout.Rigid(func(gtx C) D {
			return s.fieldHint(gtx, i18n.T("expert.quality_preset"), &s.QualityHint,
				i18n.T("hint.quality"),
				func(gtx C) D { return s.QualityDD.Layout(gtx, th) })
		}),
		gap(8),
		editor(i18n.T("expert.seed"), &s.SeedHint,
			i18n.T("hint.seed"),
			&s.Seed, "1", false),
		gap(8),
		editor(i18n.T("expert.random_candidates"), &s.RandomHint,
			i18n.T("hint.random"),
			&s.RandomEd, i18n.T("common.auto"), false),
		gap(8),
		editor(i18n.T("expert.mutated_candidates"), &s.MutatedHint,
			i18n.T("hint.mutated"),
			&s.MutatedEd, i18n.T("common.auto"), false),
		gap(8),
		editor(i18n.T("expert.sample_budget"), &s.SampleHint,
			i18n.T("hint.sample"),
			&s.SampleEd, i18n.T("common.auto"), false),
		gap(8),
		editor(i18n.T("expert.max_no_improve"), &s.MaxNIHint,
			i18n.T("hint.max_no_improve"),
			&s.MaxNIEd, i18n.T("common.auto"), false),
		gap(8),
		editor(i18n.T("expert.error_grid"), &s.GridHint,
			i18n.T("hint.grid"),
			&s.GridEd, "48", false),
		gap(8),
		editor(i18n.T("expert.overdraw"), &s.OverdrawHint,
			i18n.T("hint.overdraw"),
			&s.OverdrawEd, i18n.T("common.off"), false),
	)

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(s.expertDisclaimer),
		gap(12),
		layout.Rigid(func(gtx C) D { return s.expGroup(gtx, 0, i18n.T("expert.group_smoothness"), smoothness) }),
		gap(8),
		layout.Rigid(func(gtx C) D { return s.expGroup(gtx, 1, i18n.T("expert.group_edges"), edges) }),
		gap(8),
		layout.Rigid(func(gtx C) D { return s.expGroup(gtx, 2, i18n.T("expert.group_search"), search) }),
		gap(8),
		layout.Rigid(func(gtx C) D { return s.expGroup(gtx, 3, i18n.T("expert.group_presets"), s.presetSaveRow) }),
	)
}

// expGroup renders a collapsible expert sub-section: a clickable header that shows its content only
// when open, so collapsed groups cost nothing to lay out.
func (s *AppState) expGroup(gtx C, idx int, title string, content layout.Widget) D {
	th := s.Th
	g := &s.ExpGroups[idx]
	if g.click.Clicked(gtx) {
		g.open = !g.open
	}
	arrow := "▸"
	if g.open {
		arrow = "▾"
	}
	head := layout.Rigid(func(gtx C) D {
		return material.Clickable(gtx, &g.click, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.Lbl(gtx, 12, arrow+"  "+title, th.Accent)
		})
	})
	if !g.open {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, head)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		head,
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(content),
	)
}

// kindWeightsBlock renders one weight field per currently selected shape kind, appearing and
// disappearing as kinds are ticked. The editors are keyed by kind, so toggling preserves their values.
func (s *AppState) kindWeightsBlock(gtx C) D {
	th := s.Th
	children := []layout.FlexChild{
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return th.Dim(gtx, i18n.T("expert.kind_weights")) }),
				layout.Rigid(GapH(6).Layout),
				layout.Rigid(func(gtx C) D {
					return s.KindWeightsHint.Layout(gtx, th,
						i18n.T("hint.kind_weights"))
				}),
			)
		}),
	}
	for _, name := range s.KindsSel.Value() {
		idx := kindIndex(name)
		if idx < 0 {
			continue
		}
		ed, label := &s.KindWeightEds[idx], name
		children = append(children,
			layout.Rigid(GapV(6).Layout),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Dp(96)
						return th.Dim(gtx, label)
					}),
					layout.Rigid(GapH(8).Layout),
					layout.Flexed(1, func(gtx C) D { return th.editorBox(gtx, ed, "1") }),
				)
			}),
		)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// presetSaveRow saves the current settings as a named preset, plus rename/delete for a selected one.
func (s *AppState) presetSaveRow(gtx C) D {
	th := s.Th
	rows := []layout.FlexChild{
		layout.Rigid(func(gtx C) D {
			return s.fieldHint(gtx, i18n.T("expert.preset_name"), &s.PresetNameHint,
				i18n.T("hint.preset_name"),
				func(gtx C) D { return th.editorBox(gtx, &s.PresetNameEd, i18n.T("expert.preset_name_placeholder")) })
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.SecondaryButton(gtx, &s.SavePresetBtn, i18n.T("expert.save_preset"), true)
		}),
	}
	if s.SelectedPreset() != nil {
		rows = append(rows,
			layout.Rigid(GapV(8).Layout),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return th.DangerButton(gtx, &s.DeletePresetBtn, i18n.T("expert.delete_preset"))
			}),
		)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

// expertDisclaimer is the as-is banner at the top of the expert panel.
func (s *AppState) expertDisclaimer(gtx C) D {
	th := s.Th
	l := material.Label(th.M, 11, i18n.T("hint.expert_disclaimer"))
	l.Color = th.TextDim
	return l.Layout(gtx)
}

// sliderRow is a labelled strength slider with a hover hint and a live value readout. A disabled row
// is greyed and inert (its parent toggle is off).
func (s *AppState) sliderRow(gtx C, label string, h *Hint, help string, sl *widget.Float, lo, hi float32, format string, offAtZero, disabled bool) D {
	th := s.Th
	if disabled {
		gtx = gtx.Disabled()
	}
	val := lo + sl.Value*(hi-lo)
	readout := fmt.Sprintf(format, val)
	if offAtZero && val <= lo {
		readout = i18n.T("common.off")
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return th.Dim(gtx, label) }),
				layout.Rigid(GapH(6).Layout),
				layout.Rigid(func(gtx C) D { return h.Layout(gtx, th, help) }),
				layout.Flexed(1, func(gtx C) D {
					return layout.E.Layout(gtx, func(gtx C) D { return th.Lbl(gtx, 12, readout, th.TextDim) })
				}),
			)
		}),
		layout.Rigid(GapV(4).Layout),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			ms := material.Slider(th.M, sl)
			ms.Color = th.Accent
			return ms.Layout(gtx)
		}),
	)
}
