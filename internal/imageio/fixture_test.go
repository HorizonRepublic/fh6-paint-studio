package imageio

import (
	"bytes"
	"image"
	"testing"
)

// TestSyntheticFixtureDeterministic guards the fingerprint's input: two renders at the same size must
// be byte-identical, and the image must carry real variation (not a flat fill).
func TestSyntheticFixtureDeterministic(t *testing.T) {
	a, ok := SyntheticFixture(96, 64).(*image.NRGBA)
	if !ok {
		t.Fatalf("SyntheticFixture returned %T, want *image.NRGBA", SyntheticFixture(96, 64))
	}
	b := SyntheticFixture(96, 64).(*image.NRGBA)
	if !bytes.Equal(a.Pix, b.Pix) {
		t.Fatal("SyntheticFixture is not deterministic: two renders differ")
	}

	first := a.Pix[0]
	varied := false
	for i := 0; i < len(a.Pix); i += 4 { // compare the R channel across pixels
		if a.Pix[i] != first {
			varied = true
			break
		}
	}
	if !varied {
		t.Fatal("SyntheticFixture is uniform; expected gradient + shapes")
	}
}
