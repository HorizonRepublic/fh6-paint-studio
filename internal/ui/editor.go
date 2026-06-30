package ui

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"sort"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"fh6-paint-studio/internal/i18n"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// editor drag kinds.
const (
	dragNone = iota
	dragMove
	dragScale
	dragRotate
)

const (
	editHandleHitR = 10 // px grab radius for the bbox handles + rotate knob
	editRotateStem = 26 // px the rotate knob sits above the selection's top edge
)

// editorDrag is the in-flight manipulation. kind 0 = none.
type editorDrag struct {
	kind   int
	handle int           // 0..7 bbox handle (NW,N,NE,E,SE,S,SW,W) for dragScale
	start  []model.Shape // snapshot of EditShapes at drag start (for live recompute + undo)
	anchor f32.Point     // image-fraction pointer position at press (for move)
	cx, cy float64       // selected shape centre at drag start (image px)
	theta0 float64       // selected shape rotation at drag start (deg)
	ang0   float64       // pointer angle from centre at press (rad), for rotate
}

// cloneShapes deep-copies a shape slice (Data and Color are slices, so a shallow copy aliases them).
func cloneShapes(in []model.Shape) []model.Shape {
	if in == nil {
		return nil
	}
	out := make([]model.Shape, len(in))
	for i, s := range in {
		c := s
		c.Data = append([]float64(nil), s.Data...)
		c.Color = append([]int(nil), s.Color...)
		out[i] = c
	}
	return out
}

// hitTest returns the top-most shape index (highest = front, index 0 = background, skipped) whose
// per-kind footprint contains the image-space point, or -1 if the point is over empty canvas.
func (s *AppState) hitTest(imgX, imgY float64) int {
	x, y := int(imgX), int(imgY)
	for i := len(s.EditShapes) - 1; i >= 1; i-- {
		sh := s.EditShapes[i]
		if sh.Locked { // locked shapes are transparent to canvas clicks; pick them from the layer panel
			continue
		}
		k := model.KindFromType(sh.Type)
		if raster.Inside(k, model.ParamsFromShape(sh), x, y) {
			return i
		}
	}
	return -1
}

// moveShapeData translates a shape by an image-space delta, per its parameter layout: centre-based
// kinds (ellipse/rect/glow/disk/mask) shift slots 0,1; triangles shift all three vertex pairs; a line
// shifts both endpoints (slots 0..3).
func moveShapeData(sh *model.Shape, dx, dy float64) {
	switch model.KindFromType(sh.Type) {
	case model.KindTriangle:
		for i := 0; i+1 < len(sh.Data) && i < 6; i += 2 {
			sh.Data[i] += dx
			sh.Data[i+1] += dy
		}
	case model.KindLine:
		for i := 0; i+1 < len(sh.Data) && i < 4; i += 2 {
			sh.Data[i] += dx
			sh.Data[i+1] += dy
		}
	default:
		if len(sh.Data) >= 2 {
			sh.Data[0] += dx
			sh.Data[1] += dy
		}
	}
}

// shapeCenter returns a shape's centre in image pixels: slots 0,1 for centre-based kinds, the centroid
// for a triangle, the midpoint for a line.
func shapeCenter(sh model.Shape) (float64, float64) {
	switch model.KindFromType(sh.Type) {
	case model.KindTriangle:
		if len(sh.Data) >= 6 {
			return (sh.Data[0] + sh.Data[2] + sh.Data[4]) / 3, (sh.Data[1] + sh.Data[3] + sh.Data[5]) / 3
		}
	case model.KindLine:
		if len(sh.Data) >= 4 {
			return (sh.Data[0] + sh.Data[2]) / 2, (sh.Data[1] + sh.Data[3]) / 2
		}
	default:
		if len(sh.Data) >= 2 {
			return sh.Data[0], sh.Data[1]
		}
	}
	return 0, 0
}

// shapeTheta returns a shape's rotation in degrees (slot 4 for centre-based kinds; 0 for vertex kinds).
func shapeTheta(sh model.Shape) float64 {
	switch model.KindFromType(sh.Type) {
	case model.KindTriangle, model.KindLine:
		return 0
	default:
		if len(sh.Data) >= 5 {
			return sh.Data[4]
		}
		return 0
	}
}

// shapeHalfExtents returns a shape's half-width/height (centre→footprint-edge) in its own frame: the
// semi-axes (slots 2,3) for ellipse/rect/glow/disk; HALF of slots 2,3 for masks (whose slots 2,3 are
// the FULL extent — see raster maskCoverage); the max vertex offset for a triangle; half the endpoint
// span for a line.
func shapeHalfExtents(sh model.Shape) (float64, float64) {
	k := model.KindFromType(sh.Type)
	switch {
	case k == model.KindTriangle:
		cx, cy := shapeCenter(sh)
		var hx, hy float64
		for i := 0; i+1 < len(sh.Data) && i < 6; i += 2 {
			hx = math.Max(hx, math.Abs(sh.Data[i]-cx))
			hy = math.Max(hy, math.Abs(sh.Data[i+1]-cy))
		}
		return hx, hy
	case k == model.KindLine:
		if len(sh.Data) >= 4 {
			return math.Abs(sh.Data[2]-sh.Data[0]) / 2, math.Abs(sh.Data[3]-sh.Data[1]) / 2
		}
		return 0, 0
	case model.IsMask(k):
		if len(sh.Data) >= 4 {
			return sh.Data[2] / 2, sh.Data[3] / 2
		}
		return 0, 0
	default:
		if len(sh.Data) >= 4 {
			return sh.Data[2], sh.Data[3]
		}
		return 0, 0
	}
}

// setShapeScale resizes a shape to new half-extents about its centre: centre-based kinds set slots 2,3
// directly; vertex kinds scale every vertex offset from the centre by the per-axis ratio.
func setShapeScale(sh *model.Shape, nhx, nhy float64) {
	const minE = 0.5
	if nhx < minE {
		nhx = minE
	}
	if nhy < minE {
		nhy = minE
	}
	k := model.KindFromType(sh.Type)
	switch {
	case k == model.KindTriangle, k == model.KindLine:
		limit := 6
		if k == model.KindLine {
			limit = 4
		}
		cx, cy := shapeCenter(*sh)
		hx, hy := shapeHalfExtents(*sh)
		sx, sy := 1.0, 1.0
		if hx > 1e-6 {
			sx = nhx / hx
		}
		if hy > 1e-6 {
			sy = nhy / hy
		}
		for i := 0; i+1 < len(sh.Data) && i < limit; i += 2 {
			sh.Data[i] = cx + (sh.Data[i]-cx)*sx
			sh.Data[i+1] = cy + (sh.Data[i+1]-cy)*sy
		}
	case model.IsMask(k): // slots 2,3 are FULL extents → double the requested half-extents
		if len(sh.Data) >= 4 {
			sh.Data[2] = nhx * 2
			sh.Data[3] = nhy * 2
		}
	default:
		if len(sh.Data) >= 4 {
			sh.Data[2] = nhx
			sh.Data[3] = nhy
		}
	}
}

// applyRotation writes dst as src rotated by deltaDeg about src's centre: centre-based kinds add to the
// stored angle (slot 4); vertex kinds rotate each vertex of src about the centre. dst.Data must already
// be a copy of src.Data (same length).
func applyRotation(dst *model.Shape, src model.Shape, deltaDeg float64) {
	switch model.KindFromType(dst.Type) {
	case model.KindTriangle, model.KindLine:
		limit := 6
		if model.KindFromType(dst.Type) == model.KindLine {
			limit = 4
		}
		cx, cy := shapeCenter(src)
		r := deltaDeg * math.Pi / 180
		c, sn := math.Cos(r), math.Sin(r)
		for i := 0; i+1 < len(src.Data) && i < limit; i += 2 {
			dx, dy := src.Data[i]-cx, src.Data[i+1]-cy
			dst.Data[i] = cx + dx*c - dy*sn
			dst.Data[i+1] = cy + dx*sn + dy*c
		}
	default:
		if len(dst.Data) >= 5 {
			dst.Data[4] = src.Data[4] + deltaDeg
		}
	}
}

