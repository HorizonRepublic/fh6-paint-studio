package model

import "math"

// ShapeKind identifies a candidate primitive. P-slot meaning is documented per kind.
type ShapeKind uint8

const (
	KindEllipse   ShapeKind = iota // P = [cx, cy, rx, ry, thetaDeg, _]
	KindRectangle                  // P = [cx, cy, halfW, halfH, thetaDeg, _]
	KindTriangle                   // P = [x1, y1, x2, y2, x3, y3]
	KindLine                       // P = [x1, y1, x2, y2, halfWidth, _]
)

// Output JSON shape-type ids. These are the engine's intermediate type ids (NOT the
// game's shape codes). The injector maps them to the editor's in-game 16-bit shape
// words (layer offset 0x7A). Mapping:
//
//	TypeRectangle / TypeRotatedRectangle → Square  1048677 (rotation via the layer's rot field)
//	TypeRotatedEllipse (rx==ry)          → Circle  1048678
//	TypeRotatedEllipse                   → Ellipse 1048712
//	TypeTriangle                         → Triangle 1048679
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
	Type  int       `json:"type"`
	Data  []float64 `json:"data"`
	Color []int     `json:"color"`
	Score float64   `json:"score"` // optional per-shape bookkeeping; importer ignores it
}

// Geometry is the top-level output document.
type Geometry struct {
	Shapes []Shape `json:"shapes"`
}

// F2B converts a 0..1 channel value to a clamped 0..255 byte.
func F2B(v float32) int {
	if v < 0 {
		v = 0
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
	switch c.Kind {
	case KindRectangle:
		return Shape{Type: TypeRotatedRectangle, Color: col, Score: score,
			Data: []float64{float64(c.P[0]), float64(c.P[1]), float64(c.P[2]), float64(c.P[3]), normAngle(c.P[4])}}
	case KindTriangle:
		return Shape{Type: TypeTriangle, Color: col, Score: score,
			Data: []float64{float64(c.P[0]), float64(c.P[1]), float64(c.P[2]), float64(c.P[3]), float64(c.P[4]), float64(c.P[5])}}
	case KindLine:
		return Shape{Type: TypeLine, Color: col, Score: score,
			Data: []float64{float64(c.P[0]), float64(c.P[1]), float64(c.P[2]), float64(c.P[3]), float64(c.P[4])}}
	default:
		return Shape{Type: TypeRotatedEllipse, Color: col, Score: score,
			Data: []float64{float64(c.P[0]), float64(c.P[1]), float64(c.P[2]), float64(c.P[3]), normAngle(c.P[4])}}
	}
}

// normAngle rounds degrees into [0,360) (collapsing IEEE-754 -0 to +0).
func normAngle(deg float32) float64 {
	a := math.Mod(math.Round(float64(deg)), 360)
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
	default:
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
