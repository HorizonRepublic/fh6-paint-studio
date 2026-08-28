package engine

import (
	"os"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

// bgSolveOn gates the background re-solve. FH6_BGSOLVE=0 leaves the background at the
// colour Run started from, which is what the engine has always SCORED.
var bgSolveOn = os.Getenv("FH6_BGSOLVE") != "0"

// solveBackground re-solves the background rectangle's colour over the pixels where it
// actually shows through, and feeds the answer back into the canvas every later render
// starts from — so the engine scores the background it is going to deliver.
//
// recolorVisible used to repaint shapes[0] as a side effect, and it was wrong twice over.
// Its ownership came from raster.Inside(KindFromType(TypeRectangle)) — a type with no case,
// so it fell through to KindEllipse and Data=[0,0,w,h] was read as an ellipse CENTRED on the
// top-left corner: it claimed a quarter of the canvas and left the bottom-right corner
// owned by nothing. And nothing carried the new colour back: every render rebuilds from
// initCanvas and applies shapes[1:], so the score kept the old background while
// imageio.RenderFH6 and inject.Inject both read the new one. Measured on flat art, that gap
// was real — the exported background came out 255 where the engine had scored 249.
//
// Here the background owns exactly what it shows through: the complement of every other
// shape's footprint. A translucent or gradient shape does not own those pixels either (it
// blends over them), so its footprint is excluded rather than claimed — the mean is taken
// over pixels that are purely background. Over a disjoint pixel set the weighted mean is
// the SSE minimum, so the gate below can only ever reject a rounding-level regression.
func (r *run) solveBackground() {
	if !bgSolveOn || r.opt.TransparentBG || len(r.shapes) == 0 {
		return
	}
	bg := r.shapes[0]
	if bg.Type != model.TypeRectangle || len(bg.Color) < 4 || bg.Color[3] < 255 {
		return
	}
	w, h := r.w, r.h
	// A mean over a lattice is the same mean; testing every pixel against every shape cost 5% of
	// the whole run for a colour that is stable over tens of thousands of samples.
	m := w
	if h < m {
		m = h
	}
	stride := m / 256
	if stride < 1 {
		stride = 1
	} else if stride > 8 {
		stride = 8
	}
	sw2, sh2 := (w+stride-1)/stride, (h+stride-1)/stride
	covered := make([]bool, sw2*sh2)
	for j := 1; j < len(r.shapes); j++ {
		s := r.shapes[j]
		kind := model.KindFromType(s.Type)
		p := model.ParamsFromShape(s)
		grad := raster.IsGradient(kind)
		xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
		for y := (yMin + stride - 1) / stride * stride; y <= yMax; y += stride {
			row := y / stride * sw2
			for x := (xMin + stride - 1) / stride * stride; x <= xMax; x += stride {
				idx := row + x/stride
				if covered[idx] {
					continue
				}
				if grad {
					if raster.Coverage(kind, p, x, y) > 0.02 {
						covered[idx] = true
					}
				} else if raster.Inside(kind, p, x, y) {
					covered[idx] = true
				}
			}
		}
	}

	target, weight := r.be.Target(), r.be.Weight()
	var sw, sr, sg, sb float64
	for y := 0; y < h; y += stride {
		row := y / stride * sw2
		for x := 0; x < w; x += stride {
			if covered[row+x/stride] {
				continue
			}
			wt := float64(weight[y*w+x])
			p := (y*w + x) * 4
			sw += wt
			sr += wt * float64(target[p])
			sg += wt * float64(target[p+1])
			sb += wt * float64(target[p+2])
		}
	}
	if sw <= 0 {
		return
	}
	inv := 1 / sw
	cr := model.EncByte(float32(sr * inv))
	cg := model.EncByte(float32(sg * inv))
	cb := model.EncByte(float32(sb * inv))
	if cr == bg.Color[0] && cg == bg.Color[1] && cb == bg.Color[2] {
		return
	}

	or, og, ob := bg.Color[0], bg.Color[1], bg.Color[2]
	r.shapes[0].Color[0], r.shapes[0].Color[1], r.shapes[0].Color[2] = cr, cg, cb
	// Fill from the DECODED byte, not the raw mean: the shape ships as a byte and RenderFH6
	// decodes it, so the canvas has to hold what the decal will actually show.
	next := backgroundCanvas(model.RGBA{R: model.DecChan(cr), G: model.DecChan(cg), B: model.DecChan(cb)}, w, h)
	_ = r.be.Reset(next)
	applyShapes(r.be, r.shapes[1:])
	g, _, _, _ := r.be.ErrorGrid()
	if e := sumGrid(g); e <= r.finalErr {
		r.initCanvas, r.grid, r.finalErr = next, g, e
		return
	}
	r.shapes[0].Color[0], r.shapes[0].Color[1], r.shapes[0].Color[2] = or, og, ob
	_ = r.be.Reset(r.initCanvas)
	applyShapes(r.be, r.shapes[1:])
	r.grid, _, _, _ = r.be.ErrorGrid()
}
