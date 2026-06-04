package cpu

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

// structuredTarget builds a high-frequency opaque target so a SOFT gradient cannot match it: its
// softness must surface as residual the eval is supposed to account for. (A flat target would let any
// blob match perfectly and hide a scoring bug.)
func structuredTarget(w, h int) []float32 {
	t := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := (y*w + x) * 4
			v := float32((x+y)%16) / 16
			if (x/4+y/4)%2 == 0 {
				v = 1 - v
			}
			t[p], t[p+1], t[p+2], t[p+3] = v, 0.5*v, 1-v, 1
		}
	}
	return t
}

func sseRGBA(canvas, target, weight []float32, w, h int) float64 {
	var sse float64
	for i := 0; i < w*h; i++ {
		wt := float64(weight[i])
		for c := 0; c < 4; c++ {
			d := float64(canvas[i*4+c] - target[i*4+c])
			sse += wt * d * d
		}
	}
	return sse
}

// gradientEvalActualDelta evals one gradient candidate at the given sample budget, applies it with the
// eval's optimal colour, and returns (predicted ΔSSE from eval, actual full-res ΔSSE from apply).
func gradientEvalActualDelta(t *testing.T, budget int) (predicted, actual float64) {
	const w, h = 80, 60
	be := New(structuredTarget(w, h), w, h, 8)
	// Some non-trivial canvas state first (a hard ellipse), so the gradient blends over real content.
	_ = be.Apply(model.Candidate{Kind: model.KindEllipse, P: [6]float32{40, 30, 34, 24, 0, 0}, Color: model.RGBA{R: 0.3, G: 0.6, B: 0.2, A: 1}})

	be.SetSampleBudget(budget)
	cand := model.Candidate{Kind: model.KindGlow, P: [6]float32{38, 28, 28, 20, 10, 0}, Color: model.RGBA{A: 0.8}}
	res, _ := be.Evaluate([]model.Candidate{cand})
	predicted = float64(res[0].Score)

	// Apply with the eval's optimal colour (alpha preserved by the eval), measure the TRUE full-res ΔSSE.
	be.SetSampleBudget(1 << 30)
	before := sseRGBA(be.canvas, be.target, be.weight, w, h)
	cand.Color = res[0].Color
	_ = be.Apply(cand)
	after := sseRGBA(be.canvas, be.target, be.weight, w, h)
	actual = after - before
	return predicted, actual
}

// TestGradientEvalFormulaMatchesApply: at FULL sample budget the gradient eval's ΔSSE must equal the
// actual rendered SSE change — this pins the per-pixel-alpha optimal-colour + ΔSSE math.
func TestGradientEvalFormulaMatchesApply(t *testing.T) {
	predicted, actual := gradientEvalActualDelta(t, 1<<30)
	t.Logf("FULL budget: predicted ΔSSE=%.5f  actual ΔSSE=%.5f", predicted, actual)
	if tol := 1e-3 * math.Max(1, math.Abs(actual)); math.Abs(predicted-actual) > tol {
		t.Fatalf("gradient eval formula wrong: predicted %.5f != actual %.5f (Δ=%.5f)", predicted, actual, predicted-actual)
	}
}

// TestGradientEvalSamplingMatchesApply: at the DEFAULT progressive-sampling budget (what the greedy
// actually uses during search) the eval's ΔSSE should still track the true full-res ΔSSE. A large
// divergence means sparse sampling mis-scores the soft falloff, so the greedy selects gradients on a
// phantom error reduction — the suspected smear mechanism.
func TestGradientEvalSamplingMatchesApply(t *testing.T) {
	predicted, actual := gradientEvalActualDelta(t, defaultSampleBudget)
	rel := math.Abs(predicted-actual) / math.Max(1, math.Abs(actual))
	t.Logf("DEFAULT budget (%d): predicted ΔSSE=%.5f  actual ΔSSE=%.5f  rel err=%.1f%%", defaultSampleBudget, predicted, actual, 100*rel)
	if rel > 0.10 {
		t.Fatalf("progressive sampling mis-scores gradient: predicted %.5f vs actual %.5f (rel %.1f%%)", predicted, actual, 100*rel)
	}
}
