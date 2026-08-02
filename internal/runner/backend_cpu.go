//go:build !cuda && !vulkan && !allgpu

package runner

import (
	"errors"

	"fh6-paint-studio/internal/backend"
)

// newBackend (untagged build) has no engine: the pure-Go CPU backend was dropped (owner decision
// 2026-07-19 — CUDA is the golden reference, Vulkan the cross-vendor port; the userbase runs FH6,
// so a GPU is a given). Build with -tags cuda, vulkan, or allgpu.
func newBackend(_, _ []float32, _, _, _ int) (backend.Backend, string, error) {
	return nil, "", errors.New("this build has no GPU backend — rebuild with -tags cuda, vulkan, or allgpu")
}

// AvailableBackends reports the backends offered by this (engine-less) build.
func AvailableBackends() []string { return nil }
