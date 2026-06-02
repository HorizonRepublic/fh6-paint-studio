//go:build !cuda

package runner

import (
	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cpu"
)

// newBackend (CPU build) constructs the pure-Go reference backend.
func newBackend(pixels, weight []float32, w, h, grid int) (backend.Backend, string, error) {
	be := cpu.New(pixels, w, h, grid)
	if weight != nil {
		be.SetWeight(weight)
	}
	return be, "CPU", nil
}
