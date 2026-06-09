//go:build allgpu

package runner

import (
	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cpu"
	"fh6-paint-studio/internal/backend/cuda"
	"fh6-paint-studio/internal/backend/vulkan"
)

// newBackend (allgpu build) auto-selects the GPU backend at runtime: CUDA on NVIDIA (the
// tuned fast path), else Vulkan (cross-vendor: AMD/Intel/NVIDIA + everywhere fh6cuda.dll
// is absent), else the CPU reference. One binary runs on any GPU — ship it with both
// fh6cuda.dll (nvcc) and fh6vk.dll (Vulkan shim) beside the exe.
func newBackend(pixels, weight []float32, w, h, grid int) (backend.Backend, string, error) {
	if be, err := cuda.New(pixels, weight, w, h, grid); err == nil {
		return be, "CUDA", nil
	}
	if be, err := vulkan.New(pixels, weight, w, h, grid); err == nil {
		return be, "Vulkan", nil
	}
	c := cpu.New(pixels, w, h, grid)
	if weight != nil {
		c.SetWeight(weight)
	}
	return c, "CPU", nil
}
