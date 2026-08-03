//go:build cuda

package engine_test

import (
	"testing"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cuda"
)

// newTestBackend mirrors the package-engine helper for the external test package (see
// testbackend_test.go): engine unit tests run on the CUDA golden backend now.
func newTestBackend(t *testing.T, target []float32, w, h, grid int) backend.Backend {
	t.Helper()
	be, err := cuda.New(target, nil, w, h, grid)
	if err != nil {
		t.Fatalf("cuda backend for engine tests (is fh6cuda.dll beside internal/engine?): %v", err)
	}
	t.Cleanup(func() { be.Close() })
	return be
}
