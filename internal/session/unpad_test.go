package session

import (
	"testing"

	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/runner"
)

// A keep-inside run fits a padded canvas and hands the geometry back in the VIEW's coordinates. The
// exported document carries no dimensions of its own, so shapes[0] — the background rectangle — is
// what every reader takes the canvas size from. This pins that it describes the view after the
// unpad, and that the shapes moved with it.
func TestFinishUnpadsBackgroundRect(t *testing.T) {
	const pad, view = 10, 100
	r := &Run{
		Prep:  imageio.Prepared{W: view + 2*pad, H: view + 2*pad},
		PadPx: pad, ViewW: view, ViewH: view,
	}
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, view + 2*pad, view + 2*pad}, Color: []int{1, 2, 3, 0}},
		{Type: model.TypeRotatedEllipse, Data: []float64{pad + 5, pad + 7, 3, 4, 0}, Color: []int{9, 9, 9, 255}},
	}
	done := r.finish(runner.Done{Result: engine.Result{Shapes: shapes}}, func(runner.Event) {})

	if done.Width != view || done.Height != view {
		t.Fatalf("Done dims = %dx%d, want %dx%d", done.Width, done.Height, view, view)
	}
	bg := done.Result.Shapes[0].Data
	if bg[0] != 0 || bg[1] != 0 || bg[2] != view || bg[3] != view {
		t.Errorf("background rect = %v, want [0 0 %d %d] — an importer reads the canvas size from here",
			bg, view, view)
	}
	if e := done.Result.Shapes[1].Data; e[0] != 5 || e[1] != 7 {
		t.Errorf("ellipse centre = (%v,%v), want (5,7)", e[0], e[1])
	}
}

// Without the surround nothing is rewritten: an unpadded run's background already describes its own
// canvas, and moving it would be a change with no cause.
func TestFinishLeavesUnpaddedGeometryAlone(t *testing.T) {
	r := &Run{Prep: imageio.Prepared{W: 64, H: 48}, ViewW: 64, ViewH: 48}
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, 64, 48}, Color: []int{1, 2, 3, 255}},
		{Type: model.TypeRotatedEllipse, Data: []float64{5, 7, 3, 4, 0}, Color: []int{9, 9, 9, 255}},
	}
	done := r.finish(runner.Done{Result: engine.Result{Shapes: shapes}}, func(runner.Event) {})
	if bg := done.Result.Shapes[0].Data; bg[0] != 0 || bg[1] != 0 || bg[2] != 64 || bg[3] != 48 {
		t.Errorf("background rect = %v, want [0 0 64 48]", bg)
	}
	if done.Width != 64 || done.Height != 48 {
		t.Errorf("Done dims = %dx%d, want 64x48", done.Width, done.Height)
	}
}
