//go:build cuda

package engine

import (
	"testing"

	"fh6-paint-studio/internal/backend"
	"fh6-paint-studio/internal/backend/cuda"
)

// newTestBackend replaces the deleted pure-Go reference in the engine unit tests: they run on the
// CUDA golden backend now (build tag cuda; needs a fh6cuda.dll test copy beside this package,
// refreshed by scripts/build-cuda.ps1 rebuilds like the one in internal/backend/cuda).
func newTestBackend(t *testing.T, target []float32, w, h, grid int) backend.Backend {
	t.Helper()
	be, err := cuda.New(target, nil, w, h, grid)
	if err != nil {
		t.Fatalf("cuda backend for engine tests (is fh6cuda.dll beside internal/engine?): %v", err)
	}
	t.Cleanup(func() { be.Close() })
	return be
}
