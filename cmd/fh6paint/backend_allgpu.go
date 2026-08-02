//go:build allgpu

package main

import (
	"errors"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cuda"
	"fh6-paint-studio/internal/backend/vulkan"
)

// newBackend (allgpu build) auto-selects at runtime: CUDA on NVIDIA, else Vulkan (cross-vendor).
// One binary for any GPU — ship with both fh6cuda.dll and fh6vk.dll beside the exe. No CPU
// fallback: if neither GPU initialises, the error surfaces.
func newBackend(pixels, weight []float32, w, h, gridSize int) (backend.Backend, string, error) {
	if be, err := cuda.New(pixels, weight, w, h, gridSize); err == nil {
		return be, "cuda", nil
	}
	if be, err := vulkan.New(pixels, weight, w, h, gridSize); err == nil {
		return be, "vulkan", nil
	}
	return nil, "", errors.New("no usable GPU backend (CUDA and Vulkan init both failed — check drivers and the shipped DLLs)")
}
