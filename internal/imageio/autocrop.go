package imageio

import (
	"image"
	"image/draw"
	"os"
)

// AutoCropRect returns the tight bounding box of the image's CONTENT — the region worth
// reconstructing — so the caller can crop away uniform/empty margins BEFORE downscaling, letting the
// content fill the render (more pixels, and shapes, per feature). Detection is content-adaptive:
//   - transparent images (cutouts): pixels whose alpha exceeds a threshold (the silhouette).
//   - opaque images: pixels that differ from the BORDER colour (the 4-corner average) by more than a
//     threshold (a solid frame / letterbox / uniform background margin).
//
// A small margin is added so the crop never clips the content's anti-aliased edge.
//
// GUARDS (so it never makes a full-bleed image worse): if no content is found, or the detected box
// already covers ≥97% of the area (i.e. there is no real border to trim), the FULL bounds are
// returned — a no-op. The thresholds are deliberately conservative to avoid trimming INTO content
// that happens to match the border colour.
func AutoCropRect(img image.Image) image.Rectangle {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 8 || h < 8 {
		return b
	}
	// Normalise to straight-alpha NRGBA for direct, predictable pixel access.
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(nrgba, nrgba.Bounds(), img, b.Min, draw.Src)
	at := func(x, y int) (r, g, bl, a int) {
		i := (y*w + x) * 4
		return int(nrgba.Pix[i]), int(nrgba.Pix[i+1]), int(nrgba.Pix[i+2]), int(nrgba.Pix[i+3])
	}

	// Transparent vs opaque — same ~0.5%-near-transparent rule as PrepareFromImage.
	transparentN := 0
	for i := 0; i < w*h; i++ {
		if nrgba.Pix[i*4+3] < 128 {
			transparentN++
		}
	}
	transparent := transparentN*200 > w*h

	// Border colour = average of the four corner pixels (used only for the opaque case).
	var br, bg, bb int
	for _, c := range [4][2]int{{0, 0}, {w - 1, 0}, {0, h - 1}, {w - 1, h - 1}} {
		r, g, bl, _ := at(c[0], c[1])
		br += r
		bg += g
		bb += bl
	}
	br, bg, bb = br/4, bg/4, bb/4

	const alphaThresh = 24 // /255: "content" if more opaque than this (cutout)
	const colorThresh = 36 // sum |Δ| over RGB vs the border colour (opaque)

	isContent := func(x, y int) bool {
		r, g, bl, a := at(x, y)
		if transparent {
			return a > alphaThresh
		}
		return absI(r-br)+absI(g-bg)+absI(bl-bb) > colorThresh
	}

	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if isContent(x, y) {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x > maxX {
					maxX = x
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < minX || maxY < minY {
		return b // no content found anywhere — don't crop
	}

	// Small safety margin (~0.5% of the mean side) so the AA edge survives the crop.
	m := (w + h) / 200
	minX, minY = clamp(minX-m, 0, w-1), clamp(minY-m, 0, h-1)
	maxX, maxY = clamp(maxX+m, 0, w-1), clamp(maxY+m, 0, h-1)
	cw, ch := maxX-minX+1, maxY-minY+1

	// GUARD: nothing meaningful to trim (full-bleed) → no-op.
	if cw*ch*100 >= w*h*97 {
		return b
	}
	return image.Rect(b.Min.X+minX, b.Min.Y+minY, b.Min.X+maxX+1, b.Min.Y+maxY+1)
}

// LoadAutoCropped decodes path, auto-crops to the content bbox (AutoCropRect — a no-op when there is
// no uniform border to trim), then prepares the crop at maxRes. It returns the crop rect (source px)
// so the caller can report what was trimmed. Cropping BEFORE the downscale is the whole point: the
// content fills the render, so its features occupy more pixels (and shapes) than inside the full
// frame — the same crispness mechanism as LoadRegion, applied automatically.
func LoadAutoCropped(path string, maxRes int) (*Prepared, image.Rectangle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, image.Rectangle{}, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, image.Rectangle{}, err
	}
	rect := AutoCropRect(img)
	if rect.Eq(img.Bounds()) {
		return PrepareFromImage(img, maxRes), rect, nil // nothing to trim
	}
	crop := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(crop, crop.Bounds(), img, rect.Min, draw.Src)
	return PrepareFromImage(crop, maxRes), rect, nil
}

// LoadRegionAutoCropped decodes path, then crops the fractional sub-rectangle [fx,fy,fw,fh] taken
// relative to AutoCropRect(img) — the SAME content rectangle the Studio shows after loadImage's
// auto-crop, not the raw file bounds. This keeps a crop the user draws on the displayed source aligned
// with the region that gets reconstructed, while still cropping at FULL resolution before the maxRes
// downscale (so the region fills the render and stays crisp). Returns the source-px crop rect.
func LoadRegionAutoCropped(path string, maxRes int, fx, fy, fw, fh float64) (*Prepared, image.Rectangle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, image.Rectangle{}, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, image.Rectangle{}, err
	}
	sub := subRect(AutoCropRect(img), fx, fy, fw, fh)
	crop := image.NewRGBA(image.Rect(0, 0, sub.Dx(), sub.Dy()))
	draw.Draw(crop, crop.Bounds(), img, sub.Min, draw.Src)
	return PrepareFromImage(crop, maxRes), sub, nil
}