// mirrorShapeX reflects a shape across the canvas vertical centre line (x → w-x). Triangles and lines
// reflect their vertices and ellipses/rectangles/glow/disks reflect the centre + negate rotation — both
// are true mirrors. Masks (letters/decals) reflect position and orientation only; the glyph itself is not
// flipped, which would need a negative scale FH6 may reject (verify in-game before relying on it).
func mirrorShapeX(sh *model.Shape, w int) {
	fw := float64(w)
	switch model.KindFromType(sh.Type) {
	case model.KindTriangle:
		for i := 0; i+1 < len(sh.Data) && i < 6; i += 2 {
			sh.Data[i] = fw - sh.Data[i]
		}
	case model.KindLine:
		for i := 0; i+1 < len(sh.Data) && i < 4; i += 2 {
			sh.Data[i] = fw - sh.Data[i]
		}
	default:
		if len(sh.Data) >= 1 {
			sh.Data[0] = fw - sh.Data[0]
		}
		if len(sh.Data) >= 5 {
			sh.Data[4] = -sh.Data[4]
		}
	}
}

// mirrorShapeY reflects a shape across the canvas horizontal centre line (y → h-y) — the up/down mirror.
// Same rules as mirrorShapeX on the other axis: triangles/lines reflect their vertices, the rotatable
// primitives reflect the centre and negate rotation, masks reflect position/orientation only.
func mirrorShapeY(sh *model.Shape, h int) {
	fh := float64(h)
	switch model.KindFromType(sh.Type) {
	case model.KindTriangle:
		for i := 1; i < len(sh.Data) && i < 6; i += 2 {
			sh.Data[i] = fh - sh.Data[i]
		}
	case model.KindLine:
		for i := 1; i < len(sh.Data) && i < 4; i += 2 {
			sh.Data[i] = fh - sh.Data[i]
		}
	default:
		if len(sh.Data) >= 2 {
			sh.Data[1] = fh - sh.Data[1]
		}
		if len(sh.Data) >= 5 {
			sh.Data[4] = -sh.Data[4]
		}
	}
}

// align modes for alignSelection (parallel to AppState.alignBtns).
const (
	alignLeft = iota
	alignCenterX
	alignRight
	alignTop
	alignMiddleY
	alignBottom
	distributeH
	distributeV
)

// alignSelection aligns or evenly distributes the selected shapes by their bounding boxes. Align needs ≥2
// shapes; distribute needs ≥3 (the outermost two stay fixed, the rest space evenly between them).
func (s *AppState) alignSelection(mode int) {
	idx := s.selIndices()
	if len(idx) < 2 {
		return
	}
	type box struct {
		i              int
		x0, y0, x1, y1 float64
	}
	boxes := make([]box, len(idx))
	gx0, gy0 := math.Inf(1), math.Inf(1)
	gx1, gy1 := math.Inf(-1), math.Inf(-1)
	for n, i := range idx {
		sh := s.EditShapes[i]
		k := model.KindFromType(sh.Type)
		a, b, c, d := raster.BBox(k, model.ParamsFromShape(sh), s.EditW, s.EditH)
		boxes[n] = box{i, float64(a), float64(b), float64(c), float64(d)}
		gx0, gy0 = math.Min(gx0, float64(a)), math.Min(gy0, float64(b))
		gx1, gy1 = math.Max(gx1, float64(c)), math.Max(gy1, float64(d))
	}
	center := func(b box, horiz bool) float64 {
		if horiz {
			return (b.x0 + b.x1) / 2
		}
		return (b.y0 + b.y1) / 2
	}
	if mode == distributeH || mode == distributeV {
		if len(boxes) < 3 {
			return
		}
		horiz := mode == distributeH
		sort.Slice(boxes, func(a, b int) bool { return center(boxes[a], horiz) < center(boxes[b], horiz) })
		s.pushUndo(cloneShapes(s.EditShapes))
		n := len(boxes)
		step := (center(boxes[n-1], horiz) - center(boxes[0], horiz)) / float64(n-1)
		for k := 1; k < n-1; k++ {
			delta := center(boxes[0], horiz) + step*float64(k) - center(boxes[k], horiz)
			if horiz {
				moveShapeData(&s.EditShapes[boxes[k].i], delta, 0)
			} else {
				moveShapeData(&s.EditShapes[boxes[k].i], 0, delta)
			}
		}
		s.markEditDirty()
		return
	}
	s.pushUndo(cloneShapes(s.EditShapes))
	for _, bx := range boxes {
		var dx, dy float64
		switch mode {
		case alignLeft:
			dx = gx0 - bx.x0
		case alignCenterX:
			dx = (gx0+gx1)/2 - (bx.x0+bx.x1)/2
		case alignRight:
			dx = gx1 - bx.x1
		case alignTop:
			dy = gy0 - bx.y0
		case alignMiddleY:
			dy = (gy0+gy1)/2 - (bx.y0+bx.y1)/2
		case alignBottom:
			dy = gy1 - bx.y1
		}
		moveShapeData(&s.EditShapes[bx.i], dx, dy)
	}
	s.markEditDirty()
}

// pushUndo records a pre-edit snapshot (already a clone) on the undo stack, capped to keep memory
// bounded. A fresh action invalidates the redo stack (standard linear undo).
func (s *AppState) pushUndo(snapshot []model.Shape) {
	const limit = 50
	s.editUndo = append(s.editUndo, snapshot)
	if len(s.editUndo) > limit {
		s.editUndo = s.editUndo[len(s.editUndo)-limit:]
	}
	s.editRedo = nil
}

// finishDrag commits an active drag: it records an undo step only if the drag actually changed something
// (a click that selects a shape but never moves it must not push a no-op snapshot, or undo appears dead),
// then clears the drag state and triggers a full re-render.
func (s *AppState) finishDrag() {
	if s.editDrag.kind == dragNone {
		return
	}
	if s.editDragMoved {
		s.pushUndo(s.editDrag.start)
	}
	s.editDrag = editorDrag{}
	s.editDragBase = nil
	s.editDragBaseOp = paint.ImageOp{}
	s.editDragSkip = nil
	s.editDragMoved = false
	s.markEditDirty()
}

// undo restores the most recent pre-edit snapshot, pushing the current state onto the redo stack.
func (s *AppState) undo() {
	if len(s.editUndo) == 0 {
		return
	}
	s.editRedo = append(s.editRedo, cloneShapes(s.EditShapes))
	s.EditShapes = s.editUndo[len(s.editUndo)-1]
	s.editUndo = s.editUndo[:len(s.editUndo)-1]
	if s.EditSel >= len(s.EditShapes) {
		s.EditSel = -1
	}
	s.editSelExtra = nil
	s.markEditDirty()
}

// redo re-applies the most recently undone state, pushing the current state back onto the undo stack.
func (s *AppState) redo() {
	if len(s.editRedo) == 0 {
		return
	}
	s.editUndo = append(s.editUndo, cloneShapes(s.EditShapes))
	s.EditShapes = s.editRedo[len(s.editRedo)-1]
	s.editRedo = s.editRedo[:len(s.editRedo)-1]
	if s.EditSel >= len(s.EditShapes) {
		s.EditSel = -1
	}
	s.editSelExtra = nil
	s.markEditDirty()
}

// EnterEditor opens the editor on a deep copy of shapes (w×h canvas). Empty shapes = a blank doc, which
// gets a transparent background slot at index 0 so added shapes start at index 1 (index 0 is never drawn
// by RenderFH6 — it is the background).
func (s *AppState) EnterEditor(shapes []model.Shape, w, h int) {
	s.EditShapes = cloneShapes(shapes)
	if len(s.EditShapes) == 0 {
		s.EditShapes = []model.Shape{{Type: model.TypeRectangle, Data: []float64{0, 0, float64(w), float64(h)}, Color: []int{0, 0, 0, 0}}}
	}
	s.EditW, s.EditH = w, h
	s.EditSel = -1
	s.editDrag = editorDrag{}
	s.editPanning = false
	s.editSelExtra = nil
	s.editDragSkip = nil
	s.editMarqueeOn = false
	s.deleteArmed = false
	s.clearArmed = false
	s.colorWheelBuilt = false
	s.dblTag, s.dblIdx = 0, -1
	s.editWantFocus = true
	s.editZoom = 1
	s.editPan = f32.Point{}
	s.layerDragFrom = -1
	s.editUndo = nil
	s.editRedo = nil
	s.editLayerList.Axis = layout.Vertical
	s.bankList.Axis = layout.Vertical
	s.colorPickerOpen = false
	s.eyedropMode = false
	s.editSavePending = false
	s.editSavedMsg = ""
	s.editDragBase = nil
	s.editDirty = true
	s.inspFor = -1
	s.EditName.SingleLine = true
	for _, ed := range []*widget.Editor{&s.inspX, &s.inspY, &s.inspW, &s.inspH, &s.inspRot} {
		ed.SingleLine = true
	}
	s.arrayCount.SingleLine = true
	s.arrayCount.SetText("6")
	s.symMode = symOff
	s.fastDrag = true
	s.EditorMode = true
	s.View = ViewEditor
}

