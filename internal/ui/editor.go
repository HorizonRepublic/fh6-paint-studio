package ui

import (
	"image"
	"image/color"
	"math"
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
	s.editWantFocus = true
	s.editZoom = 1
	s.editPan = f32.Point{}
	s.layerDragFrom = -1
	s.editUndo = nil
	s.editRedo = nil
	s.editLayerList.Axis = layout.Vertical
	s.bankList.Axis = layout.Vertical
	s.colorPickerOpen = false
	s.editSavePending = false
	s.editSavedMsg = ""
	s.editDragBase = nil
	s.editDirty = true
	s.inspFor = -1
	s.EditName.SingleLine = true
	for _, ed := range []*widget.Editor{&s.inspX, &s.inspY, &s.inspW, &s.inspH, &s.inspRot} {
		ed.SingleLine = true
	}
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

	cl := clip.Rect(vp).Push(gtx.Ops)
	// During a shape drag, composite ONLY the dragged shape over the pre-rendered base (every other
	// shape, rendered once at drag start) — cheap at any doc size, so the shape redraws live while
	// staying smooth. Off-drag, rebuild the full render when dirty.
	if s.editDrag.kind != dragNone && s.editDragBase != nil && s.selValid() {
		img := image.NewNRGBA(s.editDragBase.Bounds())
		copy(img.Pix, s.editDragBase.Pix)
		imageio.CompositeShapeOnto(img, s.EditShapes[s.EditSel], s.EditW, s.EditH)
		s.editOp = paint.NewImageOp(img)
	} else if s.editDirty {
		s.editOp = paint.NewImageOp(imageio.RenderFH6Image(s.EditShapes, true, s.EditW, s.EditH, 1))
		s.editDirty = false
	}
	drawCheckerboard(gtx, rect)
	drawImageIn(gtx, s.editOp, rect)
	if s.selValid() {
		s.drawSelection(gtx, s.selScreenRect(rect))
	}
	s.addCanvasInput(gtx, vp, rect)
	cl.Pop()

	if s.editWantFocus {
		gtx.Execute(key.FocusCmd{Tag: &s.editKeyTag})
		s.editWantFocus = false
	}
	s.handleEditKeys(gtx)
	if s.selValid() { // animate the selection pulse
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(40 * time.Millisecond)})
	}
	return D{Size: sz}
}

// selValid reports whether a selectable shape (not the background) is currently selected.
func (s *AppState) selValid() bool { return s.EditSel >= 1 && s.EditSel < len(s.EditShapes) }

// selScreenRect is the selected shape's axis-aligned bounding box mapped into screen space.
func (s *AppState) selScreenRect(rect image.Rectangle) image.Rectangle {
	sh := s.EditShapes[s.EditSel]
	k := model.KindFromType(sh.Type)
	xMin, yMin, xMax, yMax := raster.BBox(k, model.ParamsFromShape(sh), s.EditW, s.EditH)
	return imgBBoxToScreen(rect, xMin, yMin, xMax, yMax, s.EditW, s.EditH)
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
			Kinds:   pointer.Press | pointer.Drag | pointer.Release | pointer.Scroll | pointer.Cancel,
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
		case pointer.Press:
			if pe.Buttons&pointer.ButtonPrimary != 0 {
				gtx.Execute(key.FocusCmd{Tag: &s.editKeyTag})
				s.editPanning = false
				s.pressEditor(pe.Position, pxToFrac(pe.Position, rect), rect)
			} else {
				s.editPanning = true
				s.panLast = pe.Position
			}
		case pointer.Drag:
			if s.editPanning {
				s.editPan = s.editPan.Add(pe.Position.Sub(s.panLast))
				s.panLast = pe.Position
			} else {
				s.editShift = pe.Modifiers.Contain(key.ModShift)
				s.dragEditor(pxToFrac(pe.Position, rect))
			}
		case pointer.Release, pointer.Cancel:
			if s.editPanning {
				s.editPanning = false
			} else if s.editDrag.kind != dragNone {
				s.pushUndo(s.editDrag.start)
				s.editDrag = editorDrag{}
				s.editDragBase = nil
				s.markEditDirty() // full render now that the drag is committed
			}
		}
	}
}

