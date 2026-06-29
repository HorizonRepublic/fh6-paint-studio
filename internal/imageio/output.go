package imageio

import (
	"encoding/json"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

func WriteGeometry(path string, g model.Geometry) error {
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	// Atomic: temp file + rename, so a crash / full disk leaves either the old geometry or the
	// complete new one — never a torn JSON that fails to parse on reload.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".geom-*")
	if err != nil {
		return os.WriteFile(path, b, 0o644) // dir not writable for a temp -> best-effort direct write
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(name)
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// ReadGeometry loads an importer-compatible geometry JSON ({"shapes":[...]}). Used
// to score/compare externally-produced outputs against a target through this renderer,
// for an apples-to-apples comparison.
func ReadGeometry(path string) (model.Geometry, error) {
	var g model.Geometry
	b, err := os.ReadFile(path)
	if err != nil {
		return g, err
	}
	err = json.Unmarshal(b, &g)
	return g, err
}

// LoadRGBAFloat decodes a PNG to straight-alpha float RGBA in [0,1] at its native
// resolution (NO downscale) — used by the image-space comparison (-img-vs), where
// two finished renders must be diffed pixel-for-pixel against the same target.
func LoadRGBAFloat(path string) ([]float32, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, 0, 0, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	px := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA() // 16-bit premultiplied
			i := (y*w + x) * 4
			px[i+3] = float32(a) / 65535
			if a > 0 { // un-premultiply to straight alpha (RGB undefined where a==0)
				px[i+0] = float32(r) / float32(a)
				px[i+1] = float32(g) / float32(a)
				px[i+2] = float32(bl) / float32(a)
			}
		}
	}
	return px, w, h, nil
}

// scaleParams scales a shape's raster parameters by ss for SSAA rendering. Position
// and size scale; the rotation angle (slot 4 for ellipse/rectangle) does not.
func scaleParams(kind model.ShapeKind, p [6]float32, ss float32) [6]float32 {
	if model.IsMask(kind) {
		// mask: [cx, cy, Hx, Hy, rotDeg, skew] — position + screen extents scale with SSAA; the
		// rotation and skew are dimensionless and pass through unchanged (the default branch would
		// zero slot 5 and kill the skew).
		return [6]float32{p[0] * ss, p[1] * ss, p[2] * ss, p[3] * ss, p[4], p[5]}
	}
	switch kind {
	case model.KindTriangle:
		return [6]float32{p[0] * ss, p[1] * ss, p[2] * ss, p[3] * ss, p[4] * ss, p[5] * ss}
	case model.KindLine:
		return [6]float32{p[0] * ss, p[1] * ss, p[2] * ss, p[3] * ss, p[4] * ss, 0}
	default: // ellipse / rectangle: [cx, cy, a, b, thetaDeg, _]
		return [6]float32{p[0] * ss, p[1] * ss, p[2] * ss, p[3] * ss, p[4], 0}
	}
}

// RenderFH6 renders the shape list the way the GAME composites it: in LINEAR light. Each shape's
// stored sRGB colour is decoded to linear, alpha-blended in linear over a linear background, the
// ss× buffer box-downsampled in linear, then sRGB-encoded for display.
// shapes[0] is the background rectangle (its sRGB colour fills the canvas; transparentBG keeps it
// empty). This is FIXED linear-light regardless of the engine's working space — it is the ground-truth
// "how it looks in-game" render. Score it vs the sRGB target to measure the real in-game fidelity (the
// semi-transparent "pop").
func RenderFH6(shapes []model.Shape, transparentBG bool, w, h, ss int) []float32 {
	if ss < 1 {
		ss = 1
	}
	W, H := w*ss, h*ss
	canvas := make([]float32, W*H*4)
	if !transparentBG && len(shapes) > 0 && len(shapes[0].Color) >= 4 {
		br := model.SRGBToLinear(float32(shapes[0].Color[0]) / 255)
		bg := model.SRGBToLinear(float32(shapes[0].Color[1]) / 255)
		bb := model.SRGBToLinear(float32(shapes[0].Color[2]) / 255)
		for i := 0; i < W*H; i++ {
			canvas[i*4+0], canvas[i*4+1], canvas[i*4+2], canvas[i*4+3] = br, bg, bb, 1
		}
	}
	fss := float32(ss)
	for si := 1; si < len(shapes); si++ {
		s := shapes[si]
		if len(s.Color) < 4 {
			continue
		}
		kind := model.KindFromType(s.Type)
		p := scaleParams(kind, model.ParamsFromShape(s), fss)
		cr := model.SRGBToLinear(float32(s.Color[0]) / 255)
		cg := model.SRGBToLinear(float32(s.Color[1]) / 255)
		cb := model.SRGBToLinear(float32(s.Color[2]) / 255)
		a := float32(s.Color[3]) / 255
		if a <= 0 {
			continue
		}
		isGrad := raster.IsGradient(kind)
		xMin, yMin, xMax, yMax := raster.BBox(kind, p, W, H)
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				// Gradient kinds composite with their baked per-pixel falloff so the preview matches
				// the in-game render; hard kinds use binary coverage (aEff = a, unchanged).
				aEff := a
				if isGrad {
					cov := float32(raster.Coverage(kind, p, x, y))
					if cov <= 0 {
						continue
					}
					aEff = a * cov
				} else if !raster.Inside(kind, p, x, y) {
					continue
				}
				ia := 1 - aEff
				q := (y*W + x) * 4
				canvas[q+0] = canvas[q+0]*ia + cr*aEff
				canvas[q+1] = canvas[q+1]*ia + cg*aEff
				canvas[q+2] = canvas[q+2]*ia + cb*aEff
				canvas[q+3] = canvas[q+3]*ia + aEff
			}
		}
	}
	lin := canvas
	if ss > 1 {
		lin = make([]float32, w*h*4)
		inv := 1.0 / float32(ss*ss)
		for oy := 0; oy < h; oy++ {
			for ox := 0; ox < w; ox++ {
				var r, g, b, a float32
				for dy := 0; dy < ss; dy++ {
					for dx := 0; dx < ss; dx++ {
						q := ((oy*ss+dy)*W + (ox*ss + dx)) * 4
						r += canvas[q+0]
						g += canvas[q+1]
						b += canvas[q+2]
						a += canvas[q+3]
					}
				}
				o := (oy*w + ox) * 4
				// The composite buffer holds PREMULTIPLIED colour over a transparent background
				// (canvas = canvas*ia + cr*aEff, bg starts 0). Un-premultiply on downsample —
				// divide the summed colour by the summed alpha — so antialiased edge pixels keep
				// full-saturation colour at fractional alpha instead of darkening. Reduces to a plain
				// box-average over an opaque bg (alpha is 1 everywhere there).
				if a > 0 {
					lin[o+0], lin[o+1], lin[o+2] = r/a, g/a, b/a
				}
				lin[o+3] = a * inv
			}
		}
	}
	out := make([]float32, w*h*4)
	for i := 0; i < w*h; i++ {
		out[i*4+0] = model.LinearToSRGB(lin[i*4+0])
		out[i*4+1] = model.LinearToSRGB(lin[i*4+1])
		out[i*4+2] = model.LinearToSRGB(lin[i*4+2])
		out[i*4+3] = lin[i*4+3] // alpha straight
	}
	return out
}