// markEditDirty flags the working render stale so editorArea rebuilds the cached image op next pass.
func (s *AppState) markEditDirty() { s.editDirty = true }

// SaveDesignName returns the trimmed design name from the field, or a default.
func (s *AppState) SaveDesignName() string {
	if n := strings.TrimSpace(s.EditName.Text()); n != "" {
		return n
	}
	return "Untitled"
}

// RequestOverride opens the name-collision confirmation for name.
func (s *AppState) RequestOverride(name string) {
	s.editSavePending = true
	s.editPendingName = name
}

// CancelOverride dismisses the collision confirmation (so the user can change the name).
func (s *AppState) CancelOverride() { s.editSavePending = false }

// PendingSaveName is the name awaiting override confirmation.
func (s *AppState) PendingSaveName() string { return s.editPendingName }

// SetSavedFeedback shows a transient "saved" confirmation in the editor toolbar.
func (s *AppState) SetSavedFeedback(name string) {
	s.editSavedMsg = i18n.T("editor.saved", name)
	s.editSavedUntil = time.Now().Add(4 * time.Second)
	s.editSavePending = false
}

// editorArea draws the working shapes fit-centered on the canvas (a transparency checker backs them so
// a transparent doc reads as empty), the pulsing selection outline + handles, and processes canvas
// pointer (zoom/pan/edit) and keyboard input. The render is cached in s.editOp and rebuilt only when
// dirty AND not mid-drag (so dragging stays smooth — the live position shows as a moving outline).
func (s *AppState) editorArea(gtx C) D {
	sz := gtx.Constraints.Max
	vp := image.Rectangle{Max: sz}
	s.updateCanvas(gtx, sz)
	rect := s.zoomedRect(sz)
	s.canvasImgRect = rect // recorded for the drag-and-drop drop mapping

	cl := clip.Rect(vp).Push(gtx.Ops)
	dragging := s.editDrag.kind != dragNone && s.editDragBase != nil && len(s.editDragSkip) > 0
	// During a shape drag, composite ONLY the selected shape(s) over the pre-rendered base. The fast path
	// keeps that base as a texture uploaded once and re-rasters just the dragged shape's region each frame
	// (drawn after the base); the slow path rebuilds the whole canvas image per frame. Off-drag, rebuild the
	// full render when dirty, then let overlayOp add the pulsing selection tint on top of a copy.
	switch {
	case dragging && s.fastDrag:
		s.editOp = s.editDragBaseOp
	case dragging:
		img := image.NewNRGBA(s.editDragBase.Bounds())
		copy(img.Pix, s.editDragBase.Pix)
		for _, i := range s.dragIndices() {
			imageio.CompositeShapeOnto(img, s.EditShapes[i], s.EditW, s.EditH)
		}
		s.editOp = paint.NewImageOp(img)
	default:
		if s.editDirty {
			s.editImg = imageio.RenderFH6Image(s.EditShapes, true, s.EditW, s.EditH, 1)
			s.editDirty = false
		}
		s.editOp = s.overlayOp(gtx)
	}
	s.drawCanvasBackdrop(gtx, rect)
	drawImageIn(gtx, s.editOp, rect)
	if dragging && s.fastDrag {
		s.drawDragSprite(gtx, rect)
	}
	s.drawCanvasGuide(gtx, vp, rect)
	s.drawSymmetryAxis(gtx, vp, rect)
	if s.selCount() > 1 {
		s.drawMultiSelection(gtx, rect)
	} else if s.selValid() {
		s.drawSelection(gtx, rect)
	}
	if s.editMarqueeOn {
		s.drawMarquee(gtx, rect)
	}
	s.drawSnapGuides(gtx, vp, rect)
	s.addCanvasInput(gtx, vp, rect)
	cl.Pop()

	if s.editWantFocus {
		gtx.Execute(key.FocusCmd{Tag: &s.editKeyTag})
		s.editWantFocus = false
	}
	s.handleEditKeys(gtx)
	if s.selValid() || s.editMarqueeOn { // animate the selection pulse / live marquee
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(40 * time.Millisecond)})
	}
	return D{Size: sz}
}

// dragIndices is the sorted, valid set of shapes being dragged (z-order for compositing).
func (s *AppState) dragIndices() []int {
	out := make([]int, 0, len(s.editDragSkip))
	for i := range s.editDragSkip {
		if i >= 0 && i < len(s.EditShapes) {
			out = append(out, i)
		}
	}
	sort.Ints(out)
	return out
}

// drawDragSprite refreshes only the dragged shapes' region: it copies that sub-rectangle of the cached
// base, composites the dragged shapes onto it (the exact linear-light render, just localized), and draws
// that small sprite over the base — so a drag costs a tiny raster + upload instead of the whole canvas.
func (s *AppState) drawDragSprite(gtx C, rect image.Rectangle) {
	idx := s.dragIndices()
	if len(idx) == 0 {
		return
	}
	bx0, by0, bx1, by1, first := 0, 0, 0, 0, true
	for _, i := range idx {
		sh := s.EditShapes[i]
		x0, y0, x1, y1 := raster.BBox(model.KindFromType(sh.Type), model.ParamsFromShape(sh), s.EditW, s.EditH)
		if first {
			bx0, by0, bx1, by1, first = x0, y0, x1, y1, false
		} else {
			bx0, by0 = min(bx0, x0), min(by0, y0)
			bx1, by1 = max(bx1, x1), max(by1, y1)
		}
	}
	if first {
		return
	}
	b := image.Rect(bx0-1, by0-1, bx1+2, by1+2).Intersect(image.Rect(0, 0, s.EditW, s.EditH))
	if b.Empty() {
		return
	}
	sprite := image.NewNRGBA(b)
	draw.Draw(sprite, b, s.editDragBase, b.Min, draw.Src)
	for _, i := range idx {
		imageio.CompositeShapeOnto(sprite, s.EditShapes[i], s.EditW, s.EditH)
	}
	screen := imgBBoxToScreen(rect, b.Min.X, b.Min.Y, b.Max.X-1, b.Max.Y-1, s.EditW, s.EditH)
	drawImageIn(gtx, paint.NewImageOp(sprite), screen)
}

// selValid reports whether a selectable shape (not the background) is currently selected.
func (s *AppState) selValid() bool { return s.EditSel >= 1 && s.EditSel < len(s.EditShapes) }

// isLocked reports whether shape i is locked (protected from edits).
func (s *AppState) isLocked(i int) bool {
	return i >= 1 && i < len(s.EditShapes) && s.EditShapes[i].Locked
}

// selLocked reports whether the primary selected shape is locked — locked selections show a grey,
// handle-less frame and refuse drag/scale/rotate.
func (s *AppState) selLocked() bool { return s.selValid() && s.EditShapes[s.EditSel].Locked }

// deselectAll clears the primary selection, any multi-selection, and an in-flight marquee.
func (s *AppState) deselectAll() {
	s.EditSel = -1
	s.editSelExtra = nil
	s.editMarqueeOn = false
	s.deleteArmed = false
}

// selectSingle makes i the sole selection (i<1 or out of range deselects).
func (s *AppState) selectSingle(i int) {
	s.editSelExtra = nil
	s.deleteArmed = false
	if i >= 1 && i < len(s.EditShapes) {
		s.EditSel = i
	} else {
		s.EditSel = -1
	}
}

// isSelected reports whether shape i is part of the current selection (primary or an extra).
func (s *AppState) isSelected(i int) bool {
	return (i == s.EditSel && s.selValid()) || s.editSelExtra[i]
}

// selCount is the number of selected shapes (primary + extras).
func (s *AppState) selCount() int {
	n := len(s.editSelExtra)
	if s.selValid() {
		n++
	}
	return n
}

// selIndices returns the selected shape indices in ascending (z-order) order.
func (s *AppState) selIndices() []int {
	out := make([]int, 0, s.selCount())
	if s.selValid() {
		out = append(out, s.EditSel)
	}
	for i := range s.editSelExtra {
		if i >= 1 && i < len(s.EditShapes) && i != s.EditSel {
			out = append(out, i)
		}
	}
	sort.Ints(out)
	return out
}

