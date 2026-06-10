package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
	presetpkg "fh6-paint-studio/internal/preset"
)

// scoreFH6 renders a geometry JSON the way the game composites it (LINEAR light) and reports its SSE
// vs the sRGB target — the in-game fidelity metric (measures the semi-transparent "pop"). The target
// is loaded sRGB regardless of -linear: the comparison lives in sRGB display space, the same as what
// the eye / the game's display shows.
func scoreFH6(jsonPath, inputPath string, maxRes, ss int, previewPath string) {
	model.LinearLight = false // target compared in sRGB display space
	prep, err := imageio.Load(inputPath, maxRes)
	must(err)
	g, err := imageio.ReadGeometry(jsonPath)
	must(err)
	if len(g.Shapes) > 0 && len(g.Shapes[0].Data) >= 4 {
		jw, jh := int(g.Shapes[0].Data[2]), int(g.Shapes[0].Data[3])
		if jw != prep.W || jh != prep.H {
			m := jw
			if jh > m {
				m = jh
			}
			fmt.Printf("WARNING: JSON canvas %dx%d != target %dx%d — re-run with -max-res %d for alignment\n", jw, jh, prep.W, prep.H, m)
		}
	}
	rendered := imageio.RenderFH6(g.Shapes, prep.HasTransparency, prep.W, prep.H, ss)
	var sse float64
	n := prep.W * prep.H * 4
	for i := 0; i < n; i++ {
		d := float64(rendered[i] - prep.Pixels[i])
		sse += d * d
	}
	pp := sse / float64(n)
	applog.Printf("fh6-score %s: in-game(linear) SSE %.1f per-pixel %.6f (%dx%d ss=%d)", jsonPath, sse, pp, prep.W, prep.H, ss)
	fmt.Printf("fh6-score: in-game-SSE %.1f  per-pixel %.6f  (%dx%d ss=%d)\n", sse, pp, prep.W, prep.H, ss)
	if previewPath != "" {
		must(ensureDir(previewPath))
		must(imageio.SavePreview(previewPath, rendered, prep.W, prep.H))
	}
}

// compareImages diffs two same-size PNGs pixel-for-pixel: reports image-space SSE +
// per-pixel error and writes a red difference HEATMAP (bright where the two disagree, so
// contour deviations glow) to previewPath. It compares finished renders, so it is immune
// to renderer/type-id/ellipse-convention differences.
func compareImages(aPath, bPath, previewPath string, blur int) {
	a, aw, ah, err := imageio.LoadRGBAFloat(aPath)
	must(err)
	b, bw, bh, err := imageio.LoadRGBAFloat(bPath)
	must(err)
	if aw != bw || ah != bh {
		fmt.Fprintf(os.Stderr, "img-vs size mismatch: %dx%d vs %dx%d\n", aw, ah, bw, bh)
		os.Exit(2)
	}
	for ; blur > 0; blur-- {
		b = boxBlur3(b, aw, ah) // soften the compared image (AA hypothesis test)
	}
	heat := make([]float32, aw*ah*4)
	var sse float64
	for i := 0; i < aw*ah; i++ {
		var e float64
		for c := 0; c < 3; c++ {
			d := float64(a[i*4+c] - b[i*4+c])
			e += d * d
		}
		sse += e
		v := float32(math.Min(1, math.Sqrt(e/3)*2.2)) // amplify so contour deviations are visible
		heat[i*4+0], heat[i*4+1], heat[i*4+2], heat[i*4+3] = v, v*0.15, 0, 1
	}
	pp := sse / float64(aw*ah*3)
	applog.Printf("img-vs %s vs %s: SSE %.1f per-pixel %.6f (%dx%d)", aPath, bPath, sse, pp, aw, ah)
	fmt.Printf("img-vs: SSE %.1f  per-pixel %.6f  (%dx%d)\n", sse, pp, aw, ah)
	if previewPath != "" {
		must(ensureDir(previewPath))
		must(imageio.SavePreview(previewPath, heat, aw, ah))
	}
}

