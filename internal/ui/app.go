package ui

import (
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Layout renders the whole window: top bar, the 3-column body, and the bottom status bar.
// The main loop calls this each frame with full-window constraints.
func (s *AppState) Layout(gtx C) D {
	th := s.Th
	paint.Fill(gtx.Ops, th.Bg)
	dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(s.topBar),
		layout.Flexed(1, func(gtx C) D {
			if s.View == ViewLibrary {
				return s.libraryScreen(gtx)
			}
			return s.bodyRow(gtx)
		}),
		layout.Rigid(s.console), // shared activity console (status strip + expandable feed) — visible in both views
	)
	if s.LightboxOn { // drawn last so the full-image overlay sits on top of everything
		s.lightboxOverlay(gtx)
	}
	if s.AboutOn {
		s.aboutOverlay(gtx)
	}
	s.escDismiss(gtx)
	return dims
}

// escDismiss lets Esc close a modal overlay (lightbox / About). Key focus is grabbed ONLY while an
// overlay is up — where no text field is reachable — so it never interferes with typing; the scrim
// stays click-to-dismiss regardless, so there's no regression if focus doesn't land.
func (s *AppState) escDismiss(gtx C) {
	if !s.LightboxOn && !s.AboutOn {
		return
	}
	// PassOp: this key-focus area covers the whole window and is registered AFTER the overlays, so
	// without pass-through it could occlude their click-to-dismiss. Pass lets pointer events fall
	// through to the scrim; the tag still receives KEY events via the focus below.
	pass := pointer.PassOp{}.Push(gtx.Ops)
	area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &s.escTag)
	area.Pop()
	pass.Pop()
	gtx.Execute(key.FocusCmd{Tag: &s.escTag})
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &s.escTag, Name: key.NameEscape})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			s.LightboxOn = false
			s.AboutOn = false
		}
	}
}

// lightboxOverlay draws a dimmed full-window scrim with the selected library preview centred. The
// whole surface EXPLICITLY captures pointer presses on its own tag: that both dismisses it on a click
// anywhere AND occludes the gallery thumbnails behind, so a click can't fall through and re-open
// (the bug a plain click-wrapped scrim had — the thumb behind kept eating the dismiss click).
func (s *AppState) lightboxOverlay(gtx C) D {
	sz := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, color.NRGBA{A: 224}, clip.Rect{Max: sz}.Op())
	pointer.CursorPointer.Add(gtx.Ops)
	// Claim pointer presses over the whole scrim: dismiss on a tap AND occlude the gallery thumbs
	// behind so the dismiss click can't fall through and re-open it.
	area := clip.Rect{Max: sz}.Push(gtx.Ops)
	event.Op(gtx.Ops, &s.lightboxTag)
	area.Pop()
	// Dismiss on Release as well as Press: a freshly-registered area can miss the first Press (the
	// hit-target only updates on a move), but the Release is delivered to the handler the Press
	// grabbed, so the tap reliably closes on the FIRST click.
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.lightboxTag, Kinds: pointer.Press | pointer.Release})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok {
			s.LightboxOn = false
		}
	}
	if (s.LightboxOp != paint.ImageOp{}) {
		gtx.Constraints.Min = sz
		layout.UniformInset(unit.Dp(32)).Layout(gtx, func(gtx C) D {
			return widget.Image{Src: s.LightboxOp, Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
		})
	}
	return D{Size: sz}
}

