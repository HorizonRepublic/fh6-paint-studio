package backend

import "fh6-paint-studio/internal/model"

// EvalResult is the score (weighted ΔSSE; negative = improvement) and the
// backend-computed optimal color for one candidate.
type EvalResult struct {
	Score float32
	Color model.RGBA
}

// Backend abstracts the hardware that scores, applies, and measures shapes.
type Backend interface {
	Evaluate(cands []model.Candidate) ([]EvalResult, error)
	Apply(c model.Candidate) error
	ErrorGrid() (grid []float32, gw, gh int, err error)
	ReadCanvas(dst []float32) error
	Reset(canvas []float32) error
	// Target returns the read-only target image (RGBA float, len w*h*4).
	Target() []float32
	// Weight returns the read-only per-pixel importance map (len w*h).
	Weight() []float32
	Close() error
}
