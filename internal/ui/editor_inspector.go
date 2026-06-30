package ui

import (
	"image"
	"image/color"
	"math"
	"strconv"
	"time"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"fh6-paint-studio/internal/i18n"
	"fh6-paint-studio/internal/model"
)

// inspectorBody is the selected-shape controls: position, size, rotation, the colour picker, and the
// z-order / duplicate / delete actions. With nothing selected it shows a hint.
func (s *AppState) inspectorBody(gtx C) D {
	th := s.Th
	if !s.selValid() {
		return th.Dim(gtx, i18n.T("editor.right_hint"))
	}
	if s.selCount() > 1 {
		return s.multiPanel(gtx)
	}
	sh := s.EditShapes[s.EditSel]
	k := model.KindFromType(sh.Type)
	hasRot := k != model.KindTriangle && k != model.KindLine

	children := []layout.FlexChild{
		s.inspPair("editor.pos_x", &s.inspX, "editor.pos_y", &s.inspY),
		layout.Rigid(GapV(8).Layout),
		s.inspPair("editor.width", &s.inspW, "editor.height", &s.inspH),
		layout.Rigid(GapV(8).Layout),
	}
	if hasRot {
		children = append(children,
			layout.Rigid(s.inspFieldWidget("editor.rotation", &s.inspRot)),
			layout.Rigid(GapV(8).Layout),
		)
	}
	children = append(children,
		layout.Rigid(s.colorSection),
		layout.Rigid(GapV(14).Layout),
		layout.Rigid(s.inspActions),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(s.mirrorButtons),
		layout.Rigid(GapV(12).Layout),
		layout.Rigid(s.arrayPanel),
	)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// mirrorButton is the full-width "mirror across the vertical centre" action shared by single & multi.
func (s *AppState) mirrorButtons(gtx C) D {
	th := s.Th
	full := func(b *widget.Clickable, key string) layout.FlexChild {
		return layout.Flexed(1, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.SecondaryButton(gtx, b, i18n.T(key), true)
		})
	}
	return layout.Flex{}.Layout(gtx,
		full(&s.editMirror, "editor.mirror_h"),
		layout.Rigid(GapH(8).Layout),
		full(&s.editMirrorV, "editor.mirror_v"),
	)
}

// multiPanel replaces the per-field inspector when several shapes are selected: align/distribute,
// mirror, and group duplicate/delete (per-shape numeric editing only makes sense for one shape).
func (s *AppState) multiPanel(gtx C) D {
	th := s.Th
	full := func(b *widget.Clickable, key string) layout.Widget {
		return func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.SecondaryButton(gtx, b, i18n.T(key), true)
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 13, i18n.T("editor.selected_n", s.selCount()), th.Text) }),
		layout.Rigid(GapV(10).Layout),
		layout.Rigid(func(gtx C) D { return th.Dim(gtx, i18n.T("editor.align")) }),
		layout.Rigid(GapV(6).Layout),
		layout.Rigid(s.alignRowX),
		layout.Rigid(GapV(6).Layout),
		layout.Rigid(s.alignRowY),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(full(&s.alignBtns[distributeH], "editor.distribute_h")),
		layout.Rigid(GapV(6).Layout),
		layout.Rigid(full(&s.alignBtns[distributeV], "editor.distribute_v")),
		layout.Rigid(GapV(12).Layout),
		layout.Rigid(s.mirrorButtons),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(s.arrayPanel),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, full(&s.editDup, "editor.duplicate")),
				layout.Rigid(GapH(8).Layout),
				layout.Flexed(1, s.deleteButton),
			)
		}),
	)
}

// alignBtn is one equal-width align button (label is an icon-like letter, no translation needed).
func (s *AppState) alignBtn(n int, label string) layout.FlexChild {
	return layout.Flexed(1, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return s.Th.SecondaryButton(gtx, &s.alignBtns[n], label, true)
	})
}

func (s *AppState) alignRowX(gtx C) D {
	return layout.Flex{}.Layout(gtx,
		s.alignBtn(alignLeft, "L"),
		layout.Rigid(GapH(6).Layout),
		s.alignBtn(alignCenterX, "C"),
		layout.Rigid(GapH(6).Layout),
		s.alignBtn(alignRight, "R"),
	)
}

