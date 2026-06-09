//go:build allgpu

package main

import "fh6-paint-studio/internal/runner"

// backendOptions (allgpu build) probes which GPU backends actually work on this machine, so the
// studio offers CUDA/Vulkan only when present (no CUDA -> Vulkan with no picker; no GPU -> CPU).
func backendOptions() []string { return runner.AvailableBackends() }
