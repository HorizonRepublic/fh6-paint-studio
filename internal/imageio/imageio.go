package imageio

import (
	"image"
	"image/draw"
	"os"

	"fh6-paint-studio/internal/model"

	xdraw "golang.org/x/image/draw"

	_ "image/jpeg"
	_ "image/png"

	// Extra input formats — pure-Go decoders (CGO_ENABLED=0 safe; x/image is already a dep).
	// image.Decode auto-dispatches by registered format. WebP matters most (browsers save webp).
	// GIF intentionally omitted. AVIF/HEIC need cgo → not supported.
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Prepared holds an image as linear-ordered RGBA float32 pixels in 0..1.
type Prepared struct {
	W, H            int
	Pixels          []float32 // len = W*H*4, RGBA
	Background      model.RGBA
	HasTransparency bool // true if a meaningful fraction of pixels are near-transparent
}

// Load decodes a PNG/JPEG file and prepares it (downscaling so max side <= maxRes).
func Load(path string, maxRes int) (*Prepared, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return PrepareFromImage(img, maxRes), nil
}

// LoadRegion decodes path, crops the fractional sub-rectangle [fx,fy,fw,fh] (each 0..1 of the
// FULL-resolution source), then prepares the crop at maxRes. Cropping the source BEFORE the
// maxRes downscale is what gives a region/detail recon its crispness: the region fills the
// render, so its features occupy far more pixels (and shapes) than the same region does inside
// the whole-image budget. Returns the source crop rect (px) so the caller can report placement.
func LoadRegion(path string, maxRes int, fx, fy, fw, fh float64) (*Prepared, image.Rectangle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, image.Rectangle{}, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, image.Rectangle{}, err
	}
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	cx := b.Min.X + int(fx*float64(W))
	cy := b.Min.Y + int(fy*float64(H))
	cw := int(fw * float64(W))
	ch := int(fh * float64(H))
	if cw < 1 {
		cw = 1
	}
	if ch < 1 {
		ch = 1
	}
	if cx+cw > b.Max.X {
		cw = b.Max.X - cx
	}
	if cy+ch > b.Max.Y {
		ch = b.Max.Y - cy
	}
	crop := image.NewRGBA(image.Rect(0, 0, cw, ch))
	draw.Draw(crop, crop.Bounds(), img, image.Pt(cx, cy), draw.Src)
	return PrepareFromImage(crop, maxRes), image.Rect(cx, cy, cx+cw, cy+ch), nil
}

// PadTransparent returns a copy of prep wrapped in a transparent (alpha-0) border of pad =
// round(padFrac*max(W,H)) px on every side, marked as a cutout (HasTransparency=true), plus pad. It
// emulates the source sitting on a fully transparent background: the engine's overhang/spill penalty
// (and the >60%-overhang rejection in evalShape) then bound every shape to the original content
// rectangle — a shape that balloons past the image edge pays for the transparent area it covers,
// instead of that area being free and invisible to the scorer. The padding enlarges the canvas, so the
// caller maps the result back with TranslateShapes/UnpadCanvas (using the returned pad) to recover an
// original-size reconstruction. Returns (prep, 0) when padFrac<=0.
func PadTransparent(prep *Prepared, padFrac float64) (*Prepared, int) {
	if prep == nil || padFrac <= 0 {
		return prep, 0
	}
	pad := int(padFrac*float64(max(prep.W, prep.H)) + 0.5)
	if pad < 1 {
		return prep, 0
	}
	nw, nh := prep.W+2*pad, prep.H+2*pad
	px := make([]float32, nw*nh*4) // zero value = transparent black (alpha 0) everywhere
	for y := 0; y < prep.H; y++ {
		src := y * prep.W * 4
		dst := ((y+pad)*nw + pad) * 4
		copy(px[dst:dst+prep.W*4], prep.Pixels[src:src+prep.W*4])
	}
	return &Prepared{W: nw, H: nh, Pixels: px, Background: prep.Background, HasTransparency: true}, pad
}