func (s *AppState) alignRowY(gtx C) D {
	return layout.Flex{}.Layout(gtx,
		s.alignBtn(alignTop, "T"),
		layout.Rigid(GapH(6).Layout),
		s.alignBtn(alignMiddleY, "M"),
		layout.Rigid(GapH(6).Layout),
		s.alignBtn(alignBottom, "B"),
	)
}

// inspFieldWidget is a dim label stacked over a single editor box that fills the available width.
func (s *AppState) inspFieldWidget(labelKey string, ed *widget.Editor) layout.Widget {
	th := s.Th
	return func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return th.Dim(gtx, i18n.T(labelKey)) }),
			layout.Rigid(GapV(3).Layout),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return th.editorBox(gtx, ed, "")
			}),
		)
	}
}

// inspPair lays two labelled fields side by side.
func (s *AppState) inspPair(k1 string, ed1 *widget.Editor, k2 string, ed2 *widget.Editor) layout.FlexChild {
	return layout.Rigid(func(gtx C) D {
		return layout.Flex{}.Layout(gtx,
			layout.Flexed(1, s.inspFieldWidget(k1, ed1)),
			layout.Rigid(GapH(8).Layout),
			layout.Flexed(1, s.inspFieldWidget(k2, ed2)),
		)
	})
}

// colorSection is the "Colour" label + a clickable swatch (toggles the picker) and, when open, the
// R/G/B and Alpha (transparency) sliders.
func (s *AppState) colorSection(gtx C) D {
	th := s.Th
	sh := s.EditShapes[s.EditSel]
	rows := []layout.FlexChild{
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return th.Dim(gtx, i18n.T("editor.color")) }),
				layout.Flexed(1, spacerW),
				layout.Rigid(func(gtx C) D {
					lbl := i18n.T("editor.eyedrop")
					if s.eyedropMode {
						lbl = "• " + lbl
					}
					return th.SecondaryButton(gtx, &s.eyedropBtn, lbl, true)
				}),
				layout.Rigid(GapH(8).Layout),
				layout.Rigid(func(gtx C) D { return s.colorSwatch(gtx, colorFromShape(sh)) }),
			)
		}),
	}
	if s.colorPickerOpen {
		rows = append(rows, layout.Rigid(GapV(8).Layout))
		if len(s.recentColors) > 0 {
			rows = append(rows, layout.Rigid(s.recentColorsRow), layout.Rigid(GapV(6).Layout))
		}
		rows = append(rows,
			layout.Rigid(s.colorWheel),
			layout.Rigid(GapV(8).Layout),
			layout.Rigid(s.brightnessSlider),
			layout.Rigid(GapV(8).Layout),
			layout.Rigid(func(gtx C) D {
				return s.colorSlider(gtx, "editor.col_r", &s.pickR, color.NRGBA{R: 220, G: 70, B: 70, A: 255})
			}),
			layout.Rigid(GapV(4).Layout),
			layout.Rigid(func(gtx C) D {
				return s.colorSlider(gtx, "editor.col_g", &s.pickG, color.NRGBA{R: 70, G: 190, B: 90, A: 255})
			}),
			layout.Rigid(GapV(4).Layout),
			layout.Rigid(func(gtx C) D {
				return s.colorSlider(gtx, "editor.col_b", &s.pickB, color.NRGBA{R: 80, G: 130, B: 230, A: 255})
			}),
			layout.Rigid(GapV(4).Layout),
			layout.Rigid(func(gtx C) D { return s.colorSlider(gtx, "editor.col_a", &s.pickA, th.TextDim) }),
		)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

// applyPaletteColor sets the selected shape's RGB to c (alpha unchanged) and syncs the sliders.
func (s *AppState) applyPaletteColor(c color.NRGBA) {
	if !s.selValid() {
		return
	}
	sh := &s.EditShapes[s.EditSel]
	if len(sh.Color) < 4 {
		nc := make([]int, 4)
		nc[3] = 255
		copy(nc, sh.Color)
		sh.Color = nc
	}
	sh.Color[0], sh.Color[1], sh.Color[2] = int(c.R), int(c.G), int(c.B)
	s.pickR.Value, s.pickG.Value, s.pickB.Value = float32(c.R)/255, float32(c.G)/255, float32(c.B)/255
	h, sat, v := rgbToHSV(c)
	s.pickH, s.pickS, s.pickV = h/360, sat, v
	s.pickVf.Value = float32(v)
	s.pushRecentColor(c)
	s.markEditDirty()
}

// pushRecentColor records an applied colour at the front of the recents (deduped, capped).
func (s *AppState) pushRecentColor(c color.NRGBA) {
	c.A = 255
	for i, e := range s.recentColors {
		if e == c {
			s.recentColors = append(s.recentColors[:i], s.recentColors[i+1:]...)
			break
		}
	}
	s.recentColors = append([]color.NRGBA{c}, s.recentColors...)
	if len(s.recentColors) > 10 {
		s.recentColors = s.recentColors[:10]
	}
}

// sampleColor (eyedropper) reads the colour at the canvas point from the last render and applies it.
func (s *AppState) sampleColor(fp f32.Point) {
	if s.editImg == nil || !s.selValid() {
		return
	}
	x := int(float64(fp.X) * float64(s.EditW))
	y := int(float64(fp.Y) * float64(s.EditH))
	b := s.editImg.Bounds()
	if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
		return
	}
	c := s.editImg.NRGBAAt(x, y)
	if c.A == 0 {
		return // empty canvas — nothing to pick
	}
	c.A = 255
	s.applyPaletteColor(c)
}

