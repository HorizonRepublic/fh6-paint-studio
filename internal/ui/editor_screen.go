package ui

import (
	"math"
	"strconv"

	"gioui.org/layout"
	"gioui.org/unit"

	"fh6-paint-studio/internal/i18n"
)

// editorScreen is the full-window editor view (its own top-level tab): a palette/bank column, the
// canvas + toolbar, and the inspector + layers column.
func (s *AppState) editorScreen(gtx C) D {
	sz := gtx.Constraints.Max
	dims := layout.UniformInset(12).Layout(gtx, func(gtx C) D {
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
			layout.Rigid(fixed(320, s.editorLeftColumn)),
			layout.Rigid(GapH(12).Layout),
			layout.Flexed(1, fillH(s.editorCenter)),
			layout.Rigid(GapH(12).Layout),
			layout.Rigid(fixed(300, s.editorRightColumn)),
		)
	})
	// Drag-and-drop layer on top of everything (pass-through, so it never steals clicks).
	s.dragOverlay(gtx, sz)
	return dims
}

// editorLeftColumn is undo/redo + the add-primitive quick palette + the categorized dictionary grid.
func (s *AppState) editorLeftColumn(gtx C) D {
	th := s.Th
	gtx.Constraints.Min = gtx.Constraints.Max
	return th.Card(gtx, func(gtx C) D {
		gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
		s.ensureBankBuilt()
		s.handlePaletteActions(gtx)
		s.handleBankActions(gtx)
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return th.Title(gtx, i18n.T("editor.title")) }),
			layout.Rigid(GapV(10).Layout),
			layout.Rigid(s.newCanvasRow),
			layout.Rigid(GapV(8).Layout),
			layout.Rigid(s.undoRedoRow),
			layout.Rigid(GapV(12).Layout),
			layout.Rigid(func(gtx C) D { return th.Dim(gtx, i18n.T("editor.add")) }),
			layout.Rigid(GapV(6).Layout),
			layout.Rigid(s.addPaletteRows),
			layout.Rigid(GapV(8).Layout),
			layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 11, i18n.T("editor.dblclick_add"), th.TextDim) }),
			layout.Rigid(GapV(10).Layout),
			layout.Flexed(1, s.bankGrid),
		)
	})
}

// editorCenter is the canvas with the zoom + Save/Close toolbar beneath it.
func (s *AppState) editorCenter(gtx C) D {
	th := s.Th
	gtx.Constraints.Min = gtx.Constraints.Max
	return th.Card(gtx, func(gtx C) D {
		gtx.Constraints.Min = gtx.Constraints.Max
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D {
				gtx.Constraints.Min = gtx.Constraints.Max
				return s.editorArea(gtx)
			}),
			layout.Rigid(s.editorToolbar),
		)
	})
}

