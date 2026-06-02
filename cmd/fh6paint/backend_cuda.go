//go:build cuda

package main

import (
	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cuda"
)

// newBackend builds the CUDA backend (fh6cuda.dll). Built with -tags cuda.
// weight may be nil; the backend defaults it to all-ones like the CPU backend.
func newBackend(pixels, weight []float32, w, h, gridSize int) (backend.Backend, string, error) {
	be, err := cuda.New(pixels, weight, w, h, gridSize)
	if err != nil {
		return nil, "cuda", err
	}
	return be, "cuda", nil
}
