package ui

import (
	"image"
	"image/color"
	"math"
	"testing"
	"time"

	"gioui.org/io/key"

	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
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

func multiDoc() *AppState {
	s := NewAppState(NewTheme())
	s.EnterEditor([]model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, 100, 100}, Color: []int{0, 0, 0, 255}},       // 0 bg
		{Type: model.TypeRotatedEllipse, Data: []float64{20, 20, 5, 5, 0}, Color: []int{1, 0, 0, 255}}, // 1
		{Type: model.TypeRotatedEllipse, Data: []float64{40, 40, 5, 5, 0}, Color: []int{2, 0, 0, 255}}, // 2
	}, 100, 100)
	return s
}

func TestToggleSelBuildsAndShrinksGroup(t *testing.T) {
	s := multiDoc()
	s.toggleSel(1)
	if s.selCount() != 1 || s.EditSel != 1 {
		t.Fatalf("toggle 1: count=%d sel=%d", s.selCount(), s.EditSel)
	}
	s.toggleSel(2)
	if s.selCount() != 2 || !s.isSelected(1) || !s.isSelected(2) {
		t.Fatalf("toggle 2: count=%d in1=%v in2=%v", s.selCount(), s.isSelected(1), s.isSelected(2))
	}
	s.toggleSel(2) // remove the primary → the other survives as primary
	if s.selCount() != 1 || s.isSelected(2) || s.EditSel != 1 {
		t.Fatalf("untoggle 2: count=%d sel=%d", s.selCount(), s.EditSel)
	}
}

func TestSelectAllAndDeselect(t *testing.T) {
	s := multiDoc()
	s.selectAll()
	if s.selCount() != 2 {
		t.Fatalf("selectAll count=%d, want 2 (background excluded)", s.selCount())
	}
	s.deselectAll()
	if s.selCount() != 0 || s.selValid() {
		t.Fatalf("deselectAll: count=%d valid=%v", s.selCount(), s.selValid())
	}
}

func TestGroupNudgeMovesEveryShape(t *testing.T) {
	s := multiDoc()
	s.selectAll()
	s.nudge(1, 0, key.ModShift) // +10 px to each selected shape
	if s.EditShapes[1].Data[0] != 30 || s.EditShapes[2].Data[0] != 50 {
		t.Fatalf("group nudge x = %v,%v, want 30,50", s.EditShapes[1].Data[0], s.EditShapes[2].Data[0])
	}
}

func TestGroupDeleteRemovesAllSelected(t *testing.T) {
	s := multiDoc()
	s.selectAll()
	s.deleteSel()
	if len(s.EditShapes) != 1 || s.selValid() {
		t.Fatalf("group delete: n=%d valid=%v, want 1 and no selection", len(s.EditShapes), s.selValid())
	}
}

func TestGroupDuplicateAppendsAndSelectsCopies(t *testing.T) {
	s := multiDoc()
	s.selectAll()
	s.duplicateSel()
	if len(s.EditShapes) != 5 {
		t.Fatalf("group duplicate n=%d, want 5", len(s.EditShapes))
	}
	if s.selCount() != 2 || !s.isSelected(3) || !s.isSelected(4) {
		t.Fatalf("group duplicate should select the 2 new copies: count=%d", s.selCount())
	}
}

func TestMirrorShapeXEllipseNegatesRotation(t *testing.T) {
	sh := model.Shape{Type: model.TypeRotatedEllipse, Data: []float64{20, 50, 5, 8, 30}}
	mirrorShapeX(&sh, 100)
	if sh.Data[0] != 80 || sh.Data[4] != -30 {
		t.Fatalf("mirror ellipse = %v, want cx 80, theta -30", sh.Data)
	}
}

func TestMirrorShapeXTriangleReflectsVertices(t *testing.T) {
	sh := model.Shape{Type: model.TypeTriangle, Data: []float64{10, 0, 30, 0, 20, 20}}
	mirrorShapeX(&sh, 100)
	want := []float64{90, 0, 70, 0, 80, 20}
	for i := range want {
		if sh.Data[i] != want[i] {
			t.Fatalf("mirror triangle = %v, want %v", sh.Data, want)
		}
	}
}

func TestMirrorSelectionDuplicatesAcrossCentre(t *testing.T) {
	s := multiDoc()
	s.selectSingle(1)
	s.mirrorSelection(false) // horizontal (left↔right)
	if len(s.EditShapes) != 4 {
		t.Fatalf("mirror should append one copy, n=%d", len(s.EditShapes))
	}
	if s.EditShapes[3].Data[0] != 80 || s.EditShapes[3].Data[1] != 20 || s.EditSel != 3 {
		t.Fatalf("horizontal mirror copy = (%v,%v) sel=%d, want (80,20) selected", s.EditShapes[3].Data[0], s.EditShapes[3].Data[1], s.EditSel)
	}
	s.selectSingle(1)
	s.mirrorSelection(true) // vertical (up↕down)
	if c := s.EditShapes[len(s.EditShapes)-1]; c.Data[0] != 20 || c.Data[1] != 80 {
		t.Fatalf("vertical mirror copy = (%v,%v), want (20,80)", c.Data[0], c.Data[1])
	}
}

