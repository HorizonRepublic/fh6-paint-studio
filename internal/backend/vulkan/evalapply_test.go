package vulkan

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"
)

// Eval against Apply — the backend checked against itself. Evaluate promises a weighted ΔSSE for a
// candidate; Apply then composites it and ErrorGrid measures what actually happened. If a family's
// scoring maths drifts from its compositing maths the two stop agreeing, and no second
// implementation of the maths is needed to see it.
//
// Coverage is deliberately every shape family the presets emit: the hard kinds, the radial
// gradients (glow/disk) and the dictionary words. A family missing here is a family that can
// silently rot — that is exactly how the gradient and mask gaps survived for months.

// totalError sums the device error grid. The grid cells partition the frame (grid.comp splits
// [0,W) and [0,H) exactly), so the sum IS the canvas-wide weighted SSE — measured on the GPU.
func totalError(t *testing.T, g *Vulkan) float64 {
	t.Helper()
	grid, _, _, err := g.ErrorGrid()
	if err != nil {
		t.Fatalf("ErrorGrid: %v", err)
	}
	var s float64
	for _, v := range grid {
		s += float64(v)
	}
	return s
}

// The consistency canvas is small on purpose: eval.comp subsamples a candidate whose bbox exceeds
// 4000 px (step > 1 turns the score into an estimate), and 48*40 = 1920 caps every possible bbox
// below that, so the comparison stays exact for every family.
const caW, caH = 48, 40

type evalFamily struct {
	name string
	c    model.Candidate
}

// evalFamilies places one candidate per shape family, all inside x∈[5,43] and y∈[4,28] so the
// transparent-target variant can put its cutout band below them.
func evalFamilies() []evalFamily {
	col := func(r, g, b, a float32) model.RGBA { return model.RGBA{R: r, G: g, B: b, A: a} }
	fs := []evalFamily{
		{"ellipse", model.Candidate{Kind: model.KindEllipse, P: [6]float32{23.5, 15.5, 9, 6, 25, 0}, Color: col(0.8, 0.2, 0.35, 0.7)}},
		{"rectangle", model.Candidate{Kind: model.KindRectangle, P: [6]float32{22.5, 14.5, 8, 5, 40, 0}, Color: col(0.3, 0.65, 0.25, 0.8)}},
		{"triangle", model.Candidate{Kind: model.KindTriangle, P: [6]float32{7.3, 5.4, 39.6, 9.2, 21.1, 26.7}, Color: col(0.45, 0.3, 0.75, 0.6)}},
		{"line", model.Candidate{Kind: model.KindLine, P: [6]float32{6.5, 8.5, 40.5, 22.5, 3.5, 0}, Color: col(0.15, 0.5, 0.9, 0.9)}},
		{"glow", model.Candidate{Kind: model.KindGlow, P: [6]float32{23.5, 15.5, 12, 10, 15, 0}, Color: col(0.9, 0.55, 0.1, 0.75)}},
		{"disk", model.Candidate{Kind: model.KindDisk, P: [6]float32{22.5, 14.5, 11, 9, 60, 0}, Color: col(0.2, 0.85, 0.6, 0.65)}},
	}
	bank := maskbank.All()
	for _, i := range []int{0, len(bank) / 2} {
		if i >= len(bank) {
			continue
		}
		fs = append(fs, evalFamily{fmt.Sprintf("word %d", bank[i].Word),
			model.Candidate{Kind: bank[i].Kind, P: [6]float32{23.5, 15.5, 10, 8, 20, 0}, Color: col(0.7, 0.4, 0.2, 0.85)}})
	}
	return fs
}

// checkEvalApply asserts the measured ΔSSE of applying each family matches the score Evaluate
// promised for it.
func checkEvalApply(t *testing.T, gpu *Vulkan, canvas []float32) {
	t.Helper()
	for _, f := range evalFamilies() {
		if err := gpu.Reset(canvas); err != nil {
			t.Fatalf("%s: Reset: %v", f.name, err)
		}
		before := totalError(t, gpu)
		res, err := gpu.Evaluate([]model.Candidate{f.c})
		if err != nil {
			t.Fatalf("%s: Evaluate: %v", f.name, err)
		}
		if res[0].Score == rejected || res[0].Score == maskRejected {
			t.Errorf("%s: rejected — this family scores nothing at all", f.name)
			continue
		}
		c := f.c
		c.Color = res[0].Color // the greedy applies the SOLVED colour, so the check must too
		if err := gpu.Apply(c); err != nil {
			t.Fatalf("%s: Apply: %v", f.name, err)
		}
		measured := float32(totalError(t, gpu) - before)
		if measured > -1 {
			t.Errorf("%s: only moved the error by %.4f — too small a change to compare against", f.name, measured)
		}
		// The two paths reduce different sums (eval accumulates per-candidate moments in fp32 and
		// solves in double; the grid re-measures the composited fp32 canvas), so agreement is to
		// fp32 accumulation noise, not bit-exact. A family whose eval and apply genuinely disagree
		// — a gradient scored as a solid disc, a word scored as an ellipse — is off by tens of
		// percent, far outside this.
		if !closeRel(measured, res[0].Score, 3e-3, 1e-3) {
			t.Errorf("%s: Evaluate promised ΔSSE %.4f, Apply delivered %.4f", f.name, res[0].Score, measured)
		}
	}
}

