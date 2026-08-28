package engine

import (
	"testing"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/model"
)

// losingBackend is a minimal Backend that reports the device lost from attempt loseAt onwards
// (counted in Reset calls, one per Run). Everything else is inert: Run only has to reach its
// device-loss return for this test to mean something.
type losingBackend struct {
	w, h    int
	target  []float32
	weight  []float32
	grid    []float32
	resets  int
	loseAt  int
	scoreOf float32
}

func (b *losingBackend) Evaluate(c []model.Candidate) ([]backend.EvalResult, error) {
	out := make([]backend.EvalResult, len(c))
	for i := range out {
		out[i] = backend.EvalResult{Score: b.scoreOf, Color: model.RGBA{A: 1}}
	}
	return out, nil
}
func (b *losingBackend) Apply(model.Candidate) error { return nil }
func (b *losingBackend) ErrorGrid() ([]float32, int, int, error) {
	return b.grid, 4, 4, nil
}
func (b *losingBackend) ReadCanvas(dst []float32) error { return nil }
func (b *losingBackend) Reset([]float32) error          { b.resets++; return nil }
func (b *losingBackend) Target() []float32              { return b.target }
func (b *losingBackend) Weight() []float32              { return b.weight }
func (b *losingBackend) Close() error                   { return nil }
func (b *losingBackend) DeviceLost() bool               { return b.loseAt > 0 && b.resets >= b.loseAt }

func TestRunBestSkipsDeviceLostAttempts(t *testing.T) {
	const w, h = 8, 8
	be := &losingBackend{
		w: w, h: h,
		target: make([]float32, w*h*4),
		weight: make([]float32, w*h),
		grid:   []float32{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		loseAt: 3, // attempt 1 completes; the device is gone by the time attempt 2 resets again
	}
	opt := Options{Width: w, Height: h, StopAt: 2, RandomSamples: 4, Seed: 1}
	res := RunBest(be, opt, 3)

	if res.DevErr != nil {
		t.Fatalf("a completed attempt was discarded for a device-lost one: %v", res.DevErr)
	}
	if res.FinalError == 0 || len(res.Shapes) == 0 {
		t.Errorf("winner looks like the device-lost attempt: err=%v shapes=%d", res.FinalError, len(res.Shapes))
	}
}
