package imageio

import (
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/model"
)

func benchPixels(n int) []float32 {
	rng := rand.New(rand.NewSource(1))
	px := make([]float32, n*4)
	for i := range px {
		px[i] = rng.Float32()
	}
	return px
}

// A live preview frame at native resolution is ~16 MP; this is what the studio pays per frame.
func BenchmarkEncodeForDisplay(b *testing.B) {
	old := model.LinearLight
	model.LinearLight = true
	defer func() { model.LinearLight = old }()
	px := benchPixels(4096 * 4096)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EncodeForDisplay(px)
	}
}

func BenchmarkEncodeDisplayBytes(b *testing.B) {
	old := model.LinearLight
	model.LinearLight = true
	defer func() { model.LinearLight = old }()
	px := benchPixels(4096 * 4096)
	dst := make([]uint8, len(px))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EncodeDisplayBytes(px, dst)
	}
}
