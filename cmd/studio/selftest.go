package main

import (
	"fmt"
	"image/png"
	"os"
	"strconv"
	"strings"
	"time"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/preset"
	"fh6-paint-studio/internal/runner"
)

// selftest runs a headless end-to-end smoke test (no window).
func selftest() {
	applog.Init("fh6-paint-studio.log")
	defer applog.Close()

	// optional image path arg (anything ending .png/.jpg/.jpeg) — defaults to the super-image.
	path := "testdata/super-image.jpg"
	for _, a := range os.Args[2:] {
		la := strings.ToLower(a)
		if strings.HasSuffix(la, ".png") || strings.HasSuffix(la, ".jpg") || strings.HasSuffix(la, ".jpeg") {
			path = a
		}
	}
	prep, err := imageio.Load(path, 600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "selftest load:", err)
		os.Exit(1)
	}
	c := preset.DefaultChoices()
	c.Shapes = 150
	if len(os.Args) > 2 {
		if n, err := strconv.Atoi(os.Args[2]); err == nil && n > 0 {
			c.Shapes = n
		}
	}
	c.Quality = "fast"
	polishOn := false
	if len(os.Args) > 3 {
		c.Quality = os.Args[3] // a given quality implies a full run (polish on)
		polishOn = true
	}
	c.Polish = &polishOn
	// Optional content-mode override (anime|photo|flat|gaussian) — lets the smoke test exercise the
	// niche gaussian path (preset.resolveGaussian -> runner -> engine.GenerateGaussian) end-to-end.
	for _, a := range os.Args[2:] {
		switch strings.ToLower(a) {
		case "anime", "photo", "flat", "lineart", "anime-ink", "gaussian", "gauss", "smooth", "pixel":
			c.Mode = strings.ToLower(a)
		}
	}
	r := preset.Resolve(*prep, c)

	done := make(chan runner.Done, 1)
	failed := make(chan error, 1)
	frames := 0
	var tFirst, tLast time.Time
	t0 := time.Now()
	runner.RunAsync(*prep, r, func(e runner.Event) {
		switch ev := e.(type) {
		case runner.Log:
			fmt.Println("  ", ev.Line)
		case runner.Progress:
			if ev.Total > 0 && ev.Shapes%250 == 0 {
				fmt.Printf("   progress: %d/%d (%.0f%%)\n", ev.Shapes, ev.Total, 100*float64(ev.Shapes)/float64(ev.Total))
			}
		case runner.Frame:
			frames++ // safe: the worker emits Done after the last Frame; the channel hand-off publishes it
			now := time.Now()
			if frames == 1 {
				tFirst = now
			}
			tLast = now
		case runner.Done:
			done <- ev
		case runner.Failed:
			failed <- ev.Err
		}
	})

	select {
	case d := <-done:
		el := time.Since(t0)
		_ = os.MkdirAll("out", 0o755)
		if err := imageio.WriteGeometry("out/selftest.json", model.Geometry{Shapes: d.Result.Shapes}); err != nil {
			fmt.Fprintln(os.Stderr, "selftest write:", err)
			os.Exit(1)
		}
		if d.Canvas != nil {
			if f, e := os.Create("out/selftest.png"); e == nil {
				_ = png.Encode(f, d.Canvas)
				f.Close()
			}
		}
		live := 0.0
		if frames > 1 && tLast.After(tFirst) {
			live = float64(frames-1) / tLast.Sub(tFirst).Seconds()
		}
		polish := d.Result.Timings.Polish.Seconds()
		fmt.Printf("OK selftest: %d shapes, error %.1f, backend %s\n",
			len(d.Result.Shapes)-1, d.Result.FinalError, d.Backend)
		fmt.Printf("   total %.1fs | %d preview frames at %.1f fps (build+polish) | polish phase %.1fs\n",
			el.Seconds(), frames, live, polish)
	case err := <-failed:
		fmt.Fprintln(os.Stderr, "selftest failed:", err)
		os.Exit(1)
	case <-time.After(180 * time.Second):
		fmt.Fprintln(os.Stderr, "selftest timeout")
		os.Exit(1)
	}
}
