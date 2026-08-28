package model

import "math"

// ShapeKind identifies a candidate primitive. P-slot meaning is documented per kind.
type ShapeKind uint16

const (
	KindEllipse   ShapeKind = iota // P = [cx, cy, rx, ry, thetaDeg, _]
	KindRectangle                  // P = [cx, cy, halfW, halfH, thetaDeg, _]
	KindTriangle                   // P = [x1, y1, x2, y2, x3, y3]
	KindLine                       // P = [x1, y1, x2, y2, halfWidth, _]
	// KindGlow and KindDisk are the native FH6 RADIAL GRADIENT primitives (calibrated live
	// 2026-06-03). Geometry is an ellipse (P = [cx, cy, rx, ry, thetaDeg, _]) but coverage is
	// PER-PIXEL alpha (a baked radial falloff), not a binary fill — see raster.Coverage:
	//   KindGlow (word 0xE4) — soft 2D-gaussian splat (the GaussianImage primitive): peak α≈0.89,
	//     fades smoothly to 0 at the footprint edge. For gradients / smooth blending.
	//   KindDisk (word 0xE2) — opaque core (α=1 to ~0.4·R) + soft alpha rim. A feathered disk:
	//     covers a flat region with no hard edge.
	// rx,ry are the alpha->0 footprint radii (in image px). effective alpha(x) = color.A · falloff(t).
	KindGlow
	KindDisk
)

// Output JSON shape-type ids. These are the engine's intermediate type ids (NOT the
// game's shape codes). The injector maps them to the editor's in-game 16-bit shape
// words (layer offset 0x7A). Mapping:
//
//	TypeRectangle / TypeRotatedRectangle -> Square  1048677 (rotation via the layer's rot field)
//	TypeRotatedEllipse (rx==ry)          -> Circle  1048678
//	TypeRotatedEllipse                   -> Ellipse 1048712
//	TypeTriangle                         -> Triangle 1048679
//	(also available in-game, not yet emitted: Circle Border 1048688, 880 font glyphs)
//
// NOTE: there is NO "line" primitive in the in-game shape catalog, so TypeLine has no
// game mapping and is NOT offered to users (kept only as internal raster geometry).
const (
	TypeRectangle        = 1 // background rectangle (used by the engine)
	TypeRotatedRectangle = 2
	TypeRotatedEllipse   = 16
	TypeTriangle         = 32
	TypeLine             = 64 // no in-game equivalent — do not use for liveries
	// Native FH6 radial-gradient primitives. The id == the in-game low 16-bit shape word, so the
	// injector maps Type->word directly (KindGlow->0xE4 glow, KindDisk->0xE2 feathered disk). Data layout
	// is the ellipse's [cx, cy, rx, ry, angle]; the falloff is baked in the mesh (word selects it).
	TypeGradGlow = 0xE4 // 228 — soft radial gaussian (GaussianImage splat)
	TypeGradDisk = 0xE2 // 226 — opaque core + soft rim
)

// RGBA is a color with channel values in 0..1.
type RGBA struct{ R, G, B, A float32 }

// Candidate is one primitive under evaluation.
type Candidate struct {
	Kind  ShapeKind
	P     [6]float32
	Color RGBA
}

// Shape is the serialized output element (importer-compatible).
type Shape struct {
	Type   int       `json:"type"`
	Data   []float64 `json:"data"`
	Color  []int     `json:"color"`
	Score  float64   `json:"score"`            // optional per-shape bookkeeping; importer ignores it
	Locked bool      `json:"locked,omitempty"` // editor-only: protected from selection-drag / nudge / delete
}

// Geometry is the top-level output document.
type Geometry struct {
	Shapes []Shape `json:"shapes"`
}

// F2B converts a 0..1 channel value to a clamped 0..255 byte.
func F2B(v float32) int {
	// !(v > 0) rather than v < 0: NaN fails EVERY comparison, so both clamps used to pass it
	// through and int(math.Round(NaN)) is -9223372036854775808. That went into Shape.Color, into
	// the .forza.json, and on to the injector — a silent wrong value where the preview path only
	// panicked. A NaN channel is already lost; encode it as 0.
	if !(v > 0) {
		return 0
	}
	if v > 1 {
		v = 1
	}
	return int(math.Round(float64(v) * 255))
}

