package inject

import (
	"encoding/binary"
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestShapeWordConstants(t *testing.T) {
	cases := []struct {
		got  uint16
		want uint16
	}{
		{WordSquare, 0x0065}, {WordCircle, 0x0066}, {WordTriangle, 0x0068},
		{WordCircleBorder, 0x0070}, {WordEllipse, 0x0088},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("shape word = 0x%04x, want 0x%04x", c.got, c.want)
		}
	}
}

func TestShapeToLayerKinds(t *testing.T) {
	cm := CanvasMap{W: 100, H: 100, K: 1, Base: 80}
	cases := []struct {
		name string
		s    model.Shape
		want uint16
		ok   bool
	}{
		{"ellipse", model.Shape{Type: model.TypeRotatedEllipse, Data: []float64{50, 50, 10, 5, 0}, Color: []int{1, 2, 3, 4}}, WordCircle, true}, // ellipse = circle + non-uniform scale (0x88 is a crescent)
		{"circle", model.Shape{Type: model.TypeRotatedEllipse, Data: []float64{50, 50, 8, 8, 0}, Color: []int{1, 2, 3, 4}}, WordCircle, true},
		{"rect", model.Shape{Type: model.TypeRotatedRectangle, Data: []float64{50, 50, 20, 10, 30}, Color: []int{1, 2, 3, 4}}, WordSquare, true},
		{"triangle", model.Shape{Type: model.TypeTriangle, Data: []float64{40, 40, 60, 40, 50, 60}, Color: []int{1, 2, 3, 4}}, WordTriangle, true},
		{"line", model.Shape{Type: model.TypeLine, Data: []float64{0, 0, 10, 10, 1}, Color: []int{1, 2, 3, 4}}, 0, false},
	}
	for _, c := range cases {
		lw, ok := ShapeToLayer(c.s, cm)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
			continue
		}
		if ok && lw.Word != c.want {
			t.Errorf("%s: word = 0x%04x, want 0x%04x", c.name, lw.Word, c.want)
		}
	}
}

func TestShapeToLayerCoords(t *testing.T) {
	// W=H=100, 2 units/pixel, scale base 10 (clean math). Editor space: centre origin, Y up.
	cm := CanvasMap{W: 100, H: 100, K: 2, Base: 10}
	// An ellipse at the image centre maps to the editor origin; half-sizes become scale multipliers
	// (half_px*K/Base); rotation is negated by the Y flip.
	lw, _ := ShapeToLayer(model.Shape{Type: model.TypeRotatedEllipse, Data: []float64{50, 50, 10, 5, 45}, Color: []int{10, 20, 30, 255}}, cm)
	if lw.X != 0 || lw.Y != 0 {
		t.Errorf("centre maps to (%.3f,%.3f), want (0,0)", lw.X, lw.Y)
	}
	if lw.SX != 2 || lw.SY != 1 { // 10*2/10=2, 5*2/10=1
		t.Errorf("scale = (%.3f,%.3f), want (2,1)", lw.SX, lw.SY)
	}
	if lw.Rotation != -45 {
		t.Errorf("rotation = %.1f, want -45 (Y flip)", lw.Rotation)
	}
	if lw.Color != [4]byte{10, 20, 30, 255} {
		t.Errorf("color = %v", lw.Color)
	}
	// An off-centre shape: X right of centre is +, Y above centre (smaller image-y) is +.
	lw2, _ := ShapeToLayer(model.Shape{Type: model.TypeRotatedEllipse, Data: []float64{60, 40, 5, 5, 0}, Color: []int{0, 0, 0, 255}}, cm)
	if lw2.X != 20 || lw2.Y != 20 { // (60-50)*2=20 ; (50-40)*2=20 (image-y 40 is above centre -> +y)
		t.Errorf("off-centre map = X%.1f Y%.1f, want X20 Y20", lw2.X, lw2.Y)
	}
	// The background rectangle [x,y,w,h] centres on the canvas; half-sizes become scale multipliers.
	bg, _ := ShapeToLayer(model.Shape{Type: model.TypeRectangle, Data: []float64{0, 0, 100, 100}, Color: []int{1, 2, 3, 255}}, cm)
	if bg.X != 0 || bg.Y != 0 || bg.SX != 10 || bg.SY != 10 { // half 50 px -> 50*2/10 = 10
		t.Errorf("bg map = X%.1f Y%.1f SX%.1f SY%.1f, want 0,0,10,10", bg.X, bg.Y, bg.SX, bg.SY)
	}
}

func TestLayerWrites(t *testing.T) {
	p := FH6Profile()
	lw := LayerWrite{X: 1, Y: 2, SX: 3, SY: 4, Rotation: 5, Skew: 0, Color: [4]byte{9, 8, 7, 6}, Mask: true, Word: WordSquare}
	ws := lw.Writes(p)
	if len(ws) != 7 {
		t.Fatalf("got %d writes, want 7", len(ws))
	}
	byOffset := map[int][]byte{}
	for _, w := range ws {
		byOffset[w.Offset] = w.Data
	}
	// Shape word is a little-endian uint16 at 0x7A.
	sw, ok := byOffset[0x7A]
	if !ok || len(sw) != 2 || binary.LittleEndian.Uint16(sw) != WordSquare {
		t.Errorf("shape-word write = %v at 0x7A, want LE %04x", sw, WordSquare)
	}
	// Position is 2 float32 at 0x18.
	pos, ok := byOffset[0x18]
	if !ok || len(pos) != 8 {
		t.Fatalf("position write missing/short at 0x18: %v", pos)
	}
	if got := math.Float32frombits(binary.LittleEndian.Uint32(pos[0:])); got != 1 {
		t.Errorf("pos.x = %v, want 1", got)
	}
	// Mask is one byte at 0x78.
	if m := byOffset[0x78]; len(m) != 1 || m[0] != 1 {
		t.Errorf("mask write = %v at 0x78, want [1]", m)
	}
	// Color is 4 bytes at 0x74.
	if col := byOffset[0x74]; len(col) != 4 || col[0] != 9 || col[3] != 6 {
		t.Errorf("color write = %v at 0x74", col)
	}
}
