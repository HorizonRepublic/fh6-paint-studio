package imageio

import (
	"encoding/json"
	"image"
	"image/png"
	"math"
	"os"

	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/raster"
)

func WriteGeometry(path string, g model.Geometry) error {
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
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
			px[i+0] = float32(r) / 65535
			px[i+1] = float32(g) / 65535
			px[i+2] = float32(bl) / 65535
			px[i+3] = float32(a) / 65535
		}
	}
	return px, w, h, nil
}

// scaleParams scales a shape's raster parameters by ss for SSAA rendering. Position
// and size scale; the rotation angle (slot 4 for ellipse/rectangle) does not.
func scaleParams(kind model.ShapeKind, p [6]float32, ss float32) [6]float32 {
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
		invA := 1 - a
		xMin, yMin, xMax, yMax := raster.BBox(kind, p, W, H)
		for y := yMin; y <= yMax; y++ {
			for x := xMin; x <= xMax; x++ {
				if !raster.Inside(kind, p, x, y) {
					continue
				}
				q := (y*W + x) * 4
				canvas[q+0] = canvas[q+0]*invA + cr*a
				canvas[q+1] = canvas[q+1]*invA + cg*a
				canvas[q+2] = canvas[q+2]*invA + cb*a
				canvas[q+3] = canvas[q+3]*invA + a
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
				lin[o+0], lin[o+1], lin[o+2], lin[o+3] = r*inv, g*inv, b*inv, a*inv
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
