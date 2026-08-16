package imageio

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// buildJPEGWithOrientation encodes a 2x1 image (red left, blue right) and splices a minimal
// EXIF APP1 with the given orientation right after SOI — the exact layout phone JPEGs use.
func buildJPEGWithOrientation(t *testing.T, o byte) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.NRGBA{255, 0, 0, 255})
	img.Set(1, 0, color.NRGBA{0, 0, 255, 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	app1 := []byte{
		'E', 'x', 'i', 'f', 0, 0,
		'M', 'M', 0, 42, 0, 0, 0, 8, // TIFF big-endian, IFD0 at 8
		0, 1, // one entry
		0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, o, 0, 0, // Orientation SHORT=o
		0, 0, 0, 0, // next IFD
	}
	seg := append([]byte{0xFF, 0xE1, byte((len(app1) + 2) >> 8), byte((len(app1) + 2) & 0xFF)}, app1...)
	out := append([]byte{0xFF, 0xD8}, seg...)
	return append(out, raw[2:]...)
}

func TestExifOrientationParsesAndApplies(t *testing.T) {
	for _, o := range []byte{1, 3, 6, 8} {
		data := buildJPEGWithOrientation(t, o)
		r := bytes.NewReader(data)
		if got := exifOrientation(r); got != int(o) {
			t.Fatalf("orientation %d parsed as %d", o, got)
		}
		r2 := bytes.NewReader(data)
		img, _, err := decodeOriented(r2)
		if err != nil {
			t.Fatalf("decode o=%d: %v", o, err)
		}
		b := img.Bounds()
		// 90/270 rotations swap the axes; identity/180 keep them.
		wantW, wantH := 2, 1
		if o == 6 || o == 8 {
			wantW, wantH = 1, 2
		}
		if b.Dx() != wantW || b.Dy() != wantH {
			t.Fatalf("o=%d: dims %dx%d, want %dx%d", o, b.Dx(), b.Dy(), wantW, wantH)
		}
	}
	// A plain JPEG (no APP1) must read as orientation 1 untouched.
	plain := buildJPEGWithOrientation(t, 1)[0:2]
	var buf bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	_ = jpeg.Encode(&buf, img, nil)
	plain = append(plain[:0], buf.Bytes()...)
	if got := exifOrientation(bytes.NewReader(plain)); got != 1 {
		t.Fatalf("plain jpeg orientation = %d", got)
	}
	// Rotation semantics: orientation 6 (90° CW) puts the LEFT-red pixel at the TOP.
	img6, _, err := decodeOriented(bytes.NewReader(buildJPEGWithOrientation(t, 6)))
	if err != nil {
		t.Fatal(err)
	}
	r0, _, b0, _ := img6.At(0, 0).RGBA()
	if r0 < b0 { // top pixel must be the red one
		t.Fatalf("orientation 6: expected red on top, got r=%d b=%d", r0, b0)
	}
}
