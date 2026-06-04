package model

import "testing"

// Gradient candidates serialise to the gradient Type ids (== the in-game shape word) with the
// ellipse Data layout, and round-trip back to their kind.
func TestGradientToShapeAndType(t *testing.T) {
	cases := []struct {
		kind     ShapeKind
		wantType int
	}{
		{KindGlow, TypeGradGlow},
		{KindDisk, TypeGradDisk},
	}
	for _, tc := range cases {
		c := Candidate{Kind: tc.kind, P: [6]float32{10, 20, 30, 15, 45, 0}, Color: RGBA{1, 0.5, 0.25, 0.8}}
		s := c.ToShape(1.5)
		if s.Type != tc.wantType {
			t.Errorf("kind %v ToShape Type = %d, want %d", tc.kind, s.Type, tc.wantType)
		}
		if len(s.Data) != 5 {
			t.Fatalf("kind %v Data len = %d, want 5 (ellipse layout)", tc.kind, len(s.Data))
		}
		if s.Data[0] != 10 || s.Data[1] != 20 || s.Data[2] != 30 || s.Data[3] != 15 || s.Data[4] != 45 {
			t.Errorf("kind %v Data = %v, want [10 20 30 15 45]", tc.kind, s.Data)
		}
		if got := KindFromType(s.Type); got != tc.kind {
			t.Errorf("KindFromType(%d) = %v, want %v", s.Type, got, tc.kind)
		}
	}
}

// The gradient Type ids equal the in-game low 16-bit shape words (injector maps Type->word directly).
func TestGradientTypeIDsMatchGameWords(t *testing.T) {
	if TypeGradGlow != 0xE4 {
		t.Errorf("TypeGradGlow = 0x%X, want 0xE4", TypeGradGlow)
	}
	if TypeGradDisk != 0xE2 {
		t.Errorf("TypeGradDisk = 0x%X, want 0xE2", TypeGradDisk)
	}
}