// editorToolbar is the zoom controls (left) and the Save flow (right): a name field + Save, replaced by
// an override confirmation when the name already exists, plus transient "saved" feedback. Leaving the
// editor is via the top-bar tabs (no Close button).
func (s *AppState) editorToolbar(gtx C) D {
	th := s.Th
	if s.editZoomOut.Clicked(gtx) {
		s.zoomStep(0.8)
	}
	if s.editZoomFit.Clicked(gtx) {
		s.zoomFit()
	}
	if s.editZoomIn.Clicked(gtx) {
		s.zoomStep(1.25)
	}
	if s.GuideBtn.Clicked(gtx) {
		s.canvasGuide = (s.canvasGuide + 1) % canvasGuideModes
	}
	if s.SnapBtn.Clicked(gtx) {
		s.snapOn = !s.snapOn
	}
	if s.SymBtn.Clicked(gtx) {
		s.symMode = (s.symMode + 1) % 3
	}
	if s.MirrorAllBtn.Clicked(gtx) {
		s.mirrorWholeDesign()
	}
	pct := strconv.Itoa(int(s.editZoom*100+0.5)) + "%"
	children := []layout.FlexChild{
		layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.editZoomOut, "−", true) }),
		layout.Rigid(GapH(6).Layout),
		layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.editZoomFit, i18n.T("editor.fit"), true) }),
		layout.Rigid(GapH(6).Layout),
		layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.editZoomIn, "+", true) }),
		layout.Rigid(GapH(10).Layout),
		layout.Rigid(func(gtx C) D { return th.Dim(gtx, pct) }),
		layout.Rigid(GapH(12).Layout),
		layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.GuideBtn, i18n.T(guideModeKey(s.canvasGuide)), true) }),
		layout.Rigid(GapH(6).Layout),
		layout.Rigid(func(gtx C) D {
			if s.snapOn {
				return th.PrimaryButton(gtx, &s.SnapBtn, i18n.T("editor.snap"), true)
			}
			return th.SecondaryButton(gtx, &s.SnapBtn, i18n.T("editor.snap"), true)
		}),
		layout.Rigid(GapH(6).Layout),
		layout.Rigid(func(gtx C) D {
			if s.symMode != symOff {
				return th.PrimaryButton(gtx, &s.SymBtn, i18n.T(s.symModeKey()), true)
			}
			return th.SecondaryButton(gtx, &s.SymBtn, i18n.T(s.symModeKey()), true)
		}),
	}
	if s.symMode != symOff {
		children = append(children,
			layout.Rigid(GapH(6).Layout),
			layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.MirrorAllBtn, i18n.T("editor.sym_all"), true) }),
		)
	}
	children = append(children,
		layout.Flexed(1, spacerW),
	)
	if s.editSavePending {
		children = append(children,
			layout.Rigid(func(gtx C) D {
				return th.Lbl(gtx, 13, i18n.T("editor.exists_q", s.editPendingName), th.Warn)
			}),
			layout.Rigid(GapH(8).Layout),
			layout.Rigid(func(gtx C) D { return th.PrimaryButton(gtx, &s.EditOverrideBtn, i18n.T("editor.override"), true) }),
			layout.Rigid(GapH(8).Layout),
			layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.EditSaveCancelBtn, i18n.T("common.cancel"), true) }),
		)
	} else {
		if s.editSavedMsg != "" && gtx.Now.Before(s.editSavedUntil) {
			children = append(children,
				layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 13, s.editSavedMsg, th.Good) }),
				layout.Rigid(GapH(10).Layout),
			)
		}
		children = append(children,
			layout.Rigid(func(gtx C) D {
				w := gtx.Dp(170)
				gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
				return th.editorBox(gtx, &s.EditName, i18n.T("editor.name"))
			}),
			layout.Rigid(GapH(8).Layout),
			layout.Rigid(func(gtx C) D { return th.PrimaryButton(gtx, &s.EditSaveBtn, i18n.T("editor.save"), true) }),
		)
	}
	return layout.Inset{Top: 10}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
	})
}

// editorRightColumn is the selected-shape inspector over the draggable layer list.
func (s *AppState) editorRightColumn(gtx C) D {
	th := s.Th
	gtx.Constraints.Min = gtx.Constraints.Max
	return th.Card(gtx, func(gtx C) D {
		gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
		s.handleEditActions(gtx)
		s.syncInspector()
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return th.Title(gtx, i18n.T("editor.inspector")) }),
			layout.Rigid(GapV(10).Layout),
			layout.Rigid(s.inspectorBody),
			layout.Rigid(GapV(14).Layout),
			layout.Rigid(s.layersHeader),
			layout.Rigid(GapV(6).Layout),
			layout.Flexed(1, s.layerList),
		)
	})
}

// zoomStep multiplies the zoom about the canvas centre (pan preserved), clamped to a sane range.
func (s *AppState) zoomStep(factor float64) {
	s.editZoom = math.Max(0.2, math.Min(16, s.editZoom*factor))
}
