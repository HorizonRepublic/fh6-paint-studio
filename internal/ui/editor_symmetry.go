package ui

import (
	"image"

	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"fh6-paint-studio/internal/i18n"
	"fh6-paint-studio/internal/model"
)

// Symmetry stamping: a toolbar cycle off → mirror-across-vertical → mirror-across-horizontal. When on,
// every newly added shape also drops a mirrored copy, and "Mirror all" reflects the whole design at once.
// This is deliberately stamp-based (no live partner tracking), so it stays robust under delete/undo.
const (
	symOff     = iota
	symMirrorH // mirror left↔right across the vertical centre
	symMirrorV // mirror up↕down across the horizontal centre
)

func (s *AppState) symModeKey() string {
	switch s.symMode {
	case symMirrorH:
		return "editor.sym_h"
	case symMirrorV:
		return "editor.sym_v"
	default:
		return "editor.sym_off"
	}
}

// mirrorAcrossAxis reflects a shape across the active symmetry axis.
func (s *AppState) mirrorAcrossAxis(sh *model.Shape) {
	if s.symMode == symMirrorV {
		mirrorShapeY(sh, s.EditH)
	} else {
		mirrorShapeX(sh, s.EditW)
	}
}

// addShape is the single entry point for creating a shape. With symmetry on it also stamps a mirrored
// copy and selects the pair, so drawing on one side fills both. Honours the shape budget.
func (s *AppState) addShape(sh model.Shape) {
	if len(s.EditShapes) >= editMaxShapes {
		s.Toast = i18n.T("editor.budget_full")
		return
	}
	s.pushUndo(cloneShapes(s.EditShapes))
	s.EditShapes = append(s.EditShapes, sh)
	sel := []int{len(s.EditShapes) - 1}
	if s.symMode != symOff && len(s.EditShapes) < editMaxShapes {
		mir := cloneShapes([]model.Shape{sh})[0]
		s.mirrorAcrossAxis(&mir)
		s.EditShapes = append(s.EditShapes, mir)
		sel = append(sel, len(s.EditShapes)-1)
	}
	s.selectFromSet(sel)
	s.markEditDirty()
}

// mirrorWholeDesign reflects every existing shape across the active axis, making the design symmetric in
// one step. The fresh copies become the selection.
func (s *AppState) mirrorWholeDesign() {
	n := len(s.EditShapes)
	if n <= 1 {
		return
	}
	s.pushUndo(cloneShapes(s.EditShapes))
	var added []int
	for i := 1; i < n; i++ { // skip the background fill at index 0
		if len(s.EditShapes) >= editMaxShapes {
			break
		}
		mir := cloneShapes(s.EditShapes[i : i+1])[0]
		s.mirrorAcrossAxis(&mir)
		s.EditShapes = append(s.EditShapes, mir)
		added = append(added, len(s.EditShapes)-1)
	}
	if len(added) == 0 {
		return
	}
	s.selectFromSet(added)
	s.markEditDirty()
}

// drawSymmetryAxis marks the mirror line on the canvas while symmetry is on.
func (s *AppState) drawSymmetryAxis(gtx C, vp, rect image.Rectangle) {
	if s.symMode == symOff || rect.Dx() <= 0 || s.EditW <= 0 {
		return
	}
	col := s.Th.Accent
	col.A = 130
	scale := float64(rect.Dx()) / float64(s.EditW)
	if s.symMode == symMirrorV { // up↕down mirror → horizontal axis line
		sy := rect.Min.Y + int(float64(s.EditH)/2*scale+0.5)
		paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(vp.Min.X, sy, vp.Max.X, sy+1)).Op())
	} else { // left↔right mirror → vertical axis line
		sx := rect.Min.X + int(float64(s.EditW)/2*scale+0.5)
		paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(sx, vp.Min.Y, sx+1, vp.Max.Y)).Op())
	}
}
