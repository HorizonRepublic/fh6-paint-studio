package ui

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/gesture"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
)

// previewArea draws the loaded source (or a reconstruction) fit-centered in the available space. In
// crop mode it shows the resizable selection box; otherwise, once a reconstruction exists, it overlays
// it on the source with a draggable before/after wipe at s.Wipe.
func (s *AppState) previewArea(gtx C) D {
	th := s.Th
	sz := gtx.Constraints.Max
	if s.Source == nil {
		layout.Center.Layout(gtx, s.emptyStateOpen)
		return D{Size: sz}
	}

	rect := fitRect(s.Source.Bounds().Size(), sz)
	drawImageIn(gtx, s.SourceOp, rect)

	// Crop tool: a resizable selection box over the source (8 handles + drag-to-move, drag-outside to
	// re-draw). Apply (in the toolbar) swaps the working source to this crop; here we only edit it.
	if s.CropMode {
		s.updateCropDrag(gtx, rect)
		sel := cropToScreen(rect, s.cropSel)
		drawScrimAround(gtx, rect, sel)
		drawRectBorder(gtx, sel, th.Accent)
		drawCropHandles(gtx, sel, th.Accent, th.Bg)
		s.addCropCursors(gtx, rect, sel)
		return D{Size: sz}
	}

	// Reconstruction present: before/after wipe (the divider tracks the cursor X over the image).
	if s.Preview != nil {
		for {
			ev, ok := s.WipeDrag.Update(gtx.Metric, gtx.Source, gesture.Horizontal)
			if !ok {
				break
			}
			if (ev.Kind == pointer.Press || ev.Kind == pointer.Drag) && rect.Dx() > 0 {
				s.Wipe.Value = clamp01((ev.Position.X - float32(rect.Min.X)) / float32(rect.Dx()))
			}
		}
		wx := rect.Min.X + int(float64(rect.Dx())*float64(clamp01(s.Wipe.Value)))
		region := image.Rect(wx, rect.Min.Y, rect.Max.X, rect.Max.Y)
		cl := clip.Rect(region).Push(gtx.Ops)
		drawImageIn(gtx, s.PreviewOp, rect)
		cl.Pop()
		if wx > rect.Min.X && wx < rect.Max.X {
			paint.FillShape(gtx.Ops, th.Accent, clip.Rect(image.Rect(wx-1, rect.Min.Y, wx+1, rect.Max.Y)).Op())
		}
		area := clip.Rect(rect).Push(gtx.Ops)
		s.WipeDrag.Add(gtx.Ops)
		pointer.CursorColResize.Add(gtx.Ops)
		area.Pop()

		// A "Zoom" button in the top-right corner opens the current reconstruction in the full-size
		// lightbox (drawn last, so its click area sits on top of the wipe drag).
		if s.PreviewZoom.Clicked(gtx) {
			s.ShowLightbox(s.Preview)
		}
		s.drawZoomButton(gtx, rect)
	}
	return D{Size: sz}
}