// recentColorsRow renders the recently-applied colours as clickable swatches.
func (s *AppState) recentColorsRow(gtx C) D {
	for len(s.recentBtns) < len(s.recentColors) {
		s.recentBtns = append(s.recentBtns, widget.Clickable{})
	}
	var cells []layout.FlexChild
	for i, c := range s.recentColors {
		if i > 0 {
			cells = append(cells, layout.Rigid(GapH(4).Layout))
		}
		i, c := i, c
		cells = append(cells, layout.Rigid(func(gtx C) D { return s.colorChipBtn(gtx, &s.recentBtns[i], c) }))
	}
	return layout.Flex{}.Layout(gtx, cells...)
}

// colorChipBtn is a small fixed swatch button for a given colour + clickable.
func (s *AppState) colorChipBtn(gtx C, b *widget.Clickable, col color.NRGBA) D {
	th := s.Th
	return material.Clickable(gtx, b, func(gtx C) D {
		sz := image.Pt(gtx.Dp(20), gtx.Dp(18))
		gtx.Constraints = layout.Exact(sz)
		borderRRect(gtx, th.Border, col, sz, 4, 1)
		pointer.CursorPointer.Add(gtx.Ops)
		return D{Size: sz}
	})
}

// hsvToRGB converts HSV (h in degrees, s,v in 0..1) to an opaque NRGBA.
func hsvToRGB(h, s, v float64) color.NRGBA {
	h = math.Mod(h, 360) / 60
	c := v * s
	x := c * (1 - math.Abs(math.Mod(h, 2)-1))
	m := v - c
	var r, g, b float64
	switch int(h) {
	case 0:
		r, g, b = c, x, 0
	case 1:
		r, g, b = x, c, 0
	case 2:
		r, g, b = 0, c, x
	case 3:
		r, g, b = 0, x, c
	case 4:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return color.NRGBA{R: uint8((r + m) * 255), G: uint8((g + m) * 255), B: uint8((b + m) * 255), A: 255}
}

// colorSwatch is the clickable colour preview (checker backing so transparency shows).
func (s *AppState) colorSwatch(gtx C, c color.NRGBA) D {
	th := s.Th
	return material.Clickable(gtx, &s.colorSwatchBtn, func(gtx C) D {
		sz := image.Pt(gtx.Dp(34), gtx.Dp(20))
		drawCheckerboard(gtx, image.Rectangle{Max: sz})
		borderRRect(gtx, th.Border, c, sz, 4, 1)
		pointer.CursorPointer.Add(gtx.Ops)
		return D{Size: sz}
	})
}

// colorSlider is one labelled 0..255 colour channel slider with a live numeric readout.
func (s *AppState) colorSlider(gtx C, labelKey string, f *widget.Float, col color.NRGBA) D {
	th := s.Th
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(14)
			return th.Lbl(gtx, 12, i18n.T(labelKey), th.TextDim)
		}),
		layout.Rigid(GapH(6).Layout),
		layout.Flexed(1, func(gtx C) D {
			sl := material.Slider(th.M, f)
			sl.Color = col
			return sl.Layout(gtx)
		}),
		layout.Rigid(GapH(6).Layout),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Dp(26)
			return th.Dim(gtx, strconv.Itoa(int(f.Value*255+0.5)))
		}),
	)
}

