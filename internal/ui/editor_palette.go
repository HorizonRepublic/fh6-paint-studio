package ui

import (
	"image"
	"math"
	"strconv"
	"time"

	"gioui.org/gesture"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"fh6-paint-studio/internal/i18n"
	"fh6-paint-studio/internal/model"
)

// editor primitive kinds for the add-palette.
const (
	primCircle = iota
	primSquare
	primTriangle
	primGlow
	primDisk
)

const editMaxShapes = 3000 // FH6 per-panel budget

// addPaletteRows is the 3 basic primitives over the 2 gradient primitives.
func (s *AppState) addPaletteRows(gtx C) D {
	gap := layout.Rigid(GapH(6).Layout)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D { return s.paletteBtn(gtx, &s.palCircle, "ellipse", "editor.k_circle") }),
				gap,
				layout.Flexed(1, func(gtx C) D { return s.paletteBtn(gtx, &s.palSquare, "rectangle", "editor.k_square") }),
				gap,
				layout.Flexed(1, func(gtx C) D { return s.paletteBtn(gtx, &s.palTriangle, "triangle", "editor.k_triangle") }),
			)
		}),
		layout.Rigid(GapV(6).Layout),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D { return s.paletteBtn(gtx, &s.palGlow, "ellipse", "editor.k_glow") }),
				gap,
				layout.Flexed(1, func(gtx C) D { return s.paletteBtn(gtx, &s.palDisk, "ellipse", "editor.k_disk") }),
			)
		}),
	)
}

// paletteBtn is one add-shape tile: an icon over a short label in a rounded surface.
func (s *AppState) paletteBtn(gtx C, b *widget.Clickable, iconKind, labelKey string) D {
	th := s.Th
	return material.Clickable(gtx, b, func(gtx C) D {
		return layout.Background{}.Layout(gtx,
			func(gtx C) D {
				sz := gtx.Constraints.Min
				bg := th.SurfaceHi
				if b.Hovered() {
					bg = th.Surface
				}
				fillRRect(gtx, bg, sz, 8)
				pointer.CursorPointer.Add(gtx.Ops)
				return D{Size: sz}
			},
			func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Top: 8, Bottom: 7, Left: 4, Right: 4}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return drawShapeIcon(gtx, iconKind, th.Text, true) }),
						layout.Rigid(GapV(4).Layout),
						layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 11, i18n.T(labelKey), th.TextDim) }),
					)
				})
			},
		)
	})
}

// undoRedoRow is the two history buttons, each disabled when its stack is empty.
func (s *AppState) undoRedoRow(gtx C) D {
	th := s.Th
	return layout.Flex{}.Layout(gtx,
		layout.Flexed(1, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.ArrowButton(gtx, &s.editUndoBtn, true, i18n.T("editor.undo"), len(s.editUndo) > 0)
		}),
		layout.Rigid(GapH(8).Layout),
		layout.Flexed(1, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.ArrowButton(gtx, &s.editRedoBtn, false, i18n.T("editor.redo"), len(s.editRedo) > 0)
		}),
	)
}

// newCanvasRow is the editor's "start fresh" button — a two-step confirm (turns red) so it can't wipe the
// working doc by accident.
func (s *AppState) newCanvasRow(gtx C) D {
	th := s.Th
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	if s.clearArmed && gtx.Now.Before(s.clearArmedAt.Add(3*time.Second)) {
		return th.DangerButton(gtx, &s.EditNewBtn, i18n.T("editor.clear_confirm"))
	}
	return th.SecondaryButton(gtx, &s.EditNewBtn, i18n.T("editor.new_canvas"), true)
}

// layersHeader is the "Layers" label with the current shape count (excluding the background).
func (s *AppState) layersHeader(gtx C) D {
	th := s.Th
	n := len(s.EditShapes) - 1
	if n < 0 {
		n = 0
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Dim(gtx, i18n.T("editor.layers")) }),
		layout.Flexed(1, spacerW),
		layout.Rigid(func(gtx C) D {
			col := th.TextDim
			if n >= editMaxShapes*9/10 {
				col = th.Warn
			}
			return th.Lbl(gtx, 12, strconv.Itoa(n)+" / "+strconv.Itoa(editMaxShapes), col)
		}),
	)
}

