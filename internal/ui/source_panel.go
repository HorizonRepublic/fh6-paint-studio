package ui

import (
	"image"
	"path/filepath"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// sourcePanel is the left column: a scrollable Source + Settings stack with the primary
// Generate / Stop action PINNED at the bottom (so configure → generate is one column and the
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
		return th.DangerButton(gtx, &s.CancelBtn, "Stop")
	}
	label := "Generate"
	if s.Phase == PhaseDone {
		label = "Generate again"
	}
	return th.PrimaryButton(gtx, &s.GenBtn, label, s.Source != nil)
}

func (s *AppState) sourceCard(gtx C) D {
	th := s.Th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Title(gtx, "Source") }),
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(s.thumbnail),
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.SecondaryButton(gtx, &s.OpenBtn, "Open image…", true)
		}),
		layout.Rigid(GapV(6).Layout),
		layout.Rigid(func(gtx C) D {
			name := "No image loaded"
			if s.ImgPath != "" {
				name = filepath.Base(s.ImgPath)
			}
			return th.Dim(gtx, name)
		}),
	)
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
	layout.Center.Layout(gtx, func(gtx C) D { return th.Dim(gtx, "no image — click Open") })
	return D{Size: sz}
}

func (s *AppState) settingsCard(gtx C) D {
	th := s.Th
	s.syncAutoToggles(gtx)
	s.syncBudget()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Title(gtx, "Settings") }),
		layout.Rigid(GapV(12).Layout),
		layout.Rigid(s.budgetRow),
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(func(gtx C) D {
			return s.fieldHint(gtx, "Preset", &s.ModeHint, "The content preset (pick to match your image): ANIME/illustration, PHOTO/realistic, or FLAT/logo/poster. Each one is benchmark-tuned — it sets the shape mix, transparency, edge sharpening and polish that suit that content. Transparency is detected automatically.", func(gtx C) D { return s.Mode.Layout(gtx, th) })
		}),
		layout.Rigid(GapV(12).Layout),
		layout.Rigid(s.advancedSection),
	)
}

func (s *AppState) advancedSection(gtx C) D {
	th := s.Th
	if s.AdvClick.Clicked(gtx) {
		s.AdvOpen = !s.AdvOpen
	}
	arrow := "▸"
	if s.AdvOpen {
		arrow = "▾"
	}
	head := func(gtx C) D {
		return material.Clickable(gtx, &s.AdvClick, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.Lbl(gtx, 13, arrow+"  Custom (advanced)", th.TextDim)
		})
	}
	if !s.AdvOpen {
		return head(gtx)
	}
	note := func(gtx C) D {
		l := material.Label(th.M, 11, "Defaults are benchmark-tuned for the best quality + speed. Leave blank for the optimal. Override only to experiment — custom values aren't validated, so the result is on you.")
		l.Color = th.TextDim
		return l.Layout(gtx)
	}
	tog := func(b *widget.Bool, label string, h *Hint, help string) layout.FlexChild {
		return layout.Rigid(func(gtx C) D { return s.toggleRow(gtx, b, label, h, help) })
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(head),
		layout.Rigid(GapV(10).Layout),
		// Shape mix / transparency / boundary / back-fitting / colour-blend are HARDWIRED to their tuned
		// defaults — no longer user toggles. Colour blend is always LINEAR (the editor's space; a clear
		// win on semi-transparent gradients, a no-op for opaque content). KEPT: Joint polish (a SPEED
		// opt-out — it's 60-85% of a run).
		tog(&s.Polish, "Joint polish", &s.PolishHint,
			"A final gradient pass that nudges every shape's position, size, rotation and colour together to better match the image — sharper edges, lower error. GPU-fast and gated so it never makes the result worse. Turn OFF for a faster, rougher preview (polish is most of the run time)."),
		layout.Rigid(GapV(4).Layout),
		tog(&s.Economy, "Economy (trim redundant layers)", &s.EconomyHint,
			"OFF (default) = use the WHOLE shape budget for maximum quality. ON = after polish, drop layers whose removal barely changes the result (a lighter, cleaner import), gated so it never gets meaningfully worse. In practice it reclaims only a handful — the pipeline is already efficient — so leave it off unless you specifically want fewer layers."),
		layout.Rigid(GapV(4).Layout),
		tog(&s.Standout, "Smooth standout shapes", &s.StandoutHint,
			"OFF (default). ON = a final perceptual pass that finds individual shapes whose OUTLINE stands out against a part of the image that should be smooth (a stray circle/square the error metric can't see) and gently blends or fades them — gated so the measured error barely moves. Subtle on an already-clean result; turn it on if you spot the odd shape 'popping' on smooth skin/fur/gradients."),
		layout.Rigid(GapV(4).Layout),
		tog(&s.KeepInside, "Keep shapes inside image", &s.KeepInsideHint,
			"ON by default. Generates against a transparent surround so the spill penalty forces every shape to stay INSIDE the picture — no circles/rectangles ballooning past the edge (the worst in-game artefact). The reconstruction is mapped back to the original size afterwards, so the preview is clean (no frame). Turn off only if you want the legacy behaviour."),
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(note),
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(func(gtx C) D {
			return s.fieldHint(gtx, "Seed", &s.SeedHint, "Random seed. The same image + settings + seed always give the exact same result. Change it for a different random variation; leave it for reproducibility.", func(gtx C) D { return th.editorBox(gtx, &s.Seed, "1") })
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			return s.field(gtx, "Random candidates (blank = auto)", func(gtx C) D { return th.editorBox(gtx, &s.RandomEd, "auto") })
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			return s.field(gtx, "Mutated candidates (blank = auto)", func(gtx C) D { return th.editorBox(gtx, &s.MutatedEd, "auto") })
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			return s.field(gtx, "Sample budget px (blank = auto)", func(gtx C) D { return th.editorBox(gtx, &s.SampleEd, "auto") })
		}),
	)
}

// budgetRow is the shape-budget control: a label + manual number entry over a full-width slider.
func (s *AppState) budgetRow(gtx C) D {
	th := s.Th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return th.Dim(gtx, "Budget (shapes)") }),
				layout.Rigid(GapH(6).Layout),
				layout.Rigid(func(gtx C) D {
					return s.BudgetHint.Layout(gtx, th, "How many shapes to draw (1-3000). More = finer detail but slower. FH6 allows up to 3000 layers per group (~1000 for a bumper).")
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

// field stacks a dim label above a control.
func (s *AppState) field(gtx C, label string, w layout.Widget) D {
	th := s.Th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Dim(gtx, label) }),
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
