//go:build vulkan && !cuda && !allgpu

package main

import (
	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cpu"
	"fh6-paint-studio/internal/backend/vulkan"
)

// newBackend builds the cross-vendor Vulkan GPU backend (fh6vk.dll). Built with -tags vulkan.
// Falls back to the CPU reference if the device cannot be initialized.
func newBackend(pixels, weight []float32, w, h, gridSize int) (backend.Backend, string, error) {
	be, err := vulkan.New(pixels, weight, w, h, gridSize)
	if err != nil {
		c := cpu.New(pixels, w, h, gridSize)
		if weight != nil {
			c.SetWeight(weight)
		}
		return c, "cpu (vulkan init failed: " + err.Error() + ")", nil
	}
	return be, "vulkan", nil
}
