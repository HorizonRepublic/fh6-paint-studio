package session

import (
	"testing"

	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
	"fh6-paint-studio/internal/runner"
)

// The hybrid ink runs on the CONTENT, not on the padded canvas. The keep-inside surround is
// transparent black and the ink engine's luma ignores alpha, so a padded source hands it a
// maximum-contrast step exactly on the content border — and it answers with a frame around the whole
// decal. Every line it draws must land inside the view; one that does not is that frame.
func TestInkStaysInsideTheView(t *testing.T) {
	const side = 192
	pix := make([]float32, side*side*4)
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			i := (y*side + x) * 4
			// A flat mid-grey field with one dark blob in the middle: the ONLY real contour is the
			// blob, so any line near the border came from the surround.
			v := float32(0.72)
			dx, dy := float64(x-side/2), float64(y-side/2)
			if dx*dx+dy*dy < 34*34 {
				v = 0.1
			}
			pix[i], pix[i+1], pix[i+2], pix[i+3] = v, v, v, 1
		}
	}
	bare := &imageio.Prepared{W: side, H: side, Pixels: pix}
	padded, pad := imageio.PadTransparent(bare, 0.10)
	if pad <= 0 {
		t.Fatal("PadTransparent produced no margin")
	}

	r := &Run{Prep: *padded, PadPx: pad, ViewW: side, ViewH: side, Ink: 200}
	shapes := []model.Shape{
		{Type: model.TypeRectangle, Data: []float64{0, 0, float64(padded.W), float64(padded.H)}, Color: []int{0, 0, 0, 0}},
	}
	done := r.finish(runner.Done{Result: engine.Result{Shapes: shapes}}, func(runner.Event) {})

	out := done.Result.Shapes
	if len(out) < 2 {
		t.Skip("the ink engine drew nothing on this synthetic field; nothing to assert")
	}
	stray := 0
	for _, s := range out[1:] {
		k := model.KindFromType(s.Type)
		// Unclamped bounds: raster.BBox clamps to the canvas and would hide exactly the overhang
		// this test is about, so ask for the box against a canvas big enough to hold it.
		x0, y0, x1, y1 := raster.BBox(k, model.ParamsFromShape(s), side*4, side*4)
		if x0 <= 0 || y0 <= 0 || x1 >= side-1 || y1 >= side-1 {
			stray++
		}
	}
	if stray > 0 {
		t.Errorf("%d of %d ink lines reach the view's edge — the surround was inked as a frame",
			stray, len(out)-1)
	}
}

// UnpadPrepared is the inverse of PadTransparent on the pixels, and it restores the pre-pad
// transparency flags rather than the padded canvas's.
func TestUnpadPreparedRoundTrip(t *testing.T) {
	const w, h = 9, 7
	px := make([]float32, w*h*4)
	for i := range px {
		px[i] = float32(i%251) / 251
	}
	bare := &imageio.Prepared{W: w, H: h, Pixels: px}
	padded, pad := imageio.PadTransparent(bare, 0.5)
	back := imageio.UnpadPrepared(padded, pad, w, h)
	if back.W != w || back.H != h {
		t.Fatalf("unpadded to %dx%d, want %dx%d", back.W, back.H, w, h)
	}
	for i := range px {
		if back.Pixels[i] != px[i] {
			t.Fatalf("pixel %d = %v, want %v", i, back.Pixels[i], px[i])
		}
	}
	if back.PaddedOpaque || back.HasTransparency {
		t.Errorf("flags = PaddedOpaque %v / HasTransparency %v, want the pre-pad pair (false/false)",
			back.PaddedOpaque, back.HasTransparency)
	}
	if same := imageio.UnpadPrepared(padded, 0, w, h); same != padded {
		t.Error("pad<=0 must be a no-op returning the input")
	}
}