// inspActions is the z-order / duplicate / delete button grid.
func (s *AppState) inspActions(gtx C) D {
	th := s.Th
	btn := func(b *widget.Clickable, key string) layout.Widget {
		return func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.SecondaryButton(gtx, b, i18n.T(key), true)
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, btn(&s.editBack, "editor.back")),
				layout.Rigid(GapH(8).Layout),
				layout.Flexed(1, btn(&s.editForward, "editor.forward")),
			)
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, btn(&s.editDup, "editor.duplicate")),
				layout.Rigid(GapH(8).Layout),
				layout.Flexed(1, s.deleteButton),
			)
		}),
	)
}

// deleteButton is the red, two-step Delete: the first click arms it (label → "Delete?"), a second within
// the window confirms. Shared by the single-shape and multi-select panels.
func (s *AppState) deleteButton(gtx C) D {
	th := s.Th
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	if s.deleteArmed && gtx.Now.Before(s.deleteArmedAt.Add(3*time.Second)) {
		return th.DangerButton(gtx, &s.editDelete, i18n.T("editor.confirm_del"))
	}
	return th.DangerButton(gtx, &s.editDelete, i18n.T("editor.delete"))
}

// syncInspector keeps the fields and the selected shape in step: it repopulates the fields when the
// selection changes or a drag is mutating the shape live, and otherwise applies typed edits to the shape.
func (s *AppState) syncInspector() {
	if !s.selValid() {
		s.inspFor = -1
		return
	}
	if s.selCount() > 1 {
		// Multi-select shows the group panel, not per-field editors — never read the (hidden, stale)
		// fields back over the primary shape. inspFor=-1 forces a fresh populate on returning to one shape.
		s.inspFor = -1
		return
	}
	if s.EditSel != s.inspFor || s.editDrag.kind != dragNone {
		s.populateInspector()
		s.inspFor = s.EditSel
		return
	}
	s.readInspector()
}

// populateInspector writes the selected shape's values into the editor fields.
func (s *AppState) populateInspector() {
	sh := s.EditShapes[s.EditSel]
	cx, cy := shapeCenter(sh)
	hx, hy := shapeHalfExtents(sh)
	s.inspX.SetText(formatFloat(round1(cx)))
	s.inspY.SetText(formatFloat(round1(cy)))
	s.inspW.SetText(formatFloat(round1(hx * 2)))
	s.inspH.SetText(formatFloat(round1(hy * 2)))
	s.inspRot.SetText(formatFloat(round1(shapeTheta(sh))))
	s.populateColorSliders()
}

// populateColorSliders sets the R/G/B/A sliders (0..1) from the selected shape's colour.
func (s *AppState) populateColorSliders() {
	sh := s.EditShapes[s.EditSel]
	get := func(i int, dflt float32) float32 {
		if i < len(sh.Color) {
			return float32(clampInt(sh.Color[i], 0, 255)) / 255
		}
		return dflt
	}
	s.pickR.Value = get(0, 0)
	s.pickG.Value = get(1, 0)
	s.pickB.Value = get(2, 0)
	s.pickA.Value = get(3, 1)
	h, sat, v := rgbToHSV(colorFromShape(sh))
	s.pickH, s.pickS, s.pickV = h/360, sat, v
	s.pickVf.Value = float32(v)
}

