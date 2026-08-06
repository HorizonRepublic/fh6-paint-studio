package preset

import (
	"testing"

	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
)

// fixture builds a tiny synthetic prepared image. Tests pin the content mode explicitly
// (Mode != "auto") so they assert the mapping, not the ContentClass thresholds.
func fixture(transparent bool) imageio.Prepared {
	const w, h = 8, 8
	px := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		px[i*4] = float32(i%4) / 3
		px[i*4+1] = float32((i/4)%4) / 3
		px[i*4+2] = 0.5
		px[i*4+3] = 1
		if transparent && i%2 == 0 {
			px[i*4+3] = 0
		}
	}
	return imageio.Prepared{
		W: w, H: h, Pixels: px,
		Background:      model.RGBA{R: 0, G: 0, B: 0, A: 1},
		HasTransparency: transparent,
	}
}

func bptr(b bool) *bool { return &b }

func eqWeights(got []float32, want ...float32) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestResolveLogoMode(t *testing.T) {
	c := DefaultChoices()
	c.Mode = "logo"
	r := Resolve(fixture(false), c)

	if r.Options.AllowAlpha {
		t.Error("flat/logo should be opaque (AllowAlpha=false)")
	}
	if !r.Options.BackFit {
		t.Error("flat/logo should auto-enable back-fitting")
	}
	if r.Options.AspectMax != 8 {
		t.Errorf("flat aspect = %v, want 8", r.Options.AspectMax)
	}
	if r.Options.PolishOpts.Tau1 != 0.06 {
		t.Errorf("flat tau1 = %v, want 0.06", r.Options.PolishOpts.Tau1)
	}
	// The fixture is a <80-colour synthetic, so the palette-aware knee routes it to the hard-edge
	// VECTOR-logo path (600 iters) rather than the textured-cartoon path (300). A many-colour
	// cartoon/text would resolve to 300.
	if r.Options.PolishOpts.Iters != 600 {
		t.Errorf("flat few-colour (vector logo) polish iters = %d, want 600", r.Options.PolishOpts.Iters)
	}
	if !eqWeights(r.Options.KindWeights, 0.8, 0.05, 0.15) {
		t.Errorf("flat kind weights = %v, want rect-rich [0.8 0.05 0.15]", r.Options.KindWeights)
	}
	// weight-strength 0 on flat -> uniform weight map (all 1.0).
	for _, wv := range r.Weight {
		if wv != 1 {
			t.Fatalf("flat weight should be uniform 1.0, got %v", wv)
		}
	}
}

func TestResolvePhotoMode(t *testing.T) {
	c := DefaultChoices()
	c.Mode = "photo"
	r := Resolve(fixture(false), c)

	if !r.Options.AllowAlpha {
		t.Error("photo should allow alpha")
	}
	if r.Options.BackFit {
		t.Error("photo back-fitting should be off by default")
	}
	if r.Options.AspectMax != 6 {
		t.Errorf("organic aspect = %v, want 6", r.Options.AspectMax)
	}
	if r.Options.PolishOpts.Tau1 != 0.08 {
		t.Errorf("organic tau1 = %v, want 0.08", r.Options.PolishOpts.Tau1)
	}
	if !eqWeights(r.Options.KindWeights, 0.5, 0.4, 0.1) {
		t.Errorf("organic kind weights = %v, want triangle-rich [0.5 0.4 0.1]", r.Options.KindWeights)
	}
}

func TestResolveTransparentForcesOpaque(t *testing.T) {
	// A transparent image is classified by CONTENT (not a blanket "cutout"), but transparency must
	// still force opaque shapes + the transparent-bg pipeline regardless of the classified mode вЂ”
	// so a smooth anime cutout gets its content's soft treatment while staying solid on the car.
	r := Resolve(fixture(true), DefaultChoices()) // auto: classify content, force opaque

	if !r.Options.TransparentBG {
		t.Error("transparent image should keep TransparentBG")
	}
	if r.Options.AllowAlpha {
		t.Error("transparent object must stay opaque (AllowAlpha=false)")
	}
	if r.Options.CompactPenalty {
		t.Error("compact penalty should be disabled for transparent objects")
	}
	if r.Mode == "cutout" {
		t.Errorf("auto should classify transparent by content (flat/photo/anime), not blanket cutout; got %q", r.Mode)
	}
}

func TestAlphaOverride(t *testing.T) {
	c := DefaultChoices()
	c.Mode = "photo"
	c.Alpha = bptr(false)
	if Resolve(fixture(false), c).Options.AllowAlpha {
		t.Error("explicit Alpha=false must win over the photo default")
	}
}

// The engine emits a background rectangle as shape 0, so what it may PLACE is one below the
// requested budget: the rectangle is an in-game layer like any other. Get this wrong and a full
// panel asks for 3001 layers, one more than the group holds, and the injector drops the last shape.
func TestShapeClamp(t *testing.T) {
	c := DefaultChoices()
	c.Mode = "photo"
	c.Shapes = 5000
	if got := Resolve(fixture(false), c).Options.StopAt; got != MaxShapes-1 {
		t.Errorf("StopAt = %d, want clamp to %d", got, MaxShapes-1)
	}
}

