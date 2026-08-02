//go:build allgpu

package runner

import (
	"errors"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cuda"
	"fh6-paint-studio/internal/backend/vulkan"
)

// newBackend (allgpu build) constructs the chosen GPU backend at runtime: CUDA on NVIDIA (the tuned
// fast path) and Vulkan everywhere else, ordered by BackendPreference so the studio's engine picker
// can override the default. A failed init falls through to the next; if neither GPU initialises the
// error surfaces to the UI (the pure-Go CPU fallback was dropped — CUDA = golden, Vulkan = the
// port). Ship with both fh6cuda.dll (nvcc) and fh6vk.dll (Vulkan shim).
func newBackend(pixels, weight []float32, w, h, grid int) (backend.Backend, string, error) {
	try := func(which string) (backend.Backend, string, bool) {
		switch which {
		case "CUDA":
			if be, err := cuda.New(pixels, weight, w, h, grid); err == nil {
				return be, "CUDA", true
			}
		case "Vulkan":
			if be, err := vulkan.New(pixels, weight, w, h, grid); err == nil {
				return be, "Vulkan", true
			}
		}
		return nil, "", false
	}
	order := []string{"CUDA", "Vulkan"}
	if BackendPreference == "Vulkan" {
		order = []string{"Vulkan", "CUDA"}
	}
	for _, which := range order {
		if be, name, ok := try(which); ok {
			return be, name, nil
		}
	}
	return nil, "", errors.New("no usable GPU backend (CUDA and Vulkan init both failed — check drivers and the shipped DLLs)")
}

// AvailableBackends (allgpu build) probes which GPU backends can initialise on this machine and
// returns them in preference order (CUDA first) — a real load-init-free of each DLL, so the studio
// only offers a backend that actually works. Empty when no GPU is usable.
func AvailableBackends() []string {
	var out []string
	if be, err := cuda.New([]float32{0, 0, 0, 1}, nil, 1, 1, 1); err == nil {
		be.Close()
		out = append(out, "CUDA")
	}
	if be, err := vulkan.New([]float32{0, 0, 0, 1}, nil, 1, 1, 1); err == nil {
		be.Close()
		out = append(out, "Vulkan")
	}
	return out
}
