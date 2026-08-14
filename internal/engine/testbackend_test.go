package engine

import (
	"testing"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/vulkan"
)

// newTestBackend gives the engine unit tests the real engine: Vulkan is the only backend, so it is
// also the reference the tests run against (needs an fh6vk.dll test copy beside this package —
// scripts/build-vulkan.ps1 refreshes it).
func newTestBackend(t *testing.T, target []float32, w, h, grid int) backend.Backend {
	t.Helper()
	be, err := vulkan.New(target, nil, w, h, grid)
	if err != nil {
		// No device or no fh6vk.dll (a CI box). Skip — the same convention as the vulkan
		// package; scripts/build-vulkan.ps1 refreshes the test copy on a dev box.
		t.Skipf("vulkan unavailable, the engine suite needs a real device: %v", err)
	}
	t.Cleanup(func() { be.Close() })
	return be
}
