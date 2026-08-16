package metric

import (
	"math"
	"math/rand"
	"runtime"
	"testing"
)

// The map builders were changed from serial loops to row-banded ones. The claim is that this is a
// pure scheduling change — same values, bit for bit — which holds only if every band writes
// disjoint output AND the loops that accumulate PER CELL split on cell boundaries, so each cell is
// summed in the same order by one goroutine. Float addition is not associative, so a band that
// straddles a cell would give a different sum, not merely a different schedule.
//
// GOMAXPROCS is forced to 1 and back to compare the two directly. Anything that survives this at
// several odd sizes has to be independent of how the rows were divided.
func withProcs(n int, fn func()) {
	old := runtime.GOMAXPROCS(n)
	defer runtime.GOMAXPROCS(old)
	fn()
}

// noiseTarget builds a deterministic RGBA float image with structure in it — a flat run, a ramp and
// a hard edge — so the cell classifiers below have all three regimes to disagree about.
func noiseTarget(w, h int, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	px := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			var v float32
			switch {
			case x < w/3:
				v = 0.2
			case x < 2*w/3:
				v = 0.2 + 0.6*float32(x-w/3)/float32(w/3) // ramp
			default:
				v = 0.9
			}
			v += 0.02 * float32(r.NormFloat64())
			px[i] = v
			px[i+1] = v * 0.9
			px[i+2] = 1 - v
			px[i+3] = 1
		}
	}
	return px
}

func sameF32(t *testing.T, what string, a, b []float32) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("%s: length %d vs %d", what, len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("%s: index %d differs: %v vs %v (bit-identical was the whole claim)", what, i, a[i], b[i])
		}
	}
}

func TestMapsAreIdenticalSerialAndBanded(t *testing.T) {
	// Sizes chosen to be awkward for the splitter: not multiples of the 12px cell, fewer rows than
	// CPUs, and a single row.
	for _, d := range [][2]int{{64, 47}, {31, 12}, {17, 1}, {96, 96}} {
		w, h := d[0], d[1]
		px := noiseTarget(w, h, int64(w*1000+h))

		var serialOrient, parOrient, serialCoh, parCoh []float32
		var serialRamp, parRamp []float32
		var serialHard, parHard []float32
		var serialStats, parStats GradientStats

		withProcs(1, func() {
			ReleaseMaps()
			serialOrient, serialCoh = OrientationCoherenceMap(px, w, h)
			ReleaseMaps()
			serialHard = HardEdgeMap(px, w, h)
			ReleaseMaps()
			serialRamp = RampMap(px, w, h)
			ReleaseMaps()
			serialStats = GradientFraction(px, w, h)
		})
		withProcs(runtime.NumCPU(), func() {
			ReleaseMaps()
			parOrient, parCoh = OrientationCoherenceMap(px, w, h)
			ReleaseMaps()
			parHard = HardEdgeMap(px, w, h)
			ReleaseMaps()
			parRamp = RampMap(px, w, h)
			ReleaseMaps()
			parStats = GradientFraction(px, w, h)
		})
		ReleaseMaps()

		sameF32(t, "OrientationMap", serialOrient, parOrient)
		sameF32(t, "coherence", serialCoh, parCoh)
		sameF32(t, "HardEdgeMap", serialHard, parHard)
		sameF32(t, "RampMap", serialRamp, parRamp)
		if serialStats != parStats {
			t.Fatalf("GradientFraction %dx%d: %+v vs %+v", w, h, serialStats, parStats)
		}
	}
}

// The angle wrap was math.Mod(along, 180) and is now a compare-subtract, which is only valid over
// the range atan2 can actually produce. Check the two agree across that whole range, endpoints
// included, rather than trusting the argument.
func TestAngleWrapMatchesMod(t *testing.T) {
	for i := 0; i <= 100000; i++ {
		// atan2 returns (-pi, pi], so 0.5*atan2*rad2deg + 90 lies in (-90, 270].
		along := -90 + 360*float64(i)/100000
		want := math.Mod(along, 180)
		if want < 0 {
			want += 180
		}
		got := along
		if got >= 180 {
			got -= 180
		} else if got < 0 {
			got += 180
		}
		if got != want {
			t.Fatalf("along=%v: compare-subtract gave %v, math.Mod gave %v", along, got, want)
		}
	}
}

func TestReleaseMapsAndEmptyInput(t *testing.T) {
	if got := HardEdgeMap(nil, 0, 0); got != nil {
		t.Fatalf("empty target should give nil, got len %d", len(got))
	}
	if got := HardEdgeMap([]float32{}, 4, 4); got != nil {
		t.Fatalf("empty slice with non-zero dims should give nil, got len %d", len(got))
	}
	px := noiseTarget(32, 32, 7)
	a := HardEdgeMap(px, 32, 32)
	ReleaseMaps()
	b := HardEdgeMap(px, 32, 32) // rebuilt from scratch, must match the memoised answer
	sameF32(t, "HardEdgeMap across ReleaseMaps", a, b)
}
