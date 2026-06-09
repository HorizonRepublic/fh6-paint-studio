package engine

import (
	"math"
	"time"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/model"
)

// gaussLRScale multiplies the polish learning rates for GenerateGaussian: from-scratch training of a
// grid of glows needs bigger steps than the refinement polish does (where shapes start near-optimal).
// Measured ~4 = stable convergence in linear light without overshoot (lr 16 diverged).
const gaussLRScale = 4.0

// GenerateGaussian reconstructs the target as N soft GLOW splats (the GaussianImage primitive), jointly
// optimised by the differentiable polish. It is a NICHE generator for SMOOTH / gradient / painterly
// content: on a pure gradient it measured 8x lower SSE than the greedy (hard ellipse/rect fills band on
// smoothness), and it renders buttery-smooth — but it LOSES the greedy on fine detail (each glow is a
// blurry blob). So it is a separate mode, not the main path.
//
// NO greedy search and NO densify (measured harmful — every densify run lost to the pure train): just
// grid-init glows (colour = local target mean) -> pure joint Adam-polish on the GPU (PolishWithBackend)
// or CPU (Polish). The output glows are native FH6 KindGlow primitives. The shipped greedy pipeline
// (engine.Run) is entirely untouched — this is an additive parallel path.
func GenerateGaussian(be backend.Backend, opt Options) Result {
	t0 := time.Now()
	w, h := opt.Width, opt.Height
	target, weight := be.Target(), be.Weight()
	bg := opt.Background
	n := opt.StopAt
	if n < 1 {
		n = 1
	}

	glows := gaussInitGlows(target, w, h, n)
	shapes := make([]model.Shape, 0, len(glows)+1)
	if !opt.TransparentBG {
		shapes = append(shapes, gaussBgRect(bg, w, h))
	}
	shapes = append(shapes, glows...)
	initErr := gaussRenderErr(be, shapes, w, h)

	po := opt.PolishOpts
	if po.Iters <= 0 {
		po = DefaultPolishOptions()
	}
	po.EarlyStopMargin = 0 // from-scratch training: run the full iteration budget (no plateau cut)
	po.LRPos *= gaussLRScale
	po.LRRad *= gaussLRScale
	po.LRAng *= gaussLRScale
	po.LRColor *= gaussLRScale
	po.LRAlpha *= gaussLRScale
	// Drive the UI % bar off the TRAINING ITERATIONS (the greedy's shape-count progress is meaningless
	// when all glows train jointly). opt.Progress is the runner's light per-event hook (no frame read).
	// The error is reported as the static initErr ON PURPOSE: a live per-iteration loss would force a
	// device sync every step (PolishLoss is only fetched on the final iter for exactly this reason), so
	// the studio shows the % bar advancing with a fixed baseline error rather than paying that cost.
	if opt.Progress != nil {
		po.OnProgress = func(iter, total int) { opt.Progress(iter, initErr) }
	}
	// Training runs for minutes at a high glow count; a 50ms preview (a full-canvas D2H copy each) would
	// steal real time. 150ms (~7fps) is plenty to watch it converge while keeping the run fast.
	po.PreviewInterval = 150 * time.Millisecond

	if acc, ok := be.(PolishAccel); ok && acc.PolishSupported() {
		shapes = PolishWithBackend(shapes, target, weight, w, h, bg, opt.TransparentBG, po, acc).Shapes
	} else {
		shapes = Polish(shapes, target, weight, w, h, bg, opt.TransparentBG, po).Shapes
	}

	finalErr := gaussRenderErr(be, shapes, w, h)
	return Result{
		Shapes:       shapes,
		InitialError: initErr,
		FinalError:   finalErr,
		Timings:      Timings{Total: time.Since(t0), Polish: time.Since(t0), PolishIters: po.Iters, PolishPre: initErr, PolishPost: finalErr},
	}
}

// gaussInitGlows tiles ~n glows on a square grid, each coloured by its cell's mean target colour and
// sized to overlap its neighbours — a blurry starting reconstruction the joint polish then sharpens.
func gaussInitGlows(target []float32, w, h, n int) []model.Shape {
	g := int(math.Sqrt(float64(n)))
	if g < 1 {
		g = 1
	}
	sx := float64(w) / float64(g)
	sy := float64(h) / float64(g)
	out := make([]model.Shape, 0, g*g)
	for gy := 0; gy < g; gy++ {
		for gx := 0; gx < g; gx++ {
			x0, x1 := int(float64(gx)*sx), int(float64(gx+1)*sx)
			y0, y1 := int(float64(gy)*sy), int(float64(gy+1)*sy)
			var r, gg, b, cnt float64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					p := (y*w + x) * 4
					r += float64(target[p])
					gg += float64(target[p+1])
					b += float64(target[p+2])
					cnt++
				}
			}
			if cnt == 0 {
				continue // degenerate grid cell (w or h < grid): skip rather than emit a black glow
			}
			r, gg, b = r/cnt, gg/cnt, b/cnt
			c := model.Candidate{
				Kind:  model.KindGlow,
				P:     [6]float32{float32((float64(gx) + 0.5) * sx), float32((float64(gy) + 0.5) * sy), float32(sx * 0.9), float32(sy * 0.9), 0, 0},
				Color: model.RGBA{R: float32(r), G: float32(gg), B: float32(b), A: 1},
			}
			out = append(out, c.ToShape(0))
		}
	}
	return out
}

func gaussBgRect(c model.RGBA, w, h int) model.Shape {
	return model.Shape{Type: model.TypeRectangle, Data: []float64{0, 0, float64(w), float64(h)},
		Color: []int{model.EncByte(c.R), model.EncByte(c.G), model.EncByte(c.B), 255}}
}

// gaussRenderErr composites the shapes over a transparent canvas via the backend (the bg rect, if any,
// fills the background) and returns the SSE vs the target — the InitialError/FinalError for the Result.
func gaussRenderErr(be backend.Backend, shapes []model.Shape, w, h int) float64 {
	canvas := make([]float32, w*h*4)
	_ = be.Reset(canvas)
	for _, s := range shapes {
		c := model.Candidate{Kind: model.KindFromType(s.Type), P: model.ParamsFromShape(s)}
		if len(s.Color) >= 4 {
			c.Color = model.RGBA{R: float32(s.Color[0]) / 255, G: float32(s.Color[1]) / 255, B: float32(s.Color[2]) / 255, A: float32(s.Color[3]) / 255}
		}
		_ = be.Apply(c)
	}
	out := make([]float32, w*h*4)
	if be.ReadCanvas(out) != nil {
		return 0
	}
	target := be.Target()
	var sse float64
	for i := range out {
		d := float64(out[i] - target[i])
		sse += d * d
	}
	return sse
}
