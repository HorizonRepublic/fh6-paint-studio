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
	"fh6-paint-studio/internal/preset"
)

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

		be, name, err := newBackend(prep.Pixels, r.Weight, w, h, r.Grid)
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

		// Throttle live frames to a steady cadence so the reconstruction visibly forms on the
		// fly regardless of how fast shapes are placed: at most one frame per frameInterval
		// (~25 fps). Reading the canvas every shape would stall the run (each ReadCanvas is a
		// D2H copy + sync on the engine goroutine). Progress (cheap counters) is still emitted
		// every shape, so the bar/error/sparkline stay fully live.
		const frameInterval = 40 * time.Millisecond // ~25 fps preview cadence
		start := time.Now()
		var lastFrame time.Time
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
			if time.Since(lastFrame) >= frameInterval {
				lastFrame = time.Now()
				if img := readCanvas(be, w, h); img != nil {
					onEvent(Frame{Img: img})
				}
			}
		}

		// Animate the polish phase too: stream the (already-computed) device soft render as it
		// sharpens. Pure read, so the polish result is unchanged; only a throttled D2H copy is added.
		if opt.Polish {
			opt.PolishOpts.OnPreview = func(render []float32, rw, rh int) {
				onEvent(Frame{Img: floatToNRGBA(render, rw, rh)})
			}
		}

		res := engine.Run(be, opt)
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
	buf := imageio.RenderFH6(shapes, transparentBG, w, h, 1)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[i*4+0] = u8(buf[i*4+0])
		img.Pix[i*4+1] = u8(buf[i*4+1])
		img.Pix[i*4+2] = u8(buf[i*4+2])
		img.Pix[i*4+3] = u8(buf[i*4+3])
	}
	return img
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
// (e.g. yellow→orange). EncodeForDisplay is a no-op in sRGB mode and never mutates the input.
func floatToNRGBA(buf []float32, w, h int) *image.NRGBA {
	buf = imageio.EncodeForDisplay(buf)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[i*4+0] = u8(buf[i*4+0])
		img.Pix[i*4+1] = u8(buf[i*4+1])
		img.Pix[i*4+2] = u8(buf[i*4+2])
		img.Pix[i*4+3] = u8(buf[i*4+3])
	}
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
