package preset

import (
	"testing"

	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
)

func uniformPrep(w, h int) imageio.Prepared {
	px := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		px[i*4], px[i*4+1], px[i*4+2], px[i*4+3] = 0.5, 0.5, 0.5, 1
	}
	return imageio.Prepared{W: w, H: h, Pixels: px, Background: model.RGBA{}}
}

func TestModeKnobDefaultsMirrorsFlatVector(t *testing.T) {
	c := ModeKnobDefaults("flat", 12, false) // < 80 colours -> vector logo
	if c.PolishIters != 600 {
		t.Errorf("PolishIters = %d, want 600", c.PolishIters)
	}
	if c.PolishTau1 != 0.06 {
		t.Errorf("PolishTau1 = %v, want 0.06", c.PolishTau1)
	}
	if c.WeightStrength != 0 {
		t.Errorf("WeightStrength = %v, want 0", c.WeightStrength)
	}
	if c.Boundary == nil || !*c.Boundary {
		t.Errorf("Boundary = %v, want true", c.Boundary)
	}
	if c.Alpha == nil || *c.Alpha {
		t.Errorf("Alpha = %v, want false (flat is opaque)", c.Alpha)
	}
}

func TestModeKnobDefaultsMirrorsAnime(t *testing.T) {
	c := ModeKnobDefaults("anime", 5000, false)
	if c.PolishTau1 != 0.08 {
		t.Errorf("PolishTau1 = %v, want 0.08", c.PolishTau1)
	}
	if c.WeightStrength != 0.15 {
		t.Errorf("WeightStrength = %v, want 0.15", c.WeightStrength)
	}
	if c.Boundary == nil || *c.Boundary {
		t.Errorf("Boundary = %v, want false", c.Boundary)
	}
	if c.Alpha == nil || !*c.Alpha {
		t.Errorf("Alpha = %v, want true", c.Alpha)
	}
	if got := ParseKindWeights(c.KindWeights); len(got) != 3 {
		t.Errorf("KindWeights %q parsed to %v, want 3 weights", c.KindWeights, got)
	}
}

func TestModeKnobDefaultsCutoutForcesOpaqueBackfit(t *testing.T) {
	c := ModeKnobDefaults("anime", 5000, true)
	if c.Alpha == nil || *c.Alpha {
		t.Errorf("Alpha = %v, want false (cutout is opaque)", c.Alpha)
	}
	if c.Backfit == nil || !*c.Backfit {
		t.Errorf("Backfit = %v, want true (cutout)", c.Backfit)
	}
}

func TestResolveAppliesAlphaMinAndStandout(t *testing.T) {
	prep := uniformPrep(8, 8)
	c := DefaultChoices()
	c.Mode = "anime"
	allow := true
	c.Alpha = &allow
	c.AlphaMin = 0.7
	c.StandoutTol = 0.005
	r := Resolve(prep, c)
	if r.Options.AlphaMin != 0.7 {
		t.Errorf("Options.AlphaMin = %v, want 0.7", r.Options.AlphaMin)
	}
	if r.Options.StandoutTol != 0.005 {
		t.Errorf("Options.StandoutTol = %v, want 0.005", r.Options.StandoutTol)
	}
}

func TestResolveAlphaMinSentinelKeepsModeFloor(t *testing.T) {
	prep := uniformPrep(8, 8)
	c := DefaultChoices() // AlphaMin -1
	c.Mode = "anime"
	allow := true
	c.Alpha = &allow
	r := Resolve(prep, c)
	if r.Options.AlphaMin != 0.40 {
		t.Errorf("Options.AlphaMin = %v, want mode floor 0.40", r.Options.AlphaMin)
	}
}
