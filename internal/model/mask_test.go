package model

import "testing"

func TestMaskWordRegistry(t *testing.T) {
	k1 := RegisterMaskWord(0x089b, 53.3, 53.3) // arc-90
	k2 := RegisterMaskWord(0x0066, 128.1, 128.0)
	if k1 < KindMaskBase || k2 < KindMaskBase {
		t.Fatalf("mask kinds must be >= KindMaskBase (%d): got %d, %d", KindMaskBase, k1, k2)
	}
	if k1 == k2 {
		t.Fatalf("distinct words must map to distinct kinds (both %d)", k1)
	}
	if again := RegisterMaskWord(0x089b, 53.3, 53.3); again != k1 {
		t.Errorf("RegisterMaskWord not idempotent: %d != %d", again, k1)
	}
	if got, ok := MaskKind(0x089b); !ok || got != k1 {
		t.Errorf("MaskKind(0x089b) = %d,%v want %d,true", got, ok, k1)
	}
	if w, ok := MaskWord(k1); !ok || w != 0x089b {
		t.Errorf("MaskWord(%d) = 0x%x,%v want 0x089b,true", k1, w, ok)
	}
	if !IsMask(k1) || IsMask(KindEllipse) {
		t.Errorf("IsMask: want true for %d, false for KindEllipse", k1)
	}
	if nw, nh, ok := MaskNative(k1); !ok || nw != 53.3 || nh != 53.3 {
		t.Errorf("MaskNative(%d) = %.1f,%.1f,%v want 53.3,53.3,true", k1, nw, nh, ok)
	}
}

func TestMaskToShapeRoundTrip(t *testing.T) {
	k := RegisterMaskWord(0x0837, 148.7, 173.1) // boomerang
	c := Candidate{Kind: k, P: [6]float32{100, 120, 200, 260, 30, 0.5}, Color: RGBA{0.2, 0.4, 0.6, 1}}
	s := c.ToShape(0)
	if s.Type != 0x0837 {
		t.Fatalf("ToShape Type = %d want 0x0837", s.Type)
	}
	if got := KindFromType(s.Type); got != k {
		t.Fatalf("KindFromType(0x0837) = %d want %d", got, k)
	}
	p := ParamsFromShape(s)
	want := [6]float32{100, 120, 200, 260, 30, 0.5}
	for i := range want {
		if p[i] != want[i] {
			t.Errorf("round-trip P[%d] = %v want %v", i, p[i], want[i])
		}
	}
}
