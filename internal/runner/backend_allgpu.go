//go:build allgpu

package runner

import (
	"errors"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cuda"
	"fh6-paint-studio/internal/backend/vulkan"
)

// newBackend (allgpu build) constructs the chosen GPU backend at runtime. VULKAN IS THE DEFAULT
// (owner decision 2026-08-03): it runs on every vendor, its polish is ~2.4x faster than CUDA's, it
// costs a third of the CPU time, and one backend is one thing to tune instead of two kept in
// lockstep. CUDA stays in the tree as an unmaintained fallback for anyone who wants to A/B it —
// BackendPreference ("CUDA") still selects it. A failed init falls through to the other; if neither
// initialises the error surfaces to the UI (there is no CPU fallback).
// Ship with fh6vk.dll; fh6cuda.dll is optional.
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
	order := []string{"Vulkan", "CUDA"}
	if BackendPreference == "CUDA" {
		order = []string{"CUDA", "Vulkan"}
	}
	for _, which := range order {
		if be, name, ok := try(which); ok {
			return be, name, nil
		}
	}
	return nil, "", errors.New("no usable GPU backend (CUDA and Vulkan init both failed — check drivers and the shipped DLLs)")
}

// AvailableBackends (allgpu build) probes which GPU backends can initialise on this machine and
// returns them in preference order (Vulkan first — the supported one) — a real load-init-free of
// each DLL, so the studio only offers a backend that actually works. Empty when no GPU is usable.
func AvailableBackends() []string {
	var out []string
	if be, err := vulkan.New([]float32{0, 0, 0, 1}, nil, 1, 1, 1); err == nil {
		be.Close()
		out = append(out, "Vulkan")
	}
	if be, err := cuda.New([]float32{0, 0, 0, 1}, nil, 1, 1, 1); err == nil {
		be.Close()
		out = append(out, "CUDA")
	}
	return out
}