func (s *AppState) topBar(gtx C) D {
	th := s.Th
	return layout.Background{}.Layout(gtx,
		func(gtx C) D {
			sz := gtx.Constraints.Min
			paint.FillShape(gtx.Ops, th.Surface, clip.Rect{Max: sz}.Op())
			paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(0, sz.Y-1), Max: sz}.Op())
			return D{Size: sz}
		},
		func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.UniformInset(12).Layout(gtx, func(gtx C) D {
				children := []layout.FlexChild{
					layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 18, "◣  FH6 Paint Studio", th.Accent) }),
					layout.Rigid(GapH(16).Layout),
					layout.Rigid(s.viewTabs),
					layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Max.X, 0)} }),
				}
				if !s.Elevated {
					children = append(children,
						layout.Rigid(func(gtx C) D { return s.adminButton(gtx) }),
						layout.Rigid(GapH(10).Layout),
					)
				}
				children = append(children,
					layout.Rigid(func(gtx C) D { return th.Dim(gtx, s.BackendLabel) }),
					layout.Rigid(GapH(10).Layout),
					layout.Rigid(s.aboutChip),
				)
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
			})
		},
	)
}

// viewTabs is the Studio/Library segmented switcher in the top bar.
func (s *AppState) viewTabs(gtx C) D {
	th := s.Th
	tab := func(btn *widget.Clickable, label string, active bool) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			return material.Clickable(gtx, btn, func(gtx C) D {
				col, txt := th.Surface, th.TextDim
				if active {
					col, txt = th.Accent, th.Bg
				}
				return layout.Background{}.Layout(gtx,
					func(gtx C) D { fillRRect(gtx, col, gtx.Constraints.Min, 8); return D{Size: gtx.Constraints.Min} },
					func(gtx C) D {
						return layout.UniformInset(8).Layout(gtx, func(gtx C) D { return th.Lbl(gtx, 13, label, txt) })
					},
				)
			})
		})
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		tab(&s.StudioTab, "Studio", s.View == ViewStudio),
		layout.Rigid(GapH(6).Layout),
		tab(&s.LibraryTab, "Library", s.View == ViewLibrary),
	)
}

// bodyRow lays out the three columns with fixed-width sides and a flexed center, each
// forced to fill the available height.
func (s *AppState) bodyRow(gtx C) D {
	return layout.UniformInset(12).Layout(gtx, func(gtx C) D {
		fillH := func(w layout.Widget) layout.Widget {
			return func(gtx C) D {
				gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
				return w(gtx)
			}
		}
		fixed := func(dp unit.Dp, w layout.Widget) layout.Widget {
			return func(gtx C) D {
				px := gtx.Dp(dp)
				gtx.Constraints.Min.X, gtx.Constraints.Max.X = px, px
				gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
				return w(gtx)
			}
		}
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(fixed(336, s.sourcePanel)),
			layout.Rigid(GapH(12).Layout),
			layout.Flexed(1, fillH(s.centerPanel)),
			layout.Rigid(GapH(12).Layout),
			layout.Rigid(fixed(300, s.runPanel)),
		)
	})
}

func (s *AppState) centerPanel(gtx C) D {
	th := s.Th
	gtx.Constraints.Min = gtx.Constraints.Max
	return th.Card(gtx, func(gtx C) D {
		gtx.Constraints.Min = gtx.Constraints.Max
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D {
				gtx.Constraints.Min = gtx.Constraints.Max
				return s.previewArea(gtx)
			}),
			layout.Rigid(s.cropBar),
			layout.Rigid(s.wipeBar),
		)
	})
}

// wipeBar is the before/after slider shown under the preview once a reconstruction exists.
func (s *AppState) wipeBar(gtx C) D {
	th := s.Th
	// Hidden without a reconstruction, and while the crop tool is active (it owns the pointer + bottom
	// bar). After a crop is applied the source IS the crop, so the wipe works on it as a normal image.
	if s.Preview == nil || s.CropMode {
		return D{}
	}
	return layout.Inset{Top: 10}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return th.Dim(gtx, "before") }),
			layout.Flexed(1, func(gtx C) D {
				sl := material.Slider(th.M, &s.Wipe)
				sl.Color = th.Accent
				return layout.Inset{Left: 10, Right: 10}.Layout(gtx, sl.Layout)
			}),
			layout.Rigid(func(gtx C) D { return th.Dim(gtx, "after") }),
		)
	})
}
