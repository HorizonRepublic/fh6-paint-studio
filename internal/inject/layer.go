package inject

import (
	"encoding/binary"
	"math"

	"fh6-paint-studio/internal/model"
)

// FH6 vinyl-group editor coordinate system, calibrated live against the running game (read off the
// editor's own Position/Scale readouts for known shapes + the shift limits of a corner shape):
//
//   - Origin (0,0) is the CENTRE of the decal canvas.
//   - Position units span X ∈ [-EditorHalfX, +EditorHalfX], Y ∈ [-EditorHalfY, +EditorHalfY].
//   - X increases to the RIGHT, Y increases UPWARD (the editor Y axis is flipped vs. our top-down
//     image space, where Y increases downward).
//   - "Scale" is a MULTIPLIER, not a size: a shape at scale 1.0 has a half-extent of ScaleBase
//     position-units. So a shape whose desired half-extent is R units is written with scale R/ScaleBase.
//     (This is why writing raw pixel half-extents — values in the hundreds/thousands — produced
//     astronomically huge shapes: they were interpreted as scale multipliers.)
const (
	EditorHalfX = 1984.0 // canvas half-width in editor position units
	EditorHalfY = 1141.0 // canvas half-height in editor position units
	ScaleBase   = 64.0   // editor units per scale-1.0 (a scale-1 circle's radius). Calibrated in-game:
	// an injected shape of known pixel half-extent with markers at ±half showed the edge UNDERSHOOTING
	// the markers at base 80 (ratio 0.798 -> shapes 20% too small -> gaps between shapes in-game). base 64
	// makes the edge touch the markers (ratio 0.995), and holds across circle, square, and ellipse.
	GradScaleBase = 66.0 // editor units per scale-1.0 for the radial gradients: the alpha->0 footprint
	// radius measured live ≈ 66·scale (linear across scale 3/6/9), slightly larger than the hard circle's
	// 64. A gradient shape's rx,ry are its alpha->0 radii, so SX = rx·K / GradScaleBase.
)

// CanvasMap maps our generation-pixel geometry (origin top-left, Y down, w×h) onto the FH6 editor
// space above: centre the art on the canvas, flip Y, fit it inside the canvas by a uniform factor
// K (units per pixel), and convert pixel half-extents to scale multipliers via Base.
type CanvasMap struct {
	W, H float32 // source image dimensions in pixels
	K    float32 // editor position units per image pixel (the fit factor)
	Base float32 // editor units per scale-1.0 (ScaleBase, exposed for calibration sweeps)
	Raw  bool    // calibration: write shape Data verbatim as editor units (pos/scale/rot/skew), no transform
}

// NewCanvasMap fits a w×h image inside the canvas at the given fill fraction (1.0 = touch the
// nearer pair of edges) using scale base `base`.
func NewCanvasMap(w, h int, fill, base float32) CanvasMap {
	if w <= 0 || h <= 0 {
		return CanvasMap{W: 1, H: 1, K: 1, Base: base}
	}
	kx := fill * 2 * EditorHalfX / float32(w)
	ky := fill * 2 * EditorHalfY / float32(h)
	k := kx
	if ky < k {
		k = ky // fit inside: limit by the tighter axis
	}
	return CanvasMap{W: float32(w), H: float32(h), K: k, Base: base}
}

// DefaultCanvasMap fits the image fully inside the decal (fill 1.0) at the calibrated scale base.
func DefaultCanvasMap(w, h int) CanvasMap { return NewCanvasMap(w, h, 1.0, ScaleBase) }

// LayerWrite is the resolved per-layer field set in game coordinate space.
type LayerWrite struct {
	X, Y     float32
	SX, SY   float32
	Rotation float32 // degrees
	Skew     float32
	Color    [4]byte // RGBA
	Mask     bool
	Word     uint16 // shape word written at ShapeIDOffset
}

