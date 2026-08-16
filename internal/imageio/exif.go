package imageio

import (
	"encoding/binary"
	"image"
	"io"
)

// EXIF orientation. Phone JPEGs store the sensor's raw pixels plus a rotation tag; every viewer
// (Explorer, the Flutter client's decoder) applies it, but Go's image/jpeg does not — so the
// engine used to fit, preview and inject a picture rotated 90°/180° from what the user saw
// everywhere else. This is the minimal reader (JPEG APP1 → TIFF IFD0 → tag 0x0112) plus the
// eight standard transforms; anything unexpected degrades to "no rotation".

// decodeOriented decodes like image.Decode but applies the JPEG EXIF orientation first.
func decodeOriented(f io.ReadSeeker) (image.Image, string, error) {
	o := exifOrientation(f)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	img, kind, err := image.Decode(f)
	if err != nil {
		return nil, kind, err
	}
	if o > 1 && o <= 8 {
		img = applyOrientation(img, o)
	}
	return img, kind, nil
}

// exifOrientation returns the EXIF orientation (1..8) or 1 when absent/unparseable.
func exifOrientation(r io.ReadSeeker) int {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 1
	}
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil || soi[0] != 0xFF || soi[1] != 0xD8 {
		return 1 // not a JPEG
	}
	for seg := 0; seg < 64; seg++ { // segment cap: a sane header fits in far fewer
		var mk [2]byte
		if _, err := io.ReadFull(r, mk[:]); err != nil || mk[0] != 0xFF {
			return 1
		}
		if mk[1] == 0xDA || mk[1] == 0xD9 { // start of scan / end: no APP1 came
			return 1
		}
		var ln [2]byte
		if _, err := io.ReadFull(r, ln[:]); err != nil {
			return 1
		}
		n := int(binary.BigEndian.Uint16(ln[:])) - 2
		if n < 0 {
			return 1
		}
		if mk[1] != 0xE1 { // not APP1: skip
			if _, err := r.Seek(int64(n), io.SeekCurrent); err != nil {
				return 1
			}
			continue
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return 1
		}
		return orientationFromAPP1(buf)
	}
	return 1
}

func orientationFromAPP1(b []byte) int {
	if len(b) < 14 || string(b[0:6]) != "Exif\x00\x00" {
		return 1
	}
	t := b[6:] // TIFF header
	var ord binary.ByteOrder
	switch {
	case t[0] == 'I' && t[1] == 'I':
		ord = binary.LittleEndian
	case t[0] == 'M' && t[1] == 'M':
		ord = binary.BigEndian
	default:
		return 1
	}
	if len(t) < 8 || ord.Uint16(t[2:4]) != 42 {
		return 1
	}
	off := int(ord.Uint32(t[4:8]))
	if off < 0 || off+2 > len(t) {
		return 1
	}
	cnt := int(ord.Uint16(t[off : off+2]))
	for i := 0; i < cnt; i++ {
		e := off + 2 + i*12
		if e+12 > len(t) {
			return 1
		}
		if ord.Uint16(t[e:e+2]) == 0x0112 { // Orientation, SHORT, count 1: value inline
			v := int(ord.Uint16(t[e+8 : e+10]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// applyOrientation returns a fresh NRGBA with the EXIF transform applied (straight alpha, so
// the rest of the pipeline sees exactly what a normal upright decode would have produced).
func applyOrientation(img image.Image, o int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	ow, oh := w, h
	if o >= 5 { // the four transposed orientations swap the axes
		ow, oh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // mirror horizontal
				dx, dy = w-1-x, y
			case 3: // rotate 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirror vertical
				dx, dy = x, h-1-y
			case 5: // transpose
				dx, dy = y, x
			case 6: // rotate 90 CW
				dx, dy = h-1-y, x
			case 7: // transverse
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 270 CW
				dx, dy = y, w-1-x
			default:
				dx, dy = x, y
			}
			dst.Set(dx, dy, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
