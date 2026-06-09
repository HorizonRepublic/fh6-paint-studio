package inject

import (
	"math"
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestShapeToLayerMaskArc(t *testing.T) {
	const word = uint16(0x1234) // synthetic test word (not a real dictionary arc)
	model.RegisterMaskWord(word, 100, 50)
	cm := DefaultCanvasMap(256, 256)
	k := cm.K

	s := model.Shape{Type: int(word), Color: []int{40, 30, 50, 255},
		Data: []float64{128, 64, 100, 50, 30, 0}} // cx,cy,Hx,Hy,rot,skew
	lw, ok := ShapeToLayer(s, cm)
	if !ok {
		t.Fatal("mask shape did not convert to a layer")
	}
	if lw.Word != word {
		t.Errorf("word = 0x%04x, want 0x%04x", lw.Word, word)
	}
	// SX = Hx*K/nativeW = 100*K/100 = K ; SY = Hy*K/nativeH = 50*K/50 = K
	if math.Abs(float64(lw.SX-k)) > 1e-4 || math.Abs(float64(lw.SY-k)) > 1e-4 {
		t.Errorf("scale = (%.4f,%.4f), want (%.4f,%.4f)", lw.SX, lw.SY, k, k)
	}
	if math.Abs(float64(lw.X-0)) > 1e-3 { // cx at canvas centre → X 0
		t.Errorf("X = %.3f, want 0", lw.X)
	}
	if math.Abs(float64(lw.Rotation-(-30))) > 1e-4 {
		t.Errorf("rotation = %.3f, want -30", lw.Rotation)
	}
}

func TestShapeToLayerMaskMirror(t *testing.T) {
	const word = uint16(0x1235)
	model.RegisterMaskWord(word, 80, 80)
	cm := DefaultCanvasMap(256, 256)
	s := model.Shape{Type: int(word), Color: []int{0, 0, 0, 255},
		Data: []float64{100, 100, -60, 60, 0, 0}} // negative Hx = mirror
	lw, ok := ShapeToLayer(s, cm)
	if !ok {
		t.Fatal("mirrored mask shape did not convert")
	}
	if lw.SX >= 0 {
		t.Errorf("mirror should give negative SX, got %.3f", lw.SX)
	}
	if lw.SY <= 0 {
		t.Errorf("SY should stay positive, got %.3f", lw.SY)
	}
}

func TestWordForTypeMask(t *testing.T) {
	const word = uint16(0x1236)
	model.RegisterMaskWord(word, 50, 50)
	if w, ok := wordForType(int(word), [6]float32{}); !ok || w != word {
		t.Errorf("wordForType(mask) = (0x%04x,%v), want (0x%04x,true)", w, ok, word)
	}
}
