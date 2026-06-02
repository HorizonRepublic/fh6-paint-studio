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
type Done struct {
	Result  engine.Result
	Canvas  *image.NRGBA
	Backend string
}

// Failed is the terminal failure event (backend init error or a recovered panic).
type Failed struct{ Err error }

// Log is a human-readable line for the execution log.
type Log struct{ Line string }

// Status names the current post-greedy phase (polish / back-fit / standout / economy)
// so the UI can show what it's doing once the shape counter has hit 100%. Empty clears it.
type Status struct{ Stage string }

func (Progress) isEvent() {}
func (Frame) isEvent()    {}
func (Done) isEvent()     {}
func (Failed) isEvent()   {}
func (Log) isEvent()      {}
func (Status) isEvent()   {}
