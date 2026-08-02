package pixel

import (
	"testing"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// sprite builds an RGBA float image from a rune legend upscaled by step (nearest neighbor).
func sprite(rows []string, legend map[rune][4]float32, step int) (pix []float32, w, h int) {
	gh, gw := len(rows), len(rows[0])
	w, h = gw*step, gh*step
	pix = make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := legend[rune(rows[y/step][x/step])]
			p := (y*w + x) * 4
			pix[p], pix[p+1], pix[p+2], pix[p+3] = c[0], c[1], c[2], c[3]
		}
	}
	return pix, w, h
}

var legend = map[rune][4]float32{
	'.': {0, 0, 0, 0}, // transparent
	'R': {1, 0, 0, 1},
	'G': {0, 1, 0, 1},
	'B': {0, 0, 1, 1},
}

func TestDetectGrid(t *testing.T) {
	rows := []string{
		"RRG",
		"RBG",
		"..G",
	}
	for _, step := range []int{1, 4, 7} {
		pix, w, h := sprite(rows, legend, step)
		if got := DetectGrid(pix, w, h); got != step {
			t.Errorf("step %d: DetectGrid = %d", step, got)
		}
	}
}

func TestGenerateReproducesExactly(t *testing.T) {
	rows := []string{
		"RRRRG",
		"RBBRG",
		"RBBRG",
		"RRRRG",
		"....G",
	}
	const step = 6
	pix, w, h := sprite(rows, legend, step)
	res, err := Generate(pix, w, h, 3000)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.GridStep != step || res.GridW != 5 || res.GridH != 5 {
		t.Fatalf("grid = step %d %dx%d, want step %d 5x5", res.GridStep, res.GridW, res.GridH, step)
	}
	if res.Colors != 3 {
		t.Fatalf("colors = %d, want 3", res.Colors)
	}
	// R frame(4 via RLE: top, bottom, left mid, right mid) + B block(1) + G column(1) = 6.
	if res.RectCount != 6 {
		t.Errorf("rects = %d, want 6 (RLE merge)", res.RectCount)
	}

	// Rasterize the shapes (hard, opaque, non-overlapping) and compare per-pixel with the source.
	canvas := make([]float32, w*h*4)
	for _, s := range res.Shapes[1:] {
		c := model.Candidate{Kind: model.KindFromType(s.Type), P: model.ParamsFromShape(s)}
		c.Color = model.RGBA{R: float32(s.Color[0]) / 255, G: float32(s.Color[1]) / 255, B: float32(s.Color[2]) / 255, A: float32(s.Color[3]) / 255}
		xMin, yMin, xMax, yMax := raster.BBox(c.Kind, c.P, w, h)
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				if !raster.Inside(c.Kind, c.P, x, y) {
					continue
				}
				p := (y*w + x) * 4
				canvas[p], canvas[p+1], canvas[p+2], canvas[p+3] = c.Color.R, c.Color.G, c.Color.B, 1
			}
		}
	}
	var wrong int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := (y*w + x) * 4
			if pix[p+3] < 0.5 {
				if canvas[p+3] > 0.5 {
					wrong++
				}
				continue
			}
			if !samePix(pix[p:p+4], canvas[p:p+4]) {
				wrong++
			}
		}
	}
	// The hard rasterizer's half-open pixel-center convention may drop a 1px seam at rect borders;
	// a pixel-PERFECT mode tolerates zero interior errors.
	if wrong != 0 {
		t.Errorf("%d/%d pixels differ from the source", wrong, w*h)
	}
}

func TestGenerateBudgetError(t *testing.T) {
	// A 60x60 checkerboard = 3600 alternating cells -> far over a 100-shape budget.
	rows := make([]string, 60)
	for y := range rows {
		b := make([]byte, 60)
		for x := range b {
			if (x+y)%2 == 0 {
				b[x] = 'R'
			} else {
				b[x] = 'B'
			}
		}
		rows[y] = string(b)
	}
	pix, w, h := sprite(rows, legend, 2)
	if _, err := Generate(pix, w, h, 100); err == nil {
		t.Fatal("expected a budget error for a 3600-cell checkerboard")
	}
}
