//go:build vulkan || cuda

package ipc

import (
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"os"
	"path/filepath"
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
	var logs, progress, frames int
	var lastShapes int
	var frameW, frameH int
	done := make(chan DoneEvent, 1)
	failed := make(chan error, 1)

	out := filepath.Join(dir, "geometry.json")
	ch := preset.DefaultChoices()
	ch.Shapes = 12
	ch.Mode = "flat"
	_, err := c.Generate(GenerateParams{Path: src, MaxRes: 64, Choices: ch, Output: out}, func(u Update) {
		mu.Lock()
		defer mu.Unlock()
		switch u.Kind {
		case "log":
			logs++
		case "progress":
			progress++
			lastShapes = u.Progress.Shapes
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
		if _, err := os.Stat(out); err != nil {
			t.Errorf("geometry not written: %v", err)
		}
	case err := <-failed:
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