// toggleSel adds/removes shape i from the selection (Ctrl+click); the survivor becomes primary.
func (s *AppState) toggleSel(i int) {
	if i < 1 || i >= len(s.EditShapes) {
		return
	}
	s.deleteArmed = false
	if i == s.EditSel { // drop the primary, promoting any one extra to primary
		s.EditSel = -1
		for k := range s.editSelExtra {
			s.EditSel = k
			delete(s.editSelExtra, k)
			break
		}
		return
	}
	if s.editSelExtra[i] { // already an extra → remove it
		delete(s.editSelExtra, i)
		return
	}
	if s.editSelExtra == nil {
		s.editSelExtra = map[int]bool{}
	}
	if s.selValid() {
		s.editSelExtra[s.EditSel] = true
	}
	s.EditSel = i
	delete(s.editSelExtra, i)
}

// selectAll selects every shape except the background (index 0).
func (s *AppState) selectAll() {
	if len(s.EditShapes) <= 1 {
		return
	}
	s.EditSel = 1
	s.editSelExtra = map[int]bool{}
	for i := 2; i < len(s.EditShapes); i++ {
		s.editSelExtra[i] = true
	}
	if len(s.editSelExtra) == 0 {
		s.editSelExtra = nil
	}
}

// selectFromSet replaces the selection with the given indices (primary = the largest, i.e. top-most).
func (s *AppState) selectFromSet(idx []int) {
	s.editSelExtra = nil
	if len(idx) == 0 {
		s.EditSel = -1
		return
	}
	sort.Ints(idx)
	s.EditSel = idx[len(idx)-1]
	if len(idx) > 1 {
		s.editSelExtra = map[int]bool{}
		for _, i := range idx[:len(idx)-1] {
			s.editSelExtra[i] = true
		}
	}
}

// tintShape returns a copy of sh recoloured to col at the given alpha — used to paint a pulsing selection
// highlight on the shape's own silhouette (not its bounding box).
func tintShape(sh model.Shape, col color.NRGBA, alpha int) model.Shape {
	t := sh
	t.Color = []int{int(col.R), int(col.G), int(col.B), alpha}
	return t
}

// doubleClicked reports whether the click on (tag,idx) completes a double-click within the window; a
// single click only arms it, so a stray single click on a palette item never spawns a shape.
func (s *AppState) doubleClicked(tag, idx int, now time.Time) bool {
	const window = 400 * time.Millisecond
	if s.dblTag == tag && s.dblIdx == idx && now.Sub(s.dblAt) < window {
		s.dblTag, s.dblIdx = 0, -1
		return true
	}
	s.dblTag, s.dblIdx, s.dblAt = tag, idx, now
	return false
}

// overlayOp returns the canvas image to draw: the plain render, or a copy with the selected shapes lit by
// a pulsing accent tint on their silhouettes (the SHAPE glows, not its bounding box).
func (s *AppState) overlayOp(gtx C) paint.ImageOp {
	base := s.editImg
	if base == nil {
		return paint.ImageOp{}
	}
	if !(s.selValid() && s.editDrag.kind == dragNone) {
		return paint.NewImageOp(base)
	}
	img := image.NewNRGBA(base.Bounds())
	copy(img.Pix, base.Pix)
	a := int(50 + 120*selPulse(gtx))
	for _, i := range s.selIndices() {
		imageio.CompositeShapeOnto(img, tintShape(s.EditShapes[i], s.Th.Accent, a), s.EditW, s.EditH)
	}
	return paint.NewImageOp(img)
}

// resetEditorCanvas wipes the working doc back to a blank canvas of the same size, so a new design can be
// started without relaunching the app.
func (s *AppState) resetEditorCanvas() {
	w, h := s.EditW, s.EditH
	if w <= 0 || h <= 0 {
		w, h = 1024, 1024
	}
	s.EnterEditor(nil, w, h)
}

// selScreenRect is the selected shape's axis-aligned bounding box mapped into screen space.
func (s *AppState) selScreenRect(rect image.Rectangle) image.Rectangle {
	return s.shapeScreenRect(s.EditSel, rect)
}

// shapeScreenRect is the on-screen bounding box of shape i.
func (s *AppState) shapeScreenRect(i int, rect image.Rectangle) image.Rectangle {
	if i < 0 || i >= len(s.EditShapes) {
		return image.Rectangle{}
	}
	sh := s.EditShapes[i]
	k := model.KindFromType(sh.Type)
	xMin, yMin, xMax, yMax := raster.BBox(k, model.ParamsFromShape(sh), s.EditW, s.EditH)
	return imgBBoxToScreen(rect, xMin, yMin, xMax, yMax, s.EditW, s.EditH)
}

// groupScreenRect is the union of the selected shapes' on-screen bounding boxes.
func (s *AppState) groupScreenRect(rect image.Rectangle) image.Rectangle {
	out, first := image.Rectangle{}, true
	for _, i := range s.selIndices() {
		r := s.shapeScreenRect(i, rect)
		if first {
			out, first = r, false
		} else {
			out = out.Union(r)
		}
	}
	return out
}

// groupImgBox is the image-space (px) bounding box of the current selection, returned as centre +
// half-extents. It uses the true rendered bbox of each shape so it matches the drawn group frame.
func (s *AppState) groupImgBox(shapes []model.Shape) (gcx, gcy, ghx, ghy float64, ok bool) {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, i := range s.selIndices() {
		if i < 0 || i >= len(shapes) {
			continue
		}
		sh := shapes[i]
		x0, y0, x1, y1 := raster.BBox(model.KindFromType(sh.Type), model.ParamsFromShape(sh), s.EditW, s.EditH)
		minX, minY = math.Min(minX, float64(x0)), math.Min(minY, float64(y0))
		maxX, maxY = math.Max(maxX, float64(x1)), math.Max(maxY, float64(y1))
		ok = true
	}
	if !ok {
		return 0, 0, 0, 0, false
	}
	return (minX + maxX) / 2, (minY + maxY) / 2, (maxX - minX) / 2, (maxY - minY) / 2, true
}

// pressInsideSelection reports whether a press at pos falls within any selected shape's screen box.
func (s *AppState) pressInsideSelection(pos f32.Point, rect image.Rectangle) bool {
	for _, i := range s.selIndices() {
		if ptInRect(pos, s.shapeScreenRect(i, rect)) {
			return true
		}
	}
	return false
}

// imgPointToScreen maps an image-space point to screen pixels within rect.
func imgPointToScreen(rect image.Rectangle, ix, iy float64, w, h int) image.Point {
	if w <= 0 || h <= 0 {
		return rect.Min
	}
	return image.Pt(
		rect.Min.X+int(ix/float64(w)*float64(rect.Dx())+0.5),
		rect.Min.Y+int(iy/float64(h)*float64(rect.Dy())+0.5),
	)
}

// selHandlePts returns the 8 scale-handle screen positions for the selected shape. Rotatable shapes get an
// oriented box that turns with the shape; triangles/lines keep their axis-aligned box.
func (s *AppState) selHandlePts(rect image.Rectangle) [8]image.Point {
	sh := s.EditShapes[s.EditSel]
	k := model.KindFromType(sh.Type)
	if k == model.KindTriangle || k == model.KindLine {
		return cropHandlePts(s.selScreenRect(rect))
	}
	cx, cy := shapeCenter(sh)
	hx, hy := shapeHalfExtents(sh)
	th := shapeTheta(sh) * math.Pi / 180
	c, sn := math.Cos(th), math.Sin(th)
	var pts [8]image.Point
	for i := range pts {
		lx, ly := float64(handleSignX(i))*hx, float64(handleSignY(i))*hy
		pts[i] = imgPointToScreen(rect, cx+lx*c-ly*sn, cy+lx*sn+ly*c, s.EditW, s.EditH)
	}
	return pts
}

// selRotateKnob is the rotate-knob screen position, along the shape's "up" direction above its top edge.
func (s *AppState) selRotateKnob(rect image.Rectangle) image.Point {
	sh := s.EditShapes[s.EditSel]
	k := model.KindFromType(sh.Type)
	if k == model.KindTriangle || k == model.KindLine {
		return rotateHandlePt(s.selScreenRect(rect))
	}
	cx, cy := shapeCenter(sh)
	_, hy := shapeHalfExtents(sh)
	th := shapeTheta(sh) * math.Pi / 180
	c, sn := math.Cos(th), math.Sin(th)
	scale := math.Max(float64(rect.Dx())/float64(s.EditW), 1e-6)
	ly := -hy - float64(editRotateStem)/scale
	return imgPointToScreen(rect, cx-ly*sn, cy+ly*c, s.EditW, s.EditH)
}

