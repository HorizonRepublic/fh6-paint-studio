package model

import (
	"encoding/json"
	"testing"
)

func TestEllipseCandidateToShape(t *testing.T) {
	c := Candidate{Kind: KindEllipse, P: [6]float32{10, 20, 5, 3, 45, 0}, Color: RGBA{1, 0, 0, 1}}
	s := c.ToShape(0.5)
	if s.Type != TypeRotatedEllipse {
		t.Fatalf("type = %d, want %d", s.Type, TypeRotatedEllipse)
	}
	want := []float64{10, 20, 5, 3, 45}
	if len(s.Data) != 5 {
		t.Fatalf("data len = %d, want 5", len(s.Data))
	}
	for i := range want {
		if s.Data[i] != want[i] {
			t.Fatalf("data[%d] = %v, want %v", i, s.Data[i], want[i])
		}
	}
	if s.Color[0] != 255 || s.Color[1] != 0 || s.Color[2] != 0 || s.Color[3] != 255 {
		t.Fatalf("color = %v, want [255 0 0 255]", s.Color)
	}
}

func TestF2BClamping(t *testing.T) {
	cases := []struct {
		in   float32
		want int
	}{
		{-0.1, 0}, {0, 0}, {0.5, 128}, {1.0, 255}, {1.1, 255},
	}
	for _, c := range cases {
		if got := F2B(c.in); got != c.want {
			t.Errorf("F2B(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestGeometryJSONSchema(t *testing.T) {
	g := Geometry{Shapes: []Shape{{Type: 1, Data: []float64{0, 0, 8, 8}, Color: []int{0, 0, 0, 255}, Score: 1}}}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Geometry
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Shapes) != 1 || decoded.Shapes[0].Type != 1 {
		t.Fatalf("roundtrip mismatch: %s", string(b))
	}
}
