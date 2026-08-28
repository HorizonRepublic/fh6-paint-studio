package engine

import (
	"strconv"
	"strings"

	"fh6-paint-studio/internal/model"
)

// ParseLockColor turns a lock-colour spec into a working-space colour for MONO mode: "auto" => the
// logo's dominant ink colour (sampled from px); "#RRGGBB" / "#RGB" => that exact sRGB colour decoded
// to working space. "" or an unparseable value => ok=false. Shared by the CLI flag and the studio.
func ParseLockColor(s string, px []float32, w, h int, inputHasAlpha bool) (model.RGBA, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "":
		return model.RGBA{}, false
	case "auto":
		return DominantInk(px, w, h, inputHasAlpha), true
	}
	s = strings.TrimPrefix(s, "#")
	if len(s) == 3 { // #RGB shorthand -> #RRGGBB
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return model.RGBA{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return model.RGBA{}, false
	}
	return model.RGBA{
		R: model.DecChan(int(v>>16) & 0xff),
		G: model.DecChan(int(v>>8) & 0xff),
		B: model.DecChan(int(v) & 0xff),
	}, true
}

// Mono single-colour reconstruction (brand logo / decal). When Options.LockColor is set the target
// is rewritten into a clean two-level cutout — every ink pixel becomes the EXACT lock colour, the
// rest fully transparent — and every reconstruction shape is snapped to that colour at the end of
// the run. The grey antialiased-edge shapes (a per-shape colour solve averaging across an
// ink/background boundary) then can't appear: the solve excludes transparent pixels, so over a pure
// mono mask it can only ever return the lock colour. The output is always a transparent-background
// cutout, whatever the input was. Host-side only -> golden-diff safe.

const (
	lockAlphaThreshold = 0.5  // alpha cutoff (input with alpha): >= is ink
	lockBgKeyDist2     = 0.06 // squared working-RGB distance from the sampled background past which an opaque-input pixel is ink (~0.24 euclidean)
)

// DominantInk estimates the logo's single ink colour (working-space RGB) as the mean of its ink
// pixels — the opaque pixels for an input that has alpha, else the pixels far from the sampled
// corner background. It is the default lock colour when the user gives none; white on empty input.
func DominantInk(px []float32, w, h int, inputHasAlpha bool) model.RGBA {
	var bg model.RGBA
	if !inputHasAlpha {
		bg = cornerBackground(px, w, h)
	}
	var sr, sg, sb, n float64
	for i := 0; i+3 < len(px); i += 4 {
		if isInk(px, i, bg, inputHasAlpha) {
			sr, sg, sb, n = sr+float64(px[i]), sg+float64(px[i+1]), sb+float64(px[i+2]), n+1
		}
	}
	if n == 0 {
		return model.RGBA{R: 1, G: 1, B: 1}
	}
	return model.RGBA{R: float32(sr / n), G: float32(sg / n), B: float32(sb / n)}
}

// BinarizeForLock rewrites px in place (working-space straight-alpha RGBA, len w*h*4) into a
// two-level mono cutout: ink pixels -> the lock colour, opaque; everything else -> fully
// transparent. Inputs with alpha threshold on alpha; opaque inputs key out the corner background.
// No-op on a malformed buffer.
func BinarizeForLock(px []float32, w, h int, lock model.RGBA, inputHasAlpha bool) {
	if len(px) < w*h*4 {
		return
	}
	var bg model.RGBA
	if !inputHasAlpha {
		bg = cornerBackground(px, w, h)
	}
	for i := 0; i+3 < len(px); i += 4 {
		if isInk(px, i, bg, inputHasAlpha) {
			px[i], px[i+1], px[i+2], px[i+3] = lock.R, lock.G, lock.B, 1
		} else {
			px[i], px[i+1], px[i+2], px[i+3] = 0, 0, 0, 0
		}
	}
}

// isInk reports whether the pixel at offset i is logo ink: opaque enough (alpha input) or far
// enough from the background colour (opaque input).
func isInk(px []float32, i int, bg model.RGBA, inputHasAlpha bool) bool {
	if inputHasAlpha {
		return px[i+3] >= lockAlphaThreshold
	}
	dr, dg, db := px[i]-bg.R, px[i+1]-bg.G, px[i+2]-bg.B
	return dr*dr+dg*dg+db*db > lockBgKeyDist2
}

// cornerBackground is the mean of the four corner pixels — the background colour to key out for an
// opaque (no-alpha) logo input.
func cornerBackground(px []float32, w, h int) model.RGBA {
	corners := []int{0, (w - 1) * 4, (h - 1) * w * 4, ((h-1)*w + (w - 1)) * 4}
	var r, g, b float64
	for _, p := range corners {
		if p >= 0 && p+2 < len(px) {
			r, g, b = r+float64(px[p]), g+float64(px[p+1]), b+float64(px[p+2])
		}
	}
	n := float64(len(corners))
	return model.RGBA{R: float32(r / n), G: float32(g / n), B: float32(b / n)}
}

// lockColors snaps every reconstruction shape (all but the background) to the exact lock colour AND
// to full opacity, then re-renders so FinalError reflects it. A single-colour brand logo must be one
// SOLID colour — the polish drifts both colour (towards grey on a boundary) and alpha (translucent
// patches), so this overrides both at the end, guaranteeing one pure opaque brand colour in the
// output. No-op when LockColor is unset.
func (r *run) lockColors() {
	lock := r.opt.LockColor
	if lock == nil {
		return
	}
	cr, cg, cb := model.EncByte(lock.R), model.EncByte(lock.G), model.EncByte(lock.B)
	for i := 1; i < len(r.shapes); i++ {
		if c := r.shapes[i].Color; len(c) >= 4 {
			c[0], c[1], c[2], c[3] = cr, cg, cb, 255
		}
	}
	_ = r.be.Reset(r.initCanvas)
	applyShapes(r.be, r.shapes[1:])
	r.grid, _, _, _ = r.be.ErrorGrid()
	r.finalErr = sumGrid(r.grid)
}