// drawLine strokes a line between two screen points (for the rotated selection frame).
func drawLine(gtx C, a, b image.Point, col color.NRGBA, width float32) {
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(float32(a.X), float32(a.Y)))
	p.LineTo(f32.Pt(float32(b.X), float32(b.Y)))
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: width}.Op())
}

// drawHandle draws one square scale handle centred at p.
func drawHandle(gtx C, p image.Point, fill, border color.NRGBA) {
	const h = 4
	paint.FillShape(gtx.Ops, border, clip.Rect(image.Rect(p.X-h-1, p.Y-h-1, p.X+h+1, p.Y+h+1)).Op())
	paint.FillShape(gtx.Ops, fill, clip.Rect(image.Rect(p.X-h, p.Y-h, p.X+h, p.Y+h)).Op())
}

// zoomedRect is the on-screen image rectangle for the current zoom + pan within a viewport of size sz.
func (s *AppState) zoomedRect(sz image.Point) image.Rectangle {
	base := fitRect(image.Pt(s.EditW, s.EditH), sz)
	z := s.editZoom
	if z <= 0 {
		z = 1
	}
	zw, zh := int(float64(base.Dx())*z), int(float64(base.Dy())*z)
	cx := float64(base.Min.X+base.Max.X)/2 + float64(s.editPan.X)
	cy := float64(base.Min.Y+base.Max.Y)/2 + float64(s.editPan.Y)
	minx, miny := int(cx)-zw/2, int(cy)-zh/2
	return image.Rect(minx, miny, minx+zw, miny+zh)
}

// zoomAbout multiplies the zoom by factor while keeping the image point under pos fixed on screen.
func (s *AppState) zoomAbout(pos f32.Point, factor float64, sz image.Point) {
	old := s.zoomedRect(sz)
	nz := s.editZoom * factor
	nz = math.Max(0.2, math.Min(16, nz))
	if nz == s.editZoom || old.Dx() <= 0 || old.Dy() <= 0 {
		return
	}
	fx := float64(pos.X-float32(old.Min.X)) / float64(old.Dx())
	fy := float64(pos.Y-float32(old.Min.Y)) / float64(old.Dy())
	s.editZoom = nz
	base := fitRect(image.Pt(s.EditW, s.EditH), sz)
	newW, newH := float64(base.Dx())*nz, float64(base.Dy())*nz
	newCx := float64(pos.X) - fx*newW + newW/2
	newCy := float64(pos.Y) - fy*newH + newH/2
	s.editPan = f32.Point{
		X: float32(newCx - float64(base.Min.X+base.Max.X)/2),
		Y: float32(newCy - float64(base.Min.Y+base.Max.Y)/2),
	}
}

// zoomFit resets the view to the fit-centered default.
func (s *AppState) zoomFit() { s.editZoom = 1; s.editPan = f32.Point{} }

// updateCanvas consumes this frame's canvas pointer events: scroll zooms about the cursor, a
// middle/secondary-button drag pans, and a primary-button press selects/moves/scales/rotates.
func (s *AppState) updateCanvas(gtx C, sz image.Point) {
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target:  &s.editKeyTag,
			Kinds:   pointer.Press | pointer.Drag | pointer.Release | pointer.Scroll | pointer.Cancel | pointer.Move | pointer.Leave,
			ScrollY: pointer.ScrollRange{Min: -1000, Max: 1000},
		})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		rect := s.zoomedRect(sz)
		switch pe.Kind {
		case pointer.Scroll:
			s.zoomAbout(pe.Position, math.Exp(float64(-pe.Scroll.Y)*0.0015), sz)
		case pointer.Move:
			s.canvasLocal = pe.Position
			s.canvasHover = true
		case pointer.Leave:
			s.canvasHover = false
		case pointer.Press:
			if s.eyedropMode {
				s.sampleColor(pxToFrac(pe.Position, rect))
				s.eyedropMode = false
			} else if pe.Buttons&pointer.ButtonPrimary != 0 {
				gtx.Execute(key.FocusCmd{Tag: &s.editKeyTag})
				s.editPanning = false
				s.pressEditor(pe.Position, pxToFrac(pe.Position, rect), rect, pe.Modifiers.Contain(key.ModShortcut))
			} else {
				s.editPanning = true
				s.panLast = pe.Position
			}
		case pointer.Drag:
			if s.editPanning {
				s.editPan = s.editPan.Add(pe.Position.Sub(s.panLast))
				s.panLast = pe.Position
			} else if s.editMarqueeOn {
				s.editMarqueeB = pxToFrac(pe.Position, rect)
			} else {
				s.editDragMoved = true
				s.editShift = pe.Modifiers.Contain(key.ModShift)
				s.editAlt = pe.Modifiers.Contain(key.ModAlt)
				scale := float64(rect.Dx()) / math.Max(float64(s.EditW), 1)
				s.snapThreshImg = 7.0 / math.Max(scale, 1e-6)
				s.snapGridStep = 0
				if s.canvasGuide == guideGrid {
					s.snapGridStep = niceStep(float64(gtx.Dp(20)) / math.Max(scale, 1e-6))
				}
				s.dragEditor(pxToFrac(pe.Position, rect))
			}
		case pointer.Release, pointer.Cancel:
			if s.editPanning {
				s.editPanning = false
			} else if s.editMarqueeOn {
				s.finishMarquee()
				s.editMarqueeOn = false
			} else if s.editDrag.kind != dragNone {
				s.finishDrag()
			}
			s.snapShowX, s.snapShowY = false, false
		}
	}
}

// finishMarquee turns the rubber-band rectangle into a selection of every shape whose box intersects it.
func (s *AppState) finishMarquee() {
	ax, ay := float64(s.editMarqueeA.X)*float64(s.EditW), float64(s.editMarqueeA.Y)*float64(s.EditH)
	bx, by := float64(s.editMarqueeB.X)*float64(s.EditW), float64(s.editMarqueeB.Y)*float64(s.EditH)
	x0, x1 := math.Min(ax, bx), math.Max(ax, bx)
	y0, y1 := math.Min(ay, by), math.Max(ay, by)
	if x1-x0 < 2 && y1-y0 < 2 { // a click, not a drag → nothing to select
		return
	}
	picked := map[int]bool{}
	if s.editMarqueeAdd {
		for _, i := range s.selIndices() {
			picked[i] = true
		}
	}
	for i := 1; i < len(s.EditShapes); i++ {
		sh := s.EditShapes[i]
		k := model.KindFromType(sh.Type)
		bx0, by0, bx1, by1 := raster.BBox(k, model.ParamsFromShape(sh), s.EditW, s.EditH)
		if float64(bx1) >= x0 && float64(bx0) <= x1 && float64(by1) >= y0 && float64(by0) <= y1 {
			picked[i] = true
		}
	}
	idx := make([]int, 0, len(picked))
	for i := range picked {
		idx = append(idx, i)
	}
	s.selectFromSet(idx)
}

// addCanvasInput registers the canvas pointer/key target + hover cursors for the next frame.
func (s *AppState) addCanvasInput(gtx C, vp, rect image.Rectangle) {
	area := clip.Rect(vp).Push(gtx.Ops)
	event.Op(gtx.Ops, &s.editKeyTag)
	switch {
	case s.eyedropMode:
		pointer.CursorCrosshair.Add(gtx.Ops)
	case s.editPanning:
		pointer.CursorGrabbing.Add(gtx.Ops)
	case s.editDrag.kind != dragNone:
		editDragCursor(s.editDrag).Add(gtx.Ops)
	default:
		pointer.CursorDefault.Add(gtx.Ops)
	}
	area.Pop()

	if s.editPanning || s.editDrag.kind != dragNone || s.editMarqueeOn || !s.selValid() {
		return
	}
	if s.selCount() > 1 { // group transform: grab over the box, resize handles on the frame, rotate knob above
		g := s.groupScreenRect(rect)
		if in := g.Intersect(vp); in.Dx() > 0 && in.Dy() > 0 {
			c := clip.Rect(in).Push(gtx.Ops)
			pointer.CursorGrab.Add(gtx.Ops)
			c.Pop()
		}
		for i, h := range cropHandlePts(g) {
			const r = editHandleHitR
			hc := clip.Rect(image.Rect(h.X-r, h.Y-r, h.X+r, h.Y+r)).Push(gtx.Ops)
			cropCursorForHandle(i).Add(gtx.Ops)
			hc.Pop()
		}
		rp := rotateHandlePt(g)
		const rr = editHandleHitR
		rc := clip.Rect(image.Rect(rp.X-rr, rp.Y-rr, rp.X+rr, rp.Y+rr)).Push(gtx.Ops)
		pointer.CursorPointer.Add(gtx.Ops)
		rc.Pop()
		return
	}
	sel := s.selScreenRect(rect)
	if in := sel.Intersect(vp); in.Dx() > 0 && in.Dy() > 0 {
		c := clip.Rect(in).Push(gtx.Ops)
		pointer.CursorGrab.Add(gtx.Ops)
		c.Pop()
	}
	for i, h := range s.selHandlePts(rect) {
		const r = editHandleHitR
		hc := clip.Rect(image.Rect(h.X-r, h.Y-r, h.X+r, h.Y+r)).Push(gtx.Ops)
		cropCursorForHandle(i).Add(gtx.Ops)
		hc.Pop()
	}
	rp := s.selRotateKnob(rect)
	const rr = editHandleHitR
	rc := clip.Rect(image.Rect(rp.X-rr, rp.Y-rr, rp.X+rr, rp.Y+rr)).Push(gtx.Ops)
	pointer.CursorPointer.Add(gtx.Ops)
	rc.Pop()
}