// layerList is the scrollable stack of shapes (front-most first): click a row to select, drag its grip
// to change z-position.
func (s *AppState) layerList(gtx C) D {
	th := s.Th
	n := len(s.EditShapes) - 1 // exclude the background slot
	if n <= 0 {
		return th.Dim(gtx, i18n.T("editor.no_shapes"))
	}
	for len(s.editLayerBtns) < len(s.EditShapes) {
		s.editLayerBtns = append(s.editLayerBtns, widget.Clickable{})
		s.layerDrags = append(s.layerDrags, gesture.Drag{})
	}
	return material.List(th.M, &s.editLayerList).Layout(gtx, n, func(gtx C, i int) D {
		idx := len(s.EditShapes) - 1 - i // top row = front-most shape
		if s.editLayerBtns[idx].Clicked(gtx) {
			s.selectSingle(idx)
		}
		s.updateLayerDrag(gtx, idx)
		return layout.Inset{Bottom: 4}.Layout(gtx, func(gtx C) D { return s.layerRow(gtx, idx) })
	})
}

// layerRow renders one layer entry: a drag grip, a colour swatch, the kind icon, and the z-index,
// highlighted when it is the selected shape.
func (s *AppState) layerRow(gtx C, idx int) D {
	th := s.Th
	sh := s.EditShapes[idx]
	selected := s.isSelected(idx)
	return s.editLayerBtns[idx].Layout(gtx, func(gtx C) D {
		return layout.Background{}.Layout(gtx,
			func(gtx C) D {
				sz := gtx.Constraints.Min
				if selected {
					borderRRect(gtx, th.Accent, th.SurfaceHi, sz, 6, 1)
				} else {
					fillRRect(gtx, th.Surface, sz, 6)
				}
				pointer.CursorPointer.Add(gtx.Ops)
				return D{Size: sz}
			},
			func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Top: 6, Bottom: 6, Left: 6, Right: 8}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return s.layerGrip(gtx, idx) }),
						layout.Rigid(GapH(6).Layout),
						layout.Rigid(func(gtx C) D {
							szb := image.Pt(gtx.Dp(14), gtx.Dp(14))
							borderRRect(gtx, th.Border, colorFromShape(sh), szb, 3, 1)
							return D{Size: szb}
						}),
						layout.Rigid(GapH(8).Layout),
						layout.Rigid(func(gtx C) D { return s.layerIcon(gtx, sh) }),
						layout.Flexed(1, spacerW),
						layout.Rigid(func(gtx C) D {
							col := th.TextDim
							if selected {
								col = th.Accent
							}
							return th.Lbl(gtx, 12, "#"+strconv.Itoa(idx), col)
						}),
					)
				})
			},
		)
	})
}

// layerGrip is the drag handle on the left of a layer row (three bars); dragging it reorders.
func (s *AppState) layerGrip(gtx C, idx int) D {
	th := s.Th
	w, h := gtx.Dp(14), gtx.Dp(16)
	bar := gtx.Dp(2)
	gap := gtx.Dp(3)
	y := h/2 - (bar + gap)
	for i := 0; i < 3; i++ {
		paint.FillShape(gtx.Ops, th.TextDim, clip.Rect(image.Rect(2, y, w-2, y+bar)).Op())
		y += bar + gap
	}
	area := clip.Rect(image.Rect(0, 0, w, h)).Push(gtx.Ops)
	s.layerDrags[idx].Add(gtx.Ops)
	pointer.CursorRowResize.Add(gtx.Ops)
	area.Pop()
	return D{Size: image.Pt(w, h)}
}

// updateLayerDrag turns a grip drag into z-order swaps: each row-pitch of vertical travel moves the
// dragged shape one step (down = toward the back/lower index, up = toward the front).
func (s *AppState) updateLayerDrag(gtx C, idx int) {
	pitch := float32(gtx.Dp(34))
	for {
		ev, ok := s.layerDrags[idx].Update(gtx.Metric, gtx.Source, gesture.Vertical)
		if !ok {
			break
		}
		switch ev.Kind {
		case pointer.Press:
			s.layerDragFrom = idx
			s.layerDragLastY = ev.Position.Y
			s.layerDragAccum = 0
			s.layerDragMoved = false
			s.selectSingle(idx)
		case pointer.Drag:
			if s.layerDragFrom < 1 {
				break
			}
			s.layerDragAccum += float64(ev.Position.Y - s.layerDragLastY)
			s.layerDragLastY = ev.Position.Y
			for float32(s.layerDragAccum) >= pitch && s.layerDragFrom > 1 {
				s.swapLayers(s.layerDragFrom, s.layerDragFrom-1)
				s.layerDragFrom--
				s.layerDragAccum -= float64(pitch)
			}
			for float32(s.layerDragAccum) <= -pitch && s.layerDragFrom < len(s.EditShapes)-1 {
				s.swapLayers(s.layerDragFrom, s.layerDragFrom+1)
				s.layerDragFrom++
				s.layerDragAccum += float64(pitch)
			}
		case pointer.Release, pointer.Cancel:
			s.layerDragFrom = -1
		}
	}
}

