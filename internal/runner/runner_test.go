//go:build cuda

package runner_test

import (
	"sync"
	"testing"
	"time"

	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/preset"
	"fh6-paint-studio/internal/runner"
)

func bptr(b bool) *bool { return &b }

func makePrep(w, h int) imageio.Prepared {
	px := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		px[i*4] = float32(i%w) / float32(w)
		px[i*4+1] = float32(i/w) / float32(h)
		px[i*4+2] = 0.5
		px[i*4+3] = 1
	}
	return imageio.Prepared{W: w, H: h, Pixels: px, Background: model.RGBA{R: 0, G: 0, B: 0, A: 1}}
}

func fastChoices(shapes int) preset.Choices {
	c := preset.DefaultChoices()
	c.Mode = "photo"
	c.Quality = "fast"
	c.Shapes = shapes
	c.Polish = bptr(false) // keep the test quick + deterministic
	c.Backfit = bptr(false)
	return c
}

// TestRunAsyncEmitsOrderedEvents: a run produces logs, per-shape progress, throttled live
// frames, and a terminal Done with a final canvas. Frames must be fewer than progress ticks.
func TestRunAsyncEmitsOrderedEvents(t *testing.T) {
	prep := makePrep(48, 48)
	r := preset.Resolve(prep, fastChoices(200))

	var mu sync.Mutex
	var logs, progs, frames int
	var done *runner.Done
	finished := make(chan struct{})

	runner.RunAsync(prep, r, func(e runner.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev := e.(type) {
		case runner.Log:
			logs++
		case runner.Progress:
			progs++
		case runner.Frame:
			frames++
		case runner.Done:
			d := ev
			done = &d
			close(finished)
		case runner.Failed:
			t.Errorf("unexpected Failed: %v", ev.Err)
			close(finished)
		}
	})

	select {
	case <-finished:
	case <-time.After(60 * time.Second):
		t.Fatal("timeout waiting for Done")
	}

	mu.Lock()
	defer mu.Unlock()
	if logs == 0 {
		t.Error("expected log events")
	}
	if progs == 0 {
		t.Error("expected progress events")
	}
	if frames == 0 {
		t.Error("expected at least one live frame")
	}
	if frames >= progs {
		t.Errorf("frames (%d) should be throttled below progress ticks (%d)", frames, progs)
	}
	if done == nil || done.Canvas == nil {
		t.Fatal("expected Done with a final canvas")
	}
	if got := done.Canvas.Bounds().Dx(); got != 48 {
		t.Errorf("final canvas width = %d, want 48", got)
	}
}

// TestRunAsyncCancel: cancelling shortly after the run starts stops it well short of budget.
func TestRunAsyncCancel(t *testing.T) {
	prep := makePrep(48, 48)
	r := preset.Resolve(prep, fastChoices(400))

	prog := make(chan int, 8192)
	resCh := make(chan engine.Result, 1)
	cancel := runner.RunAsync(prep, r, func(e runner.Event) {
		switch ev := e.(type) {
		case runner.Progress:
			select {
			case prog <- ev.Shapes:
			default:
			}
		case runner.Done:
			resCh <- ev.Result
		case runner.Failed:
			t.Errorf("unexpected Failed: %v", ev.Err)
			resCh <- engine.Result{}
		}
	})

	// Cancel from the test goroutine once progress crosses 10 shapes (avoids racing the
	// returned cancel against the worker's first events).
	go func() {
		for n := range prog {
			if n >= 10 {
				cancel()
				return
			}
		}
	}()

	select {
	case res := <-resCh:
		if got := len(res.Shapes); got > 80 {
			t.Errorf("cancel did not stop early: placed %d shapes of a 400 budget", got)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("timeout waiting for cancelled run")
	}
}
