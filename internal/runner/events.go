// Package runner drives the reconstruction engine off the UI goroutine. RunAsync builds a
// backend (CUDA when built -tags cuda, else CPU), runs engine.Run in a worker goroutine, and
// streams typed Events back via a callback: log lines, throttled progress + live preview
// frames, and a terminal Done or Failed. A returned cancel func stops the run cooperatively.
package runner

import (
	"image"
	"time"

	"fh6-paint-studio/internal/engine"
)

// Event is one of the concrete event types below (a closed sum type).
type Event interface{ isEvent() }

// Progress is emitted after every placed shape: counts, current error, elapsed time.
type Progress struct {
	Shapes, Total int
	Err           float64
	Elapsed       time.Duration
}

// Frame is a throttled live snapshot of the reconstruction canvas (straight-alpha NRGBA).
type Frame struct{ Img *image.NRGBA }

// Done is the terminal success event: the result, the final canvas, and the backend used.
//
// Width/Height and Quality are filled by internal/session, which knows the coordinate system the
// caller asked about — the engine's own dimensions include the keep-inside surround, and the score
// has to be taken before that surround comes off. RunAsync leaves them zero and nil.
type Done struct {
	Result  engine.Result
	Canvas  *image.NRGBA
	Backend string

	Width, Height int
	Quality       *Quality
}

// Quality is the perceptual score of the finished render against the source it was fitted to.
type Quality struct{ DeltaE, SSIM float64 }

// Failed is the terminal failure event (backend init error or a recovered panic).
type Failed struct{ Err error }

// Log is a human-readable line for the execution log.
type Log struct{ Line string }

// Phase is run-wide progress with a time estimate that spans EVERY phase, not just shape
// placement. The shape counter can only describe the greedy loop; this one keeps counting through
// the polish and the post-passes, which is most of the time the old bar spent frozen at 100%.
type Phase struct {
	Name      string
	PhaseFrac float64
	Overall   float64
	ETA       time.Duration
}

// Status names the current post-greedy phase (polish / back-fit / standout)
// so the UI can show what it's doing once the shape counter has hit 100%. Empty clears it.
type Status struct{ Stage string }

func (Progress) isEvent() {}
func (Frame) isEvent()    {}
func (Done) isEvent()     {}
func (Failed) isEvent()   {}
func (Log) isEvent()      {}
func (Status) isEvent()   {}
func (Phase) isEvent()    {}