func TestPlaceBudgetLeavesRoomForTheBackground(t *testing.T) {
	for _, req := range []int{1, 2, 100, MaxShapes, MaxShapes + 1} {
		got := PlaceBudget(req)
		if got < 1 {
			t.Errorf("PlaceBudget(%d) = %d, want at least one shape", req, got)
		}
		if total := got + 1; total > req && req > 1 {
			t.Errorf("PlaceBudget(%d) = %d — %d shapes with the background, over the request", req, got, total)
		}
		if total := got + 1; total > MaxShapes {
			t.Errorf("PlaceBudget(%d) = %d — %d shapes with the background, over the group ceiling %d",
				req, got, total, MaxShapes)
		}
	}
}

func TestQualityPreset(t *testing.T) {
	c := DefaultChoices()
	c.Mode = "photo"
	c.Quality = "quality"
	r := Resolve(fixture(false), c)
	if r.Options.RandomSamples != 50000 || r.Options.MutatedSamples != 5000 ||
		r.Options.SampleBudget != 32000 || r.Options.MaxNoImprove != 2000 {
		t.Errorf("quality preset = r%d m%d sb%d ni%d, want 50000/5000/32000/2000",
			r.Options.RandomSamples, r.Options.MutatedSamples, r.Options.SampleBudget, r.Options.MaxNoImprove)
	}
}

// TestHardwiredPresets pins the tuned defaults for the 3 manual presets, so a future edit can't
// silently revert them. STE + recolor-var apply to all content; alphaMin 0.40 organic;
// boundary OFF organic / ON flat.
func TestHardwiredPresets(t *testing.T) {
	check := func(mode string, wantAlpha bool, wantAlphaMin float32, wantBoundary, wantBackfit bool) {
		t.Helper()
		c := DefaultChoices()
		c.Mode = mode
		o := Resolve(fixture(false), c).Options
		if o.AllowAlpha != wantAlpha {
			t.Errorf("%s AllowAlpha=%v want %v", mode, o.AllowAlpha, wantAlpha)
		}
		if wantAlpha && o.AlphaMin != wantAlphaMin {
			t.Errorf("%s AlphaMin=%v want %v", mode, o.AlphaMin, wantAlphaMin)
		}
		if o.BoundaryRadius != wantBoundary {
			t.Errorf("%s BoundaryRadius=%v want %v", mode, o.BoundaryRadius, wantBoundary)
		}
		if o.BackFit != wantBackfit {
			t.Errorf("%s BackFit=%v want %v", mode, o.BackFit, wantBackfit)
		}
		if !o.PolishOpts.STE {
			t.Errorf("%s STE should be ON (universal win)", mode)
		}
		if o.RecolorVarSkip != 0.03 {
			t.Errorf("%s RecolorVarSkip=%v want 0.03 (universal)", mode, o.RecolorVarSkip)
		}
		wantFE := 0.0
		if mode == "anime" {
			wantFE = 0.012 // false-edge polish term: anime-only default (GPU-measured, seed-replicated)
		}
		if o.PolishOpts.FalseEdgeLambda != wantFE {
			t.Errorf("%s FalseEdgeLambda=%v want %v", mode, o.PolishOpts.FalseEdgeLambda, wantFE)
		}
		wantSSIM := 0.0
		if mode == "anime" {
			wantSSIM = 0.006 // SSIM polish term: anime-only default (GPU λ-grid, seed-replicated)
		}
		if o.PolishOpts.SSIMLambda != wantSSIM {
			t.Errorf("%s SSIMLambda=%v want %v", mode, o.PolishOpts.SSIMLambda, wantSSIM)
		}
	}
	check("anime", true, 0.30, false, false) // organic: alpha, alphaMin .30 (replicated floor grid), boundary OFF, no backfit
	check("photo", true, 0.30, false, false) // photo: LOWER alpha floor (smoother tonal ramps; measured on cat+car)
	check("flat", false, 0, true, true)      // opaque, boundary ON, backfit ON
	// legacy names collapse
	if m := Resolve(fixture(false), Choices{Mode: "logo"}).Mode; m != "flat" {
		t.Errorf("logo should collapse to flat preset, got %q", m)
	}
	if m := Resolve(fixture(false), Choices{Mode: "illustration"}).Mode; m != "anime" {
		t.Errorf("illustration should collapse to anime, got %q", m)
	}
}

func TestSeedAndGrid(t *testing.T) {
	c := DefaultChoices()
	c.Mode = "photo"
	c.Seed = 42
	r := Resolve(fixture(false), c)
	if r.Options.Seed != 42 {
		t.Errorf("Seed = %d, want 42", r.Options.Seed)
	}
	if r.Grid != 48 {
		t.Errorf("Grid = %d, want 48", r.Grid)
	}
}
