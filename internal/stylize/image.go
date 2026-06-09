package stylize

import (
	"fmt"
	"image"
	_ "image/jpeg" // allow mixed-format banks
	_ "image/png"
	"os"

	xdraw "golang.org/x/image/draw"

	"fh6-paint-studio/internal/model"
)

// SrcImage is the working-resolution source: row-major sRGB RGBA in [0,1].
type SrcImage struct {
	W, H int
	Pix  []model.RGBA
}

// At returns the pixel at (x,y) (no bounds check beyond the slice).
func (s *SrcImage) At(x, y int) model.RGBA { return s.Pix[y*s.W+x] }

// Load decodes an image (png/jpeg) and downscales it so the longest side ≤ maxSide (maxSide ≤ 0 keeps
// the native size), high-quality (Catmull-Rom). Pixels are returned as sRGB RGBA in [0,1].
func Load(path string, maxSide int) (*SrcImage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("stylize: decode %s: %w", path, err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxSide > 0 && (w > maxSide || h > maxSide) {
		long := w
		if h > long {
			long = h
		}
		nw, nh := w*maxSide/long, h*maxSide/long
		if nw < 1 {
			nw = 1
		}
		if nh < 1 {
			nh = 1
		}
		dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
		img, b, w, h = dst, dst.Bounds(), nw, nh
	}
	src := &SrcImage{W: w, H: h, Pix: make([]model.RGBA, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			src.Pix[y*w+x] = model.RGBA{R: float32(r) / 65535, G: float32(g) / 65535, B: float32(bl) / 65535, A: float32(a) / 65535}
		}
	}
	return src, nil
}