// readInspector parses the fields and applies any change to the selected shape, marking the render dirty
// only when a value actually moved (so steady-state frames don't churn the cache).
func (s *AppState) readInspector() {
	sh := &s.EditShapes[s.EditSel]
	kind := model.KindFromType(sh.Type)
	changed := false

	// Tolerance just above populate's 0.1 rounding, so re-displaying a field never nudges the shape
	// (and a steady selection never re-renders); a genuine typed edit is always larger.
	const eps = 0.051
	cx, cy := shapeCenter(*sh)
	if v, ok := editorFloat(&s.inspX); ok && math.Abs(v-cx) > eps {
		moveShapeData(sh, v-cx, 0)
		changed = true
	}
	cx, cy = shapeCenter(*sh)
	if v, ok := editorFloat(&s.inspY); ok && math.Abs(v-cy) > eps {
		moveShapeData(sh, 0, v-cy)
		changed = true
	}
	hx, hy := shapeHalfExtents(*sh)
	if v, ok := editorFloat(&s.inspW); ok && v > 0 && math.Abs(v/2-hx) > eps {
		setShapeScale(sh, v/2, hy)
		changed = true
	}
	hx, hy = shapeHalfExtents(*sh)
	if v, ok := editorFloat(&s.inspH); ok && v > 0 && math.Abs(v/2-hy) > eps {
		setShapeScale(sh, hx, v/2)
		changed = true
	}
	if kind != model.KindTriangle && kind != model.KindLine {
		if v, ok := editorFloat(&s.inspRot); ok && len(sh.Data) >= 5 && math.Abs(v-sh.Data[4]) > eps {
			sh.Data[4] = v
			changed = true
		}
	}
	if s.colorPickerOpen && s.applyColorSliders(sh) {
		changed = true
		// keep the disc + value slider in step when the R/G/B sliders are the driver
		h, sat, v := rgbToHSV(colorFromShape(*sh))
		s.pickH, s.pickS, s.pickV = h/360, sat, v
		s.pickVf.Value = float32(v)
	}
	if changed {
		s.markEditDirty()
	}
}

// applyColorSliders writes the R/G/B/A sliders into the shape colour, returning whether anything changed.
func (s *AppState) applyColorSliders(sh *model.Shape) bool {
	if len(sh.Color) < 4 {
		c := make([]int, 4)
		c[3] = 255
		copy(c, sh.Color)
		sh.Color = c
	}
	vals := [4]float32{s.pickR.Value, s.pickG.Value, s.pickB.Value, s.pickA.Value}
	changed := false
	for i := 0; i < 4; i++ {
		cv := clampInt(int(vals[i]*255+0.5), 0, 255)
		if sh.Color[i] != cv {
			sh.Color[i] = cv
			changed = true
		}
	}
	return changed
}

// handleEditActions processes the colour-swatch toggle and the z-order / duplicate / delete buttons.
func (s *AppState) handleEditActions(gtx C) {
	if s.colorSwatchBtn.Clicked(gtx) {
		s.colorPickerOpen = !s.colorPickerOpen
		if s.colorPickerOpen && s.selValid() {
			s.populateColorSliders()
		}
	}
	if s.eyedropBtn.Clicked(gtx) {
		s.eyedropMode = !s.eyedropMode
	}
	if s.colorPickerOpen {
		s.handleColorPicker(gtx)
		for i := range s.recentBtns {
			if i < len(s.recentColors) && s.recentBtns[i].Clicked(gtx) {
				s.applyPaletteColor(s.recentColors[i])
			}
		}
	}
	if s.editForward.Clicked(gtx) {
		s.bringForward()
	}
	if s.editBack.Clicked(gtx) {
		s.sendBack()
	}
	if s.editDup.Clicked(gtx) {
		s.duplicateSel()
	}
	if s.editDelete.Clicked(gtx) {
		if s.deleteArmed && gtx.Now.Before(s.deleteArmedAt.Add(3*time.Second)) {
			s.deleteArmed = false
			s.deleteSel()
		} else {
			s.deleteArmed = true
			s.deleteArmedAt = gtx.Now
		}
	}
	if s.editMirror.Clicked(gtx) {
		s.mirrorSelection(false)
	}
	if s.editMirrorV.Clicked(gtx) {
		s.mirrorSelection(true)
	}
	if s.arrayRowBtn.Clicked(gtx) {
		s.arraySelection(s.arrayCountVal(), false)
	}
	if s.arrayRingBtn.Clicked(gtx) {
		s.arraySelection(s.arrayCountVal(), true)
	}
	for n := range s.alignBtns {
		if s.alignBtns[n].Clicked(gtx) {
			s.alignSelection(n)
		}
	}
}

