package imageio

import (
	"math/rand"
	"testing"

	"fh6-paint-studio/internal/model"
)

// The preview encoder trades a pow per channel for a quantised table plus a boundary correction.
// It must land on exactly the same display byte as the pow path.
func TestEncodeDisplayBytesMatchesExact(t *testing.T) {
	old := model.LinearLight
	model.LinearLight = true
	defer func() { model.LinearLight = old }()

	rng := rand.New(rand.NewSource(3))
	px := make([]float32, 4*20000)
	for i := range px {
		switch i % 7 {
		case 0:
			px[i] = 0
		case 1:
			px[i] = 1
		case 2:
			px[i] = rng.Float32() * 0.01 // the linear toe, where the curve is steepest
		default:
			px[i] = rng.Float32()
		}
	}
	dst := make([]uint8, len(px))
	EncodeDisplayBytes(px, dst)
	exact := EncodeForDisplay(px)
	off := 0
	for i := range px {
		want := u8(exact[i])
		if i%4 == 3 {
			want = u8(px[i]) // alpha is not encoded
		}
		if dst[i] != want {
			off++
			if off < 5 {
				t.Errorf("channel %d: got %d, exact %d (linear %v)", i, dst[i], want, px[i])
			}
		}
	}
	if off > 0 {
		t.Fatalf("%d of %d channels disagree with the exact encode", off, len(px))
	}
}