// drawZoomButton renders the corner "Zoom" pill, right-aligned in the reconstruction's top-right. It is
// recorded then replayed under an offset so it can be right-aligned by its measured width.
func (s *AppState) drawZoomButton(gtx C, rect image.Rectangle) {
	th := s.Th
	pad := gtx.Dp(10)
	gtx.Constraints.Min = image.Point{} // size the pill to its content, not the full preview area
	macro := op.Record(gtx.Ops)
	dims := s.PreviewZoom.Layout(gtx, func(gtx C) D {
		return layout.Background{}.Layout(gtx,
			func(gtx C) D {
				fillRRect(gtx, color.NRGBA{A: 160}, gtx.Constraints.Min, 8)
				return D{Size: gtx.Constraints.Min}
			},
			func(gtx C) D {
				return layout.Inset{Top: 6, Bottom: 6, Left: 11, Right: 11}.Layout(gtx, func(gtx C) D {
					pointer.CursorPointer.Add(gtx.Ops)
					return th.Lbl(gtx, 13, "Zoom", th.Text)
				})
			},
		)
	})
	call := macro.Stop()
	off := op.Offset(image.Pt(rect.Max.X-dims.Size.X-pad, rect.Min.Y+pad)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	off.Pop()
}

// addCropCursors registers the crop drag gesture and the per-region mouse cursors. Gio resolves the
// cursor from the topmost clip area under the pointer, and clip areas pass pointer events through, so
// the handle/interior cursor areas (added on top) change the cursor on hover WITHOUT stealing the drag
// events from the base gesture beneath. During an active drag the cursor is locked to the operation.
func (s *AppState) addCropCursors(gtx C, rect, sel image.Rectangle) {
	base := clip.Rect(rect).Push(gtx.Ops) // the drag gesture + the default crosshair over the whole area
	s.CropDrag.Add(gtx.Ops)
	pointer.CursorCrosshair.Add(gtx.Ops)
	base.Pop()

	if s.cropDragKind != cropNone { // mid-drag: lock the cursor to the operation across the whole area
		lock := clip.Rect(rect).Push(gtx.Ops)
		cropCursorFor(s.cropDragKind).Add(gtx.Ops)
		lock.Pop()
		return
	}
	if !s.cropSelValid() {
		return
	}
	in := clip.Rect(sel).Push(gtx.Ops) // hover inside the selection -> move (grab) cursor
	pointer.CursorGrab.Add(gtx.Ops)
	in.Pop()
	for i, h := range cropHandlePts(sel) { // hover over a handle -> its directional resize cursor (on top)
		const r = 9
		hc := clip.Rect(image.Rect(h.X-r, h.Y-r, h.X+r, h.Y+r)).Push(gtx.Ops)
		cropCursorForHandle(i).Add(gtx.Ops)
		hc.Pop()
	}
}

// cropCursorFor maps an active crop drag kind to the cursor shown while dragging.
func cropCursorFor(kind int) pointer.Cursor {
	switch {
	case kind == cropMove:
		return pointer.CursorGrabbing
	case kind >= cropHandle0:
		return cropCursorForHandle(kind - cropHandle0)
	default:
		return pointer.CursorCrosshair // cropNew (drawing a fresh selection)
	}
}

// cropCursorForHandle maps a handle index (0..7 = NW,N,NE,E,SE,S,SW,W) to its directional resize cursor.
func cropCursorForHandle(i int) pointer.Cursor {
	switch i {
	case 0:
		return pointer.CursorNorthWestResize
	case 1:
		return pointer.CursorNorthResize
	case 2:
		return pointer.CursorNorthEastResize
	case 3:
		return pointer.CursorEastResize
	case 4:
		return pointer.CursorSouthEastResize
	case 5:
		return pointer.CursorSouthResize
	case 6:
		return pointer.CursorSouthWestResize
	case 7:
		return pointer.CursorWestResize
	default:
		return pointer.CursorCrosshair
	}
}

// updateCropDrag edits the pending crop selection from this frame's pointer events: a press hit-tests
// the 8 handles / interior of the current box to pick resize-edge vs move vs re-draw, drags adjust the
// selection, release ends the gesture. All selection math is in image-fraction space (cropSel).
func (s *AppState) updateCropDrag(gtx C, rect image.Rectangle) {
	for {
		ev, ok := s.CropDrag.Update(gtx.Metric, gtx.Source, gesture.Both)
		if !ok {
			break
		}
		fp := pxToFrac(ev.Position, rect)
		switch ev.Kind {
		case pointer.Press:
			s.cropDragKind = s.cropHitTest(ev.Position, rect)
			s.cropStartSel = s.cropSel
			s.cropAnchor = fp
		case pointer.Drag:
			s.applyCropDrag(fp)
		case pointer.Release, pointer.Cancel:
			s.cropDragKind = cropNone
		}
	}
}

// cropHitTest classifies a press: a handle (cropHandle0+i) when near one of the 8 grips, cropMove when
// inside the box, else cropNew (start a fresh selection anchored at the press).
func (s *AppState) cropHitTest(p f32.Point, rect image.Rectangle) int {
	if s.cropSelValid() {
		sel := cropToScreen(rect, s.cropSel)
		const hit = 10 // px grab radius
		for i, h := range cropHandlePts(sel) {
			dx, dy := p.X-float32(h.X), p.Y-float32(h.Y)
			if dx*dx+dy*dy <= hit*hit {
				return cropHandle0 + i
			}
		}
		if p.X > float32(sel.Min.X) && p.X < float32(sel.Max.X) && p.Y > float32(sel.Min.Y) && p.Y < float32(sel.Max.Y) {
			return cropMove
		}
	}
	return cropNew
}

// applyCropDrag updates cropSel for the current pointer fraction fp, per the active drag kind.
func (s *AppState) applyCropDrag(fp f32.Point) {
	ax, ay := float64(s.cropAnchor.X), float64(s.cropAnchor.Y)
	cx, cy := float64(fp.X), float64(fp.Y)
	switch {
	case s.cropDragKind == cropNew:
		x0, x1 := minMax64(ax, cx)
		y0, y1 := minMax64(ay, cy)
		s.cropSel = [4]float64{x0, y0, x1 - x0, y1 - y0}
	case s.cropDragKind == cropMove:
		nx := clamp64(s.cropStartSel[0]+(cx-ax), 0, 1-s.cropStartSel[2])
		ny := clamp64(s.cropStartSel[1]+(cy-ay), 0, 1-s.cropStartSel[3])
		s.cropSel = [4]float64{nx, ny, s.cropStartSel[2], s.cropStartSel[3]}
	case s.cropDragKind >= cropHandle0:
		s.resizeCropHandle(s.cropDragKind-cropHandle0, cx, cy)
	}
}

// resizeCropHandle moves the edges controlled by handle i (0..7 = NW,N,NE,E,SE,S,SW,W) to (cx,cy),
// keeping the opposite edges fixed (from cropStartSel), then normalises to a positive rect.
func (s *AppState) resizeCropHandle(i int, cx, cy float64) {
	b := s.cropStartSel
	x0, y0, x1, y1 := b[0], b[1], b[0]+b[2], b[1]+b[3]
	left := i == 0 || i == 6 || i == 7
	right := i == 2 || i == 3 || i == 4
	top := i == 0 || i == 1 || i == 2
	bottom := i == 4 || i == 5 || i == 6
	if left {
		x0 = clamp64(cx, 0, 1)
	}
	if right {
		x1 = clamp64(cx, 0, 1)
	}
	if top {
		y0 = clamp64(cy, 0, 1)
	}
	if bottom {
		y1 = clamp64(cy, 0, 1)
	}
	x0, x1 = minMax64(x0, x1)
	y0, y1 = minMax64(y0, y1)
	s.cropSel = [4]float64{x0, y0, x1 - x0, y1 - y0}
}

// emptyStateOpen is the centered affordance shown before any image is loaded. The whole card is
// clickable (s.PreviewOpen) and triggers the Open flow, so the large preview area itself acts as an
// open button — far more discoverable than the small one in the side panel.
func (s *AppState) emptyStateOpen(gtx C) D {
	th := s.Th
	return s.PreviewOpen.Layout(gtx, func(gtx C) D {
		border, bg := th.Border, th.Surface
		if s.PreviewOpen.Hovered() {
			border, bg = th.Accent, th.SurfaceHi
		}
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx C) D {
				sz := gtx.Constraints.Min
				fillRRect(gtx, border, sz, 14)
				in := image.Rectangle{Min: image.Pt(2, 2), Max: image.Pt(sz.X-2, sz.Y-2)}
				paint.FillShape(gtx.Ops, bg, clip.UniformRRect(in, 12).Op(gtx.Ops))
				pointer.CursorPointer.Add(gtx.Ops) // hand cursor over the whole card
				return D{Size: sz}
			}),
			layout.Stacked(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Dp(300)
				return layout.UniformInset(30).Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 34, "+", th.Accent) }),
						layout.Rigid(GapV(6).Layout),
						layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 15, "Open an image", th.Text) }),
						layout.Rigid(GapV(4).Layout),
						layout.Rigid(func(gtx C) D { return th.Dim(gtx, "Click here to choose a file") }),
					)
				})
			}),
		)
	})
}

