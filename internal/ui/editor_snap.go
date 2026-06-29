package ui

import (
	"image"
	"math"

	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// snapAxis finds the smallest shift that lands one of the moving anchors on a target within threshold.
// It returns the shift to add to the move, the target coordinate (for the guide line), and whether a
// snap was found.
func snapAxis(anchors, targets []float64, threshold float64) (shift, guide float64, ok bool) {
	best := threshold
	for _, a := range anchors {
		for _, t := range targets {
			if d := math.Abs(t - a); d <= best {
				best, shift, guide, ok = d, t-a, t, true
			}
		}
	}
	return shift, guide, ok
}

// snapTargets is the set of x and y coordinates (image px) a moving selection can snap to: every other
// shape's left/centre/right and top/middle/bottom, the canvas edges and centre, and grid lines when the
// grid guide is on.
func (s *AppState) snapTargets() (xs, ys []float64) {
	sel := map[int]bool{}
	for _, i := range s.selIndices() {
		sel[i] = true
	}
	for i := 1; i < len(s.EditShapes); i++ {
		if sel[i] {
			continue
		}
		sh := s.EditShapes[i]
		x0, y0, x1, y1 := raster.BBox(model.KindFromType(sh.Type), model.ParamsFromShape(sh), s.EditW, s.EditH)
		fx0, fy0, fx1, fy1 := float64(x0), float64(y0), float64(x1), float64(y1)
		xs = append(xs, fx0, (fx0+fx1)/2, fx1)
		ys = append(ys, fy0, (fy0+fy1)/2, fy1)
	}
	w, h := float64(s.EditW), float64(s.EditH)
	xs = append(xs, 0, w/2, w)
	ys = append(ys, 0, h/2, h)
	if s.snapGridStep > 0 {
		for x := 0.0; x <= w; x += s.snapGridStep {
			xs = append(xs, x)
		}
		for y := 0.0; y <= h; y += s.snapGridStep {
			ys = append(ys, y)
		}
	}
	return xs, ys
}

// snapMoveDelta nudges a raw move delta so the moving selection's bbox edges/centres land on a nearby
// target, recording the active guide lines for drawing. It is a no-op when snapping is off or suspended.
func (s *AppState) snapMoveDelta(box [4]float64, dx, dy float64) (float64, float64) {
	if !s.snapOn || s.editAlt || s.snapThreshImg <= 0 {
		return dx, dy
	}
	xs, ys := s.snapTargets()
	ax := []float64{box[0] + dx, (box[0]+box[2])/2 + dx, box[2] + dx}
	ay := []float64{box[1] + dy, (box[1]+box[3])/2 + dy, box[3] + dy}
	if sh, g, ok := snapAxis(ax, xs, s.snapThreshImg); ok {
		dx += sh
		s.snapGuideX, s.snapShowX = g, true
	}
	if sh, g, ok := snapAxis(ay, ys, s.snapThreshImg); ok {
		dy += sh
		s.snapGuideY, s.snapShowY = g, true
	}
	return dx, dy
}

// drawSnapGuides paints the accent lines for the snaps that fired this frame.
func (s *AppState) drawSnapGuides(gtx C, vp, rect image.Rectangle) {
	if (!s.snapShowX && !s.snapShowY) || rect.Dx() <= 0 || s.EditW <= 0 {
		return
	}
	col := s.Th.Accent
	col.A = 200
	scale := float64(rect.Dx()) / float64(s.EditW)
	cl := clip.Rect(vp).Push(gtx.Ops)
	if s.snapShowX {
		sx := rect.Min.X + int(s.snapGuideX*scale+0.5)
		paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(sx, vp.Min.Y, sx+1, vp.Max.Y)).Op())
	}
	if s.snapShowY {
		sy := rect.Min.Y + int(s.snapGuideY*scale+0.5)
		paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(vp.Min.X, sy, vp.Max.X, sy+1)).Op())
	}
	cl.Pop()
}
