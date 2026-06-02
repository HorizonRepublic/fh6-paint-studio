package model

import "testing"

func eqData(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestToShapeRectangle(t *testing.T) {
	c := Candidate{Kind: KindRectangle, P: [6]float32{5, 6, 7, 8, 30, 0}, Color: RGBA{1, 1, 1, 1}}
	s := c.ToShape(0)
	if s.Type != TypeRotatedRectangle {
		t.Fatalf("type=%d want %d", s.Type, TypeRotatedRectangle)
	}
	if !eqData(s.Data, []float64{5, 6, 7, 8, 30}) {
		t.Fatalf("data=%v", s.Data)
	}
}

func TestToShapeTriangle(t *testing.T) {
	c := Candidate{Kind: KindTriangle, P: [6]float32{1, 2, 3, 4, 5, 6}}
	s := c.ToShape(0)
	if s.Type != TypeTriangle {
		t.Fatalf("type=%d want %d", s.Type, TypeTriangle)
	}
	if !eqData(s.Data, []float64{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("data=%v", s.Data)
	}
}

func TestToShapeLine(t *testing.T) {
	c := Candidate{Kind: KindLine, P: [6]float32{1, 2, 3, 4, 5, 0}}
	s := c.ToShape(0)
	if s.Type != TypeLine {
		t.Fatalf("type=%d want %d", s.Type, TypeLine)
	}
	if !eqData(s.Data, []float64{1, 2, 3, 4, 5}) {
		t.Fatalf("data=%v", s.Data)
	}
}
