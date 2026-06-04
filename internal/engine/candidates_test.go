package engine

import (
	"math/rand"
	"testing"
)

func TestSamplerBiasesToHotCell(t *testing.T) {
	// 4x4 grid, all zero except cell (3,3) hot.
	grid := make([]float32, 16)
	grid[15] = 1000
	s := NewErrorSampler(grid, 4, 4, 40, 40)
	rng := rand.New(rand.NewSource(1))
	hits := 0
	for i := 0; i < 200; i++ {
		x, y := s.Sample(rng)
		if x >= 30 && y >= 30 { // bottom-right quadrant ~ cell (3,3)
			hits++
		}
	}
	if hits < 180 {
		t.Fatalf("only %d/200 samples landed in hot cell region", hits)
	}
}

func TestSamplerUniformFallback(t *testing.T) {
	grid := make([]float32, 16) // all-zero grid -> total 0 -> uniform fallback
	s := NewErrorSampler(grid, 4, 4, 40, 40)
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 100; i++ {
		x, y := s.Sample(rng)
		if x < 0 || x >= 40 || y < 0 || y >= 40 {
			t.Fatalf("fallback sample out of bounds: (%v,%v)", x, y)
		}
	}
}
