//go:build vulkan && !cuda && !allgpu

package runner

import (
	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cpu"
	"fh6-paint-studio/internal/backend/vulkan"
)

// newBackend (Vulkan build) constructs the cross-vendor GPU backend, falling back to the
// CPU reference if the device cannot be initialized so the GUI still works everywhere.
func newBackend(pixels, weight []float32, w, h, grid int) (backend.Backend, string, error) {
	be, err := vulkan.New(pixels, weight, w, h, grid)
	if err != nil {
		c := cpu.New(pixels, w, h, grid)
		if weight != nil {
			c.SetWeight(weight)
		}
		return c, "CPU (Vulkan init failed: " + err.Error() + ")", nil
	}
	return be, "Vulkan", nil
}