// ShapeToLayer converts one model.Shape to a LayerWrite via the canvas map. ok=false for shapes
// that have no FH6 primitive (lines). Triangles are approximated by their bounding box + centroid
// (the in-game Triangle is a positioned/scaled primitive, not three free vertices).
func ShapeToLayer(s model.Shape, cm CanvasMap) (LayerWrite, bool) {
	var lw LayerWrite
	p := model.ParamsFromShape(s)

	// Calibration path: Data is already editor-space [x, y, sx, sy, rot, skew]; write it verbatim,
	// AND write the color verbatim (no fh6Alpha remap) so a calibration grid measures FH6's true
	// response to the exact alpha byte we store.
	if cm.Raw {
		word, ok := wordForType(s.Type, p)
		if !ok {
			return lw, false
		}
		lw.Color = colorBytesRaw(s.Color)
		lw.X, lw.Y, lw.SX, lw.SY = p[0], p[1], p[2], p[3]
		lw.Rotation, lw.Skew = p[4], p[5]
		lw.Word = word
		return lw, true
	}

	lw.Color = colorBytes(s.Color)
	if s.Type >= 0 && s.Type <= 0xFFFF { // KindMask dictionary arc: Type is the word, Data = [cx,cy,Hx,Hy,rot,skew]
		if k, ok := model.MaskKind(uint16(s.Type)); ok {
			nw, nh, _ := model.MaskNative(k)
			lw.placeMask(cm, p[0], p[1], p[2], p[3], p[4], p[5], nw, nh)
			lw.Word = uint16(s.Type)
			return lw, true
		}
	}
	switch s.Type {
	case model.TypeRectangle: // background / corner rect: [x, y, w, h] -> centre + half-extent
		lw.place(cm, p[0]+p[2]/2, p[1]+p[3]/2, p[2]/2, p[3]/2, 0)
		lw.Word = WordSquare
	case model.TypeRotatedRectangle: // [cx, cy, halfW, halfH, deg, skew]
		lw.place(cm, p[0], p[1], p[2], p[3], p[4])
		lw.Skew = -p[5] // Y-flip reverses shear sense just as it reverses rotation; 0 for generated shapes
		lw.Word = WordSquare
	case model.TypeRotatedEllipse: // [cx, cy, rx, ry, deg, skew] — Circle word + non-uniform scale = ellipse
		lw.place(cm, p[0], p[1], p[2], p[3], p[4])
		lw.Skew = -p[5]
		lw.Word = WordCircle
	case model.TypeTriangle: // [x1,y1,x2,y2,x3,y3] -> exact FH6 triangle via affine fit (pos/scale/rot/skew)
		px, py, sx, sy, rot, skew := TriangleFit(
			triToEditor(cm, p[0], p[1]),
			triToEditor(cm, p[2], p[3]),
			triToEditor(cm, p[4], p[5]),
		)
		lw.X, lw.Y = float32(px), float32(py)
		lw.SX, lw.SY = float32(sx), float32(sy)
		lw.Rotation, lw.Skew = float32(rot), float32(skew)
		lw.Word = WordTriangle
	case model.TypeGradGlow: // soft radial gradient: ellipse footprint, gradient scale base
		lw.placeBase(cm, p[0], p[1], p[2], p[3], p[4], GradScaleBase)
		lw.Word = WordGradGlow
	case model.TypeGradDisk: // radial gradient with opaque core + soft rim
		lw.placeBase(cm, p[0], p[1], p[2], p[3], p[4], GradScaleBase)
		lw.Word = WordGradDisk
	default: // TypeLine or unknown — no in-game primitive
		return lw, false
	}
	return lw, true
}

// wordForType maps a shape Type id to its page-1 shape word. ok=false for the line, which has no
// in-game primitive. NOTE (verified live): the distinct "Ellipse" word 0x88 renders as a CRESCENT
// in-game — an ellipse is just a Circle (0x66) with a non-uniform scale, so every ellipse/circle uses
// WordCircle. The Triangle word is 0x68; word-only is enough — the editor selects the mesh from the
// word (the geometry resource at 0xA8 is never written; doing so crashes the game).
func wordForType(t int, p [6]float32) (uint16, bool) {
	switch t {
	case model.TypeRectangle, model.TypeRotatedRectangle:
		return WordSquare, true
	case model.TypeRotatedEllipse:
		return WordCircle, true // ellipse = circle + non-uniform scale; 0x88 is a crescent, not an ellipse
	case model.TypeTriangle:
		return WordTriangle, true
	case model.TypeGradGlow:
		return WordGradGlow, true
	case model.TypeGradDisk:
		return WordGradDisk, true
	}
	// Raw/calibration is the word-probing path: any in-range word id passes verbatim, including
	// ones not (yet) in the mask bank — probing unmapped catalog ranges is what calibration is for.
	if t > 0 && t <= 0xFFFF {
		return uint16(t), true
	}
	return 0, false
}

// place maps a pixel centre + pixel half-extent + degrees into the FH6 editor space: centre on the
// canvas (origin = image centre), flip Y (editor Y points up), scale positions by K, and convert
// pixel half-extents to scale multipliers via Base. The Y flip reverses the sense of rotation, so
// the angle is negated.
func (lw *LayerWrite) place(cm CanvasMap, cx, cy, hx, hy, deg float32) {
	base := cm.Base
	if base == 0 {
		base = ScaleBase
	}
	lw.placeBase(cm, cx, cy, hx, hy, deg, base)
}

// placeMask maps a KindMask word: the full image-px extents Hx,Hy normalised by the word's native size
// (nativeW,nativeH editor units at scale 1), preserving skew. A negative Hx (renderer mirror) passes
// through as a negative SX — verify the mirror in-game.
func (lw *LayerWrite) placeMask(cm CanvasMap, cx, cy, hx, hy, deg, skew, nativeW, nativeH float32) {
	lw.X = (cx - cm.W/2) * cm.K
	lw.Y = (cm.H/2 - cy) * cm.K // editor Y is up
	if nativeW != 0 {
		lw.SX = hx * cm.K / nativeW
	}
	if nativeH != 0 {
		lw.SY = hy * cm.K / nativeH
	}
	lw.Rotation = -deg
	lw.Skew = -skew // Y-flip reverses shear sense (same negation the box cases apply)
}

