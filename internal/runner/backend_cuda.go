//go:build cuda && !allgpu

package runner

import (
	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cpu"
	"fh6-paint-studio/internal/backend/cuda"
)

// newBackend (CUDA build) constructs the GPU backend, falling back to the CPU reference if
// the device cannot be initialized so the GUI still works on machines without a usable GPU.
func newBackend(pixels, weight []float32, w, h, grid int) (backend.Backend, string, error) {
	be, err := cuda.New(pixels, weight, w, h, grid)
	if err != nil {
		c := cpu.New(pixels, w, h, grid)
		if weight != nil {
			c.SetWeight(weight)
		}
		return c, "CPU (CUDA init failed: " + err.Error() + ")", nil
	}
	return be, "CUDA", nil
}