// boxBlur3 applies one edge-clamped 3x3 box blur over RGBA. Used by -img-blur to test
// the anti-aliasing hypothesis: does softening our hard 1px edges match the (AA) target?
func boxBlur3(px []float32, w, h int) []float32 {
	out := make([]float32, len(px))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s [4]float32
			var n float32
			for dy := -1; dy <= 1; dy++ {
				yy := y + dy
				if yy < 0 || yy >= h {
					continue
				}
				for dx := -1; dx <= 1; dx++ {
					xx := x + dx
					if xx < 0 || xx >= w {
						continue
					}
					q := (yy*w + xx) * 4
					s[0] += px[q]
					s[1] += px[q+1]
					s[2] += px[q+2]
					s[3] += px[q+3]
					n++
				}
			}
			p := (y*w + x) * 4
			out[p] = s[0] / n
			out[p+1] = s[1] / n
			out[p+2] = s[2] / n
			out[p+3] = s[3] / n
		}
	}
	return out
}

// scoreGeometryJSON renders an external geometry JSON through our backend and reports
// its unweighted SSE vs the target — an apples-to-apples comparison on the SAME renderer +
// SAME metric. Shape coords are absolute px at the JSON's generation resolution, so the
// caller must set -max-res so our canvas matches (we warn on mismatch).
func scoreGeometryJSON(be backend.Backend, jsonPath string, target []float32, w, h int, previewPath string) {
	g, err := imageio.ReadGeometry(jsonPath)
	must(err)
	if len(g.Shapes) > 0 && len(g.Shapes[0].Data) >= 4 {
		jw, jh := int(g.Shapes[0].Data[2]), int(g.Shapes[0].Data[3])
		if jw != w || jh != h {
			m := jw
			if jh > m {
				m = jh
			}
			applog.Printf("WARNING: JSON canvas %dx%d != render %dx%d — shapes misalign; re-run with -max-res %d", jw, jh, w, h, m)
			fmt.Printf("WARNING: JSON canvas %dx%d != render %dx%d — re-run with -max-res %d for alignment\n", jw, jh, w, h, m)
		}
	}
	// Clean transparent canvas; the JSON's first (full-canvas bg) shape fills it for
	// opaque sources, or leaves it transparent (alpha 0) for cutouts.
	must(be.Reset(make([]float32, w*h*4)))
	n := 0
	for _, s := range g.Shapes {
		var c model.Candidate
		if s.Type == 1 && len(s.Data) >= 4 {
			// type-1 = axis-aligned (background) rectangle, data = [x,y,w,h]. KindFromType
			// maps it to ellipse (our engine never renders it — the canvas is pre-filled),
			// so render it here as a real rect, else the bg fill becomes a giant ellipse.
			x, y, ww, hh := s.Data[0], s.Data[1], s.Data[2], s.Data[3]
			c = model.Candidate{Kind: model.KindRectangle, P: [6]float32{float32(x + ww/2), float32(y + hh/2), float32(ww / 2), float32(hh / 2), 0, 0}}
		} else {
			c = model.Candidate{Kind: model.KindFromType(s.Type), P: model.ParamsFromShape(s)}
		}
		if len(s.Color) >= 4 {
			c.Color = model.RGBA{R: float32(s.Color[0]) / 255, G: float32(s.Color[1]) / 255, B: float32(s.Color[2]) / 255, A: float32(s.Color[3]) / 255}
		} else {
			c.Color.A = 1
		}
		must(be.Apply(c))
		n++
	}
	canvas := make([]float32, w*h*4)
	must(be.ReadCanvas(canvas))
	var sse float64
	for i := range canvas {
		d := float64(canvas[i] - target[i])
		sse += d * d
	}
	pp := sse / float64(w*h*4)
	applog.Printf("score-json %s: %d shapes, unweighted SSE %.1f, per-pixel %.6f (%dx%d)", jsonPath, n, sse, pp, w, h)
	fmt.Printf("score-json: %d shapes  unweighted-SSE %.1f  per-pixel %.6f  (%dx%d)\n", n, sse, pp, w, h)
	if previewPath != "" {
		must(ensureDir(previewPath))
		must(imageio.SavePreview(previewPath, canvas, w, h))
		applog.Printf("wrote preview %s", previewPath)
	}
}

