package engine

import (
	"os"
	"strconv"
)

// alphagrid.go — EXPERIMENTAL analytic per-candidate alpha (Options.AnalyticAlpha, default off).
// Generators sample a candidate's alpha ~U(alphaMin,1) and eval solves only the color; the eval
// accumulators are alpha-independent though, so the epilogue can re-solve the color for a small
// alpha GRID and keep the ΔSSE-min (alpha, color) pair at zero extra memory traffic — every
// candidate's alpha becomes (grid-)exact instead of sampled. Implemented in the CPU reference
// (SetAlphaGrid) and the CUDA eval kernels (fp_set_alpha_grid); wired in newRun.

// alphaGridN is the grid resolution. 6 points over [alphaMin, 1] keep the epilogue negligible
// while quantizing alpha no coarser than the polish can later refine it.
const alphaGridN = 6

// alphaGridMax caps the grid's top end. The uncapped grid over-picks α≈1 — greedy-optimal opaque
// claims that kill the soft layering the polish co-adapts (measured: img_5 frac α>0.95 7%→28%,
// SSIM −0.02, 5/6 SSE worse). Capped at 0.75 the win is stable on anime: 7/9 SSE better across
// 3 imgs × 3 seeds with perceptual parity-or-better, zero wall cost. FH6_ALPHAGRID_MAX overrides
// for lab A/Bs.
const alphaGridMax = 0.75

// alphaGridValues returns the analytic-alpha grid: alphaGridN evenly spaced values spanning
// [alphaMin, alphaGridMax] inclusive. Deterministic and identical across backends (golden-diff
// relies on it).
func alphaGridValues(alphaMin float32) []float32 {
	alphaMax := float32(alphaGridMax)
	if s := os.Getenv("FH6_ALPHAGRID_MAX"); s != "" {
		if v, err := strconv.ParseFloat(s, 32); err == nil && float32(v) > alphaMin && v <= 1 {
			alphaMax = float32(v)
		}
	}
	vals := make([]float32, alphaGridN)
	for i := range vals {
		vals[i] = alphaMin + (alphaMax-alphaMin)*float32(i)/float32(alphaGridN-1)
	}
	return vals
}