// handleEditKeys runs the editor keyboard shortcuts when the canvas holds focus: Ctrl+Z undo,
// Ctrl+Shift+Z redo, Delete/Backspace remove the selected shape.
func (s *AppState) handleEditKeys(gtx C) {
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: &s.editKeyTag, Name: "Z", Required: key.ModShortcut},
			key.Filter{Focus: &s.editKeyTag, Name: "Z", Required: key.ModShortcut | key.ModShift},
			key.Filter{Focus: &s.editKeyTag, Name: "D", Required: key.ModShortcut},
			key.Filter{Focus: &s.editKeyTag, Name: "A", Required: key.ModShortcut},
			key.Filter{Focus: &s.editKeyTag, Name: "M", Required: key.ModShortcut},
			key.Filter{Focus: &s.editKeyTag, Name: "M", Required: key.ModShortcut | key.ModShift},
			key.Filter{Focus: &s.editKeyTag, Name: key.NameDeleteForward},
			key.Filter{Focus: &s.editKeyTag, Name: key.NameDeleteBackward},
			key.Filter{Focus: &s.editKeyTag, Name: key.NameEscape},
			key.Filter{Focus: &s.editKeyTag, Name: key.NameLeftArrow, Optional: key.ModShift},
			key.Filter{Focus: &s.editKeyTag, Name: key.NameRightArrow, Optional: key.ModShift},
			key.Filter{Focus: &s.editKeyTag, Name: key.NameUpArrow, Optional: key.ModShift},
			key.Filter{Focus: &s.editKeyTag, Name: key.NameDownArrow, Optional: key.ModShift},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		switch {
		case ke.Name == "Z" && ke.Modifiers.Contain(key.ModShortcut) && ke.Modifiers.Contain(key.ModShift):
			s.redo()
		case ke.Name == "Z" && ke.Modifiers.Contain(key.ModShortcut):
			s.undo()
		case ke.Name == "D" && ke.Modifiers.Contain(key.ModShortcut):
			s.duplicateSel()
		case ke.Name == "A" && ke.Modifiers.Contain(key.ModShortcut):
			s.selectAll()
		case ke.Name == "M" && ke.Modifiers.Contain(key.ModShortcut) && ke.Modifiers.Contain(key.ModShift):
			s.mirrorSelection(true)
		case ke.Name == "M" && ke.Modifiers.Contain(key.ModShortcut):
			s.mirrorSelection(false)
		case ke.Name == key.NameDeleteForward || ke.Name == key.NameDeleteBackward:
			s.deleteSel()
		case ke.Name == key.NameEscape:
			s.deselectAll()
		case ke.Name == key.NameLeftArrow:
			s.nudge(-1, 0, ke.Modifiers)
		case ke.Name == key.NameRightArrow:
			s.nudge(1, 0, ke.Modifiers)
		case ke.Name == key.NameUpArrow:
			s.nudge(0, -1, ke.Modifiers)
		case ke.Name == key.NameDownArrow:
			s.nudge(0, 1, ke.Modifiers)
		}
	}
}

// nudge moves the selected shape by the arrow keys: 1px, or 10px with Shift held.
func (s *AppState) nudge(dx, dy float64, mods key.Modifiers) {
	var idx []int
	for _, i := range s.selIndices() {
		if !s.EditShapes[i].Locked {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return
	}
	step := 1.0
	if mods.Contain(key.ModShift) {
		step = 10
	}
	s.pushUndo(cloneShapes(s.EditShapes))
	for _, i := range idx {
		moveShapeData(&s.EditShapes[i], dx*step, dy*step)
	}
	s.markEditDirty()
}

// drawSelection draws the selected shape's pulsing outline (accent↔white over ~0.8s, so it reads
// clearly over busy art), the 8 resize handles, and the rotate knob.
func (s *AppState) drawSelection(gtx C, rect image.Rectangle) {
	th := s.Th
	pts := s.selHandlePts(rect)
	// Oriented frame: the four corner handles (NW,NE,SE,SW) joined in order — turns with the shape.
	frame := th.Accent
	if s.selLocked() {
		frame = th.TextDim // locked: a grey, handle-less box
	}
	corners := [4]image.Point{pts[0], pts[2], pts[4], pts[6]}
	for i := 0; i < 4; i++ {
		a, b := corners[i], corners[(i+1)%4]
		drawLine(gtx, a, b, color.NRGBA{A: 160}, 3)
		drawLine(gtx, a, b, frame, 1.5)
	}
	if s.selLocked() {
		return
	}
	knob := s.selRotateKnob(rect)
	drawLine(gtx, pts[1], knob, th.Accent, 1.5) // stem from the top-edge midpoint
	drawKnob(gtx, knob, th.Accent, th.Bg)
	for _, p := range pts {
		drawHandle(gtx, p, th.Accent, th.Bg)
	}
	cx, cy := shapeCenter(s.EditShapes[s.EditSel])
	drawMoveAnchor(gtx, imgPointToScreen(rect, cx, cy, s.EditW, s.EditH), th.Accent, th.Bg)
}

// drawMultiSelection outlines every shape in a multi-selection (pulsing) plus their shared group box
// with a single move anchor — group transforms are move-only, so no scale handles or rotate knob.
func (s *AppState) drawMultiSelection(gtx C, rect image.Rectangle) {
	th := s.Th
	// Thin static per-shape frames + a bolder group frame carrying scale handles and a rotate knob; the
	// pulse is on the shapes' silhouettes (overlayOp), so nothing here flashes. A locked primary greys it
	// out and drops the handles.
	frame := th.Accent
	if s.selLocked() {
		frame = th.TextDim
	}
	for _, i := range s.selIndices() {
		drawRectBorderW(gtx, s.shapeScreenRect(i, rect), frame, 1)
	}
	g := s.groupScreenRect(rect)
	drawRectBorderW(gtx, g, color.NRGBA{A: 160}, 2)
	drawRectBorderW(gtx, g, frame, 1)
	if s.selLocked() {
		return
	}
	knob := rotateHandlePt(g)
	drawLine(gtx, image.Pt((g.Min.X+g.Max.X)/2, g.Min.Y), knob, th.Accent, 1.5)
	drawKnob(gtx, knob, th.Accent, th.Bg)
	for _, p := range cropHandlePts(g) {
		drawHandle(gtx, p, th.Accent, th.Bg)
	}
	drawMoveAnchor(gtx, image.Pt((g.Min.X+g.Max.X)/2, (g.Min.Y+g.Max.Y)/2), th.Accent, th.Bg)
}

// drawMarquee paints the in-progress rubber-band rectangle (translucent fill + accent border).
func (s *AppState) drawMarquee(gtx C, rect image.Rectangle) {
	th := s.Th
	a := fracToScreen(s.editMarqueeA, rect)
	b := fracToScreen(s.editMarqueeB, rect)
	x0, x1 := a.X, b.X
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	y0, y1 := a.Y, b.Y
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	r := image.Rect(x0, y0, x1, y1)
	fill := th.Accent
	fill.A = 40
	paint.FillShape(gtx.Ops, fill, clip.Rect(r).Op())
	drawRectBorder(gtx, r, th.Accent)
}

// fracToScreen maps an image-fraction point to a screen pixel inside rect.
func fracToScreen(p f32.Point, rect image.Rectangle) image.Point {
	return image.Pt(
		rect.Min.X+int(p.X*float32(rect.Dx())+0.5),
		rect.Min.Y+int(p.Y*float32(rect.Dy())+0.5),
	)
}

// selPulse is the shared 0..1 selection-highlight pulse (period 0.8s).
func selPulse(gtx C) float32 {
	secs := float64(gtx.Now.UnixNano()) / 1e9
	return float32(0.5 + 0.5*math.Sin(secs*2*math.Pi/0.8))
}

// drawRectBorderW strokes a w-px border of col just inside r (four filled bars), for a bolder outline.
func drawRectBorderW(gtx C, r image.Rectangle, col color.NRGBA, w int) {
	if r.Dx() <= 0 || r.Dy() <= 0 || w <= 0 {
		return
	}
	paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+w)).Op())
	paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(r.Min.X, r.Max.Y-w, r.Max.X, r.Max.Y)).Op())
	paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(r.Min.X, r.Min.Y, r.Min.X+w, r.Max.Y)).Op())
	paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(r.Max.X-w, r.Min.Y, r.Max.X, r.Max.Y)).Op())
}