// swapLayers exchanges two shapes' z-positions (one undo snapshot per drag), keeping the selection on
// the moved shape.
func (s *AppState) swapLayers(a, b int) {
	if a < 1 || b < 1 || a >= len(s.EditShapes) || b >= len(s.EditShapes) {
		return
	}
	if !s.layerDragMoved {
		s.pushUndo(cloneShapes(s.EditShapes))
		s.layerDragMoved = true
	}
	s.EditShapes[a], s.EditShapes[b] = s.EditShapes[b], s.EditShapes[a]
	switch s.EditSel {
	case a:
		s.EditSel = b
	case b:
		s.EditSel = a
	}
	s.markEditDirty()
}

// handlePaletteActions processes the add-shape, undo and redo buttons.
func (s *AppState) handlePaletteActions(gtx C) {
	prims := []struct {
		b *widget.Clickable
		k int
	}{
		{&s.palCircle, primCircle},
		{&s.palSquare, primSquare},
		{&s.palTriangle, primTriangle},
		{&s.palGlow, primGlow},
		{&s.palDisk, primDisk},
	}
	for i, p := range prims {
		if p.b.Pressed() && s.bankCandKind == 0 && !s.bankDragging {
			s.bankCandKind, s.bankCandPrim = 2, p.k // a press that may become a drag
		}
		if p.b.Clicked(gtx) && s.doubleClicked(2, i, gtx.Now) {
			s.addPrimitive(p.k)
		}
	}
	if s.editUndoBtn.Clicked(gtx) {
		s.undo()
	}
	if s.editRedoBtn.Clicked(gtx) {
		s.redo()
	}
	if s.EditNewBtn.Clicked(gtx) {
		if s.clearArmed && gtx.Now.Before(s.clearArmedAt.Add(3*time.Second)) {
			s.clearArmed = false
			s.resetEditorCanvas()
		} else {
			s.clearArmed = true
			s.clearArmedAt = gtx.Now
		}
	}
}

// addPrimitive inserts a default-sized primitive of the given kind at the canvas centre, on top, and
// selects it. It refuses past the FH6 per-panel budget.
func (s *AppState) addPrimitive(kind int) {
	if len(s.EditShapes) >= editMaxShapes {
		s.Toast = i18n.T("editor.budget_full")
		return
	}
	s.pushUndo(cloneShapes(s.EditShapes))
	s.EditShapes = append(s.EditShapes, defaultPrimitive(kind, s.EditW, s.EditH))
	s.selectSingle(len(s.EditShapes) - 1)
	s.markEditDirty()
}

// defaultPrimitive builds a centred, ~1/8-canvas shape of the given kind in a neutral grey.
func defaultPrimitive(kind, w, h int) model.Shape {
	cx, cy := float64(w)/2, float64(h)/2
	r := math.Max(8, math.Min(float64(w), float64(h))/8)
	col := []int{180, 180, 180, 255}
	switch kind {
	case primSquare:
		return model.Shape{Type: model.TypeRotatedRectangle, Data: []float64{cx, cy, r, r, 0}, Color: col}
	case primTriangle:
		return model.Shape{Type: model.TypeTriangle, Data: []float64{
			cx, cy - r,
			cx - r*0.866, cy + r*0.5,
			cx + r*0.866, cy + r*0.5,
		}, Color: col}
	case primGlow:
		return model.Shape{Type: model.TypeGradGlow, Data: []float64{cx, cy, r, r, 0, 0}, Color: col}
	case primDisk:
		return model.Shape{Type: model.TypeGradDisk, Data: []float64{cx, cy, r, r, 0, 0}, Color: col}
	default: // primCircle
		return model.Shape{Type: model.TypeRotatedEllipse, Data: []float64{cx, cy, r, r, 0}, Color: col}
	}
}

// iconForShape maps a shape kind to the closest palette icon (ellipse covers ellipse/glow/disk/mask/line).
func iconForShape(sh model.Shape) string {
	switch model.KindFromType(sh.Type) {
	case model.KindRectangle:
		return "rectangle"
	case model.KindTriangle:
		return "triangle"
	default:
		return "ellipse"
	}
}