// TranslateShapes shifts every shape's position by (dx,dy) canvas pixels, in place, returning the
// slice. Used to map a padded reconstruction's geometry back into the original (un-padded) canvas after
// a transparent-surround run, so preview/export/inject coordinates land in the original image space.
func TranslateShapes(shapes []model.Shape, dx, dy float64) []model.Shape {
	for i := range shapes {
		d := shapes[i].Data
		switch shapes[i].Type {
		case model.TypeTriangle: // [x1,y1,x2,y2,x3,y3]
			if len(d) >= 6 {
				d[0] += dx
				d[1] += dy
				d[2] += dx
				d[3] += dy
				d[4] += dx
				d[5] += dy
			}
		case model.TypeLine: // [x1,y1,x2,y2,halfWidth]
			if len(d) >= 4 {
				d[0] += dx
				d[1] += dy
				d[2] += dx
				d[3] += dy
			}
		default: // ellipse/rect/background: [cx,cy,...]
			if len(d) >= 2 {
				d[0] += dx
				d[1] += dy
			}
		}
	}
	return shapes
}

// UnpadCanvas crops a padded reconstruction canvas back to the original w×h content rectangle (the
// inner region offset by padPx on each side), returning a fresh origin-0 image. A no-op when padPx<=0.
func UnpadCanvas(img *image.NRGBA, padPx, w, h int) *image.NRGBA {
	if img == nil || padPx <= 0 {
		return img
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), img, image.Pt(padPx, padPx), draw.Src)
	return dst
}

// PrepareFromImage converts an image.Image to a Prepared, downscaling if needed.
func PrepareFromImage(img image.Image, maxRes int) *Prepared {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if maxRes > 0 && (w > maxRes || h > maxRes) {
		scale := float64(maxRes) / float64(max(w, h))
		nw, nh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
		dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, xdraw.Src, nil)
		img, bounds, w, h = dst, dst.Bounds(), nw, nh
	}
	// Normalize to straight-alpha (non-premultiplied) so Pixels and Background
	// match the engine's straight-alpha compositing.
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(nrgba, nrgba.Bounds(), img, bounds.Min, draw.Src)

	px := make([]float32, w*h*4)
	var sr, sg, sb float64
	var opaqueN, transparentN int
	for i := 0; i < w*h; i++ {
		r := float32(nrgba.Pix[i*4+0]) / 255
		g := float32(nrgba.Pix[i*4+1]) / 255
		blu := float32(nrgba.Pix[i*4+2]) / 255
		a := float32(nrgba.Pix[i*4+3]) / 255
		// Linear-light mode: the engine composites in the space FH6 renders in, so decode the
		// sRGB target to linear here (alpha stays straight). All downstream maths is unchanged.
		if model.LinearLight {
			r, g, blu = model.SRGBToLinear(r), model.SRGBToLinear(g), model.SRGBToLinear(blu)
		}
		px[i*4+0], px[i*4+1], px[i*4+2], px[i*4+3] = r, g, blu, a
		if a < 0.5 {
			transparentN++
		} else {
			// Mean background is taken over opaque pixels only; transparent
			// pixels carry undefined RGB and would skew it.
			sr, sg, sb = sr+float64(r), sg+float64(g), sb+float64(blu)
			opaqueN++
		}
	}
	bg := model.RGBA{A: 1}
	if opaqueN > 0 {
		inv := 1.0 / float64(opaqueN)
		bg.R, bg.G, bg.B = float32(sr*inv), float32(sg*inv), float32(sb*inv)
	}
	// "Has transparency" if more than ~0.5% of pixels are near-transparent —
	// i.e. the image is a cutout with an empty background, not a solid photo.
	hasT := transparentN*200 > w*h
	return &Prepared{W: w, H: h, Pixels: px, Background: bg, HasTransparency: hasT}
}