// drawMoveAnchor draws the centre grab handle: a filled disc with a small plus.
func drawMoveAnchor(gtx C, p image.Point, fill, mark color.NRGBA) {
	const r = 8
	paint.FillShape(gtx.Ops, fill, clip.Ellipse(image.Rect(p.X-r, p.Y-r, p.X+r, p.Y+r)).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, mark, clip.Rect(image.Rect(p.X-4, p.Y-1, p.X+4, p.Y+1)).Op())
	paint.FillShape(gtx.Ops, mark, clip.Rect(image.Rect(p.X-1, p.Y-4, p.X+1, p.Y+4)).Op())
}

// lerpColor linearly interpolates two opaque colours.
func lerpColor(a, b color.NRGBA, t float32) color.NRGBA {
	return color.NRGBA{
		R: uint8(float32(a.R) + (float32(b.R)-float32(a.R))*t),
		G: uint8(float32(a.G) + (float32(b.G)-float32(a.G))*t),
		B: uint8(float32(a.B) + (float32(b.B)-float32(a.B))*t),
		A: 255,
	}
}

// editDragCursor maps an active drag to the cursor shown for its duration.
func editDragCursor(d editorDrag) pointer.Cursor {
	switch d.kind {
	case dragMove:
		return pointer.CursorGrabbing
	case dragScale:
		return cropCursorForHandle(d.handle)
	case dragRotate:
		return pointer.CursorPointer
	default:
		return pointer.CursorDefault
	}
}

// rotateHandlePt is the rotate knob centre: above the selection's top edge, on the vertical centre line.
func rotateHandlePt(sel image.Rectangle) image.Point {
	return image.Pt((sel.Min.X+sel.Max.X)/2, sel.Min.Y-editRotateStem)
}

// drawKnob fills a small bordered circle at p (the rotate handle).
func drawKnob(gtx C, p image.Point, fill, border color.NRGBA) {
	const r = 5
	paint.FillShape(gtx.Ops, border, clip.Ellipse(image.Rect(p.X-r-1, p.Y-r-1, p.X+r+1, p.Y+r+1)).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, fill, clip.Ellipse(image.Rect(p.X-r, p.Y-r, p.X+r, p.Y+r)).Op(gtx.Ops))
}

// pressEditor classifies a press into a transform-handle grab on the current selection, or a fresh
// selection + move, and starts the matching drag.
func (s *AppState) pressEditor(pos, fp f32.Point, rect image.Rectangle, additive bool) {
	// Scale/rotate handles act only on a single primary selection (group transform = move-only).
	if s.selValid() && s.selCount() == 1 && !additive && !s.selLocked() {
		if within(pos, s.selRotateKnob(rect), editHandleHitR) {
			s.startTransform(dragRotate, 0, fp)
			return
		}
		for i, h := range s.selHandlePts(rect) {
			if within(pos, h, editHandleHitR) {
				s.startTransform(dragScale, i, fp)
				return
			}
		}
	}
	// Group scale/rotate handles act on the group's axis-aligned box.
	if s.selCount() > 1 && !additive && !s.selLocked() {
		g := s.groupScreenRect(rect)
		if within(pos, rotateHandlePt(g), editHandleHitR) {
			s.startTransform(dragRotate, 0, fp)
			return
		}
		for i, h := range cropHandlePts(g) {
			if within(pos, h, editHandleHitR) {
				s.startTransform(dragScale, i, fp)
				return
			}
		}
	}
	// A press inside ANY selected shape's box moves the whole selection — so sparse shapes (a barcode,
	// a dotted decal) can be grabbed by their box, not only their solid pixels, and a multi-selection
	// drags as one.
	if !additive && s.selCount() >= 1 && !s.selLocked() {
		inside := s.pressInsideSelection(pos, rect)
		if !inside && s.selCount() > 1 { // also grab the drawn group-centre move anchor
			g := s.groupScreenRect(rect)
			anchor := image.Pt((g.Min.X+g.Max.X)/2, (g.Min.Y+g.Max.Y)/2)
			inside = within(pos, anchor, editHandleHitR+4)
		}
		if inside {
			s.startTransform(dragMove, 0, fp)
			return
		}
	}
	hit := s.hitTest(float64(fp.X)*float64(s.EditW), float64(fp.Y)*float64(s.EditH))
	if additive { // Ctrl+click toggles a shape; Ctrl-drag on empty space = additive marquee
		if hit >= 1 {
			s.toggleSel(hit)
			s.editDrag = editorDrag{}
			return
		}
		s.editMarqueeOn = true
		s.editMarqueeAdd = true
		s.editMarqueeA, s.editMarqueeB = fp, fp
		s.editDrag = editorDrag{}
		return
	}
	if hit >= 1 {
		s.selectSingle(hit)
		s.startTransform(dragMove, 0, fp)
		return
	}
	// Empty space: begin a rubber-band marquee (replaces the selection on release).
	s.deselectAll()
	s.editMarqueeOn = true
	s.editMarqueeAdd = false
	s.editMarqueeA, s.editMarqueeB = fp, fp
	s.editDrag = editorDrag{}
}

// ptInRect reports whether pointer p lies within rectangle r.
func ptInRect(p f32.Point, r image.Rectangle) bool {
	return p.X >= float32(r.Min.X) && p.X < float32(r.Max.X) && p.Y >= float32(r.Min.Y) && p.Y < float32(r.Max.Y)
}

// startTransform snapshots the working set and records the selected shape's centre / rotation / press
// angle, so a drag of the given kind can be recomputed from a stable origin each frame.
func (s *AppState) startTransform(kind, handle int, fp f32.Point) {
	d := editorDrag{kind: kind, handle: handle, start: cloneShapes(s.EditShapes), anchor: fp}
	if s.selValid() {
		if s.selCount() > 1 {
			// Group transform: pivot is the group box centre, with no intrinsic base angle.
			if gcx, gcy, _, _, ok := s.groupImgBox(s.EditShapes); ok {
				d.cx, d.cy, d.theta0 = gcx, gcy, 0
			}
		} else {
			sh := s.EditShapes[s.EditSel]
			d.cx, d.cy = shapeCenter(sh)
			d.theta0 = shapeTheta(sh)
		}
		d.ang0 = math.Atan2(float64(fp.Y)*float64(s.EditH)-d.cy, float64(fp.X)*float64(s.EditW)-d.cx)
		// Pre-render everything except the selected shape(s) once, so each drag frame only re-composites
		// those (live + smooth at any doc size, even for a multi-shape group transform).
		skip := map[int]bool{}
		for _, i := range s.selIndices() {
			skip[i] = true
		}
		s.editDragSkip = skip
		s.editDragBase = imageio.RenderFH6ImageSkipSet(s.EditShapes, true, s.EditW, s.EditH, skip)
		if s.fastDrag {
			s.editDragBaseOp = paint.NewImageOp(s.editDragBase) // upload the static base once, not every frame
		}
	}
	s.editDrag = d
	s.editDragMoved = false
}