// ToShape serializes this candidate to its output Shape — the Type id and Data
// layout depend on Kind. Color is converted to 0..255 RGBA. The injector maps
// these Type ids to the editor's shape words.
func (c Candidate) ToShape(score float64) Shape {
	col := []int{EncByte(c.Color.R), EncByte(c.Color.G), EncByte(c.Color.B), F2B(c.Color.A)}
	if IsMask(c.Kind) {
		// Mask word: Type == the in-game shape word; Data is the full affine [cx,cy,Hx,Hy,rot,skew].
		// rot is NOT rounded (mask placement keeps sub-degree fidelity) and skew is carried raw.
		word, _ := MaskWord(c.Kind)
		return Shape{Type: int(word), Color: col, Score: score,
			Data: []float64{float64(c.P[0]), float64(c.P[1]), float64(c.P[2]), float64(c.P[3]), float64(c.P[4]), float64(c.P[5])}}
	}
	switch c.Kind {
	case KindRectangle:
		// A sheared rectangle (skew != 0) is a parallelogram — carry the raw skew as a 6th field so the
		// render/injector reproduce it. skew == 0 stays 5 fields, so every non-sheared rect is unchanged.
		data := []float64{float64(c.P[0]), float64(c.P[1]), float64(c.P[2]), float64(c.P[3]), normAngle(c.P[4])}
		if c.P[5] != 0 {
			data = append(data, float64(c.P[5]))
		}
		return Shape{Type: TypeRotatedRectangle, Color: col, Score: score, Data: data}
	case KindTriangle:
		return Shape{Type: TypeTriangle, Color: col, Score: score,
			Data: []float64{float64(c.P[0]), float64(c.P[1]), float64(c.P[2]), float64(c.P[3]), float64(c.P[4]), float64(c.P[5])}}
	case KindLine:
		return Shape{Type: TypeLine, Color: col, Score: score,
			Data: []float64{float64(c.P[0]), float64(c.P[1]), float64(c.P[2]), float64(c.P[3]), float64(c.P[4])}}
	case KindGlow:
		return Shape{Type: TypeGradGlow, Color: col, Score: score, Data: radialData(c.P)}
	case KindDisk:
		return Shape{Type: TypeGradDisk, Color: col, Score: score, Data: radialData(c.P)}
	default:
		return Shape{Type: TypeRotatedEllipse, Color: col, Score: score, Data: radialData(c.P)}
	}
}

// radialData is the ellipse-footprint wire form. The optional 6th field is the skew the editor's
// grip can set — the raster and the injector both honour it, so dropping it here silently
// unsheared any edited radial/ellipse shape on the next round trip. skew==0 stays 5 fields.
func radialData(P [6]float32) []float64 {
	data := []float64{float64(P[0]), float64(P[1]), float64(P[2]), float64(P[3]), normAngle(P[4])}
	if P[5] != 0 {
		data = append(data, float64(P[5]))
	}
	return data
}

// normAngle rounds degrees to 0.01° into [0,360) (collapsing IEEE-754 -0 to +0). Sub-degree
// precision is kept on purpose: the in-game rotation field is a float (the mask path already
// injects fractional rot), and whole-degree rounding measurably costs quality — the polish
// optimises continuous angles, and rounding 3000 of them to integers raised the re-rendered
// error ~6-9% (the "snap gap"). 0.01° is far below sub-pixel at any canvas size while keeping
// the exported JSON compact.
func normAngle(deg float32) float64 {
	a := math.Mod(math.Round(float64(deg)*100)/100, 360)
	if a < 0 {
		a += 360
	}
	if a == 0 {
		a = 0
	}
	return a
}

// KindFromType maps an output Type id back to its ShapeKind (inverse of ToShape).
func KindFromType(t int) ShapeKind {
	switch t {
	case TypeRotatedRectangle:
		return KindRectangle
	case TypeTriangle:
		return KindTriangle
	case TypeLine:
		return KindLine
	case TypeGradGlow:
		return KindGlow
	case TypeGradDisk:
		return KindDisk
	default:
		if t >= 0 && t <= 0xFFFF {
			if k, ok := MaskKind(uint16(t)); ok {
				return k
			}
		}
		return KindEllipse
	}
}

// ParamsFromShape copies a Shape's Data into the raster parameter array.
func ParamsFromShape(s Shape) [6]float32 {
	var p [6]float32
	for i := 0; i < len(s.Data) && i < 6; i++ {
		p[i] = float32(s.Data[i])
	}
	return p
}
