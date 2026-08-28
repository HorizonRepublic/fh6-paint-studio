package vulkan

import (
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/model"
)

// The on-device hill climb has three promises worth pinning: it never returns a WORSE score than
// the incumbent it was given (the argmin's keep rule), the winner it returns really scores what it
// claims (re-evaluated through the ordinary Evaluate path), and the same seed reproduces the same
// answer (a run must stay deterministic).
func TestSearchMutateImprovesAndIsDeterministic(t *testing.T) {
	const w, h = 64, 48
	rng := rand.New(rand.NewSource(7))
	target, weight := smoothTarget(rng, w, h)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Skipf("vulkan unavailable: %v", err)
	}
	defer gpu.Close()
	if err := gpu.Reset(flatCanvas(w, h, 0.5)); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// A deliberately mediocre incumbent, scored honestly first.
	seed := model.Candidate{
		Kind:  model.KindEllipse,
		P:     [6]float32{20, 15, 6, 4, 30, 0},
		Color: model.RGBA{R: 0.5, G: 0.5, B: 0.5, A: 0.8},
	}
	res, err := gpu.Evaluate([]model.Candidate{seed})
	if err != nil || len(res) != 1 {
		t.Fatalf("Evaluate(seed): %v", err)
	}
	seed.Color = res[0].Color
	base := res[0].Score

	best, score, ok := gpu.SearchMutate(99, seed, base, 8, 64, 4, 3, true, 0.3, false, 5, 0)
	if !ok {
		t.Skip("fp_search_mutate unavailable")
	}
	if score > base {
		t.Fatalf("hill climb went uphill: %.4f -> %.4f", base, score)
	}
	// The returned score must be the shape's real score, not a stale slot.
	re, err := gpu.Evaluate([]model.Candidate{best})
	if err != nil || len(re) != 1 {
		t.Fatalf("Evaluate(best): %v", err)
	}
	if d := re[0].Score - score; d > 1e-2 || d < -1e-2 {
		t.Fatalf("returned score %.4f but the candidate re-scores %.4f", score, re[0].Score)
	}

	best2, score2, ok2 := gpu.SearchMutate(99, seed, base, 8, 64, 4, 3, true, 0.3, false, 5, 0)
	if !ok2 || best2.P != best.P || score2 != score {
		t.Fatalf("same seed, different answer: (%v, %.4f) vs (%v, %.4f)", best.P, score, best2.P, score2)
	}
}

// The coarse filter's contract: the candidate stream is untouched (same seed, same generation),
// the winner it returns is FULL-budget scored (re-evaluating it reproduces the score), it is
// deterministic, and it can only match or lose to the exhaustive single-pass argmin — never
// invent a better score than scoring everything exactly.
func TestCoarseFilterContract(t *testing.T) {
	const w, h = 96, 64
	rng := rand.New(rand.NewSource(3))
	target, weight := smoothTarget(rng, w, h)
	gpu, err := New(target, weight, w, h, 8)
	if err != nil {
		t.Skipf("vulkan unavailable: %v", err)
	}
	defer gpu.Close()
	if err := gpu.Reset(flatCanvas(w, h, 0.5)); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	grid := make([]float32, 8*6)
	for i := range grid {
		grid[i] = 1
	}
	kinds := []model.ShapeKind{model.KindEllipse, model.KindRectangle}
	kindCDF := []float32{0.5, 1}

	search := func() (model.Candidate, float32) {
		c, sc, ok := gpu.SearchRandom(777, 2048, kinds, kindCDF, 32, true, 0.3, 4, false, 3, grid, 8, 6, 0, 0, 0)
		if !ok {
			t.Skip("on-device search unavailable")
		}
		return c, sc
	}

	gpu.SetCoarse(false, 0, 0)
	_, full := search()

	gpu.SetCoarse(true, 500, 64) // 2048 > 4*64 -> the filter engages
	cCoarse, sCoarse := search()
	if sCoarse < full-1e-3 {
		t.Fatalf("coarse found a better score than scoring everything exactly: %.4f < %.4f", sCoarse, full)
	}
	re, err := gpu.Evaluate([]model.Candidate{cCoarse})
	if err != nil || len(re) != 1 {
		t.Fatalf("Evaluate(coarse winner): %v", err)
	}
	if d := re[0].Score - sCoarse; d > 1e-2 || d < -1e-2 {
		t.Fatalf("coarse winner's score %.4f is not its full-budget score %.4f", sCoarse, re[0].Score)
	}
	c2, s2 := search()
	if c2.P != cCoarse.P || s2 != sCoarse {
		t.Fatalf("coarse search is not deterministic: (%v, %.4f) vs (%v, %.4f)", cCoarse.P, sCoarse, c2.P, s2)
	}
	gpu.SetCoarse(false, 0, 0)
}