// bringForward swaps the selected shape one step toward the front (higher index = drawn later).
func (s *AppState) bringForward() {
	if !s.selValid() || s.EditSel >= len(s.EditShapes)-1 {
		return
	}
	s.pushUndo(cloneShapes(s.EditShapes))
	i := s.EditSel
	s.EditShapes[i], s.EditShapes[i+1] = s.EditShapes[i+1], s.EditShapes[i]
	s.EditSel = i + 1
	s.markEditDirty()
}

// sendBack swaps the selected shape one step toward the back, but never below the background (index 0).
func (s *AppState) sendBack() {
	if !s.selValid() || s.EditSel <= 1 {
		return
	}
	s.pushUndo(cloneShapes(s.EditShapes))
	i := s.EditSel
	s.EditShapes[i], s.EditShapes[i-1] = s.EditShapes[i-1], s.EditShapes[i]
	s.EditSel = i - 1
	s.markEditDirty()
}

// duplicateSel inserts a slightly-offset clone of the selected shape just above it, and selects it.
func (s *AppState) duplicateSel() {
	idx := s.selIndices()
	if len(idx) == 0 {
		return
	}
	if len(idx) == 1 { // single: insert the copy directly above the original
		s.pushUndo(cloneShapes(s.EditShapes))
		i := idx[0]
		clone := cloneShapes(s.EditShapes[i : i+1])[0]
		moveShapeData(&clone, 8, 8)
		s.EditShapes = append(s.EditShapes, model.Shape{})
		copy(s.EditShapes[i+2:], s.EditShapes[i+1:])
		s.EditShapes[i+1] = clone
		s.selectSingle(i + 1)
		s.markEditDirty()
		return
	}
	s.pushUndo(cloneShapes(s.EditShapes))
	var newIdx []int
	for _, i := range idx {
		if len(s.EditShapes) >= editMaxShapes {
			break
		}
		clone := cloneShapes(s.EditShapes[i : i+1])[0]
		moveShapeData(&clone, 8, 8)
		s.EditShapes = append(s.EditShapes, clone)
		newIdx = append(newIdx, len(s.EditShapes)-1)
	}
	s.selectFromSet(newIdx)
	s.markEditDirty()
}

// arrayCountVal reads the requested copy count, clamped to a sane 2..24 (default 6 on bad input).
func (s *AppState) arrayCountVal() int {
	n, err := strconv.Atoi(s.arrayCount.Text())
	if err != nil || n < 2 {
		return 6
	}
	if n > 24 {
		return 24
	}
	return n
}

