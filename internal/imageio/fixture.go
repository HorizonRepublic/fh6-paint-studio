package imageio

import (
	"image"
	"image/color"
	"math"
)

// SyntheticFixture renders a deterministic test image: a diagonal colour gradient with a soft radial
// highlight, overlaid with hard-edged filled shapes (rectangle, ellipse, triangle) and two straight
// lines. It is a pure function of (w, h) — identical dimensions always yield byte-identical pixels — so
// it is a stable input for the behaviour-preserving engine fingerprint.
// The mix of a smooth gradient with sharp edges makes a reconstruction exercise every shape kind.
func SyntheticFixture(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	fw, fh := float64(w), float64(h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x)/fw, float64(y)/fh
			hl := math.Max(0, 1-2*math.Hypot(fx-0.5, fy-0.5)) // soft radial highlight, peak at centre
			img.SetNRGBA(x, y, color.NRGBA{
				R: fxClamp8(40 + 180*fx + 50*hl),
				G: fxClamp8(60 + 150*fy + 40*hl),
				B: fxClamp8(200 - 60*(fx+fy) + 30*hl),
				A: 255,
			})
		}
	}
	fxRect(img, w/8, h/8, 3*w/8, 3*h/8, color.NRGBA{R: 220, G: 40, B: 40, A: 255})
	fxEllipse(img, 5*w/8, h/4, w/6, h/6, color.NRGBA{R: 40, G: 200, B: 80, A: 255})
	fxTriangle(img, w/2, 5*h/8, w/4, 7*h/8, 3*w/4, 7*h/8, color.NRGBA{R: 250, G: 210, B: 40, A: 255})
	fxLine(img, 0, h-1, w-1, 0, color.NRGBA{R: 20, G: 20, B: 20, A: 255})
	fxLine(img, 0, h/2, w-1, h/2, color.NRGBA{R: 245, G: 245, B: 245, A: 255})
	return img
}

func fxClamp8(v float64) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	default:
		return uint8(v)
	}
}

// fxRect fills the half-open rectangle [x0,x1)×[y0,y1). Out-of-bounds writes are no-ops (SetNRGBA clips).
func fxRect(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

// fxEllipse fills the axis-aligned ellipse centred at (cx,cy) with radii (rx,ry).
func fxEllipse(img *image.NRGBA, cx, cy, rx, ry int, c color.NRGBA) {
	for y := cy - ry; y <= cy+ry; y++ {
		for x := cx - rx; x <= cx+rx; x++ {
			nx, ny := float64(x-cx)/float64(rx), float64(y-cy)/float64(ry)
			if nx*nx+ny*ny <= 1 {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

// fxTriangle fills the triangle (x0,y0),(x1,y1),(x2,y2) via the edge-function (half-plane) test.
func fxTriangle(img *image.NRGBA, x0, y0, x1, y1, x2, y2 int, c color.NRGBA) {
	if fxEdge(x0, y0, x1, y1, x2, y2) == 0 {
		return // degenerate
	}
	minX, maxX := fxMin3(x0, x1, x2), fxMax3(x0, x1, x2)
	minY, maxY := fxMin3(y0, y1, y2), fxMax3(y0, y1, y2)
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			w0, w1, w2 := fxEdge(x1, y1, x2, y2, x, y), fxEdge(x2, y2, x0, y0, x, y), fxEdge(x0, y0, x1, y1, x, y)
			if (w0 >= 0 && w1 >= 0 && w2 >= 0) || (w0 <= 0 && w1 <= 0 && w2 <= 0) {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

// fxLine draws a 1-pixel Bresenham line from (x0,y0) to (x1,y1).
func fxLine(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	dx, dy := fxAbs(x1-x0), -fxAbs(y1-y0)
	sx, sy := fxSign(x1-x0), fxSign(y1-y0)
	err := dx + dy
	for {
		img.SetNRGBA(x0, y0, c)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

// fxEdge is twice the signed area of triangle (a,b,c) — positive when c is left of a->b.
func fxEdge(ax, ay, bx, by, cx, cy int) int { return (bx-ax)*(cy-ay) - (by-ay)*(cx-ax) }

func fxAbs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func fxSign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}

func fxMin3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func fxMax3(a, b, c int) int {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}