// RenderFH6Image is RenderFH6 packed into an *image.NRGBA (sRGB display bytes), shared by the studio
// preview path and the editor. RenderFH6 already returns sRGB-display floats with straight alpha, so
// the pack is a straight per-channel clamp via u8.
func RenderFH6Image(shapes []model.Shape, transparentBG bool, w, h, ss int) *image.NRGBA {
	buf := RenderFH6(shapes, transparentBG, w, h, ss)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h && i*4+3 < len(buf); i++ {
		img.Pix[i*4+0] = u8(buf[i*4+0])
		img.Pix[i*4+1] = u8(buf[i*4+1])
		img.Pix[i*4+2] = u8(buf[i*4+2])
		img.Pix[i*4+3] = u8(buf[i*4+3])
	}
	return img
}

// CompositeShapeOnto composites a single shape over an existing sRGB-display NRGBA in linear light,
// matching RenderFH6's per-shape blend. The editor uses it to redraw only the shape being dragged on
// top of a pre-rendered base (the other shapes), so live deformation stays smooth at any doc size.
func CompositeShapeOnto(img *image.NRGBA, s model.Shape, w, h int) {
	if img == nil || len(s.Color) < 4 {
		return
	}
	a := float32(s.Color[3]) / 255
	if a <= 0 {
		return
	}
	kind := model.KindFromType(s.Type)
	p := scaleParams(kind, model.ParamsFromShape(s), 1)
	cr := model.SRGBToLinear(float32(s.Color[0]) / 255)
	cg := model.SRGBToLinear(float32(s.Color[1]) / 255)
	cb := model.SRGBToLinear(float32(s.Color[2]) / 255)
	isGrad := raster.IsGradient(kind)
	xMin, yMin, xMax, yMax := raster.BBox(kind, p, w, h)
	for y := yMin; y <= yMax; y++ {
		for x := xMin; x <= xMax; x++ {
			aEff := a
			if isGrad {
				cov := float32(raster.Coverage(kind, p, x, y))
				if cov <= 0 {
					continue
				}
				aEff = a * cov
			} else if !raster.Inside(kind, p, x, y) {
				continue
			}
			q := (y*w + x) * 4
			br := model.SRGBToLinear(float32(img.Pix[q+0]) / 255)
			bg := model.SRGBToLinear(float32(img.Pix[q+1]) / 255)
			bb := model.SRGBToLinear(float32(img.Pix[q+2]) / 255)
			ba := float32(img.Pix[q+3]) / 255
			ia := 1 - aEff
			img.Pix[q+0] = u8(model.LinearToSRGB(br*ia + cr*aEff))
			img.Pix[q+1] = u8(model.LinearToSRGB(bg*ia + cg*aEff))
			img.Pix[q+2] = u8(model.LinearToSRGB(bb*ia + cb*aEff))
			img.Pix[q+3] = u8(ba*ia + aEff)
		}
	}
}