func TestEvalMatchesApply(t *testing.T) {
	rng := rand.New(rand.NewSource(101))
	target, weight := smoothTarget(rng, caW, caH)
	gpu, err := New(target, weight, caW, caH, 8)
	if err != nil {
		t.Skipf("vulkan unavailable: %v", err)
	}
	defer gpu.Close()
	if !gpu.MasksOnDevice() {
		t.Fatal("mask atlas is not on the device — the dictionary family would go unchecked")
	}
	if len(evalFamilies()) < 7 {
		t.Fatalf("only %d families placed — the word family is missing", len(evalFamilies()))
	}
	// Honest gradient scoring. The shipped search deliberately scores a glow as a solid ellipse
	// (see TestGradientGateIsLive), which is exactly an eval/apply disagreement on purpose — so the
	// consistency check has to ask for the honest branch.
	gpu.SetGradients(true)

	canvas := flatCanvas(caW, caH, 0.5)
	checkEvalApply(t, gpu, canvas)
}

// TestEvalMatchesApplyTransparent repeats the check on a cutout target. The candidates stay clear
// of the transparent band, because a shape that overlaps it is charged a SPILL PENALTY that is not
// part of the canvas error by design — the equality only holds on opaque ground.
func TestEvalMatchesApplyTransparent(t *testing.T) {
	rng := rand.New(rand.NewSource(103))
	target, weight := smoothTarget(rng, caW, caH)
	const cutY = 32 // every family above ends by y≈29, so none reaches the band
	for y := cutY; y < caH; y++ {
		for x := 0; x < caW; x++ {
			target[(y*caW+x)*4+3] = 0
		}
	}
	gpu, err := New(target, weight, caW, caH, 8)
	if err != nil {
		t.Skipf("vulkan unavailable: %v", err)
	}
	defer gpu.Close()
	gpu.SetGradients(true)

	canvas := flatCanvas(caW, caH, 0.5)
	checkEvalApply(t, gpu, canvas)

	// The cutout rule itself: a candidate lying wholly inside the transparent band covers no
	// scorable pixel and must come back rejected rather than with some score for painting a hole.
	if err := gpu.Reset(canvas); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	hole := model.Candidate{Kind: model.KindEllipse, P: [6]float32{23.5, 36.5, 3, 2, 0, 0},
		Color: model.RGBA{R: 0.5, G: 0.5, B: 0.5, A: 0.8}}
	res, err := gpu.Evaluate([]model.Candidate{hole})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res[0].Score != rejected {
		t.Errorf("a candidate entirely inside the cutout scored %.4f, want rejected", res[0].Score)
	}
}

// smoothTarget builds a low-frequency colour field. White noise would leave every candidate scoring
// a tiny improvement over the flat canvas, and a ΔSSE near zero compares nothing — a smooth target
// gives each shape a coherent region to fit and a score worth checking. The weight map stays random.
func smoothTarget(rng *rand.Rand, w, h int) (target, weight []float32) {
	target = make([]float32, w*h*4)
	weight = make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			fx, fy := float64(x), float64(y)
			target[i*4+0] = 0.5 + 0.45*float32(math.Sin(fx*0.13)*math.Cos(fy*0.11))
			target[i*4+1] = 0.5 + 0.45*float32(math.Sin(fx*0.07+1))
			target[i*4+2] = 0.5 + 0.45*float32(math.Cos(fy*0.09+2))
			target[i*4+3] = 1
			weight[i] = 0.2 + 0.8*rng.Float32()
		}
	}
	return
}

// flatCanvas returns an opaque constant canvas — a uniform start leaves every family a large,
// unambiguous improvement to score, so the ΔSSE comparison is never a difference of near-equals.
func flatCanvas(w, h int, v float32) []float32 {
	c := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		c[i*4+0], c[i*4+1], c[i*4+2], c[i*4+3] = v, v, v, 1
	}
	return c
}
