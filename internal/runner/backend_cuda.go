//go:build cuda && !allgpu

package runner

import (
	"fmt"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cuda"
)

// newBackend (CUDA build) constructs the GPU backend. No CPU fallback — the pure-Go reference was
// dropped (CUDA = golden, Vulkan = the port); a failed init is a real error the UI must surface
// (driver/DLL problem), not something to paper over with an unusably slow engine.
func newBackend(pixels, weight []float32, w, h, grid int) (backend.Backend, string, error) {
	be, err := cuda.New(pixels, weight, w, h, grid)
	if err != nil {
		return nil, "", fmt.Errorf("cuda init failed (driver/fh6cuda.dll): %w", err)
	}
	return be, "CUDA", nil
}

// AvailableBackends reports the backends offered by this (CUDA-only) build.
func AvailableBackends() []string { return []string{"CUDA"} }