// arraySelection repeats the current selection count-1 more times: a horizontal row a box-width apart,
// or evenly spaced around the canvas centre. The originals plus every copy end up selected.
func (s *AppState) arraySelection(count int, radial bool) {
	idx := s.selIndices()
	if len(idx) == 0 || count < 2 {
		return
	}
	_, _, ghx, _, ok := s.groupImgBox(s.EditShapes)
	if !ok {
		return
	}
	originals := make([]model.Shape, len(idx))
	for n, i := range idx {
		originals[n] = cloneShapes(s.EditShapes[i : i+1])[0]
	}
	s.pushUndo(cloneShapes(s.EditShapes))
	pcx, pcy := float64(s.EditW)/2, float64(s.EditH)/2 // radial pivot = canvas centre
	stepX := 2*ghx + math.Max(2*ghx*0.15, 6)           // box width + a small gap
	sel := append([]int(nil), idx...)
	for k := 1; k < count; k++ {
		r := float64(k) * 2 * math.Pi / float64(count)
		c, sn := math.Cos(r), math.Sin(r)
		for _, src := range originals {
			if len(s.EditShapes) >= editMaxShapes {
				s.selectFromSet(sel)
				s.markEditDirty()
				return
			}
			clone := cloneShapes([]model.Shape{src})[0]
			if radial {
				applyRotation(&clone, src, float64(k)*360/float64(count))
				scx, scy := shapeCenter(src)
				nx := pcx + (scx-pcx)*c - (scy-pcy)*sn
				ny := pcy + (scx-pcx)*sn + (scy-pcy)*c
				moveShapeData(&clone, nx-scx, ny-scy)
			} else {
				moveShapeData(&clone, float64(k)*stepX, 0)
			}
			s.EditShapes = append(s.EditShapes, clone)
			sel = append(sel, len(s.EditShapes)-1)
		}
	}
	s.selectFromSet(sel)
	s.markEditDirty()
}

// arrayPanel is the row/ring duplicate controls shown for any selection.
func (s *AppState) arrayPanel(gtx C) D {
	th := s.Th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Dim(gtx, i18n.T("editor.array")) }),
		layout.Rigid(GapV(6).Layout),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					w := gtx.Dp(52)
					gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
					return th.editorBox(gtx, &s.arrayCount, i18n.T("editor.array_count"))
				}),
				layout.Rigid(GapH(8).Layout),
				layout.Flexed(1, func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return th.SecondaryButton(gtx, &s.arrayRowBtn, i18n.T("editor.array_row"), true)
				}),
				layout.Rigid(GapH(6).Layout),
				layout.Flexed(1, func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return th.SecondaryButton(gtx, &s.arrayRingBtn, i18n.T("editor.array_ring"), true)
				}),
			)
		}),
	)
}

// mirrorSelection appends a copy of each selected shape reflected across the canvas vertical centre and
// selects the new copies — the "build one half, mirror to the other" symmetry workflow.
func (s *AppState) mirrorSelection(vertical bool) {
	idx := s.selIndices()
	if len(idx) == 0 {
		return
	}
	s.pushUndo(cloneShapes(s.EditShapes))
	var newIdx []int
	for _, i := range idx {
		if len(s.EditShapes) >= editMaxShapes {
			break
		}
		clone := cloneShapes(s.EditShapes[i : i+1])[0]
		if vertical {
			mirrorShapeY(&clone, s.EditH)
		} else {
			mirrorShapeX(&clone, s.EditW)
		}
		s.EditShapes = append(s.EditShapes, clone)
		newIdx = append(newIdx, len(s.EditShapes)-1)
	}
	if len(newIdx) == 0 {
		return
	}
	s.selectFromSet(newIdx)
	s.markEditDirty()
}

// deleteSel removes the selected shape (never the background) and clears the selection.
func (s *AppState) deleteSel() {
	var idx []int
	for _, i := range s.selIndices() {
		if !s.EditShapes[i].Locked { // locked shapes are protected from deletion
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return
	}
	s.pushUndo(cloneShapes(s.EditShapes))
	for j := len(idx) - 1; j >= 0; j-- { // descending so earlier indices stay valid
		i := idx[j]
		s.EditShapes = append(s.EditShapes[:i], s.EditShapes[i+1:]...)
	}
	s.deselectAll()
	s.markEditDirty()
}

// colorFromShape returns the shape's sRGB-byte colour as an NRGBA for the swatch preview.
func colorFromShape(sh model.Shape) color.NRGBA {
	c := color.NRGBA{A: 255}
	if len(sh.Color) >= 3 {
		c.R = uint8(clampInt(sh.Color[0], 0, 255))
		c.G = uint8(clampInt(sh.Color[1], 0, 255))
		c.B = uint8(clampInt(sh.Color[2], 0, 255))
	}
	if len(sh.Color) >= 4 {
		c.A = uint8(clampInt(sh.Color[3], 0, 255))
	}
	return c
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
