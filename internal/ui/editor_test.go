package ui

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"
)

func TestEnterEditorCopies(t *testing.T) {
	s := NewAppState(NewTheme())
	src := []model.Shape{{Type: model.TypeRectangle, Data: []float64{0, 0, 100, 100}, Color: []int{0, 0, 0, 255}}}
	s.EnterEditor(src, 100, 100)
	if !s.EditorMode || len(s.EditShapes) != 1 || s.EditSel != -1 {
		t.Fatalf("enter: mode=%v n=%d sel=%d", s.EditorMode, len(s.EditShapes), s.EditSel)
	}
	// Editing the working copy must NOT touch the caller's slice.
	s.EditShapes[0].Color[0] = 200
	if src[0].Color[0] == 200 {
		t.Fatal("EnterEditor must deep-copy shapes, not alias them")
	}
}

func TestExitEditorClears(t *testing.T) {
	s := NewAppState(NewTheme())
	s.EnterEditor([]model.Shape{{Type: model.TypeRectangle, Data: []float64{0, 0, 10, 10}, Color: []int{0, 0, 0, 255}}}, 10, 10)
	s.ExitEditor()
	if s.EditorMode || s.EditShapes != nil {
		t.Fatalf("exit: mode=%v shapes=%v", s.EditorMode, s.EditShapes)
	}
}

func TestHitTestTopMost(t *testing.T) {
	s := NewAppState(NewTheme())
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, 100, 100}, Color: []int{0, 0, 0, 255}},             // idx0 bg (skipped)
		{Type: model.TypeRotatedRectangle, Data: []float64{50, 50, 40, 40, 0}, Color: []int{255, 0, 0, 255}}, // idx1 big
		{Type: model.TypeRotatedRectangle, Data: []float64{50, 50, 20, 20, 0}, Color: []int{0, 255, 0, 255}}, // idx2 small on top
	}
	s.EnterEditor(shapes, 100, 100)
	if got := s.hitTest(50, 50); got != 2 {
		t.Fatalf("centre hit = %d, want top-most idx 2", got)
	}
	if got := s.hitTest(15, 50); got != 1 { // inside idx1 only (idx2 spans 30..70)
		t.Fatalf("edge hit = %d, want idx 1", got)
	}
	if got := s.hitTest(1, 1); got != -1 { // background only -> -1 (index 0 is never selectable)
		t.Fatalf("corner hit = %d, want -1", got)
	}
}

func TestMoveShapeCentre(t *testing.T) {
	sh := model.Shape{Type: model.TypeRotatedEllipse, Data: []float64{10, 20, 5, 5, 0}}
	moveShapeData(&sh, 3, -4)
	if sh.Data[0] != 13 || sh.Data[1] != 16 {
		t.Fatalf("centre move = %v, want [13 16 ...]", sh.Data[:2])
	}
}

func TestMoveShapeTriangle(t *testing.T) {
	sh := model.Shape{Type: model.TypeTriangle, Data: []float64{0, 0, 10, 0, 5, 10}}
	moveShapeData(&sh, 2, 3)
	want := []float64{2, 3, 12, 3, 7, 13}
	for i := range want {
		if sh.Data[i] != want[i] {
			t.Fatalf("triangle move = %v, want %v", sh.Data, want)
		}
	}
}

func TestSetShapeScaleEllipse(t *testing.T) {
	sh := model.Shape{Type: model.TypeRotatedEllipse, Data: []float64{10, 10, 4, 6, 0}}
	setShapeScale(&sh, 8, 3)
	if sh.Data[2] != 8 || sh.Data[3] != 3 {
		t.Fatalf("half-extents = %v, want [.. 8 3 ..]", sh.Data)
	}
}

func TestSetShapeScaleTriangleAboutCentroid(t *testing.T) {
	sh := model.Shape{Type: model.TypeTriangle, Data: []float64{0, 0, 12, 0, 0, 9}} // centroid (4,3)
	if hx, hy := shapeHalfExtents(sh); hx != 8 || hy != 6 {
		t.Fatalf("start extents = %v,%v, want 8,6", hx, hy)
	}
	setShapeScale(&sh, 4, 3) // halve each axis
	nhx, nhy := shapeHalfExtents(sh)
	if math.Abs(nhx-4) > 1e-9 || math.Abs(nhy-3) > 1e-9 {
		t.Fatalf("scaled extents = %v,%v, want 4,3", nhx, nhy)
	}
	if cx, cy := shapeCenter(sh); math.Abs(cx-4) > 1e-9 || math.Abs(cy-3) > 1e-9 {
		t.Fatalf("centroid moved to %v,%v, want 4,3", cx, cy)
	}
}