// addCanvasInput registers the canvas pointer/key target + hover cursors for the next frame.
func (s *AppState) addCanvasInput(gtx C, vp, rect image.Rectangle) {
	area := clip.Rect(vp).Push(gtx.Ops)
	event.Op(gtx.Ops, &s.editKeyTag)
	switch {
	case s.editPanning:
		pointer.CursorGrabbing.Add(gtx.Ops)
	case s.editDrag.kind != dragNone:
		editDragCursor(s.editDrag).Add(gtx.Ops)
	default:
		pointer.CursorDefault.Add(gtx.Ops)
	}
	area.Pop()

	if s.editPanning || s.editDrag.kind != dragNone || !s.selValid() {
		return
	}
	sel := s.selScreenRect(rect)
	if in := sel.Intersect(vp); in.Dx() > 0 && in.Dy() > 0 {
		c := clip.Rect(in).Push(gtx.Ops)
		pointer.CursorGrab.Add(gtx.Ops)
		c.Pop()
	}
	for i, h := range cropHandlePts(sel) {
		const r = editHandleHitR
		hc := clip.Rect(image.Rect(h.X-r, h.Y-r, h.X+r, h.Y+r)).Push(gtx.Ops)
		cropCursorForHandle(i).Add(gtx.Ops)
		hc.Pop()
	}
	rp := rotateHandlePt(sel)
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
		case ke.Name == key.NameDeleteForward || ke.Name == key.NameDeleteBackward:
			s.deleteSel()
		case ke.Name == key.NameEscape:
			s.EditSel = -1
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
	if !s.selValid() {
		return
	}
	step := 1.0
	if mods.Contain(key.ModShift) {
		step = 10
	}
	s.pushUndo(cloneShapes(s.EditShapes))
	moveShapeData(&s.EditShapes[s.EditSel], dx*step, dy*step)
	s.markEditDirty()
}

// drawSelection draws the selected shape's pulsing outline (accent↔white over ~0.8s, so it reads
// clearly over busy art), the 8 resize handles, and the rotate knob.
func (s *AppState) drawSelection(gtx C, sel image.Rectangle) {
	th := s.Th
	secs := float64(gtx.Now.UnixNano()) / 1e9
	pulse := float32(0.5 + 0.5*math.Sin(secs*2*math.Pi/0.8))
	col := lerpColor(th.Accent, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, pulse)
	drawRectBorder(gtx, sel, col)
	drawCropHandles(gtx, sel, th.Accent, th.Bg)
	rp := rotateHandlePt(sel)
	mx := (sel.Min.X + sel.Max.X) / 2
	paint.FillShape(gtx.Ops, th.Accent, clip.Rect(image.Rect(mx-1, rp.Y, mx+1, sel.Min.Y)).Op())
	drawKnob(gtx, rp, th.Accent, th.Bg)
	// Centre move anchor (grab to drag — works even where the shape itself is sparse).
	drawMoveAnchor(gtx, image.Pt(mx, (sel.Min.Y+sel.Max.Y)/2), th.Accent, th.Bg)
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
func (s *AppState) pressEditor(pos, fp f32.Point, rect image.Rectangle) {
	if s.selValid() {
		sel := s.selScreenRect(rect)
		if within(pos, rotateHandlePt(sel), editHandleHitR) {
			s.startTransform(dragRotate, 0, fp)
			return
		}
		for i, h := range cropHandlePts(sel) {
			if within(pos, h, editHandleHitR) {
				s.startTransform(dragScale, i, fp)
				return
			}
		}
		// A press anywhere inside the selected shape's bounding box moves it — so sparse shapes (a
		// barcode, a dotted decal) can be grabbed by their box, not only their solid pixels (which would
		// otherwise let the press fall through the gaps and drop the selection).
		if ptInRect(pos, sel) {
			s.startTransform(dragMove, 0, fp)
			return
		}
	}
	s.EditSel = s.hitTest(float64(fp.X)*float64(s.EditW), float64(fp.Y)*float64(s.EditH))
	if s.EditSel >= 1 {
		s.startTransform(dragMove, 0, fp)
	} else {
		s.editDrag = editorDrag{}
	}
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
		sh := s.EditShapes[s.EditSel]
		d.cx, d.cy = shapeCenter(sh)
		d.theta0 = shapeTheta(sh)
		d.ang0 = math.Atan2(float64(fp.Y)*float64(s.EditH)-d.cy, float64(fp.X)*float64(s.EditW)-d.cx)
		// Pre-render everything except the dragged shape once, so each drag frame only re-composites
		// that one shape (live + smooth at any doc size).
		s.editDragBase = imageio.RenderFH6ImageSkip(s.EditShapes, true, s.EditW, s.EditH, s.EditSel)
	}
	s.editDrag = d
}

// dragEditor recomputes the selected shape for the current pointer fraction, per the active drag kind.
func (s *AppState) dragEditor(fp f32.Point) {
	d := s.editDrag
	if d.kind == dragNone || s.EditSel < 1 || s.EditSel >= len(d.start) {
		return
	}
	start := d.start[s.EditSel]
	s.EditShapes[s.EditSel].Data = append([]float64(nil), start.Data...)
	dst := &s.EditShapes[s.EditSel]
	switch d.kind {
	case dragMove:
		dx := float64(fp.X-d.anchor.X) * float64(s.EditW)
		dy := float64(fp.Y-d.anchor.Y) * float64(s.EditH)
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
	s.editDrag = editorDrag{}
	s.editUndo = nil
	s.editRedo = nil
	if s.View == ViewEditor {
		s.View = ViewStudio
	}
}