// subRect maps the fractional rectangle [fx,fy,fw,fh] of base into absolute pixels, clamped to base
// with a minimum 1px extent so a degenerate selection never produces an empty image.
func subRect(base image.Rectangle, fx, fy, fw, fh float64) image.Rectangle {
	W, H := base.Dx(), base.Dy()
	x0 := base.Min.X + clamp(int(fx*float64(W)), 0, W-1)
	y0 := base.Min.Y + clamp(int(fy*float64(H)), 0, H-1)
	x1 := x0 + max(1, int(fw*float64(W)))
	y1 := y0 + max(1, int(fh*float64(H)))
	if x1 > base.Max.X {
		x1 = base.Max.X
	}
	if y1 > base.Max.Y {
		y1 = base.Max.Y
	}
	return image.Rect(x0, y0, x1, y1)
}

// LoadAbsRegion decodes path and crops the ABSOLUTE pixel rectangle abs (in the raw source's
// coordinates), then prepares it at maxRes. Unlike LoadRegion/LoadAutoCropped it applies no auto-crop
// and no fractional mapping — the caller supplies the exact source rect. This is the crop-tool's
// source-swap primitive: each Apply re-decodes the original at the composed absolute rect, so repeated
// crops never compound downscale loss. A degenerate/empty rect falls back to the full bounds.
func LoadAbsRegion(path string, maxRes int, abs image.Rectangle) (*Prepared, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	abs = abs.Intersect(img.Bounds())
	if abs.Dx() < 1 || abs.Dy() < 1 {
		abs = img.Bounds()
	}
	crop := image.NewRGBA(image.Rect(0, 0, abs.Dx(), abs.Dy()))
	draw.Draw(crop, crop.Bounds(), img, abs.Min, draw.Src)
	return PrepareFromImage(crop, maxRes), nil
}

// SubAbs maps the fractional rectangle [fx,fy,fw,fh] of base into an absolute pixel rect (clamped to
// base) — used to compose a new crop's source rect from the current view's rect and the user's
// fractional selection on screen.
func SubAbs(base image.Rectangle, fx, fy, fw, fh float64) image.Rectangle {
	W, H := base.Dx(), base.Dy()
	x0 := base.Min.X + int(fx*float64(W))
	y0 := base.Min.Y + int(fy*float64(H))
	x1 := x0 + int(fw*float64(W))
	y1 := y0 + int(fh*float64(H))
	return image.Rect(x0, y0, x1, y1).Intersect(base)
}

func absI(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