func TestApplyRotationEllipseAddsAngle(t *testing.T) {
	src := model.Shape{Type: model.TypeRotatedEllipse, Data: []float64{0, 0, 5, 5, 10}}
	dst := model.Shape{Type: model.TypeRotatedEllipse, Data: append([]float64(nil), src.Data...)}
	applyRotation(&dst, src, 25)
	if dst.Data[4] != 35 {
		t.Fatalf("theta = %v, want 35", dst.Data[4])
	}
}

func TestZOrderDuplicateDelete(t *testing.T) {
	s := NewAppState(NewTheme())
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, 10, 10}, Color: []int{0, 0, 0, 255}},       // 0 bg
		{Type: model.TypeRotatedEllipse, Data: []float64{1, 1, 1, 1, 0}, Color: []int{1, 0, 0, 255}}, // 1
		{Type: model.TypeRotatedEllipse, Data: []float64{2, 2, 1, 1, 0}, Color: []int{2, 0, 0, 255}}, // 2
	}
	s.EnterEditor(shapes, 10, 10)

	s.EditSel = 1
	s.bringForward()
	if s.EditSel != 2 || s.EditShapes[2].Color[0] != 1 {
		t.Fatalf("bringForward: sel=%d top=%v", s.EditSel, s.EditShapes[2].Color)
	}
	s.sendBack()
	if s.EditSel != 1 || s.EditShapes[1].Color[0] != 1 {
		t.Fatalf("sendBack: sel=%d", s.EditSel)
	}
	s.EditSel = 1
	s.sendBack() // must never pass the background (index 0)
	if s.EditSel != 1 {
		t.Fatalf("sendBack passed the background: sel=%d", s.EditSel)
	}

	n := len(s.EditShapes)
	s.EditSel = 1
	s.duplicateSel()
	if len(s.EditShapes) != n+1 || s.EditSel != 2 {
		t.Fatalf("duplicate: n=%d sel=%d", len(s.EditShapes), s.EditSel)
	}

	n = len(s.EditShapes)
	s.deleteSel()
	if len(s.EditShapes) != n-1 || s.EditSel != -1 {
		t.Fatalf("delete: n=%d sel=%d", len(s.EditShapes), s.EditSel)
	}
}

func TestBlankCanvasSeedsBackground(t *testing.T) {
	s := NewAppState(NewTheme())
	s.EnterEditor(nil, 256, 256)
	if len(s.EditShapes) != 1 {
		t.Fatalf("blank canvas shapes = %d, want 1 background slot", len(s.EditShapes))
	}
	if a := s.EditShapes[0].Color[3]; a != 0 {
		t.Fatalf("background alpha = %d, want 0 (transparent)", a)
	}
}

func TestAddPrimitiveAndUndoRedo(t *testing.T) {
	s := NewAppState(NewTheme())
	s.EnterEditor(nil, 256, 256)

	s.addPrimitive(primCircle)
	if len(s.EditShapes) != 2 || s.EditSel != 1 {
		t.Fatalf("add: n=%d sel=%d", len(s.EditShapes), s.EditSel)
	}
	if model.KindFromType(s.EditShapes[1].Type) != model.KindEllipse {
		t.Fatalf("added kind = %v, want ellipse", model.KindFromType(s.EditShapes[1].Type))
	}

	s.undo()
	if len(s.EditShapes) != 1 {
		t.Fatalf("after undo n=%d, want 1", len(s.EditShapes))
	}
	s.redo()
	if len(s.EditShapes) != 2 {
		t.Fatalf("after redo n=%d, want 2", len(s.EditShapes))
	}

	// A fresh action must clear the redo stack.
	s.undo()
	s.addPrimitive(primSquare)
	if len(s.editRedo) != 0 {
		t.Fatalf("redo stack not cleared after a new action: %d", len(s.editRedo))
	}
}

func TestSwapLayersKeepsSelectionAndProtectsBackground(t *testing.T) {
	s := NewAppState(NewTheme())
	s.EnterEditor(nil, 100, 100) // background at index 0
	s.addPrimitive(primCircle)   // index 1
	s.addPrimitive(primSquare)   // index 2
	s.EditSel = 1
	s.layerDragMoved = false
	s.swapLayers(1, 2)
	if s.EditSel != 2 {
		t.Fatalf("selection after swap = %d, want 2 (follows the moved shape)", s.EditSel)
	}
	before := len(s.EditShapes)
	s.layerDragMoved = false
	s.swapLayers(1, 0) // must be a no-op: index 0 is the background
	if len(s.EditShapes) != before {
		t.Fatal("swapLayers must not touch the background slot")
	}
}

