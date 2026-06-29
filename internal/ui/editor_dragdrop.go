package ui

import (
	"image"
	"math"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"fh6-paint-studio/internal/i18n"
	"fh6-paint-studio/internal/model"
)

const bankDragThreshold = 6 // px the cursor must move before a press becomes a drag

// dragOverlay is a full-window pass-through pointer layer, added last so it sits atop everything without
// blocking clicks below it. Because it contains the press point, the router keeps it in the press handler
// set, so it receives the drag events even while a bank thumbnail holds the implicit grab — that is what
// lets a shape be dragged out of the left panel and onto the canvas. It tracks the cursor in its own
// (window) coordinates, learns the window→canvas offset while the cursor hovers the canvas, and on release
// over the canvas drops the held shape at that image position.
func (s *AppState) dragOverlay(gtx C, sz image.Point) {
	pass := pointer.PassOp{}.Push(gtx.Ops)
	area := clip.Rect(image.Rectangle{Max: sz}).Push(gtx.Ops)
	event.Op(gtx.Ops, &s.dragTag)
	area.Pop()
	pass.Pop()

	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target: &s.dragTag,
			Kinds:  pointer.Press | pointer.Move | pointer.Drag | pointer.Release | pointer.Cancel,
		})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			s.dragStartWin = pe.Position
		case pointer.Move:
			s.dragWin = pe.Position
			if s.canvasHover {
				s.canvasOff = pe.Position.Sub(s.canvasLocal)
				s.canvasOffOK = true
			}
		case pointer.Drag:
			s.dragWin = pe.Position
			if s.bankCandKind != 0 && !s.bankDragging {
				d := pe.Position.Sub(s.dragStartWin)
				if math.Hypot(float64(d.X), float64(d.Y)) > bankDragThreshold {
					s.bankDragging = true
				}
			}
		case pointer.Release, pointer.Cancel:
			if s.bankDragging {
				s.dropBankDrag()
			}
			s.bankDragging = false
			s.bankCandKind = 0
		}
	}

	if s.bankDragging {
		s.drawDragGhost(gtx)
		gtx.Execute(op.InvalidateCmd{})
	}
}

// candidateShape builds the shape the current drag would create (centred; the drop re-centres it).
func (s *AppState) candidateShape() (model.Shape, bool) {
	switch s.bankCandKind {
	case 1:
		if e, ok := maskEntryByWord(uint16(s.bankCandWord)); ok {
			return defaultMaskShape(e, s.EditW, s.EditH), true
		}
	case 2:
		return defaultPrimitive(s.bankCandPrim, s.EditW, s.EditH), true
	}
	return model.Shape{}, false
}

// dropBankDrag inserts the held shape at the cursor's image position. If the window→canvas offset hasn't
// been observed yet it falls back to the canvas centre; a drop outside the canvas is cancelled.
func (s *AppState) dropBankDrag() {
	sh, ok := s.candidateShape()
	if !ok {
		return
	}
	if len(s.EditShapes) >= editMaxShapes {
		s.Toast = i18n.T("editor.budget_full")
		return
	}
	cx, cy := float64(s.EditW)/2, float64(s.EditH)/2 // fallback: centre
	if r := s.canvasImgRect; s.canvasOffOK && r.Dx() > 0 && r.Dy() > 0 {
		local := s.dragWin.Sub(s.canvasOff)
		if local.X < float32(r.Min.X) || local.X > float32(r.Max.X) || local.Y < float32(r.Min.Y) || local.Y > float32(r.Max.Y) {
			return // dropped outside the canvas
		}
		cx = float64(local.X-float32(r.Min.X)) / float64(r.Dx()) * float64(s.EditW)
		cy = float64(local.Y-float32(r.Min.Y)) / float64(r.Dy()) * float64(s.EditH)
	}
	scx, scy := shapeCenter(sh)
	moveShapeData(&sh, cx-scx, cy-scy)
	s.pushUndo(cloneShapes(s.EditShapes))
	s.EditShapes = append(s.EditShapes, sh)
	s.selectSingle(len(s.EditShapes) - 1)
	s.markEditDirty()
}

// drawDragGhost paints a small marker of the dragged shape at the cursor while a drag is underway.
func (s *AppState) drawDragGhost(gtx C) {
	switch s.bankCandKind {
	case 1:
		thumb, ok := s.maskThumbOp(uint16(s.bankCandWord))
		if !ok {
			return
		}
		tsz := thumb.Size()
		off := op.Offset(image.Pt(int(s.dragWin.X)-tsz.X/2, int(s.dragWin.Y)-tsz.Y/2)).Push(gtx.Ops)
		thumb.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		off.Pop()
	case 2:
		d := gtx.Dp(9)
		off := op.Offset(image.Pt(int(s.dragWin.X)-d, int(s.dragWin.Y)-d)).Push(gtx.Ops)
		drawShapeIcon(gtx, primIcon(s.bankCandPrim), s.Th.Accent, true)
		off.Pop()
	}
}

func primIcon(kind int) string {
	switch kind {
	case primSquare:
		return "rectangle"
	case primTriangle:
		return "triangle"
	default:
		return "ellipse"
	}
}
