//go:build vulkan && !cuda && !allgpu

package runner

import (
	"fmt"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/vulkan"
)

// newBackend (Vulkan build) constructs the cross-vendor GPU backend. No CPU fallback — the pure-Go
// reference was dropped (CUDA = golden, Vulkan = the port); a failed init is a real error the UI
// must surface (driver/DLL problem), not something to paper over with an unusably slow engine.
func newBackend(pixels, weight []float32, w, h, grid int) (backend.Backend, string, error) {
	be, err := vulkan.New(pixels, weight, w, h, grid)
	if err != nil {
		return nil, "", fmt.Errorf("vulkan init failed (driver/fh6vk.dll): %w", err)
	}
	return be, "Vulkan", nil
}

// AvailableBackends reports the backends offered by this (Vulkan-only) build.
func AvailableBackends() []string { return []string{"Vulkan"} }