func TestApplyColorSlidersWritesAlpha(t *testing.T) {
	s := NewAppState(NewTheme())
	sh := &model.Shape{Type: model.TypeRotatedEllipse, Data: []float64{0, 0, 5, 5, 0}, Color: []int{0, 0, 0, 255}}
	s.pickR.Value, s.pickG.Value, s.pickB.Value, s.pickA.Value = 1, 0.5, 0, 0.5
	if !s.applyColorSliders(sh) {
		t.Fatal("expected a colour change")
	}
	want := []int{255, 128, 0, 128} // 0.5*255+0.5 rounds to 128
	for i := range want {
		if sh.Color[i] != want[i] {
			t.Fatalf("colour = %v, want %v", sh.Color, want)
		}
	}
}

func TestZoomStepClamps(t *testing.T) {
	s := NewAppState(NewTheme())
	s.editZoom = 1
	for i := 0; i < 50; i++ {
		s.zoomStep(1.25)
	}
	if s.editZoom > 16.0001 {
		t.Fatalf("zoom not clamped high: %v", s.editZoom)
	}
	for i := 0; i < 60; i++ {
		s.zoomStep(0.8)
	}
	if s.editZoom < 0.2-1e-9 {
		t.Fatalf("zoom not clamped low: %v", s.editZoom)
	}
}

func TestMaskScaleFullExtentConvention(t *testing.T) {
	entries := maskbank.All()
	if len(entries) == 0 {
		t.Skip("no mask bank registered")
	}
	sh := defaultMaskShape(entries[0], 300, 300)
	if k := model.KindFromType(sh.Type); !model.IsMask(k) {
		t.Fatalf("inserted word is not a mask kind: %v", k)
	}
	// Mask slots 2,3 are FULL extents, so half-extents must be half of them.
	hx, hy := shapeHalfExtents(sh)
	if math.Abs(hx-sh.Data[2]/2) > 1e-9 || math.Abs(hy-sh.Data[3]/2) > 1e-9 {
		t.Fatalf("mask half-extents = %v,%v, want %v,%v", hx, hy, sh.Data[2]/2, sh.Data[3]/2)
	}
	// Scaling to half-extents 50,40 must store full extents 100,80.
	setShapeScale(&sh, 50, 40)
	if math.Abs(sh.Data[2]-100) > 1e-9 || math.Abs(sh.Data[3]-80) > 1e-9 {
		t.Fatalf("mask setShapeScale stored %v,%v, want 100,80", sh.Data[2], sh.Data[3])
	}
}

func TestBuildBankRowsHasCategories(t *testing.T) {
	rows := buildBankRows(maskbank.All(), bankCols)
	headers := map[string]bool{}
	thumbCount := 0
	for _, r := range rows {
		if r.header != "" {
			headers[r.header] = true
		} else {
			thumbCount += len(r.thumbs)
		}
	}
	for _, want := range []string{"editor.cat_letters", "editor.cat_curves", "editor.cat_decals"} {
		if !headers[want] {
			t.Fatalf("missing category header %q (got %v)", want, headers)
		}
	}
	// Primitives are excluded from the picker (they live in the quick-add row).
	prims := 0
	for _, e := range maskbank.All() {
		if e.Category == "primitive" {
			prims++
		}
	}
	if thumbCount != len(maskbank.All())-prims {
		t.Fatalf("thumb count %d, want %d (all minus %d primitives)", thumbCount, len(maskbank.All())-prims, prims)
	}
}

func TestApplyRotationTrianglePreservesCentroid(t *testing.T) {
	src := model.Shape{Type: model.TypeTriangle, Data: []float64{0, 0, 12, 0, 6, 9}}
	dst := model.Shape{Type: model.TypeTriangle, Data: append([]float64(nil), src.Data...)}
	cx0, cy0 := shapeCenter(src)
	applyRotation(&dst, src, 37)
	cx1, cy1 := shapeCenter(dst)
	if math.Abs(cx1-cx0) > 1e-9 || math.Abs(cy1-cy0) > 1e-9 {
		t.Fatalf("centroid moved: (%v,%v) -> (%v,%v)", cx0, cy0, cx1, cy1)
	}
	if dst.Data[0] == src.Data[0] && dst.Data[1] == src.Data[1] {
		t.Fatal("rotation did not move the first vertex")
	}
}
