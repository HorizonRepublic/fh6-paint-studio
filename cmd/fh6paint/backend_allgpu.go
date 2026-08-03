//go:build allgpu

package main

import (
	"errors"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cuda"
	"fh6-paint-studio/internal/backend/vulkan"
)

// newBackend (allgpu build) selects at runtime. VULKAN IS THE DEFAULT (owner decision 2026-08-03):
// cross-vendor, faster polish, a third of the CPU time. CUDA remains as an unmaintained fallback,
// reachable with -backend cuda. One binary for any GPU — ship fh6vk.dll beside the exe (fh6cuda.dll
// only if you want the fallback). No CPU fallback: if neither GPU initialises, the error surfaces.
func newBackend(pixels, weight []float32, w, h, gridSize int) (backend.Backend, string, error) {
	order := []string{"vulkan", "cuda"}
	if backendPref == "cuda" {
		order = []string{"cuda", "vulkan"}
	}
	for _, which := range order {
		switch which {
		case "vulkan":
			if be, err := vulkan.New(pixels, weight, w, h, gridSize); err == nil {
				return be, "vulkan", nil
			}
		case "cuda":
			if be, err := cuda.New(pixels, weight, w, h, gridSize); err == nil {
				return be, "cuda", nil
			}
		}
	}
	return nil, "", errors.New("no usable GPU backend (Vulkan and CUDA init both failed — check drivers and the shipped DLLs)")
}
