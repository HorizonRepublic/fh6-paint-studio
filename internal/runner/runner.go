package runner

import (
	"fmt"
	"image"
	"sync/atomic"
	"time"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/pixel"
	"fh6-paint-studio/internal/preset"
)

// BackendPreference biases newBackend toward a named GPU backend ("CUDA"/"Vulkan") on allgpu builds
// where both are compiled in. Empty = the build's default order (CUDA first). Single-backend builds
// ignore it. Set by the studio's engine picker.
var BackendPreference string

// RunAsync builds the backend from the resolved run config and runs engine.Run in a worker
// goroutine. onEvent is called FROM THAT GOROUTINE for every event (Log/Progress/Frame and a
// terminal Done/Failed) — the UI is responsible for marshalling these onto its own loop
// (e.g. via a channel + window.Invalidate). The returned cancel func stops the run early,
// keeping the shapes placed so far; calling it after completion is a no-op.
func RunAsync(prep imageio.Prepared, r preset.Resolved, onEvent func(Event)) (cancel func()) {
	var stop atomic.Bool
	cancel = func() { stop.Store(true) }

	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				onEvent(Failed{Err: fmt.Errorf("panic during generation: %v", rec)})
			}
		}()

		w, h := prep.W, prep.H
		onEvent(Log{Line: fmt.Sprintf("loaded image %dx%d", w, h)})

		// Resolve already produced the exact target the backend should fit (the loaded pixels, or — for
		// MONO mode — a binarized single-colour copy that never mutates the shared source). Reuse it so
		// the target is binarized ONCE, never twice.
		target := r.Target
		if target == nil {
			target = prep.Pixels
		}

		// PIXEL-ART mode: exact reproduction, no engine and no backend (see internal/pixel). The
		// count is defined by the art; a budget overflow surfaces as a friendly Failed message.
		if r.PixelArt {
			pres, perr := pixel.Generate(target, w, h, preset.MaxShapes)
			if perr != nil {
				onEvent(Failed{Err: perr})
				return
			}
			onEvent(Log{Line: fmt.Sprintf("pixel: grid %dx%d (step %d), %d colors -> %d rects",
				pres.GridW, pres.GridH, pres.GridStep, pres.Colors, pres.RectCount)})
			res := engine.Result{Shapes: pres.Shapes}
			onEvent(Progress{Shapes: pres.RectCount, Total: pres.RectCount, Elapsed: 0})
			onEvent(Done{Result: res, Canvas: renderInGame(res.Shapes, true, w, h), Backend: "Pixel"})
			return
		}

		be, name, err := newBackend(target, r.Weight, w, h, r.Grid)
		if err != nil {
			onEvent(Failed{Err: err})
			return
		}
		defer be.Close()
		onEvent(Log{Line: "backend: " + name})
		for _, s := range r.Summary {
			onEvent(Log{Line: s})
		}

		opt := r.Options
		opt.Cancel = stop.Load
		opt.Status = func(stage string) { onEvent(Status{Stage: stage}) }
		total := opt.StopAt
		if total < 1 {
			total = 1
		}

		// Live preview cadence. Each frame does a full-canvas ReadCanvas (D2H + sync) + RGBA
		// convert ON THIS engine goroutine, so it directly steals generation time, and the per-frame
		// cost scales with the canvas resolution. A fixed high rate (the old 25 fps) therefore starves
		// the engine badly at studio resolutions (measured ~3x slower than the raw run). So throttle
		// ADAPTIVELY: space frames at >= measured-cost / previewBudget, floored at minInterval — i.e.
		// the preview may consume at most ~previewBudget of wall time, automatically backing off to a
		// lower fps when each frame is expensive. Progress (cheap counters) still fires every shape, so
		// the bar/error/sparkline stay fully live; only the IMAGE refresh adapts.
		const minInterval = 40 * time.Millisecond
		const previewBudget = 0.12 // preview costs at most ~12% of run time
		start := time.Now()
		var lastFrame time.Time
		var frameCost time.Duration
		refineHinted := false
		postBuild := opt.Polish || opt.BackFit // phases that run after the greedy build with no live frames
		opt.Progress = func(n int, e float64) {
			onEvent(Progress{Shapes: n, Total: total, Err: e, Elapsed: time.Since(start)})
			// Once the greedy build fills the budget, the polish/back-fit refinement runs with
			// no live frames (the preview holds its last frame). Hint it so the pause is not
			// mistaken for a hang.
			if postBuild && !refineHinted && n >= total {
				refineHinted = true
				onEvent(Log{Line: "build complete — joint polish refining (edges sharpen)…"})
			}
			interval := minInterval
			if d := time.Duration(float64(frameCost) / previewBudget); d > interval {
				interval = d
			}
			if time.Since(lastFrame) >= interval {
				t := time.Now()
				img := readCanvas(be, w, h)
				frameCost = time.Since(t)
				if img != nil {
					onEvent(Frame{Img: img})
				}
				lastFrame = time.Now()
			}
		}

		// Animate the polish phase too: stream the (already-computed) device soft render as it
		// sharpens. Pure read, so the polish result is unchanged; only a throttled D2H copy is added.
		// Gaussian mode trains via the same polish loop, so its live preview rides the same hook.
		if opt.Polish || opt.Gaussian {
			opt.PolishOpts.OnPreview = func(render []float32, rw, rh int) {
				onEvent(Frame{Img: floatToNRGBA(render, rw, rh)})
			}
		}

		var res engine.Result
		if opt.Gaussian {
			// NICHE Gaussian mode: no greedy — reconstruct as soft glow splats jointly trained by the
			// polish (engine.GenerateGaussian). The training streams to the live preview via OnPreview.
			// The % bar tracks TRAINING ITERATIONS (not shape count, which is fixed): override the
			// heavy frame-reading Progress with a LIGHT iter-counter (frames already come from OnPreview).
			gTotal := opt.PolishOpts.Iters
			if gTotal < 1 {
				gTotal = 1
			}
			// THROTTLE: OnProgress fires every training iteration (thousands), and the studio's event
			// pump calls w.Invalidate() per event — firing all of them would flood the UI with repaints
			// and visibly slow the run. Emit ~200 evenly-spaced updates + always the final one.
			progStep := gTotal / 200
			if progStep < 1 {
				progStep = 1
			}
			opt.Progress = func(iter int, e float64) {
				if iter != gTotal && iter%progStep != 0 {
					return
				}
				onEvent(Progress{Shapes: iter, Total: gTotal, Err: e, Elapsed: time.Since(start)})
			}
			onEvent(Log{Line: fmt.Sprintf("Gaussian mode: training %d glow splats over %d iters (smooth/gradient content)…", opt.StopAt, gTotal)})
			res = engine.GenerateGaussian(be, opt)
		} else if r.BestOf > 1 {
			res = engine.RunBest(be, opt, r.BestOf)
		} else {
			res = engine.Run(be, opt)
		}
		// Final preview = RenderFH6 of the FINAL SHAPES — the exact in-game-faithful render of what
		// injection places, IDENTICAL to the CLI's WYSIWYG preview. NOT readCanvas: the engine's working
		// canvas composites in the working space (sRGB-byte when not linear) at float precision, so it
		// does NOT match the injected 8-bit shapes composited in LINEAR by the game. That mismatch was
		// the "preview perfect, inject квашня" gap. RenderFH6 closes it: preview == inject == game.
		onEvent(Done{Result: res, Canvas: renderInGame(res.Shapes, opt.TransparentBG, w, h), Backend: name})
	}()

	return cancel
}

