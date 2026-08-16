package metric

import (
	"math"
	"sort"
)

// perceptual.go — full-reference perceptual metrics for judging a stylized render against its source.
// These correlate with the eye far better than raw SSE (the project's durable finding: SSE↔perception
// diverge), so they are the quantitative double-check for stylizer quality work.
//
// Inputs are sRGB-encoded straight-alpha RGBA buffers in [0,1] — the format of both a loaded source and
// the RenderFH6 result — NOT linear light. Alpha is ignored (renders are opaque over the bg).
// Lower ΔE = closer colour; higher SSIM = closer structure (max 1).

func srgbToLinD(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// srgbToLab maps one sRGB pixel ([0,1]) to CIELAB under a D65 white point.
func srgbToLab(r, g, b float64) (L, A, B float64) {
	rl, gl, bl := srgbToLinD(r), srgbToLinD(g), srgbToLinD(b)
	// linear sRGB -> XYZ (D65), then normalise by the D65 white.
	x := (0.4124564*rl + 0.3575761*gl + 0.1804375*bl) / 0.95047
	y := 0.2126729*rl + 0.7151522*gl + 0.0721750*bl
	z := (0.0193339*rl + 0.1191920*gl + 0.9503041*bl) / 1.08883
	f := func(t float64) float64 {
		if t > 216.0/24389.0 {
			return math.Cbrt(t)
		}
		return (24389.0/27.0*t + 16) / 116
	}
	fx, fy, fz := f(x), f(y), f(z)
	return 116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)
}

// DeltaE76 returns the mean and 95th-percentile CIEDE76 (Euclidean Lab) over all pixels of two equal-size
// sRGB RGBA buffers. ΔE≈2.3 is the just-noticeable difference; the p95 catches localised banding the mean
// would average away.
func DeltaE76(a, b []float32, w, h int) (mean, p95 float64) {
	n := w * h
	if n == 0 || len(a) < n*4 || len(b) < n*4 {
		return 0, 0
	}
	de := make([]float64, n)
	var sum float64
	for i := 0; i < n; i++ {
		l1, a1, b1 := srgbToLab(float64(a[i*4]), float64(a[i*4+1]), float64(a[i*4+2]))
		l2, a2, b2 := srgbToLab(float64(b[i*4]), float64(b[i*4+1]), float64(b[i*4+2]))
		d := math.Sqrt((l1-l2)*(l1-l2) + (a1-a2)*(a1-a2) + (b1-b2)*(b1-b2))
		de[i] = d
		sum += d
	}
	sort.Float64s(de)
	return sum / float64(n), de[int(float64(n)*0.95)]
}

// DeltaE76Mean is DeltaE76's mean alone — the SAME serial accumulation, so the number is
// bit-identical to DeltaE76's first return — without materialising and sorting an n-element
// array for a percentile the caller was discarding (134MB + a 16.7M-element sort at the 4096
// fit, paid at the end of EVERY run). Callers that want the p95 keep using DeltaE76.
func DeltaE76Mean(a, b []float32, w, h int) float64 {
	n := w * h
	if n == 0 || len(a) < n*4 || len(b) < n*4 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		l1, a1, b1 := srgbToLab(float64(a[i*4]), float64(a[i*4+1]), float64(a[i*4+2]))
		l2, a2, b2 := srgbToLab(float64(b[i*4]), float64(b[i*4+1]), float64(b[i*4+2]))
		sum += math.Sqrt((l1-l2)*(l1-l2) + (a1-a2)*(a1-a2) + (b1-b2)*(b1-b2))
	}
	return sum / float64(n)
}

// FalseEdges quantifies POSTERIZATION (banding): the mean render edge energy at pixels where the SOURCE
// is smooth. A flat-cell render puts hard steps inside smooth gradients — exactly the artefact ΔE/SSIM
// are blind to and the eye hates. a=source, b=render (sRGB RGBA [0,1]); smoothThresh is the source-gradient
// ceiling that counts as "smooth" (luma units, ~0.02). Lower = smoother where the source is smooth.
// Scaled ×100 for readable digits.
func FalseEdges(a, b []float32, w, h int, smoothThresh float64) float64 {
	n := w * h
	if n == 0 || len(a) < n*4 || len(b) < n*4 || w < 3 || h < 3 {
		return 0
	}
	lum := func(buf []float32, i int) float64 {
		return 0.299*float64(buf[i*4]) + 0.587*float64(buf[i*4+1]) + 0.114*float64(buf[i*4+2])
	}
	grad := func(buf []float32, x, y int) float64 {
		dx := lum(buf, y*w+x+1) - lum(buf, y*w+x-1)
		dy := lum(buf, (y+1)*w+x) - lum(buf, (y-1)*w+x)
		return math.Hypot(dx, dy)
	}
	var sum float64
	var cnt int
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			if grad(a, x, y) < smoothThresh { // source is smooth here
				sum += grad(b, x, y) // ...so any render edge is spurious (banding)
				cnt++
			}
		}
	}
	if cnt == 0 {
		return 0
	}
	return 100 * sum / float64(cnt)
}

// SSIM returns the mean structural similarity over the luma channel (Wang et al. 2004), computed on
// 8×8 windows at stride 4 with the standard stabilisers C1=(0.01)², C2=(0.03)² for a [0,1] range.
func SSIM(a, b []float32, w, h int) float64 {
	n := w * h
	if n == 0 || len(a) < n*4 || len(b) < n*4 {
		return 0
	}
	la := make([]float64, n)
	lb := make([]float64, n)
	for i := 0; i < n; i++ {
		la[i] = 0.299*float64(a[i*4]) + 0.587*float64(a[i*4+1]) + 0.114*float64(a[i*4+2])
		lb[i] = 0.299*float64(b[i*4]) + 0.587*float64(b[i*4+1]) + 0.114*float64(b[i*4+2])
	}
	const win, stride = 8, 4
	const c1, c2 = 0.0001, 0.0009 // (0.01*L)^2, (0.03*L)^2 for L=1
	var sum float64
	var cnt int
	for y := 0; y+win <= h; y += stride {
		for x := 0; x+win <= w; x += stride {
			var ma, mb float64
			for dy := 0; dy < win; dy++ {
				for dx := 0; dx < win; dx++ {
					p := (y+dy)*w + (x + dx)
					ma += la[p]
					mb += lb[p]
				}
			}
			const m = win * win
			ma /= m
			mb /= m
			var va, vb, cov float64
			for dy := 0; dy < win; dy++ {
				for dx := 0; dx < win; dx++ {
					p := (y+dy)*w + (x + dx)
					da, db := la[p]-ma, lb[p]-mb
					va += da * da
					vb += db * db
					cov += da * db
				}
			}
			va /= m - 1
			vb /= m - 1
			cov /= m - 1
			s := ((2*ma*mb + c1) * (2*cov + c2)) / ((ma*ma + mb*mb + c1) * (va + vb + c2))
			sum += s
			cnt++
		}
	}
	if cnt == 0 {
		return 0
	}
	return sum / float64(cnt)
}
