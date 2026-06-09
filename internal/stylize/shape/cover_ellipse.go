package shape

import (
	"math"

	"fh6-paint-studio/internal/model"
)

// FitEllipse fits a single ellipse to a region by its image moments (centroid + 2nd-order central
// moments → orientation and axis lengths) and reports how well it covers the mask (IoU). A compact
// region — round blob OR straight sliver — is covered by ONE ellipse instead of the ~6-10 triangles or
// the dozen axis-aligned blocks the other strategies spend, freeing budget for detail. Coords are canvas
// space. iou is 0 for a degenerate (line/point) region.
func FitEllipse(r *Region) (cx, cy, rx, ry, angleDeg, iou float64) {
	var n, sx, sy float64
	for y := 0; y < r.BH; y++ {
		for x := 0; x < r.BW; x++ {
			if r.Mask[y*r.BW+x] {
				n++
				sx += float64(x)
				sy += float64(y)
			}
		}
	}
	if n < 4 {
		return 0, 0, 0, 0, 0, 0
	}
	mx, my := sx/n, sy/n
	var m20, m02, m11 float64
	for y := 0; y < r.BH; y++ {
		for x := 0; x < r.BW; x++ {
			if !r.Mask[y*r.BW+x] {
				continue
			}
			dx, dy := float64(x)-mx, float64(y)-my
			m20 += dx * dx
			m02 += dy * dy
			m11 += dx * dy
		}
	}
	m20 /= n
	m02 /= n
	m11 /= n
	// eigenvalues of the 2×2 covariance → semi-axis² = 4·λ (uniform-density ellipse: var = a²/4).
	t := (m20 + m02) / 2
	d := math.Sqrt(((m20-m02)/2)*((m20-m02)/2) + m11*m11)
	l1, l2 := t+d, t-d
	if l1 <= 0 {
		return 0, 0, 0, 0, 0, 0
	}
	if l2 < 0 {
		l2 = 0
	}
	a := 2 * math.Sqrt(l1)
	b := 2 * math.Sqrt(l2)
	if b < 0.5 {
		b = 0.5 // keep a sliver renderable
	}
	ang := 0.5 * math.Atan2(2*m11, m20-m02)
	// IoU vs the mask: rasterise the ellipse over the bbox.
	ca, sa := math.Cos(-ang), math.Sin(-ang)
	var inter, union float64
	for y := 0; y < r.BH; y++ {
		for x := 0; x < r.BW; x++ {
			dx, dy := float64(x)-mx, float64(y)-my
			xr := ca*dx - sa*dy
			yr := sa*dx + ca*dy
			inE := (xr*xr)/(a*a)+(yr*yr)/(b*b) <= 1
			inM := r.Mask[y*r.BW+x]
			if inE && inM {
				inter++
			}
			if inE || inM {
				union++
			}
		}
	}
	if union <= 0 {
		return 0, 0, 0, 0, 0, 0
	}
	return float64(r.X0) + mx, float64(r.Y0) + my, a, b, ang * 180 / math.Pi, inter / union
}

// CoverEllipse returns the moment-fit ellipse as a one-shape cover of the region. scale slightly enlarges
// it (e.g. 1.12) so it reaches past the mask boundary and overlaps neighbours — no seam/gap at the edge.
func CoverEllipse(r *Region, scale float64) []model.Shape {
	cx, cy, rx, ry, deg, _ := FitEllipse(r)
	if rx <= 0 || ry <= 0 {
		return nil
	}
	if scale <= 0 {
		scale = 1
	}
	return []model.Shape{{Type: model.TypeRotatedEllipse,
		Color: []int{C255(r.Color.R), C255(r.Color.G), C255(r.Color.B), 255},
		Data:  []float64{cx, cy, rx * scale, ry * scale, deg}}}
}