// renderInGame produces the in-game-faithful preview from the FINAL shapes via imageio.RenderFH6 —
// the SAME render the CLI's WYSIWYG preview uses and the SAME shapes the injector writes, so the
// studio preview shows EXACTLY what lands in the game (no readCanvas working-space/precision gap).
// RenderFH6 returns sRGB-display floats already, so convert straight (no EncodeForDisplay). ss=1: the
// game rasterizes the geometry itself (hard edges), so a single sample is the honest representation.
func renderInGame(shapes []model.Shape, transparentBG bool, w, h int) *image.NRGBA {
	return imageio.RenderFH6Image(shapes, transparentBG, w, h, 1)
}

// readCanvas copies the backend's current canvas into a fresh straight-alpha NRGBA image
// (matching imageio's preview convention). Returns nil if the read fails.
func readCanvas(be backend.Backend, w, h int) *image.NRGBA {
	buf := make([]float32, w*h*4)
	if err := be.ReadCanvas(buf); err != nil {
		return nil
	}
	return floatToNRGBA(buf, w, h)
}

// floatToNRGBA converts a straight-alpha RGBA float buffer (len w*h*4) to an NRGBA image.
// The buffer is the engine's WORKING canvas, which is LINEAR light in -linear mode, so it must be
// sRGB-encoded before display — otherwise linear values shown as raw bytes look dark/colour-shifted
// (e.g. yellow->orange). EncodeForDisplay is a no-op in sRGB mode and never mutates the input.
func floatToNRGBA(buf []float32, w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	imageio.EncodeDisplayBytes(buf, img.Pix)
	return img
}

func u8(f float32) uint8 {
	if f <= 0 {
		return 0
	}
	if f >= 1 {
		return 255
	}
	return uint8(f*255 + 0.5)
}
