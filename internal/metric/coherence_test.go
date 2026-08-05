package metric

import "testing"

// fill builds a w*h RGBA plane from a luma function, so each test states its pattern directly.
func fill(w, h int, f func(x, y int) float32) []float32 {
	out := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := f(x, y)
			p := (y*w + x) * 4
			out[p+0], out[p+1], out[p+2], out[p+3] = v, v, v, 1
		}
	}
	return out
}

// meanIn averages the coherence over an interior window, keeping the border out of it — the
// structure tensor clamps at the edges, so border pixels report a pattern the image does not have.
func meanIn(coh []float32, w, x0, y0, x1, y1 int) float64 {
	var s float64
	var n int
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			s += float64(coh[y*w+x])
			n++
		}
	}
	return s / float64(n)
}

// TestCoherenceSeparatesEdgeFromFlat is the property the whole anisotropy prior rests on: a
// straight edge must read as strongly directional and a flat region as not directional at all. If
// these two were not separated, the prior would elongate candidates everywhere, which is exactly
// the behaviour it exists to replace.
func TestCoherenceSeparatesEdgeFromFlat(t *testing.T) {
	const w, h = 48, 48
	edge := fill(w, h, func(x, y int) float32 {
		if x < w/2 {
			return 0.15
		}
		return 0.85
	})
	flat := fill(w, h, func(x, y int) float32 { return 0.4 })

	_, ce := OrientationCoherenceMap(edge, w, h)
	_, cf := OrientationCoherenceMap(flat, w, h)

	// Sample only the columns the Sobel window actually touches (a step edge lights up four of
	// them); a wider band would average in the flat columns either side and understate a field that
	// is exactly 1 where it is defined.
	got := meanIn(ce, w, w/2-2, 8, w/2+1, h-9)
	if got < 0.9 {
		t.Errorf("coherence across a straight edge = %.3f, want >=0.9", got)
	}
	if got := meanIn(cf, w, 8, 8, w-9, h-9); got != 0 {
		t.Errorf("coherence in a flat region = %.3f, want exactly 0 (no gradient at all)", got)
	}
}

// TestCoherenceIsLowWhereDirectionsCompete distinguishes "has gradient" from "has a DIRECTION". A
// fine checkerboard has strong gradients in both axes at once, so an oriented element fits it no
// better than a round one — the prior must not elongate there. This is the case a plain edge-energy
// map cannot tell apart from an edge, and it is why the prior keys on coherence rather than on the
// gradient magnitude the engine already had.
func TestCoherenceIsLowWhereDirectionsCompete(t *testing.T) {
	const w, h = 48, 48
	checker := fill(w, h, func(x, y int) float32 {
		if (x/2+y/2)%2 == 0 {
			return 0.2
		}
		return 0.8
	})
	edge := fill(w, h, func(x, y int) float32 {
		if y < h/2 {
			return 0.2
		}
		return 0.8
	})

	_, cc := OrientationCoherenceMap(checker, w, h)
	_, ce := OrientationCoherenceMap(edge, w, h)

	chk := meanIn(cc, w, 8, 8, w-9, h-9)
	edg := meanIn(ce, w, 8, h/2-2, w-9, h/2+1)
	if chk >= edg {
		t.Errorf("checkerboard coherence %.3f is not below straight-edge coherence %.3f", chk, edg)
	}
	if chk > 0.6 {
		t.Errorf("checkerboard coherence = %.3f, want well below an edge's", chk)
	}
}
