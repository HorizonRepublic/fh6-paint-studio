package inject

import "math"

// FH6 draws its Triangle primitive by applying a layer's affine transform to a fixed base triangle.
// Measured live against the running editor, the base triangle (scale 1, rotation 0, skew 0) is a
// RIGHT ISOSCELES triangle with vertices at (-A, +A), (-A, -A), (+A, -A): the lower-left half of the
// [-A, +A]^2 square, right angle at the bottom-left, and the pivot (0,0) at the MIDDLE OF THE
// HYPOTENUSE (the 45° edge). TriBaseHalf is A in editor units.
const TriBaseHalf = 64.0

// triBaseVerts returns the three base-triangle vertices in editor units (scale 1, rot 0, skew 0).
func triBaseVerts() [3][2]float64 {
	a := TriBaseHalf
	return [3][2]float64{{-a, a}, {-a, -a}, {a, -a}}
}

// TriangleFit solves for the FH6 layer transform (position, scale, rotation°, skew°) that makes the
// Triangle primitive coincide with the target triangle given by its three editor-space vertices.
// FH6's forward transform is v = pos + R(rot)·Shear(k)·diag(sx,sy)·b for each base vertex b, with
//
//	R(rot)   = [[cos,-sin],[sin,cos]]   rotation in DEGREES (editor space, Y up)
//	Shear(k) = [[1, k],[0,1]]           k is FH6's raw skew-slider value — a shear FACTOR, NOT an angle
//
// (Calibrated live: the editor's skew field is the shear coefficient itself — k=20 is a huge shear
// that looks like a line, k≈0.65 a gentle lean — so skew must be written as U01/sy, not atan.)
//
// The linear part M = R·Shear·diag(sx,sy) is recovered from two edges (M·(b_i-b_0) = t_i-t_0), then
// RQ-decomposed into rotation, shear and scale; pos is the image of the base pivot (0,0). Six
// equations (3 vertices × 2) determine the six unknowns exactly, so an arbitrary triangle maps with
// no loss (negative sx encodes a reflection — FH6 supports mirrored scale).
func TriangleFit(t0, t1, t2 [2]float64) (posX, posY, sx, sy, rotDeg, skew float64) {
	b := triBaseVerts()
	db1 := [2]float64{b[1][0] - b[0][0], b[1][1] - b[0][1]}
	db2 := [2]float64{b[2][0] - b[0][0], b[2][1] - b[0][1]}
	dt1 := [2]float64{t1[0] - t0[0], t1[1] - t0[1]}
	dt2 := [2]float64{t2[0] - t0[0], t2[1] - t0[1]}

	det := db1[0]*db2[1] - db2[0]*db1[1]
	if math.Abs(det) < 1e-9 {
		return t0[0], t0[1], 1, 1, 0, 0 // degenerate base (shouldn't happen)
	}
	// M = [dt1 dt2] · [db1 db2]^{-1} (columns).
	bi := [2][2]float64{{db2[1] / det, -db2[0] / det}, {-db1[1] / det, db1[0] / det}}
	m00 := dt1[0]*bi[0][0] + dt2[0]*bi[1][0]
	m01 := dt1[0]*bi[0][1] + dt2[0]*bi[1][1]
	m10 := dt1[1]*bi[0][0] + dt2[1]*bi[1][0]
	m11 := dt1[1]*bi[0][1] + dt2[1]*bi[1][1]

	posX = t0[0] - (m00*b[0][0] + m01*b[0][1])
	posY = t0[1] - (m10*b[0][0] + m11*b[0][1])

	// RQ-decompose M = R(rot)·U with U = [[sx, sy·tan(skew)],[0, sy]] upper-triangular.
	rot := math.Atan2(m10, m00)
	c, s := math.Cos(rot), math.Sin(rot)
	sx = c*m00 + s*m10   // U00
	u01 := c*m01 + s*m11 // U01 = k·sy
	sy = -s*m01 + c*m11  // U11
	skew = 0.0
	if math.Abs(sy) > 1e-9 {
		skew = u01 / sy // shear factor k (FH6 writes this raw, not as an angle)
	}
	return posX, posY, sx, sy, rot * 180 / math.Pi, skew
}

// triApply is the forward FH6 transform — used by tests to verify TriangleFit round-trips. rotDeg is
// degrees; skew is the raw shear factor (FH6's skew slider).
func triApply(posX, posY, sx, sy, rotDeg, skew float64) [3][2]float64 {
	rot := rotDeg * math.Pi / 180
	c, s := math.Cos(rot), math.Sin(rot)
	var out [3][2]float64
	for i, b := range triBaseVerts() {
		// Shear then scale: u = [[1,skew],[0,1]]·[[sx,0],[0,sy]]·b
		ux := sx*b[0] + skew*sy*b[1]
		uy := sy * b[1]
		// Rotate then translate.
		out[i] = [2]float64{posX + c*ux - s*uy, posY + s*ux + c*uy}
	}
	return out
}
