package engine_test

import (
	"testing"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/vulkan"
)

// newTestBackend mirrors the package-engine helper for the external test package (see
// testbackend_test.go): the engine unit tests run on Vulkan, the only backend.
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
