package ui

import (
	"testing"

	"gioui.org/f32"
)

func approx4(a, b [4]float64) bool {
	for i := range a {
		if d := a[i] - b[i]; d > 1e-6 || d < -1e-6 { // float32 pointer coords -> ~1e-7 precision
			return false
		}
	}
	return true
}

// A new-selection drag builds a positive rect from the anchor to the cursor, in either direction.
func TestCropNewDrag(t *testing.T) {
	s := &AppState{cropDragKind: cropNew}
	want := [4]float64{0.2, 0.3, 0.4, 0.2}
	s.cropAnchor = f32.Pt(0.2, 0.3)
	s.applyCropDrag(f32.Pt(0.6, 0.5))
	if !approx4(s.cropSel, want) {
		t.Fatalf("new drag = %v, want %v", s.cropSel, want)
	}
	s.cropAnchor = f32.Pt(0.6, 0.5) // reversed direction -> same rect
	s.applyCropDrag(f32.Pt(0.2, 0.3))
	if !approx4(s.cropSel, want) {
		t.Fatalf("reversed drag = %v, want %v", s.cropSel, want)
	}
}

// Moving the box keeps its size and clamps it inside [0,1].
func TestCropMoveClamps(t *testing.T) {
	s := &AppState{cropDragKind: cropMove}
	s.cropStartSel = [4]float64{0.5, 0.5, 0.4, 0.4}
	s.cropAnchor = f32.Pt(0.5, 0.5)
	s.applyCropDrag(f32.Pt(0.9, 0.9)) // pushes past the edge -> clamps to fit
	if s.cropSel[2] != 0.4 || s.cropSel[3] != 0.4 {
		t.Fatalf("move changed size: %v", s.cropSel)
	}
	if s.cropSel[0]+s.cropSel[2] > 1.0000001 || s.cropSel[1]+s.cropSel[3] > 1.0000001 {
		t.Fatalf("move not clamped inside: %v", s.cropSel)
	}
}

// Each handle moves only its own edges; the opposite edges stay fixed.
func TestCropResizeHandle(t *testing.T) {
	s := &AppState{}
	s.cropStartSel = [4]float64{0.2, 0.2, 0.6, 0.6} // x0,y0=0.2  x1,y1=0.8
	s.resizeCropHandle(2, 0.9, 0.1)                 // NE: x1->0.9, y0->0.1
	if want := [4]float64{0.2, 0.1, 0.7, 0.7}; !approx4(s.cropSel, want) {
		t.Fatalf("NE resize = %v, want %v", s.cropSel, want)
	}
	s.cropStartSel = [4]float64{0.2, 0.2, 0.6, 0.6}
	s.resizeCropHandle(6, 0.1, 0.9) // SW: x0->0.1, y1->0.9
	if want := [4]float64{0.1, 0.2, 0.7, 0.7}; !approx4(s.cropSel, want) {
		t.Fatalf("SW resize = %v, want %v", s.cropSel, want)
	}
}
