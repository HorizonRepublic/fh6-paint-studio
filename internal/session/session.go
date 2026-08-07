// Package session turns a request stated in the user's terms — this file, this region of it, this
// preset — into a finished run.
//
// It exists because the preparation around engine.Run is not incidental: the working image is
// re-decoded at a resolution the mode has been measured to benefit from, optionally wrapped in a
// transparent surround, and a hybrid preset reserves part of the budget for ink lines that are laid
// on afterwards. Every one of those steps has a matching step at the other end — the surround has to
// come back off the geometry and the canvas, the ink has to be composited, the quality score has to
// be taken before the unpad.
//
// That pairing lived in the studio's event loop, so a second client had no way to reproduce a run
// without reproducing the loop. Here it is one object: Prepare decides everything, Start runs it and
// undoes what it did, and the caller sees a finished result in the coordinate system it asked about.
package session

import (
	"errors"
	"fmt"
	"image"

	"fh6-paint-studio/internal/hybrid"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/metric"
	"fh6-paint-studio/internal/preset"
	"fh6-paint-studio/internal/runner"
)

const (
	// DisplayRes is the resolution a client shows the source at, and the fit resolution for the modes
	// that gain nothing from more pixels. Content fills the render up to this long side.
	DisplayRes = 1100

	// FitCap bounds every high-resolution fit. The 2026-07-20 resolution ladder on a 3541px line-art
	// source, scored at native, went 54088 -> 38896 -> 29034 unweighted SSE across caps 2000 -> 2800
	// -> 4096: a clean -46% with no low-view tradeoff, for 155 -> 259s of wall. Sources already below
	// a cap are byte-identical across caps, so raising the ceiling only slows the genuinely large
	// sources, which is exactly where it pays. This number is therefore not a quality tradeoff — it
	// protects time and VRAM from a pathological scan.
	FitCap = 4096

	// PadFrac is the transparent surround "Keep shapes inside image" adds on every side, as a
	// fraction of the long edge. The empty margin makes the spill penalty bound every shape to the
	// content rectangle; the result is mapped back to the original size on completion.
	PadFrac = 0.10
)

// Request is one run, described the way a user asks for it. No pixels: a client names the file and
// the daemon decodes it, because the daemon has to decode it anyway and shipping 20 MB through a
// socket to hand it straight to the decoder buys nothing.
type Request struct {
	Path string

	// Region is an absolute rectangle in the RAW file's coordinates — the crop the user applied. Nil
	// means the whole image, auto-cropped to its content box. It is absolute rather than fractional
	// because a fraction of WHAT is ambiguous once a crop has been composed onto a previous crop, and
	// resolving that ambiguity wrongly returns a plausible reconstruction of the wrong region.
	Region *image.Rectangle

	DisplayRes int  // the resolution the client is showing the source at; 0 means DisplayRes
	SourceRes  bool // fit at the source's own resolution regardless of mode
	KeepInside bool // wrap the target in a transparent surround

	Choices preset.Choices
}

// Run is a prepared request: everything decided, nothing started.
type Run struct {
	// Prep is what the engine fits — the view plus the keep-inside surround when there is one. Ink and
	// the quality score are both taken against it, in its coordinates.
	Prep     imageio.Prepared
	Resolved preset.Resolved

	// PadPx is the surround width; ViewW/ViewH are the dimensions results are mapped back to.
	PadPx        int
	ViewW, ViewH int

	// Ink is the hybrid budget held back from the fill and appended as FDoG lines on completion.
	Ink int

	// Notes are the decisions worth telling the user about, emitted as log lines when the run starts
	// so both drivers narrate a run identically.
	Notes []string
}

// Prepare decodes the working image, applies the fit resolution and the surround, and resolves the
// preset. It does no GPU work, so a caller can prepare a run to inspect it and never start it.
func Prepare(req Request) (*Run, error) {
	if req.Path == "" {
		return nil, errors.New("session: a run needs a source path")
	}
	display := req.DisplayRes
	if display <= 0 {
		display = DisplayRes
	}
	fit := fitRes(req.Choices.Mode, req.SourceRes, display)

	var (
		prep *imageio.Prepared
		err  error
	)
	if req.Region != nil {
		prep, err = imageio.LoadAbsRegion(req.Path, fit, *req.Region)
	} else {
		// Auto-crop uniform margins to the content bbox before the downscale, identical to the CLI's
		// default: content fills the render, so more detail and more shapes land per feature.
		prep, _, err = imageio.LoadAutoCropped(req.Path, fit)
	}
	if err != nil {
		return nil, err
	}

	run := &Run{Prep: *prep, ViewW: prep.W, ViewH: prep.H}
	if fit > display && (prep.W > display || prep.H > display) {
		run.Notes = append(run.Notes, fmt.Sprintf("hi-res fit: engine input %dx%d (display stays at %dpx)", prep.W, prep.H, display))
	}

	// Always pad when asked, never gated on how transparent the image is: content can touch an edge
	// even when the middle is transparent, and a cutout silhouette touches its bbox by construction
	// after the auto-crop. Quality-neutral, because the empty margin draws no shapes.
	if req.KeepInside {
		padded, padPx := imageio.PadTransparent(&run.Prep, PadFrac)
		run.Prep, run.PadPx = *padded, padPx
		run.Notes = append(run.Notes, fmt.Sprintf("keep-inside: transparent surround %dpx (%dx%d), bounding shapes on all edges", padPx, run.Prep.W, run.Prep.H))
	}

	run.Resolved = preset.Resolve(run.Prep, req.Choices)
	if preset.IsHybridMode(req.Choices.Mode) {
		// A client that says nothing about the split gets the mode's own. The Gio studio always sent
		// an explicit ratio, so this never mattered until a client arrived that does not — and the
		// symptom was silent: "Line art" drew its badge, asked for zero ink lines, and produced a
		// plain fill. DefaultInkRatio is 0 for every mode that has no ink, so nothing else moves.
		ratio := req.Choices.InkRatio
		if ratio == 0 {
			ratio = preset.DefaultInkRatio(req.Choices.Mode)
		}
		// Split what the engine may actually place, not the raw request: the background rectangle is
		// already deducted (preset.PlaceBudget) and the ceiling applied, so fill + ink + background
		// lands exactly on the budget instead of one over it.
		budget := preset.PlaceBudget(req.Choices.Shapes)
		if ink := preset.InkBudget(ratio, budget); ink > 0 {
			run.Ink = ink
			run.Resolved.Options.StopAt = budget - ink
			if run.Resolved.Options.StopAt < 1 {
				run.Resolved.Options.StopAt = 1
			}
			run.Notes = append(run.Notes, fmt.Sprintf("%s: %d ink lines + %d fill (%.0f%% lines)",
				req.Choices.Mode, ink, run.Resolved.Options.StopAt, ratio*100))
		}
	}
	return run, nil
}

