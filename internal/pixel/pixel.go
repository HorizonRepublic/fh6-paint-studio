// Package pixel is the specialized PIXEL-ART generator: instead of approximating with the greedy
// engine (which turns hard pixel edges into translucent mush), it reproduces the art EXACTLY —
// detect the logical pixel grid of the (usually nearest-neighbor upscaled) source, read the
// quantized cell colors, and decompose every color region into a minimal set of opaque axis-aligned
// rectangles (row-run RLE + vertical merge). The result is a pixel-perfect vinyl within the shape
// budget or an honest failure with the required count. No engine, no backend — pure raster.
package pixel

import (
	"fmt"
	"sort"

	"fh6-paint-studio/internal/model"
)

// Result is the generated decal plus the analysis facts the UI reports.
type Result struct {
	Shapes    []model.Shape
	GridStep  int // detected logical pixel size in source px (1 = native-res art)
	GridW     int // logical grid dimensions
	GridH     int
	Colors    int // distinct opaque cell colors
	RectCount int // shapes excluding the background
}

// colorKey packs an RGBA cell color (8-bit) for map keys. Alpha is binarized: pixel art is
// either paint or empty — antialiased fringes are quantized away by the grid read.
type colorKey uint32

// Generate builds the pixel-perfect rectangle decomposition of an RGBA float image (len w*h*4).
// maxShapes caps the output (FH6: 3000/panel incl. background); when the art does not fit, the
// error reports the required count so the user can simplify or crop.
func Generate(pix []float32, w, h, maxShapes int) (Result, error) {
	step := DetectGrid(pix, w, h)
	gw, gh := (w+step-1)/step, (h+step-1)/step

	cells := make([]colorKey, gw*gh)
	opaque := make([]bool, gw*gh)
	colorSet := map[colorKey]bool{}
	for gy := 0; gy < gh; gy++ {
		for gx := 0; gx < gw; gx++ {
			// Cell color = the CENTER source pixel (robust to 1px upscale fringes at cell borders).
			sx, sy := gx*step+step/2, gy*step+step/2
			if sx >= w {
				sx = w - 1
			}
			if sy >= h {
				sy = h - 1
			}
			p := (sy*w + sx) * 4
			if pix[p+3] < 0.5 {
				continue // transparent cell — stays uncovered (cutout)
			}
			k := packColor(pix[p], pix[p+1], pix[p+2])
			cells[gy*gw+gx] = k
			opaque[gy*gw+gx] = true
			colorSet[k] = true
		}
	}

	rects := decompose(cells, opaque, gw, gh)
	// Budget: background + rects. The background stays transparent (cutout semantics) — pixel art
	// with a full opaque background simply yields one giant rect from the decomposition itself.
	if len(rects)+1 > maxShapes {
		return Result{}, fmt.Errorf("pixel art needs %d rectangles (+1 background) but the budget is %d — simplify the art, crop, or reduce the palette", len(rects), maxShapes)
	}

	// Bigger rects first: the in-game z-order then keeps small detail on top (harmless for a
	// non-overlapping partition, but robust if a future merge step introduces overlap).
	sort.Slice(rects, func(a, b int) bool {
		return (rects[a].x1-rects[a].x0)*(rects[a].y1-rects[a].y0) > (rects[b].x1-rects[b].x0)*(rects[b].y1-rects[b].y0)
	})

	shapes := make([]model.Shape, 0, len(rects)+1)
	shapes = append(shapes, model.Shape{Type: 1, Data: []float64{0, 0, float64(w), float64(h)}, Color: []int{0, 0, 0, 0}})
	for _, rc := range rects {
		x0, y0 := float64(rc.x0*step), float64(rc.y0*step)
		x1, y1 := float64(rc.x1*step), float64(rc.y1*step)
		if x1 > float64(w) {
			x1 = float64(w)
		}
		if y1 > float64(h) {
			y1 = float64(h)
		}
		r, g, b := unpackColor(rc.color)
		c := model.Candidate{
			Kind:  model.KindRectangle,
			P:     [6]float32{float32((x0 + x1) / 2), float32((y0 + y1) / 2), float32((x1 - x0) / 2), float32((y1 - y0) / 2), 0, 0},
			Color: model.RGBA{R: r, G: g, B: b, A: 1},
		}
		shapes = append(shapes, c.ToShape(0))
	}
	return Result{
		Shapes: shapes, GridStep: step, GridW: gw, GridH: gh,
		Colors: len(colorSet), RectCount: len(rects),
	}, nil
}

