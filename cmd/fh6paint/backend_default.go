//go:build !cuda && !vulkan && !allgpu

package main

import (
	"errors"

	"fh6-paint-studio/internal/backend"
)

// newBackend (untagged build) has no engine: the pure-Go CPU backend was dropped (CUDA = golden
// reference, Vulkan = the cross-vendor port). Build with -tags cuda, vulkan, or allgpu.
func newBackend(_, _ []float32, _, _, _ int) (backend.Backend, string, error) {
	return nil, "", errors.New("this build has no GPU backend — rebuild with -tags cuda, vulkan, or allgpu")
}