// fitRes is the engine-side resolution for a mode. Thin strokes at the display resolution degrade to
// about a pixel of gray anti-aliasing the search can neither detect nor cover, so flat and anime fit
// at native; pixel-art must, because any downscale destroys the grid it reproduces exactly; photo
// measured a wash and stays where the client is showing it.
func fitRes(mode string, sourceRes bool, display int) int {
	if sourceRes {
		return FitCap
	}
	switch preset.PresetMode(mode) {
	case "pixel", "flat", "anime":
		return FitCap
	}
	return display
}

// Start runs the prepared request and streams events. Frames arrive with the surround already
// stripped and the terminal Done carries the finished geometry in the view's coordinates, so a
// client never sees the padding it did not ask for. The returned func cancels the run.
func (r *Run) Start(onEvent func(runner.Event)) func() {
	for _, n := range r.Notes {
		onEvent(runner.Log{Line: n})
	}
	return runner.RunAsync(r.Prep, r.Resolved, func(ev runner.Event) {
		switch e := ev.(type) {
		case runner.Frame:
			onEvent(runner.Frame{Img: r.unpad(e.Img)})
		case runner.Done:
			onEvent(r.finish(e, onEvent))
		default:
			onEvent(ev)
		}
	})
}

// finish appends the hybrid ink, scores the render, and maps everything back out of the surround.
// The order matters: the ink goes on before the score so the score describes what the user will see,
// and both happen before the unpad so they run in the coordinates the engine actually fitted.
func (r *Run) finish(e runner.Done, onEvent func(runner.Event)) runner.Done {
	shapes, canvas := e.Result.Shapes, e.Canvas

	if r.Ink > 0 {
		if lines := hybrid.Ink(&r.Prep, r.Ink, false); len(lines) > 0 {
			shapes = append(shapes, lines...)
			canvas = imageio.RenderFH6Image(shapes, r.Prep.HasTransparency, r.Prep.W, r.Prep.H, 2)
			onEvent(runner.Log{Line: fmt.Sprintf("hybrid: +%d FDoG lines on top of the fill", len(lines))})
		}
	}

	var q *runner.Quality
	if canvas != nil && canvas.Bounds().Dx() == r.Prep.W && canvas.Bounds().Dy() == r.Prep.H {
		src := imageio.EncodeForDisplay(r.Prep.Pixels)
		got := nrgbaToFloat(canvas)
		de, _ := metric.DeltaE76(src, got, r.Prep.W, r.Prep.H)
		q = &runner.Quality{DeltaE: de, SSIM: metric.SSIM(src, got, r.Prep.W, r.Prep.H)}
		onEvent(runner.Log{Line: fmt.Sprintf("quality: ΔE76 %.2f · SSIM %.3f", q.DeltaE, q.SSIM)})
	}

	w, h := r.Prep.W, r.Prep.H
	if r.PadPx > 0 {
		shapes = imageio.TranslateShapes(shapes, -float64(r.PadPx), -float64(r.PadPx))
		canvas = imageio.UnpadCanvas(canvas, r.PadPx, r.ViewW, r.ViewH)
		w, h = r.ViewW, r.ViewH
	}

	res := e.Result
	res.Shapes = shapes
	return runner.Done{Result: res, Canvas: canvas, Backend: e.Backend, Width: w, Height: h, Quality: q}
}

func (r *Run) unpad(img *image.NRGBA) *image.NRGBA {
	if r.PadPx <= 0 {
		return img
	}
	return imageio.UnpadCanvas(img, r.PadPx, r.ViewW, r.ViewH)
}

func nrgbaToFloat(img *image.NRGBA) []float32 {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	out := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for x := 0; x < w*4; x++ {
			out[y*w*4+x] = float32(row[x]) / 255
		}
	}
	return out
}