// polishGeometryJSON loads a saved greedy geometry JSON and runs ONLY the gated joint polish
// on it (the current -polish-* opts) against the backend's target, then saves the polished
// geometry + WYSIWYG preview and reports the hard error. Because the greedy input is FIXED,
// re-running with a different polish setting isolates its effect exactly — and it is faster
// than a full greedy+polish run.
func polishGeometryJSON(be backend.Backend, jsonPath string, prep *imageio.Prepared, opt engine.Options, outPath, previewPath string, ss int) {
	g, err := imageio.ReadGeometry(jsonPath)
	must(err)
	if len(g.Shapes) > 0 && len(g.Shapes[0].Data) >= 4 {
		jw, jh := int(g.Shapes[0].Data[2]), int(g.Shapes[0].Data[3])
		if jw != prep.W || jh != prep.H {
			m := jw
			if jh > m {
				m = jh
			}
			fmt.Printf("WARNING: JSON canvas %dx%d != target %dx%d — re-run with -max-res %d for alignment\n", jw, jh, prep.W, prep.H, m)
		}
	}
	t0 := time.Now()
	out, hardErr, tm := engine.PolishGeometry(be, g.Shapes, opt, prep.W, prep.H)
	dt := time.Since(t0)
	applog.Printf("polish-json %s: %d shapes, hard-err %.1f, polish %.2fs (iters=%d soft %.1f->%.1f)",
		jsonPath, len(out)-1, hardErr, dt.Seconds(), tm.PolishIters, tm.PolishPre, tm.PolishPost)
	fmt.Printf("polish-json: %d shapes  hard-err %.1f  %.2fs  (iters=%d soft %.1f->%.1f)\n",
		len(out)-1, hardErr, dt.Seconds(), tm.PolishIters, tm.PolishPre, tm.PolishPost)
	if outPath != "" {
		must(ensureDir(outPath))
		must(imageio.WriteGeometry(outPath, model.Geometry{Shapes: out}))
	}
	if previewPath != "" {
		must(ensureDir(previewPath))
		canvas := imageio.RenderFH6(out, prep.HasTransparency, prep.W, prep.H, ss)
		must(imageio.SavePreview(previewPath, canvas, prep.W, prep.H))
	}
}

// polishOpts builds polish config from the iteration count (other knobs default).
func polishOpts(iters int, tau0, tau1 float64, ste, early, oklab bool, feLambda float64) engine.PolishOptions {
	o := engine.DefaultPolishOptions()
	if iters > 0 {
		o.Iters = iters
	}
	if tau0 > 0 {
		o.Tau0 = tau0
	}
	if tau1 > 0 {
		o.Tau1 = tau1
	}
	o.STE = ste
	o.OKLab = oklab
	o.FalseEdgeLambda = feLambda
	if !early {
		o.EarlyStopMargin = 0 // run the full Iters (no plateau early-stop)
	}
	return o
}