// dragEditor recomputes the selected shape for the current pointer fraction, per the active drag kind.
func (s *AppState) dragEditor(fp f32.Point) {
	d := s.editDrag
	if d.kind == dragNone || s.EditSel < 1 || s.EditSel >= len(d.start) {
		return
	}
	s.snapShowX, s.snapShowY = false, false
	// Group move: translate every selected shape by the same delta from its drag-start snapshot.
	if d.kind == dragMove && s.selCount() > 1 {
		dx := float64(fp.X-d.anchor.X) * float64(s.EditW)
		dy := float64(fp.Y-d.anchor.Y) * float64(s.EditH)
		if gcx, gcy, ghx, ghy, ok := s.groupImgBox(d.start); ok {
			dx, dy = s.snapMoveDelta([4]float64{gcx - ghx, gcy - ghy, gcx + ghx, gcy + ghy}, dx, dy)
		}
		for _, i := range s.selIndices() {
			if i >= len(d.start) {
				continue
			}
			s.EditShapes[i].Data = append([]float64(nil), d.start[i].Data...)
			moveShapeData(&s.EditShapes[i], dx, dy)
		}
		s.markEditDirty()
		return
	}
	// Group scale/rotate: transform every selected shape about the group box.
	if (d.kind == dragScale || d.kind == dragRotate) && s.selCount() > 1 {
		s.dragGroup(fp)
		return
	}
	start := d.start[s.EditSel]
	s.EditShapes[s.EditSel].Data = append([]float64(nil), start.Data...)
	dst := &s.EditShapes[s.EditSel]
	switch d.kind {
	case dragMove:
		dx := float64(fp.X-d.anchor.X) * float64(s.EditW)
		dy := float64(fp.Y-d.anchor.Y) * float64(s.EditH)
		x0, y0, x1, y1 := raster.BBox(model.KindFromType(start.Type), model.ParamsFromShape(start), s.EditW, s.EditH)
		dx, dy = s.snapMoveDelta([4]float64{float64(x0), float64(y0), float64(x1), float64(y1)}, dx, dy)
		moveShapeData(dst, dx, dy)
	case dragScale:
		// Anchored scale: in the shape's local frame (rotated by theta0, relative to the start centre)
		// move ONLY the dragged edge(s) to the pointer; the opposite edge stays fixed and the centre
		// shifts to suit. This makes the corner follow the cursor instead of jumping/growing symmetrically.
		th := d.theta0 * math.Pi / 180
		c, sn := math.Cos(th), math.Sin(th)
		pdx := float64(fp.X)*float64(s.EditW) - d.cx
		pdy := float64(fp.Y)*float64(s.EditH) - d.cy
		plx := pdx*c + pdy*sn // R(-theta) — raster convention kx = dx*c + dy*s
		ply := -pdx*sn + pdy*c
		hx0, hy0 := shapeHalfExtents(start)
		xlo, xhi, ylo, yhi := -hx0, hx0, -hy0, hy0
		switch handleSignX(d.handle) {
		case 1:
			xhi = plx
		case -1:
			xlo = plx
		}
		switch handleSignY(d.handle) {
		case 1:
			yhi = ply
		case -1:
			ylo = ply
		}
		nhx, nhy := math.Abs(xhi-xlo)/2, math.Abs(yhi-ylo)/2
		lcx, lcy := (xlo+xhi)/2, (ylo+yhi)/2
		ncx := d.cx + lcx*c - lcy*sn // R(theta) back into image space
		ncy := d.cy + lcx*sn + lcy*c
		curCx, curCy := shapeCenter(*dst)
		moveShapeData(dst, ncx-curCx, ncy-curCy)
		setShapeScale(dst, nhx, nhy)
	case dragRotate:
		ang := math.Atan2(float64(fp.Y)*float64(s.EditH)-d.cy, float64(fp.X)*float64(s.EditW)-d.cx)
		deltaDeg := (ang - d.ang0) * 180 / math.Pi
		if s.editShift { // snap to 15° steps
			deltaDeg = math.Round((d.theta0+deltaDeg)/15)*15 - d.theta0
		}
		applyRotation(dst, start, deltaDeg)
	}
	s.markEditDirty()
}

// dragGroup scales or rotates the whole selection about its group box. Scale is uniform (driven by the
// dragged handle's distance from the fixed opposite corner/edge); rotate turns each shape's centre about
// the group centre and adds the same delta to the shape's own orientation.
func (s *AppState) dragGroup(fp f32.Point) {
	d := s.editDrag
	gcx, gcy, ghx, ghy, ok := s.groupImgBox(d.start)
	if !ok {
		return
	}
	px := float64(fp.X) * float64(s.EditW)
	py := float64(fp.Y) * float64(s.EditH)
	switch d.kind {
	case dragRotate:
		deltaDeg := (math.Atan2(py-gcy, px-gcx) - d.ang0) * 180 / math.Pi
		if s.editShift {
			deltaDeg = math.Round(deltaDeg/15) * 15
		}
		r := deltaDeg * math.Pi / 180
		c, sn := math.Cos(r), math.Sin(r)
		for _, i := range s.selIndices() {
			if i >= len(d.start) {
				continue
			}
			src := d.start[i]
			dst := &s.EditShapes[i]
			dst.Data = append([]float64(nil), src.Data...)
			applyRotation(dst, src, deltaDeg)
			scx, scy := shapeCenter(src)
			nx := gcx + (scx-gcx)*c - (scy-gcy)*sn
			ny := gcy + (scx-gcx)*sn + (scy-gcy)*c
			moveShapeData(dst, nx-scx, ny-scy)
		}
	case dragScale:
		sx, sy := float64(handleSignX(d.handle)), float64(handleSignY(d.handle))
		ax, ay := gcx-sx*ghx, gcy-sy*ghy // fixed opposite corner/edge
		v0x, v0y := 2*sx*ghx, 2*sy*ghy
		denom := v0x*v0x + v0y*v0y
		if denom < 1e-9 {
			return
		}
		f := ((px-ax)*v0x + (py-ay)*v0y) / denom
		if f < 0.05 {
			f = 0.05
		}
		for _, i := range s.selIndices() {
			if i >= len(d.start) {
				continue
			}
			src := d.start[i]
			dst := &s.EditShapes[i]
			dst.Data = append([]float64(nil), src.Data...)
			scx, scy := shapeCenter(src)
			moveShapeData(dst, ax+(scx-ax)*f-scx, ay+(scy-ay)*f-scy)
			hx, hy := shapeHalfExtents(src)
			setShapeScale(dst, hx*f, hy*f)
		}
	}
	s.markEditDirty()
}

// handleSignX/handleSignY give which side of the box a resize handle controls (handles 0..7 =
// NW,N,NE,E,SE,S,SW,W): -1 = left/top edge, +1 = right/bottom edge, 0 = that axis is unchanged.
func handleSignX(h int) int {
	switch h {
	case 0, 6, 7:
		return -1
	case 2, 3, 4:
		return 1
	default:
		return 0
	}
}

func handleSignY(h int) int {
	switch h {
	case 0, 1, 2:
		return -1
	case 4, 5, 6:
		return 1
	default:
		return 0
	}
}

// within reports whether pointer p is inside radius r of point c.
func within(p f32.Point, c image.Point, r float32) bool {
	dx, dy := p.X-float32(c.X), p.Y-float32(c.Y)
	return dx*dx+dy*dy <= r*r
}

// imgBBoxToScreen maps an inclusive image-pixel bounding box into a screen rect within the fitted canvas.
func imgBBoxToScreen(rect image.Rectangle, xMin, yMin, xMax, yMax, w, h int) image.Rectangle {
	if w <= 0 || h <= 0 {
		return image.Rectangle{}
	}
	fx := float64(xMin) / float64(w)
	fy := float64(yMin) / float64(h)
	fw := float64(xMax-xMin+1) / float64(w)
	fh := float64(yMax-yMin+1) / float64(h)
	return cropToScreen(rect, [4]float64{fx, fy, fw, fh})
}

// editorEntry renders the buttons that open the editor from the activity card: always "New blank
// canvas", plus "Edit shapes" once a generation exists (showEdit).
func (s *AppState) editorEntry(gtx C, showEdit bool) D {
	th := s.Th
	var children []layout.FlexChild
	if showEdit {
		children = append(children,
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return th.SecondaryButton(gtx, &s.EditBtn, i18n.T("editor.edit"), true)
			}),
			layout.Rigid(GapV(8).Layout),
		)
	}
	children = append(children,
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.SecondaryButton(gtx, &s.NewBlankBtn, i18n.T("editor.new"), true)
		}),
	)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// ExitEditor ends the edit session, drops the working copy, and returns to the Studio view.
func (s *AppState) ExitEditor() {
	s.EditorMode = false
	s.EditShapes = nil
	s.EditSel = -1
	s.editSelExtra = nil
	s.editDrag = editorDrag{}
	s.editUndo = nil
	s.editRedo = nil
	if s.View == ViewEditor {
		s.View = ViewStudio
	}
}