// RenderFH6ImageSkip is RenderFH6Image with one shape index omitted (the editor's drag base: every
// shape except the one being dragged).
func RenderFH6ImageSkip(shapes []model.Shape, transparentBG bool, w, h, skip int) *image.NRGBA {
	if skip < 0 || skip >= len(shapes) {
		return RenderFH6Image(shapes, transparentBG, w, h, 1)
	}
	rest := make([]model.Shape, 0, len(shapes)-1)
	rest = append(rest, shapes[:skip]...)
	rest = append(rest, shapes[skip+1:]...)
	return RenderFH6Image(rest, transparentBG, w, h, 1)
}

// EncodeForDisplay sRGB-encodes a linear-light RGBA buffer (the engine's working canvas in -linear
// mode) for preview/PNG output; alpha stays straight. A no-op when not in linear mode.
func EncodeForDisplay(px []float32) []float32 {
	if !model.LinearLight {
		return px
	}
	out := make([]float32, len(px))
	for i := 0; i+3 < len(px); i += 4 {
		out[i+0] = model.LinearToSRGB(px[i+0])
		out[i+1] = model.LinearToSRGB(px[i+1])
		out[i+2] = model.LinearToSRGB(px[i+2])
		out[i+3] = px[i+3]
	}
	return out
}

func SavePreview(path string, px []float32, w, h int) error {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[i*4+0] = u8(px[i*4+0])
		img.Pix[i*4+1] = u8(px[i*4+1])
		img.Pix[i*4+2] = u8(px[i*4+2])
		img.Pix[i*4+3] = u8(px[i*4+3])
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func u8(v float32) uint8 {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return uint8(math.Round(float64(v) * 255))
}