// logTimings writes the per-phase wall-clock breakdown (and % of total) so each
// snapshot shows where time goes — host candidate-gen vs GPU eval vs post-process.
func logTimings(t engine.Timings) {
	total := t.Total.Seconds()
	if total <= 0 {
		return
	}
	pct := func(d time.Duration) float64 { return 100 * d.Seconds() / total }
	applog.Printf("profile (total %.2fs): setup %.2fs(%.0f%%) generate %.2fs(%.0f%%) evaluate %.2fs(%.0f%%) mutate %.2fs(%.0f%%) apply %.2fs(%.0f%%) errorgrid %.2fs(%.0f%%) sampler %.2fs(%.0f%%) postprocess %.2fs(%.0f%%)",
		total,
		t.Setup.Seconds(), pct(t.Setup),
		t.Generate.Seconds(), pct(t.Generate),
		t.Evaluate.Seconds(), pct(t.Evaluate),
		t.Mutate.Seconds(), pct(t.Mutate),
		t.Apply.Seconds(), pct(t.Apply),
		t.ErrorGrid.Seconds(), pct(t.ErrorGrid),
		t.Sampler.Seconds(), pct(t.Sampler),
		t.PostProcess.Seconds(), pct(t.PostProcess))
	if t.BackFit > 0 {
		if t.BackFitBase > 0 {
			kept := math.Min(t.BackFitBase, t.BackFitTrial)
			applog.Printf("backfit: %.2fs(%.0f%%) branchA(polish-only) %.1f  branchB(backfit) %.1f  -> kept %.1f (gain %.3f%%)",
				t.BackFit.Seconds(), pct(t.BackFit), t.BackFitBase, t.BackFitTrial, kept, 100*(t.BackFitBase-kept)/t.BackFitBase)
		} else {
			applog.Printf("backfit: %.2fs(%.0f%%)", t.BackFit.Seconds(), pct(t.BackFit))
		}
	}
	if t.Polish > 0 {
		applog.Printf("polish: %.2fs(%.0f%%) iters=%d soft-loss %.1f -> %.1f", t.Polish.Seconds(), pct(t.Polish), t.PolishIters, t.PolishPre, t.PolishPost)
		ph := t.PolishPhases
		applog.Printf("  polish phases: upload %.2fs forward %.2fs loss %.2fs backward %.2fs readgrad %.2fs adam %.2fs hardloss %.2fs",
			ph[0].Seconds(), ph[1].Seconds(), ph[2].Seconds(), ph[3].Seconds(), ph[4].Seconds(), ph[5].Seconds(), ph[6].Seconds())
	}
}

func must(err error) {
	if err != nil {
		applog.Printf("FATAL: %v", err)
		applog.Close()
		os.Exit(1)
	}
}

// applyPreset sets -random/-mutated/-sample-budget/-max-no-improve from a named
// quality preset, unless the user passed those flags explicitly (explicit flags win).
//
// The headline quality lever is candidate VOLUME per shape (more random candidates +
// full budget placed). Because our candidate centres are importance-sampled (error grid)
// and orientation-seeded, far fewer candidates find a good placement than blind random
// would need, so the presets start lower and scale up. sampleBudget is the
// progressive-sampling pixel cap (0 -> backend default 4000; 1<<30 -> full-res).
func applyPreset(name string, random, mutated, sampleBudget, maxNoImprove *int) {
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	r, m, sb, ni := presetpkg.PresetCounts(name) // single source of truth (shared with the studio)
	if !set["random"] {
		*random = r
	}
	if !set["mutated"] {
		*mutated = m
	}
	if !set["sample-budget"] {
		*sampleBudget = sb
	}
	if !set["max-no-improve"] {
		*maxNoImprove = ni
	}
}

func ensureDir(file string) error {
	d := filepath.Dir(file)
	if d == "" || d == "." {
		return nil
	}
	return os.MkdirAll(d, 0o755)
}

// parseRegion parses a "fx,fy,fw,fh" fractional-rect string for -region.
func parseRegion(s string) (fx, fy, fw, fh float64, err error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return 0, 0, 0, 0, fmt.Errorf("-region needs fx,fy,fw,fh (4 comma-separated fractions), got %q", s)
	}
	v := make([]float64, 4)
	for i, p := range parts {
		v[i], err = strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("-region value %q: %v", p, err)
		}
	}
	return v[0], v[1], v[2], v[3], nil
}
