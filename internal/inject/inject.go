// Package inject is the seam to the Forza Horizon livery editor. v1 ships a Stub (the real
// memory injector — auto-locating the signature, writing the 16-bit shape-word typecodes,
// trimming the 3000-template — needs the running game and lands in S5). The GUI talks to the
// Injector interface so the real implementation is a drop-in replacement.
package inject

import (
	"errors"

	"fh6-paint-studio/internal/model"
)

// ErrNotImplemented is returned by the stub injector.
var ErrNotImplemented = errors.New("FH6 injector not implemented yet")

// Injector applies a reconstructed geometry to a target (the running game, a save file, …).
type Injector interface {
	Name() string
	Available() bool
	Inject(shapes []model.Shape, w, h int) error
}

// Stub is the no-op injector used on platforms without a native injector.
type Stub struct{}

func (Stub) Name() string    { return "FH6 injector" }
func (Stub) Available() bool { return false }
func (Stub) Inject([]model.Shape, int, int) error {
	return ErrNotImplemented
}
