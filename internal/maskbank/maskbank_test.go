package maskbank

import (
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestBankLoaded(t *testing.T) {
	all := All()
	if len(all) != 36 {
		t.Fatalf("bank has %d masks, want 36", len(all))
	}
	seen := map[uint16]bool{}
	for _, e := range all {
		if e.W != 256 || e.H != 256 {
			t.Errorf("0x%04x dims %dx%d want 256x256", e.Word, e.W, e.H)
		}
		if len(e.Cov) != e.W*e.H {
			t.Errorf("0x%04x cov len %d want %d", e.Word, len(e.Cov), e.W*e.H)
		}
		for _, c := range e.Cov {
			if c < 0 || c > 1 {
				t.Fatalf("0x%04x coverage out of range: %v", e.Word, c)
			}
		}
		if !model.IsMask(e.Kind) {
			t.Errorf("0x%04x kind %d is not a mask kind", e.Word, e.Kind)
		}
		if w, h, ok := model.MaskNative(e.Kind); !ok || w != e.NativeW || h != e.NativeH {
			t.Errorf("0x%04x native mismatch: bank %v,%v vs registry %v,%v", e.Word, e.NativeW, e.NativeH, w, h)
		}
		seen[e.Word] = true
	}
	for _, w := range []uint16{0x0066, 0x0065, 0x0068, 0x089b} { // circle, square, triangle, arc-90
		if !seen[w] {
			t.Errorf("expected word 0x%04x in bank", w)
		}
	}
}

// TestBankTriangleNotSquare guards the capture-contamination bug (a triangle that captured a square):
// the triangle fills ~half its box, the square ~all of it, so their mean coverage must differ.
func TestBankTriangleNotSquare(t *testing.T) {
	mean := func(word uint16) float64 {
		for _, e := range All() {
			if e.Word == word {
				var s float64
				for _, c := range e.Cov {
					s += float64(c)
				}
				return s / float64(len(e.Cov))
			}
		}
		t.Fatalf("word 0x%04x missing from bank", word)
		return 0
	}
	if tri := mean(0x0068); tri > 0.7 {
		t.Errorf("triangle mean coverage %.3f too high — contaminated by a square?", tri)
	}
	if sq := mean(0x0065); sq < 0.85 {
		t.Errorf("square mean coverage %.3f too low", sq)
	}
}