// cropBar is the crop toolbar under the preview. State-dependent: while editing a selection it shows
// Apply / Cancel; otherwise a Crop button (+ Reset once the source is a crop) with a hint.
func (s *AppState) cropBar(gtx C) D {
	th := s.Th
	if s.Source == nil {
		return D{}
	}
	return layout.Inset{Top: 10}.Layout(gtx, func(gtx C) D {
		if s.CropMode {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return th.PrimaryButton(gtx, &s.CropApplyBtn, "Apply crop", true) }),
				layout.Rigid(GapH(8).Layout),
				layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.CropCancelBtn, "Cancel", true) }),
				layout.Rigid(GapH(12).Layout),
				layout.Flexed(1, func(gtx C) D {
					return th.Dim(gtx, "drag to select · handles to resize · inside to move")
				}),
			)
		}
		children := []layout.FlexChild{
			layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.CropBtn, "Crop region", true) }),
		}
		if s.Cropped {
			children = append(children,
				layout.Rigid(GapH(8).Layout),
				layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.ResetBtn, "Reset to original", true) }))
		}
		children = append(children,
			layout.Rigid(GapH(12).Layout),
			layout.Flexed(1, func(gtx C) D {
				if s.Cropped {
					return th.Dim(gtx, "working on a crop — Generate rebuilds just this at the full budget")
				}
				return th.Dim(gtx, "crop a detail (a face, a logo) to spend the whole budget on it")
			}))
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
	})
}

// fitRect returns the largest rectangle of img's aspect that fits centered within area.
func fitRect(img, area image.Point) image.Rectangle {
	if img.X <= 0 || img.Y <= 0 {
		return image.Rectangle{Max: area}
	}
	scale := min(float64(area.X)/float64(img.X), float64(area.Y)/float64(img.Y))
	dw, dh := int(scale*float64(img.X)), int(scale*float64(img.Y))
	ox, oy := (area.X-dw)/2, (area.Y-dh)/2
	return image.Rect(ox, oy, ox+dw, oy+dh)
}

