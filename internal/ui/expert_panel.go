package ui

import (
	"fmt"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
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
		tog(&s.PolishOn, "Joint polish", &s.PolishHint,
			"Refines all shapes together after the greedy build. On = smoother, better colour fit; off = faster but rawer and more faceted.\n\nAffects: time and quality."),
		gap(8),
		editor("Polish iterations", &s.PolishItersHint,
			"How many polish refinement steps. More = closer fit and smoother gradients but slower; early-stop trims the tail. Blank = the preset's tuned count. For flat art the tuned count depends on the palette.\n\nAffects: time and quality.",
			&s.PolishItersEd, "auto", polishOff),
		gap(8),
		slider("Edge softness", &s.TauHint,
			"Softness of the polished edges. Lower = crisper, harder edges; higher = softer, smoother blends.\n\nAffects: quality.",
			&s.TauSlider, tauLo, tauHi, "%.3f", false, polishOff),
		gap(10),
		tog(&s.Alpha, "Semi-transparency", &s.AlphaHint,
			"Allow semi-transparent shapes for smooth gradients. On = smoother organic content; off = fully opaque and crisper. Cutouts stay opaque regardless.\n\nAffects: quality."),
		gap(8),
		slider("Transparency floor", &s.AlphaMinHint,
			"Lowest opacity a shape may use when semi-transparency is on. Higher = more opaque and crisper; lower = more see-through layering and smoother.\n\nAffects: quality.",
			&s.AlphaMinSlider, alphaFloorLo, alphaFloorHi, "%.2f", false, alphaOff),
	)

	edges := vbox(
		tog(&s.WeightedOn, "Edge weighting", &s.WeightedHint,
			"Focus the error on salient edges instead of weighting every pixel equally. On = sharper contours; off = uniform.\n\nAffects: quality."),
		gap(8),
		slider("Edge-weight strength", &s.WeightStrHint,
			"How strongly the edge map biases shape placement. Higher = more edge-focused detail; 0 = uniform.\n\nAffects: quality.",
			&s.WeightStrSlider, weightStrLo, weightStrHi, "%.2f", false, weightedOff),
		gap(10),
		tog(&s.Boundary, "Boundary-aware radius", &s.BoundaryHint,
			"Cap each shape by its distance to the nearest edge so shapes can't balloon across contours. On = cleaner flat/logo silhouettes; can fray smooth content.\n\nAffects: quality."),
		gap(8),
		tog(&s.Backfit, "Back-fitting", &s.BackfitHint,
			"Remove the weakest shapes and regrow them against the residual before polish. On = better budget use on flat/cutout art, at a small time cost.\n\nAffects: time and quality."),
		gap(8),
		tog(&s.CompactOn, "Compact bias", &s.CompactHint,
			"Bias the per-shape pick toward compact shapes, especially the first few, for a cleaner coarse base.\n\nAffects: quality."),
		gap(8),
		editor("Sliver aspect cap", &s.AspectHint,
			"Maximum elongation for ellipse/rectangle slivers. Higher = thin slivers that trace sharp contours; 0 = round shapes for smooth content. Blank = preset default.\n\nAffects: quality.",
			&s.AspectEd, "auto", false),
		gap(8),
		layout.Rigid(func(gtx C) D {
			return s.fieldHint(gtx, "Shape kinds", &s.KindsHint,
				"Which primitives to use. Tick the kinds to include; fewer kinds is faster but coarser.\n\nAffects: time and quality.",
				func(gtx C) D { return s.KindsSel.Layout(gtx, th) })
		}),
		gap(8),
		layout.Rigid(s.kindWeightsBlock),
		gap(8),
		slider("Standout suppression", &s.StandoutHint,
			"Post-polish pass that recolours or removes shapes drawing an edge the target lacks (a visible circle or square). The value is the allowed global-error rise; 0 = off. Judge by eye.\n\nAffects: quality.",
			&s.StandoutSlider, standoutLo, standoutHi, "%.4f", true, false),
	)

	search := vbox(
		layout.Rigid(func(gtx C) D {
			return s.fieldHint(gtx, "Quality preset", &s.QualityHint,
				"Search depth: how many shape candidates are scored per step. Higher (max/quality/ultra) = better placement but much slower; lower (fast/balanced) = quicker and rougher.\n\nAffects: time most, then quality.",
				func(gtx C) D { return s.QualityDD.Layout(gtx, th) })
		}),
		gap(8),
		editor("Seed", &s.SeedHint,
			"Random seed. The same image, settings, and seed always give an identical result. Change it for a different variation.",
			&s.Seed, "1", false),
		gap(8),
		editor("Random candidates", &s.RandomHint,
			"Random candidate shapes generated per step. More = a better search but slower. Blank = the quality preset's count.\n\nAffects: time and quality.",
			&s.RandomEd, "auto", false),
		gap(8),
		editor("Mutated candidates", &s.MutatedHint,
			"Hill-climb mutations of the best candidate per step. More = finer local refinement but slower. Blank = preset count.\n\nAffects: time and quality.",
			&s.MutatedEd, "auto", false),
		gap(8),
		editor("Sample budget (px)", &s.SampleHint,
			"Pixels scored per candidate (progressive sampling). Higher = more accurate scoring but slower. Blank = backend default.\n\nAffects: time and quality.",
			&s.SampleEd, "auto", false),
		gap(8),
		editor("Max no-improve", &s.MaxNIHint,
			"Consecutive non-improving shapes before the greedy stops early. Higher = fills more of the budget. Blank = preset default.\n\nAffects: time and quality.",
			&s.MaxNIEd, "auto", false),
		gap(8),
		editor("Error grid", &s.GridHint,
			"Backend error-grid resolution for candidate sampling. Blank = 48. Mostly a performance knob.\n\nAffects: time.",
			&s.GridEd, "48", false),
		gap(8),
		editor("Overdraw", &s.OverdrawHint,
			"Generate this multiple of the budget, then prune back to it. Above 1 trades time for a slightly better final set. Blank = off.\n\nAffects: time and quality.",
			&s.OverdrawEd, "off", false),
	)

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(s.expertDisclaimer),
		gap(12),
		layout.Rigid(func(gtx C) D { return s.expGroup(gtx, 0, "Smoothness and crispness", smoothness) }),
		gap(8),
		layout.Rigid(func(gtx C) D { return s.expGroup(gtx, 1, "Edges and shapes", edges) }),
		gap(8),
		layout.Rigid(func(gtx C) D { return s.expGroup(gtx, 2, "Search and quality", search) }),
		gap(8),
		layout.Rigid(func(gtx C) D { return s.expGroup(gtx, 3, "Custom presets", s.presetSaveRow) }),
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
				layout.Rigid(func(gtx C) D { return th.Dim(gtx, "Kind weights") }),
				layout.Rigid(GapH(6).Layout),
				layout.Rigid(func(gtx C) D {
					return s.KindWeightsHint.Layout(gtx, th,
						"Relative pick weight per selected kind (higher = picked more often). Leave blank for an even mix.\n\nAffects: quality.")
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
			return s.fieldHint(gtx, "Preset name", &s.PresetNameHint,
				"Save the current settings, including budget and seed, as a reusable preset. It appears under the built-in presets in the dropdown above.",
				func(gtx C) D { return th.editorBox(gtx, &s.PresetNameEd, "my preset") })
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.SecondaryButton(gtx, &s.SavePresetBtn, "Save as new preset", true)
		}),
	}
	if s.SelectedPreset() != nil {
		rows = append(rows,
			layout.Rigid(GapV(8).Layout),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return th.DangerButton(gtx, &s.DeletePresetBtn, "Delete preset")
			}),
		)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

// expertDisclaimer is the as-is banner at the top of the expert panel.
func (s *AppState) expertDisclaimer(gtx C) D {
	th := s.Th
	l := material.Label(th.M, 11, "Expert mode is unlocked as-is. Every control can change render time and quality, in either direction. For a good result without fuss, use the presets above and leave this off.")
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
		readout = "off"
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
