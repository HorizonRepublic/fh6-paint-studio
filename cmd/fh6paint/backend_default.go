//go:build !cuda

package main

import (
	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cpu"
)

// newBackend builds the default pure-Go CPU backend. Built unless -tags cuda.
func newBackend(pixels, weight []float32, w, h, gridSize int) (backend.Backend, string, error) {
	be := cpu.New(pixels, w, h, gridSize)
	if weight != nil {
		be.SetWeight(weight)
	}
	return be, "cpu", nil
}
