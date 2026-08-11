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
		t.Fatalf("vulkan backend for engine tests (is fh6vk.dll beside internal/engine?): %v", err)
	}
	t.Cleanup(func() { be.Close() })
	return be
}
