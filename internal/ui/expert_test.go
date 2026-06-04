package ui

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/preset"
)

func boolEq(p *bool, want bool) bool { return p != nil && *p == want }

// TestApplyChoicesInverse checks that loading a configuration into the widgets and reading it back
// reproduces it: integers/booleans/strings exactly, slider-backed floats within quantisation tolerance.
func TestApplyChoicesInverse(t *testing.T) {
	s := NewAppState(NewTheme())

	c := preset.ModeKnobDefaults("flat", 12, false) // concrete flat-vector config, as a preset would hold
	c.Shapes = 2000
	c.Seed = 42
	polishOff := false
	c.Polish = &polishOff // a manual tweak on top

	s.ApplyChoices(c)
	if !s.Expert.Value {
		t.Fatal("ApplyChoices should turn Expert mode on")
	}
	got := s.Choices()

	if got.Mode != c.Mode {
		t.Errorf("Mode = %q, want %q", got.Mode, c.Mode)
	}
	if got.Shapes != c.Shapes {
		t.Errorf("Shapes = %d, want %d", got.Shapes, c.Shapes)
	}
	if got.Seed != c.Seed {
		t.Errorf("Seed = %d, want %d", got.Seed, c.Seed)
	}
	if got.Quality != c.Quality {
		t.Errorf("Quality = %q, want %q", got.Quality, c.Quality)
	}
	if !boolEq(got.Polish, false) {
		t.Errorf("Polish = %v, want false", got.Polish)
	}
	if !boolEq(got.Alpha, derefBool(c.Alpha, true)) {
		t.Errorf("Alpha = %v, want %v", got.Alpha, c.Alpha)
	}
	if !boolEq(got.Boundary, derefBool(c.Boundary, false)) {
		t.Errorf("Boundary = %v, want %v", got.Boundary, c.Boundary)
	}
	if !boolEq(got.Backfit, derefBool(c.Backfit, false)) {
		t.Errorf("Backfit = %v, want %v", got.Backfit, c.Backfit)
	}

	ints := []struct {
		name      string
		got, want int
	}{
		{"PolishIters", got.PolishIters, c.PolishIters},
		{"Random", got.Random, c.Random},
		{"Mutated", got.Mutated, c.Mutated},
		{"SampleBudget", got.SampleBudget, c.SampleBudget},
		{"MaxNoImprove", got.MaxNoImprove, c.MaxNoImprove},
		{"Grid", got.Grid, c.Grid},
	}
	for _, p := range ints {
		if p.got != p.want {
			t.Errorf("%s = %d, want %d", p.name, p.got, p.want)
		}
	}

	if got.Kinds != c.Kinds {
		t.Errorf("Kinds = %q, want %q", got.Kinds, c.Kinds)
	}
	if got.KindWeights != c.KindWeights {
		t.Errorf("KindWeights = %q, want %q", got.KindWeights, c.KindWeights)
	}

	floats := []struct {
		name      string
		got, want float64
	}{
		{"PolishTau1", got.PolishTau1, c.PolishTau1},
		{"AlphaMin", got.AlphaMin, c.AlphaMin},
		{"WeightStrength", got.WeightStrength, c.WeightStrength},
		{"StandoutTol", got.StandoutTol, c.StandoutTol},
		{"Aspect", got.Aspect, c.Aspect},
	}
	for _, p := range floats {
		if math.Abs(p.got-p.want) > 1e-4 {
			t.Errorf("%s = %v, want ~%v", p.name, p.got, p.want)
		}
	}
}
