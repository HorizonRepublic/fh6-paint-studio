package stroke

import (
	"reflect"
	"testing"
)

// mkSeg builds a 1px-step straight branch from (x0,y0) to (x1,y1) along one axis.
func mkSeg(x0, y0, x1, y1 int) [][2]float64 {
	var p [][2]float64
	dx, dy := sign(x1-x0), sign(y1-y0)
	x, y := x0, y0
	for {
		p = append(p, [2]float64{float64(x), float64(y)})
		if x == x1 && y == y1 {
			break
		}
		x += dx
		y += dy
	}
	return p
}

func sign(v int) int {
	if v > 0 {
		return 1
	}
	if v < 0 {
		return -1
	}
	return 0
}

// A "+" crossing of two straight lines at (5,5): 4 branches meeting at one node should stitch into the
// 2 straight lines that pass THROUGH the junction (left+right, top+bottom), not stay 4 fragments.
func TestStitchCrossMergesStraightThrough(t *testing.T) {
	left := mkSeg(0, 5, 5, 5)
	right := mkSeg(5, 5, 10, 5)
	top := mkSeg(5, 0, 5, 5)
	bottom := mkSeg(5, 5, 5, 10)
	got := stitchThroughJunctions([][][2]float64{left, right, top, bottom}, 40)
	if len(got) != 2 {
		t.Fatalf("want 2 stitched polylines through the cross, got %d", len(got))
	}
	for _, p := range got {
		if polyLen(p) < 9.5 {
			t.Errorf("stitched line too short (junction not bridged): len=%.2f pts=%d", polyLen(p), len(p))
		}
	}
}

// A 90° corner (only two branches meeting, perpendicular) must NOT be merged — sharp corners
// (eyelash tips, hair) stay split. Their outward tangents are perpendicular (dot 0), far from straight.
func TestStitchKeepsSharpCornerSplit(t *testing.T) {
	a := mkSeg(0, 5, 5, 5)  // horizontal into the corner
	b := mkSeg(5, 5, 5, 10) // vertical out of the corner
	got := stitchThroughJunctions([][][2]float64{a, b}, 40)
	if len(got) != 2 {
		t.Fatalf("90° corner must stay 2 polylines, got %d", len(got))
	}
}

// An isolated branch (no shared node) passes through unchanged.
func TestStitchIsolatedBranchUnchanged(t *testing.T) {
	a := mkSeg(0, 0, 9, 0)
	got := stitchThroughJunctions([][][2]float64{a}, 40)
	if len(got) != 1 || len(got[0]) != len(a) {
		t.Fatalf("isolated branch should be unchanged, got %d polylines", len(got))
	}
}

// Determinism: identical input → byte-identical output (golden-reference rule).
func TestStitchDeterministic(t *testing.T) {
	in := [][][2]float64{
		mkSeg(0, 5, 5, 5), mkSeg(5, 5, 10, 5), mkSeg(5, 0, 5, 5), mkSeg(5, 5, 5, 10),
		mkSeg(5, 5, 12, 12),
	}
	a := stitchThroughJunctions(in, 40)
	b := stitchThroughJunctions(in, 40)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("stitch output is non-deterministic")
	}
}