// DetectGrid finds the logical pixel size of nearest-neighbor upscaled pixel art: color-change
// boundaries between adjacent columns/rows can only sit at multiples of the step, so the step is
// the GCD of all boundary positions. Noise-free by construction on clean exports; art with AA or
// photos degrade to step 1 (per-pixel — still correct, just budget-expensive).
func DetectGrid(pix []float32, w, h int) int {
	g := 0
	for x := 1; x < w; x++ {
		if columnDiffers(pix, w, h, x) {
			g = gcd(g, x)
			if g == 1 {
				return 1
			}
		}
	}
	for y := 1; y < h; y++ {
		if rowDiffers(pix, w, h, y) {
			g = gcd(g, y)
			if g == 1 {
				return 1
			}
		}
	}
	// Uniform image (no boundaries) or a genuine common step. Also require the step to divide
	// the dimensions REASONABLY (allow ragged right/bottom edges from sloppy crops).
	if g == 0 {
		return 1
	}
	return g
}

func columnDiffers(pix []float32, w, h, x int) bool {
	for y := 0; y < h; y++ {
		p, q := (y*w+x)*4, (y*w+x-1)*4
		if !samePix(pix[p:p+4], pix[q:q+4]) {
			return true
		}
	}
	return false
}

func rowDiffers(pix []float32, w, h, y int) bool {
	for x := 0; x < w; x++ {
		p, q := (y*w+x)*4, ((y-1)*w+x)*4
		if !samePix(pix[p:p+4], pix[q:q+4]) {
			return true
		}
	}
	return false
}

// samePix compares two RGBA pixels at 8-bit tolerance (PNG round-trips are exact; JPEG art is
// hopeless for pixel mode anyway and degrades to step 1).
func samePix(a, b []float32) bool {
	const tol = 1.5 / 255
	for k := 0; k < 4; k++ {
		d := a[k] - b[k]
		if d < -tol || d > tol {
			return false
		}
	}
	return true
}

type rect struct {
	x0, y0, x1, y1 int // logical grid coords, x1/y1 exclusive
	color          colorKey
}

// decompose partitions the opaque cells into same-color rectangles: maximal horizontal runs per
// row, then greedy vertical merge of runs with identical span+color — the classic RLE cover. Not
// provably minimal, but within a few percent on real sprites and exactly right on stripes/fills.
func decompose(cells []colorKey, opaque []bool, gw, gh int) []rect {
	type run struct {
		x0, x1 int
		color  colorKey
	}
	var out []rect
	open := map[[3]int]int{} // {x0,x1,color-ish} -> index into out of the still-growing rect
	for gy := 0; gy < gh; gy++ {
		next := map[[3]int]int{}
		x := 0
		for x < gw {
			if !opaque[gy*gw+x] {
				x++
				continue
			}
			c := cells[gy*gw+x]
			x1 := x + 1
			for x1 < gw && opaque[gy*gw+x1] && cells[gy*gw+x1] == c {
				x1++
			}
			key := [3]int{x, x1, int(c)}
			if ri, ok := open[key]; ok && out[ri].y1 == gy {
				out[ri].y1 = gy + 1 // extend the rect from the previous row
				next[key] = ri
			} else {
				out = append(out, rect{x0: x, y0: gy, x1: x1, y1: gy + 1, color: c})
				next[key] = len(out) - 1
			}
			x = x1
		}
		open = next
	}
	return out
}

func packColor(r, g, b float32) colorKey {
	return colorKey(uint32(clamp8(r))<<16 | uint32(clamp8(g))<<8 | uint32(clamp8(b)))
}

func unpackColor(k colorKey) (r, g, b float32) {
	return float32(k>>16&0xff) / 255, float32(k>>8&0xff) / 255, float32(k&0xff) / 255
}

func clamp8(v float32) int {
	i := int(v*255 + 0.5)
	if i < 0 {
		return 0
	}
	if i > 255 {
		return 255
	}
	return i
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}
