package ipc

import (
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/preset"
)

// TestGenerateOverTheWire is the gate for the whole boundary: a real run, driven entirely through
// the protocol, with the client seeing the same sequence a UI needs — log lines, progress that
// advances, at least one preview frame, and a terminal done carrying the shape count.
//
// It runs the ENGINE rather than a stub on purpose. The parts most likely to break here are the ones
// a stub cannot exercise: a preview frame is millions of bytes and must survive framing intact, and
// the events arrive from a worker goroutine while the client is reading, which is exactly the race
// the write mutex exists for.
func TestGenerateOverTheWire(t *testing.T) {
	defer func(prev bool) { model.LinearLight = prev }(model.LinearLight)
	model.LinearLight = true

	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	writeTestImage(t, src, 64, 48)

	cli, srv := net.Pipe()
	server := NewServer(srv, srv)
	go func() { _ = server.Serve() }()

	c := NewClient(cli, cli)
	go func() { _ = c.Listen() }()

	var mu sync.Mutex
	var logs, progress, frames, phases int
	var lastShapes int
	var lastOverall float64
	var sawETA, overallWentBack bool
	var frameW, frameH int
	done := make(chan DoneEvent, 1)
	failed := make(chan error, 1)

	out := filepath.Join(dir, "geometry.json")
	ch := preset.DefaultChoices()
	ch.Shapes = 12
	ch.Mode = "flat"
	_, err := c.Generate(GenerateParams{Path: src, DisplayRes: 64, Choices: ch, Output: out}, func(u Update) {
		mu.Lock()
		defer mu.Unlock()
		switch u.Kind {
		case "log":
			logs++
		case "progress":
			progress++
			lastShapes = u.Progress.Shapes
		case "phase":
			phases++
			if u.Phase.Overall < lastOverall {
				overallWentBack = true
			}
			lastOverall = u.Phase.Overall
			if u.Phase.EtaMs > 0 {
				sawETA = true
			}
		case "frame":
			frames++
			frameW, frameH = u.Frame.Bounds().Dx(), u.Frame.Bounds().Dy()
		case "done":
			done <- u.Done
		case "failed":
			failed <- u.Err
		}
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	select {
	case d := <-done:
		if d.ShapeCount <= 0 {
			t.Errorf("done reported %d shapes", d.ShapeCount)
		}
		// The shapes themselves must arrive inline: everything a UI does after a run — editor,
		// export, injection — needs them, and a path alone would tie the client to the daemon's disk.
		if d.Geometry == nil || len(d.Geometry.Shapes) != d.ShapeCount+1 {
			t.Errorf("done carried no usable geometry: %+v", d.Geometry)
		}
		if d.GeometryPath != out {
			t.Errorf("done geometry path = %q, want %q", d.GeometryPath, out)
		}
		// The dimensions the geometry is expressed in have to travel with it: a client that has to
		// re-derive them from the source file gets a cropped or high-resolution run wrong.
		if d.Width <= 0 || d.Height <= 0 {
			t.Errorf("done carried dimensions %dx%d", d.Width, d.Height)
		}
		if _, err := os.Stat(out); err != nil {
			t.Errorf("geometry not written: %v", err)
		}
		// Run-wide progress has to reach the client, or its countdown goes quiet the moment shape
		// placement ends — which is most of a real run.
		mu.Lock()
		p, ov, back, eta := phases, lastOverall, overallWentBack, sawETA
		mu.Unlock()
		if p == 0 {
			t.Error("no phase events arrived over the wire")
		}
		if back {
			t.Error("overall progress went backwards")
		}
		if ov <= 0 || ov > 1.0001 {
			t.Errorf("final overall progress = %.4f, want (0,1]", ov)
		}
		_ = eta // a 12-shape run can finish before the estimate's warm-up elapses
	case err := <-failed:
		if err != nil && strings.Contains(err.Error(), "vulkan init failed") {
			t.Skipf("vulkan unavailable: %v", err) // CI box without a device/DLL — same convention as the engine suite
		}
		t.Fatalf("run failed over the wire: %v", err)
	case <-time.After(4 * time.Minute):
		t.Fatal("timed out waiting for the run to finish")
	}

	mu.Lock()
	defer mu.Unlock()
	if logs == 0 {
		t.Error("no log lines reached the client")
	}
	if progress == 0 || lastShapes == 0 {
		t.Errorf("progress did not advance: %d events, last shape count %d", progress, lastShapes)
	}
	if frames == 0 {
		t.Error("no preview frame reached the client — the binary path is what this test exists for")
	}
	if frameW == 0 || frameH == 0 {
		t.Errorf("frame arrived with dimensions %dx%d", frameW, frameH)
	}
}

// TestRegionAndSurroundSurviveTheWire pins the two things a client cannot verify for itself.
//
// A REGION is an absolute rectangle, and a service that ignored it would return a completely
// convincing reconstruction of the wrong part of the picture — the failure gives no signal at all.
// The keep-inside SURROUND is the mirror case: the engine fits a larger, padded image, and if the
// padding is not taken back off the geometry, every shape is offset by the border width.
//
// Both are asserted through the dimensions the run reports, because those are what a client uses to
// export and inject: they must describe the region the user asked for, with no surround in them.
func TestRegionAndSurroundSurviveTheWire(t *testing.T) {
	defer func(prev bool) { model.LinearLight = prev }(model.LinearLight)
	model.LinearLight = true

	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	writeTestImage(t, src, 64, 48)

	cli, srv := net.Pipe()
	go func() { _ = NewServer(srv, srv).Serve() }()
	c := NewClient(cli, cli)
	go func() { _ = c.Listen() }()

	ch := preset.DefaultChoices()
	ch.Shapes = 8
	ch.Mode = "flat"

	done := make(chan DoneEvent, 1)
	failed := make(chan error, 1)
	_, err := c.Generate(GenerateParams{
		Path:       src,
		Region:     &[4]int{16, 8, 32, 32},
		DisplayRes: 32,
		KeepInside: true,
		Choices:    ch,
	}, func(u Update) {
		switch u.Kind {
		case "done":
			done <- u.Done
		case "failed":
			failed <- u.Err
		}
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	select {
	case d := <-done:
		if d.Width != 32 || d.Height != 32 {
			t.Errorf("run reported %dx%d, want the requested region's 32x32 with no surround left in it",
				d.Width, d.Height)
		}
		if d.Geometry == nil || len(d.Geometry.Shapes) == 0 {
			t.Fatal("done carried no geometry")
		}
		// Shape 0 is the base rectangle covering the whole fitted canvas, surround included. Once the
		// surround is subtracted it starts OUTSIDE the region, at minus the border width; left at zero
		// it would mean every shape in the document is offset by that border.
		if x := d.Geometry.Shapes[0].Data[0]; x >= 0 {
			t.Errorf("base rect starts at x=%.1f, want a negative origin — the surround was not taken back off", x)
		}
	case err := <-failed:
		if err != nil && strings.Contains(err.Error(), "vulkan init failed") {
			t.Skipf("vulkan unavailable: %v", err) // CI box without a device/DLL — same convention as the engine suite
		}
		t.Fatalf("run failed: %v", err)
	case <-time.After(4 * time.Minute):
		t.Fatal("timed out")
	}
}

// TestConnectionLossFailsPendingRuns: if the daemon dies mid-run, a UI must be told. Left waiting,
// it would show a progress bar that never moves and give the user no way to understand why.
func TestConnectionLossFailsPendingRuns(t *testing.T) {
	cli, srv := net.Pipe()
	c := NewClient(cli, cli)
	listenDone := make(chan struct{})
	go func() { _ = c.Listen(); close(listenDone) }()

	failed := make(chan error, 1)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	writeTestImage(t, src, 8, 8)
	// The server side is never served; the request goes into the pipe and nothing answers.
	go func() { _, _ = io.Copy(io.Discard, srv) }()
	if _, err := c.Generate(GenerateParams{Path: src, Choices: preset.DefaultChoices()}, func(u Update) {
		if u.Kind == "failed" {
			failed <- u.Err
		}
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	srv.Close()
	select {
	case err := <-failed:
		if err == nil {
			t.Error("connection loss reported a nil error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a dropped connection left the run pending forever")
	}
	<-listenDone
}

// TestKnobDefaultsOverTheWire checks the expert panel's contract: without an image the values are
// the mode's generic concrete defaults, and with one the image-dependent branches (flat's palette
// split) are taken from the actual file. Pure CPU — no device needed.
func TestKnobDefaultsOverTheWire(t *testing.T) {
	cli, srv := net.Pipe()
	server := NewServer(srv, srv)
	go func() { _ = server.Serve() }()
	c := NewClient(cli, cli)
	go func() { _ = c.Listen() }()
	defer cli.Close()

	anime, err := c.KnobDefaults("anime", "")
	if err != nil {
		t.Fatalf("knobDefaults(anime): %v", err)
	}
	// AlphaMin crosses a float32 on its way here, so compare with slack.
	if anime.PolishIters != 200 || anime.AlphaMin < 0.29 || anime.AlphaMin > 0.31 || anime.Random <= 0 {
		t.Fatalf("anime defaults look wrong: iters=%d alphaMin=%v random=%d",
			anime.PolishIters, anime.AlphaMin, anime.Random)
	}
	if anime.Alpha == nil || !*anime.Alpha {
		t.Fatal("anime without a cutout should allow alpha")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	writeTestImage(t, src, 64, 48) // three flat colours: the vector-flat palette branch
	flat, err := c.KnobDefaults("flat", src)
	if err != nil {
		t.Fatalf("knobDefaults(flat): %v", err)
	}
	if flat.PolishIters != 600 {
		t.Fatalf("flat on a 3-colour image should take the vector branch (600 iters), got %d", flat.PolishIters)
	}

	// A missing file must degrade to the generic answer, not an error: the panel may hold a path
	// the user has since deleted.
	if _, err := c.KnobDefaults("photo", filepath.Join(dir, "gone.png")); err != nil {
		t.Fatalf("knobDefaults with a dead path: %v", err)
	}
}

func writeTestImage(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Two flat blocks and a diagonal, so the engine has something to fit and the auto-crop
			// does not trim the frame to nothing.
			c := color.NRGBA{R: 30, G: 60, B: 120, A: 255}
			if x > w/2 {
				c = color.NRGBA{R: 220, G: 200, B: 40, A: 255}
			}
			if x == y {
				c = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test image: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode test image: %v", err)
	}
}
