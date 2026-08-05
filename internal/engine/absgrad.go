package engine

import (
	"math"
	"os"
	"sort"

	"fh6-paint-studio/internal/applog"
)

// Gradient-collision diagnostic (AbsGS, arXiv 2404.10484).
//
// A shape's positional gradient is a SUM over the pixels it covers. When it lies over a detailed
// region, the pixels beneath it disagree about which way it should move, so their contributions
// cancel and the NET gradient is near zero while the individual demands are large. The descent then
// leaves that shape where it is, and leave-one-out reads it as mildly useful rather than misplaced
// — a candidate mechanism for the rim artefact, since a shape frozen across a boundary is exactly
// what draws an outline the target lacks.
//
// The statistic is |Σ g_p| / Σ |g_p| over the shape's pixels, per position axis. Near 1 means the
// pixels agree and the shape is genuinely converged; near 0 means they cancel and the shape is
// stuck for a reason the loss cannot see. This only MEASURES. Acting on it (the paper's fix is to
// accumulate the absolute value and use it to drive densification) is a separate change, gated on
// what this reports.
//
// FH6_ABSGRAD=1 turns it on. The host polish driver carries it; the device path is untouched, so a
// diagnostic run must force the CPU polish.

// absGradOn reports whether the diagnostic is enabled for this run.
func absGradOn() bool {
	v := os.Getenv("FH6_ABSGRAD")
	return v != "" && v != "0"
}

// absGradArm allocates the per-shape accumulators so polishBackward starts collecting. Cheap
// enough to call unconditionally; it no-ops when the diagnostic is off.
func absGradArm(ps []pshape) {
	if !absGradOn() {
		return
	}
	for i := range ps {
		ps[i].absP = new([2]float64)
	}
}

// absGradReport logs the collision distribution over the shapes that actually accumulated a
// gradient. Shapes whose absolute sum is zero never moved (frozen geometry, empty bbox) and are
// excluded rather than counted as perfectly collided, which would be the opposite of the truth.
//
// The distribution alone proves nothing: at ANY converged optimum the net gradient vanishes while
// the per-pixel demands do not, so a low ratio is the expected reading for a shape that is simply
// finished. The claim under test is COMPARATIVE — that shapes over DETAIL collide more than shapes
// over smooth target — so the report splits by the target's own local gradient inside each shape's
// bbox. A flat split refutes the mechanism here; a falling one supports it.
func absGradReport(ps []pshape, bbx [][4]int, target []float32, w, h int) {
	if !absGradOn() {
		return
	}
	type row struct{ ratio, detail float64 }
	rows := make([]row, 0, len(ps))
	ratios := make([]float64, 0, len(ps))
	for i := range ps {
		a := ps[i].absP
		if a == nil || a[1] <= 0 {
			continue
		}
		r := a[0] / a[1]
		ratios = append(ratios, r)
		rows = append(rows, row{r, bboxDetail(bbx[i], target, w, h)})
	}
	if len(ratios) == 0 {
		applog.Printf("absgrad: no shape accumulated a positional gradient (frozen geometry?)")
		return
	}
	sort.Float64s(ratios)
	q := func(f float64) float64 {
		i := int(f * float64(len(ratios)-1))
		return ratios[i]
	}
	var sum float64
	collided := 0
	for _, r := range ratios {
		sum += r
		if r < 0.1 {
			collided++
		}
	}
	applog.Printf("absgrad: n=%d mean=%.4f p10=%.4f p50=%.4f p90=%.4f  below0.1=%d (%.1f%%)",
		len(ratios), sum/float64(len(ratios)), q(0.10), q(0.50), q(0.90),
		collided, 100*float64(collided)/float64(len(ratios)))

	// The comparative half: mean agreement per tercile of target detail.
	sort.Slice(rows, func(i, j int) bool { return rows[i].detail < rows[j].detail })
	t := len(rows) / 3
	if t == 0 {
		return
	}
	band := func(lo, hi int) (mr, md float64) {
		for _, r := range rows[lo:hi] {
			mr += r.ratio
			md += r.detail
		}
		n := float64(hi - lo)
		return mr / n, md / n
	}
	lr, ld := band(0, t)
	mr, mdd := band(t, 2*t)
	hr, hd := band(2*t, len(rows))
	applog.Printf("absgrad by target detail: smooth ratio=%.4f (detail %.4f) | mid ratio=%.4f (detail %.4f) | detailed ratio=%.4f (detail %.4f)",
		lr, ld, mr, mdd, hr, hd)
}

// bboxDetail is the mean luma gradient magnitude of the TARGET inside a shape's bbox — how much
// the pixels underneath a shape disagree about what colour they want, independent of the render.
func bboxDetail(bb [4]int, target []float32, w, h int) float64 {
	xMin, yMin, xMax, yMax := bb[0], bb[1], bb[2], bb[3]
	if xMin < 1 {
		xMin = 1
	}
	if yMin < 1 {
		yMin = 1
	}
	if xMax > w-2 {
		xMax = w - 2
	}
	if yMax > h-2 {
		yMax = h - 2
	}
	if xMax < xMin || yMax < yMin {
		return 0
	}
	luma := func(x, y int) float64 {
		p := (y*w + x) * 4
		return 0.2126*float64(target[p]) + 0.7152*float64(target[p+1]) + 0.0722*float64(target[p+2])
	}
	var sum float64
	var n int
	for y := yMin; y <= yMax; y++ {
		for x := xMin; x <= xMax; x++ {
			gx := luma(x+1, y) - luma(x-1, y)
			gy := luma(x, y+1) - luma(x, y-1)
			sum += math.Hypot(gx, gy)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