func TestMirrorShapeYTriangle(t *testing.T) {
	sh := model.Shape{Type: model.TypeTriangle, Data: []float64{10, 0, 30, 0, 20, 20}}
	mirrorShapeY(&sh, 100)
	want := []float64{10, 100, 30, 100, 20, 80}
	for i := range want {
		if sh.Data[i] != want[i] {
			t.Fatalf("mirrorShapeY triangle = %v, want %v", sh.Data, want)
		}
	}
}

func bboxX0(s *AppState, i int) int {
	sh := s.EditShapes[i]
	x0, _, _, _ := raster.BBox(model.KindFromType(sh.Type), model.ParamsFromShape(sh), s.EditW, s.EditH)
	return x0
}

func TestAlignLeftEqualisesLeftEdges(t *testing.T) {
	s := multiDoc()
	s.selectAll()
	s.alignSelection(alignLeft)
	if bboxX0(s, 1) != bboxX0(s, 2) {
		t.Fatalf("align-left left edges differ: %d vs %d", bboxX0(s, 1), bboxX0(s, 2))
	}
}

func TestDistributeNeedsThree(t *testing.T) {
	s := multiDoc()
	s.selectAll() // only 2 selected
	before := s.EditShapes[1].Data[0]
	s.alignSelection(distributeH)
	if s.EditShapes[1].Data[0] != before {
		t.Fatal("distribute with <3 shapes must be a no-op")
	}
}

func TestDoubleClickedRequiresTwo(t *testing.T) {
	s := NewAppState(NewTheme())
	t0 := time.Unix(100, 0)
	if s.doubleClicked(1, 3, t0) {
		t.Fatal("first click must only arm, not fire")
	}
	if !s.doubleClicked(1, 3, t0.Add(100*time.Millisecond)) {
		t.Fatal("second click within the window must fire")
	}
	if s.doubleClicked(1, 3, t0.Add(150*time.Millisecond)) {
		t.Fatal("a click right after firing must not immediately fire again")
	}
	s.doubleClicked(1, 3, t0)
	if s.doubleClicked(1, 4, t0.Add(50*time.Millisecond)) {
		t.Fatal("a click on a different item must not fire")
	}
	s.doubleClicked(1, 3, t0)
	if s.doubleClicked(1, 3, t0.Add(time.Second)) {
		t.Fatal("second click outside the window must not fire")
	}
}

func TestNiceStep(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{0.3, 1}, {1, 1}, {1.5, 2}, {3, 5}, {7, 10}, {12, 20}, {30, 50}, {77, 100}, {240, 500},
	} {
		if got := niceStep(c.in); got != c.want {
			t.Fatalf("niceStep(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSelHandlePtsRotates(t *testing.T) {
	s := NewAppState(NewTheme())
	s.EnterEditor([]model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, 100, 100}, Color: []int{0, 0, 0, 255}},
		{Type: model.TypeRotatedEllipse, Data: []float64{50, 50, 20, 10, 90}, Color: []int{1, 0, 0, 255}},
	}, 100, 100)
	s.EditSel = 1
	rect := image.Rect(0, 0, 100, 100) // screen == image coords
	pts := s.selHandlePts(rect)
	// At 90° the oriented box turns: NW handle (local -20,-10) lands at (60,30), SE at (40,70).
	if pts[0].X != 60 || pts[0].Y != 30 {
		t.Fatalf("rotated NW handle = %v, want (60,30)", pts[0])
	}
	if pts[4].X != 40 || pts[4].Y != 70 {
		t.Fatalf("rotated SE handle = %v, want (40,70)", pts[4])
	}
}

func TestRGBHSVRoundTrip(t *testing.T) {
	di := func(a, b uint8) int {
		d := int(a) - int(b)
		if d < 0 {
			return -d
		}
		return d
	}
	for _, c := range []color.NRGBA{
		{R: 200, G: 40, B: 90, A: 255},
		{R: 10, G: 220, B: 130, A: 255},
		{R: 128, G: 128, B: 128, A: 255},
		{R: 0, G: 0, B: 0, A: 255},
		{R: 255, G: 255, B: 255, A: 255},
	} {
		h, sat, v := rgbToHSV(c)
		got := hsvToRGB(h, sat, v)
		if di(got.R, c.R) > 2 || di(got.G, c.G) > 2 || di(got.B, c.B) > 2 {
			t.Fatalf("HSV roundtrip %v -> %v", c, got)
		}
	}
}

func TestResetEditorCanvasBlanksKeepingSize(t *testing.T) {
	s := multiDoc() // 100x100, background + 2 shapes, 2 selected
	s.selectAll()
	s.resetEditorCanvas()
	if len(s.EditShapes) != 1 || s.selValid() {
		t.Fatalf("reset: n=%d valid=%v, want 1 (background) and no selection", len(s.EditShapes), s.selValid())
	}
	if s.EditW != 100 || s.EditH != 100 {
		t.Fatalf("reset changed size to %dx%d, want 100x100", s.EditW, s.EditH)
	}
}

func TestApplyHSVWritesShapeColour(t *testing.T) {
	s := multiDoc()
	s.selectSingle(1)
	s.pickH, s.pickS, s.pickV = 0, 1, 1 // pure red
	s.applyHSV()
	if c := s.EditShapes[1].Color; c[0] != 255 || c[1] != 0 || c[2] != 0 {
		t.Fatalf("applyHSV red = %v, want [255 0 0 ...]", c)
	}
}
