//go:build vulkan && !cuda && !allgpu

package main

import (
	"fmt"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/vulkan"
)

// newBackend builds the cross-vendor Vulkan GPU backend (fh6vk.dll). Built with -tags vulkan.
// No CPU fallback — a failed init is a real driver/DLL error, not something to paper over.
func newBackend(pixels, weight []float32, w, h, gridSize int) (backend.Backend, string, error) {
	be, err := vulkan.New(pixels, weight, w, h, gridSize)
	if err != nil {
		return nil, "", fmt.Errorf("vulkan init failed (driver/fh6vk.dll): %w", err)
	}
	return be, "vulkan", nil
}
