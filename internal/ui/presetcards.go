package ui

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"fh6-paint-studio/internal/i18n"
	"fh6-paint-studio/internal/preset"
)

// presetCard is the display metadata for one built-in preset shown as a selectable card in step 1
// ("What are you making?"). The title is the plain-language intent (for the least technical user); the
// subtitle keeps the engine word discoverable next to a short "what it's for".
type presetCard struct {
	mode     string // the engine mode this card selects
	title    string // i18n key for the plain-language intent name
	subtitle string // i18n key for the "<engine-word> · short what-for" line
	ink      bool   // hybrid family (reveals the Artist Lines<->Fill knob)
}

// builtinCards is the curated palette. Order clusters the drawing styles first (the common case), then
// graphics, photo, and the niche soft-glow engine last. The two ‹ink› cards are the hybrids.
var builtinCards = []presetCard{
	{"anime", "mode.anime_title", "mode.anime_subtitle", false},
	{"anime-ink", "mode.anime_ink_title", "mode.anime_ink_subtitle", true},
	{"lineart", "mode.lineart_title", "mode.lineart_subtitle", true},
	{"flat", "mode.flat_title", "mode.flat_subtitle", false},
	{"photo", "mode.photo_title", "mode.photo_subtitle", false},
	{"gaussian", "mode.gaussian_title", "mode.gaussian_subtitle", false},
	{"pixel", "mode.pixel_title", "mode.pixel_subtitle", false},
}

// presetCardsSection renders step 1: the built-in preset cards, then a "Saved presets" group if the user
// has any. Clicking a card selects that preset (a built-in mode or a saved snapshot) via SelectPreset.
func (s *AppState) presetCardsSection(gtx C) D {
	th := s.Th
	cur := s.Mode.Value()
	children := make([]layout.FlexChild, 0, len(builtinCards)*2+len(s.Presets)*2+3)
	for i := range builtinCards {
		i, c := i, builtinCards[i]
		if i >= len(s.BuiltinCards) {
			break
		}
		children = append(children,
			layout.Rigid(func(gtx C) D {
				if s.BuiltinCards[i].Clicked(gtx) {
					s.SelectPreset(c.mode)
				}
				return s.presetCardView(gtx, &s.BuiltinCards[i], i18n.T(c.title), i18n.T(c.subtitle), c.ink, cur == c.mode)
			}),
			layout.Rigid(GapV(6).Layout),
		)
	}
	if len(s.Presets) > 0 {
		children = append(children,
			layout.Rigid(GapV(4).Layout),
			layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 11, i18n.T("mode.saved_presets"), th.TextDim) }),
			layout.Rigid(GapV(6).Layout),
		)
		for i := range s.Presets {
			if i >= len(s.PresetCards) {
				break
			}
			i, p := i, s.Presets[i]
			children = append(children,
				layout.Rigid(func(gtx C) D {
					if s.PresetCards[i].Clicked(gtx) {
						s.SelectPreset(p.Name)
					}
					return s.presetCardView(gtx, &s.PresetCards[i], p.Name, i18n.T("mode.saved_preset"), preset.IsHybridMode(p.Choices.Mode), cur == p.Name)
				}),
				layout.Rigid(GapV(6).Layout),
			)
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// presetCardView draws one selectable preset card: a raised tile with the intent title, the engine
// subtitle, an optional ‹ink› badge for the hybrids, and an accent ring + tick when it is the selection.
func (s *AppState) presetCardView(gtx C, clk *widget.Clickable, title, subtitle string, ink, selected bool) D {
	th := s.Th
	return material.Clickable(gtx, clk, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return s.cardFrame(gtx, selected, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							col := th.Text
							if selected {
								col = th.Accent
							}
							return th.Lbl(gtx, 14, title, col)
						}),
						layout.Rigid(GapV(2).Layout),
						layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 12, subtitle, th.TextDim) }),
					)
				}),
				// explicit-size flex spacer so the trailing badge/tick hug the right edge
				layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
				layout.Rigid(func(gtx C) D {
					if !ink {
						return D{}
					}
					return s.inkBadge(gtx)
				}),
				layout.Rigid(func(gtx C) D {
					if !selected {
						return D{}
					}
					return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D { return th.Lbl(gtx, 15, "✓", th.Accent) })
				}),
			)
		})
	})
}

// cardFrame is a rounded raised tile (SurfaceHi) with a 1px border, switching to a 2px accent ring when
// selected. Mirrors Theme.CardBg's Stack layout so the content lays out at full width.
func (s *AppState) cardFrame(gtx C, selected bool, w layout.Widget) D {
	th := s.Th
	const radius = 10
	border, bw := th.Border, 1
	if selected {
		border, bw = th.Accent, 2
	}
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			sz := gtx.Constraints.Min
			fillRRect(gtx, border, sz, radius)
			in := image.Rectangle{Min: image.Pt(bw, bw), Max: image.Pt(sz.X-bw, sz.Y-bw)}
			rr := clip.UniformRRect(in, radius-bw)
			paint.FillShape(gtx.Ops, th.SurfaceHi, rr.Op(gtx.Ops))
			return D{Size: sz}
		}),
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.UniformInset(10).Layout(gtx, w)
		}),
	)
}

// inkBadge is the small accent "ink" pill marking the hybrid presets (the ones with the Artist knob).
func (s *AppState) inkBadge(gtx C) D {
	th := s.Th
	return layout.Background{}.Layout(gtx,
		func(gtx C) D { fillRRect(gtx, th.Accent, gtx.Constraints.Min, 6); return D{Size: gtx.Constraints.Min} },
		func(gtx C) D {
			return layout.Inset{Top: 2, Bottom: 2, Left: 6, Right: 6}.Layout(gtx, func(gtx C) D {
				return th.Lbl(gtx, 11, i18n.T("mode.ink_badge"), th.OnAccent)
			})
		},
	)
}