// drawImageIn draws an image op scaled to exactly fill rect.
func drawImageIn(gtx C, src paint.ImageOp, rect image.Rectangle) {
	off := op.Offset(rect.Min).Push(gtx.Ops)
	cl := clip.Rect{Max: rect.Size()}.Push(gtx.Ops)
	g := gtx
	g.Constraints = layout.Exact(rect.Size())
	widget.Image{Src: src, Fit: widget.Cover, Position: layout.Center}.Layout(g)
	cl.Pop()
	off.Pop()
}

// pxToFrac converts a pointer position (preview-local px) to image-fraction space, clamped to [0,1].
func pxToFrac(p f32.Point, rect image.Rectangle) f32.Point {
	if rect.Dx() <= 0 || rect.Dy() <= 0 {
		return f32.Point{}
	}
	return f32.Point{
		X: clamp01((p.X - float32(rect.Min.X)) / float32(rect.Dx())),
		Y: clamp01((p.Y - float32(rect.Min.Y)) / float32(rect.Dy())),
	}
}

// cropToScreen maps a fractional rect {fx,fy,fw,fh} into a pixel rect within rect.
func cropToScreen(rect image.Rectangle, c [4]float64) image.Rectangle {
	x0 := rect.Min.X + int(c[0]*float64(rect.Dx()))
	y0 := rect.Min.Y + int(c[1]*float64(rect.Dy()))
	x1 := x0 + int(c[2]*float64(rect.Dx()))
	y1 := y0 + int(c[3]*float64(rect.Dy()))
	return image.Rect(x0, y0, x1, y1)
}

// cropHandlePts returns the 8 grip centers of r in the order NW,N,NE,E,SE,S,SW,W.
func cropHandlePts(r image.Rectangle) [8]image.Point {
	mx, my := (r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2
	return [8]image.Point{
		{X: r.Min.X, Y: r.Min.Y}, {X: mx, Y: r.Min.Y}, {X: r.Max.X, Y: r.Min.Y},
		{X: r.Max.X, Y: my}, {X: r.Max.X, Y: r.Max.Y}, {X: mx, Y: r.Max.Y},
		{X: r.Min.X, Y: r.Max.Y}, {X: r.Min.X, Y: my},
	}
}

// drawCropHandles draws a filled grip square (with a 1px border) at each of the 8 handle points.
func drawCropHandles(gtx C, sel image.Rectangle, fill, border color.NRGBA) {
	const h = 4 // half-size
	for _, p := range cropHandlePts(sel) {
		paint.FillShape(gtx.Ops, border, clip.Rect(image.Rect(p.X-h-1, p.Y-h-1, p.X+h+1, p.Y+h+1)).Op())
		paint.FillShape(gtx.Ops, fill, clip.Rect(image.Rect(p.X-h, p.Y-h, p.X+h, p.Y+h)).Op())
	}
}

// drawScrimAround dims the four bands of outer between it and the selection sel (a focus vignette).
func drawScrimAround(gtx C, outer, sel image.Rectangle) {
	sel = sel.Intersect(outer)
	scrim := color.NRGBA{A: 130}
	fill := func(r image.Rectangle) {
		if r.Dx() > 0 && r.Dy() > 0 {
			paint.FillShape(gtx.Ops, scrim, clip.Rect(r).Op())
		}
	}
	fill(image.Rect(outer.Min.X, outer.Min.Y, outer.Max.X, sel.Min.Y)) // top
	fill(image.Rect(outer.Min.X, sel.Max.Y, outer.Max.X, outer.Max.Y)) // bottom
	fill(image.Rect(outer.Min.X, sel.Min.Y, sel.Min.X, sel.Max.Y))     // left
	fill(image.Rect(sel.Max.X, sel.Min.Y, outer.Max.X, sel.Max.Y))     // right
}

// drawRectBorder strokes a 2px border just inside r.
func drawRectBorder(gtx C, r image.Rectangle, col color.NRGBA) {
	if r.Dx() <= 0 || r.Dy() <= 0 {
		return
	}
	const w = 2
	band := func(x0, y0, x1, y1 int) {
		paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(x0, y0, x1, y1)).Op())
	}
	band(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+w) // top
	band(r.Min.X, r.Max.Y-w, r.Max.X, r.Max.Y) // bottom
	band(r.Min.X, r.Min.Y, r.Min.X+w, r.Max.Y) // left
	band(r.Max.X-w, r.Min.Y, r.Max.X, r.Max.Y) // right
}

func clamp01(f float32) float32 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func minMax64(a, b float64) (float64, float64) {
	if a <= b {
		return a, b
	}
	return b, a
}

func clamp64(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