// placeBase is place with an explicit scale base, so the radial gradients can use their own
// calibrated GradScaleBase (≈66) instead of the hard-circle ScaleBase (64).
func (lw *LayerWrite) placeBase(cm CanvasMap, cx, cy, hx, hy, deg, base float32) {
	if base == 0 {
		base = ScaleBase
	}
	lw.X = (cx - cm.W/2) * cm.K
	lw.Y = (cm.H/2 - cy) * cm.K // editor Y is up
	lw.SX = hx * cm.K / base
	lw.SY = hy * cm.K / base
	lw.Rotation = -deg
}

// triToEditor maps one image-space pixel vertex (origin top-left, Y down) into editor units (origin
// centre, Y up) — the same centre+flip+fit as place(), but per vertex (triangles are fitted from
// their three vertices, not a centre+half-extent).
func triToEditor(cm CanvasMap, x, y float32) [2]float64 {
	return [2]float64{float64((x - cm.W/2) * cm.K), float64((cm.H/2 - y) * cm.K)}
}

// FieldWrite is one (offset, bytes) write into a layer struct.
type FieldWrite struct {
	Offset int
	Data   []byte
}

// Writes returns the exact field writes for this layer, in the editor's import order:
// pos, scale, rotation, skew, color, mask, shape word.
func (lw LayerWrite) Writes(p GameProfile) []FieldWrite {
	// Word-only — these 7 fields are all the editor's importer writes. The editor selects the shape
	// MESH from the shape-word at ShapeIDOffset; the per-layer geometry resource (0xA8) must NEVER be
	// written — aliasing one pointer across many layers corrupts the game's per-layer ownership and
	// crashes it on free (internal error FHE01, e.g. on editor exit). Word-only renders every
	// primitive (including triangles) correctly in-game.
	return []FieldWrite{
		{p.PosOffset, f32f32(lw.X, lw.Y)},
		{p.ScaleOffset, f32f32(lw.SX, lw.SY)},
		{p.RotationOffset, f32b(lw.Rotation)},
		{p.SkewOffset, f32b(lw.Skew)},
		{p.ColorOffset, lw.Color[:]},
		{p.MaskOffset, maskByte(lw.Mask)},
		{p.ShapeIDOffset, u16b(lw.Word)},
	}
}

// ClearWrites blanks an unused template layer: zero position,
// near-zero scale, zero rotation/color/mask.
func ClearWrites(p GameProfile) []FieldWrite {
	return []FieldWrite{
		{p.PosOffset, f32f32(0, 0)},
		{p.ScaleOffset, f32f32(0.001, 0.001)},
		{p.RotationOffset, f32b(0)},
		{p.ColorOffset, []byte{0, 0, 0, 0}},
		{p.MaskOffset, []byte{0}},
	}
}

// vinylPathPrefix is the FH6 virtual directory every vinyl mesh path starts with.
const vinylPathPrefix = `GAME:\Media\Livery\Vinyls\`

// wordToMeshPath returns the full mesh path a layer must point at to render `word` as its shape,
// or ok=false for a word with no known mesh file (left stale). The map is generated in
// meshpaths_gen.go.
func wordToMeshPath(word uint16) (string, bool) {
	f, ok := meshFileByWord[word]
	if !ok {
		return "", false
	}
	return vinylPathPrefix + f + ".modelbin", true
}

// --- helpers ---------------------------------------------------------------

// colorBytesRaw clamps an int RGBA slice to bytes verbatim (no FH6 alpha remap).
func colorBytesRaw(c []int) [4]byte {
	out := [4]byte{255, 255, 255, 255}
	for i := 0; i < 4 && i < len(c); i++ {
		v := c[i]
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		out[i] = byte(v)
	}
	return out
}

// colorBytes converts an int RGBA slice to bytes, remapping alpha onto FH6's transparency range.
func colorBytes(c []int) [4]byte {
	out := colorBytesRaw(c)
	out[3] = fh6Alpha(out[3])
	return out
}

// fh6Alpha maps a 0..255 straight alpha onto the editor's transparency range (its floor is ~0.78%,
// never a true 0). The mapping is intentionally linear: a gamma pre-distortion to counter the editor's
// linear-light compositing makes semi-transparent shapes pop MORE in-game, not less.
func fh6Alpha(a byte) byte {
	const minPct = 0.78
	pct := minPct + float64(a)/255*(100-minPct)
	return byte(pct/100*255 + 0.5)
}

func f32b(v float32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, math.Float32bits(v))
	return b
}

func f32f32(a, b float32) []byte {
	out := make([]byte, 8)
	binary.LittleEndian.PutUint32(out[0:], math.Float32bits(a))
	binary.LittleEndian.PutUint32(out[4:], math.Float32bits(b))
	return out
}

func u16b(v uint16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	return b
}

func u64b(v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b
}

func maskByte(on bool) []byte {
	if on {
		return []byte{1}
	}
	return []byte{0}
}
