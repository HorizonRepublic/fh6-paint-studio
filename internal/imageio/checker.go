package imageio

import (
	"image"
	"image/draw"

	"fh6-paint-studio/internal/applog"
)

// Baked-checkerboard transparency stripper. Photostock "transparent" PNGs often arrive with the
// editor's checkerboard BAKED into an opaque image (the youtube-logo case: alpha=255 everywhere,
// ~20px (255,255,255)/(239,239,239) lattice) — the engine then spends shapes reconstructing the
// checker and the auto-knee trips on the low-contrast pattern. Detect that lattice and return a
// copy with it stripped to real alpha BEFORE the maxRes downscale blurs it away.
//
// The lattice is rarely integer-perfect (a rescaled source drops an occasional ±1px run), so the
// cell grid is recovered from the actual border run boundaries, not assumed at x/T — the parity of
// a pixel is its (cellX+cellY)%2 against those boundaries.
//
// The detection is deliberately conservative (a false positive silently deletes content):
//   - the image must be fully opaque (real alpha → nothing to do),
//   - row 0 and column 0 must alternate between exactly TWO light, low-saturation colors with a
//     near-constant square period (runs within ±1 of the median),
//   - ≥97% of the perimeter must match the recovered lattice (a legit in-content chess texture
//     doesn't own the whole frame),
//   - stripped pixels must be lattice-exact in PHASE: connected to the perimeter through matching
//     pixels, or an enclosed matching component spanning ≥2T² (uniform content that coincidentally
//     hits one checker color only matches its own parity — isolated ≤T² squares — and is rejected).

const (
	checkerMinPeriod = 4
	checkerMaxPeriod = 128
	checkerTol       = 10  // per-channel match tolerance for lattice pixels
	checkerEdgeTol   = 28  // looser tolerance for the 1px anti-aliased halo pass
	checkerMinLuma   = 140 // both lattice colors must be light...
	checkerMaxSat    = 14  // ...and near-gray (editor checkers are white/light-gray)
)

type checkerLattice struct {
	period int
	c      [2][3]uint8 // color of even/odd-parity cells (cell (0,0) = c[0])
	cellX  []int       // per-pixel cell index along x (from the row-0 run boundaries)
	cellY  []int       // per-pixel cell index along y (from the column-0 run boundaries)
}

func (lat *checkerLattice) expected(x, y int) [3]uint8 {
	return lat.c[(lat.cellX[x]+lat.cellY[y])%2]
}

// stripBakedChecker returns img with a detected baked checkerboard turned into alpha 0, or img
// unchanged when no lattice passes the gates.
func stripBakedChecker(img image.Image) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 4*checkerMinPeriod || h < 4*checkerMinPeriod {
		return img
	}
	n, ok := img.(*image.NRGBA)
	if !ok || b.Min != (image.Point{}) || n.Stride != w*4 {
		n = image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(n, n.Bounds(), img, b.Min, draw.Src)
	}
	lat := detectLattice(n, w, h)
	if lat == nil {
		return img
	}
	mask := latticeMask(n, w, h, lat)
	if mask == nil {
		return img
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	copy(out.Pix, n.Pix)
	var stripped int
	for i, m := range mask {
		if m {
			out.Pix[i*4+3] = 0
			stripped++
		}
	}
	applog.Printf("imageio: baked checkerboard transparency stripped (period=%dpx, %.0f%% of pixels)",
		lat.period, 100*float64(stripped)/float64(w*h))
	return out
}

func chMatch(p []uint8, c [3]uint8, tol int) bool {
	return absI(int(p[0])-int(c[0])) <= tol && absI(int(p[1])-int(c[1])) <= tol && absI(int(p[2])-int(c[2])) <= tol
}

func checkerColorOK(c [3]uint8) bool {
	luma := (299*int(c[0]) + 587*int(c[1]) + 114*int(c[2])) / 1000
	mx, mn := int(c[0]), int(c[0])
	for _, v := range c[1:] {
		if int(v) > mx {
			mx = int(v)
		}
		if int(v) < mn {
			mn = int(v)
		}
	}
	return luma >= checkerMinLuma && mx-mn <= checkerMaxSat
}

// borderRuns walks one border line and returns the color-flip cell boundaries (start index of every
// cell, 0 first) and the two run colors; ok=false when the line is not a clean two-color
// alternation with a near-constant (±1) period.
func borderRuns(n *image.NRGBA, w, h int, vertical bool) (bounds []int, cols [2][3]uint8, period int, ok bool) {
	length := w
	if vertical {
		length = h
	}
	at := func(i int) []uint8 {
		if vertical {
			return n.Pix[i*n.Stride : i*n.Stride+4]
		}
		return n.Pix[i*4 : i*4+4]
	}
	p0 := at(0)
	cols[0] = [3]uint8{p0[0], p0[1], p0[2]}
	bounds = append(bounds, 0)
	colorIdx, haveSecond := 0, false
	for i := 1; i < length; i++ {
		p := at(i)
		if chMatch(p, cols[colorIdx], checkerTol) {
			continue
		}
		next := 1 - colorIdx
		if !haveSecond {
			cols[1] = [3]uint8{p[0], p[1], p[2]}
			haveSecond = true
		} else if !chMatch(p, cols[next], checkerTol) {
			return nil, cols, 0, false // a third color — not an editor lattice
		}
		bounds = append(bounds, i)
		colorIdx = next
	}
	if len(bounds) < 4 {
		return nil, cols, 0, false
	}
	// Median run; every full run must sit within ±1 of it (rescale jitter), incl. the last cell.
	runs := make([]int, 0, len(bounds))
	for i := 1; i < len(bounds); i++ {
		runs = append(runs, bounds[i]-bounds[i-1])
	}
	period = medianInt(runs)
	if period < checkerMinPeriod || period > checkerMaxPeriod {
		return nil, cols, 0, false
	}
	// Rescaled sources jitter the flip points (anti-aliased seams swallow a few px), so allow a
	// per-run drift of period/4; the recovered boundaries absorb it exactly in the phase mask.
	maxJitter := period / 4
	if maxJitter < 1 {
		maxJitter = 1
	}
	for _, r := range runs {
		if absI(r-period) > maxJitter {
			return nil, cols, 0, false
		}
	}
	if last := length - bounds[len(bounds)-1]; last > period+maxJitter {
		return nil, cols, 0, false
	}
	return bounds, cols, period, true
}

func medianInt(v []int) int {
	s := append([]int(nil), v...)
	for i := 1; i < len(s); i++ { // insertion sort — the slice is tiny
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s[len(s)/2]
}

// cellIndex expands run boundaries into a per-pixel cell index lookup.
func cellIndex(bounds []int, length int) []int {
	idx := make([]int, length)
	cell := 0
	for i := 0; i < length; i++ {
		if cell+1 < len(bounds) && i >= bounds[cell+1] {
			cell++
		}
		idx[i] = cell
	}
	return idx
}

// detectLattice infers the lattice from the borders and verifies it owns the perimeter.
func detectLattice(n *image.NRGBA, w, h int) *checkerLattice {
	// Fully-opaque gate: an image with real alpha needs no stripping.
	for i := 3; i < len(n.Pix); i += 4 {
		if n.Pix[i] != 255 {
			return nil
		}
	}
	xb, cols, pRow, ok := borderRuns(n, w, h, false)
	if !ok {
		return nil
	}
	yb, _, pCol, ok := borderRuns(n, w, h, true)
	if !ok || absI(pRow-pCol) > 1 {
		return nil
	}
	if !checkerColorOK(cols[0]) || !checkerColorOK(cols[1]) || cols[0] == cols[1] {
		return nil
	}
	lat := &checkerLattice{period: pRow, c: cols, cellX: cellIndex(xb, w), cellY: cellIndex(yb, h)}
	match, total := 0, 0
	visit := func(x, y int) {
		p := n.Pix[y*n.Stride+x*4 : y*n.Stride+x*4+4]
		if chMatch(p, lat.expected(x, y), checkerTol) {
			match++
		}
		total++
	}
	for x := 0; x < w; x++ {
		visit(x, 0)
		visit(x, h-1)
	}
	for y := 1; y < h-1; y++ {
		visit(0, y)
		visit(w-1, y)
	}
	if match*100 < total*97 {
		return nil
	}
	return lat
}

// latticeMask marks the strippable pixels: perimeter-connected lattice matches plus enclosed
// matching components ≥2T² (interior holes of letters etc.), then one anti-alias halo pass.
func latticeMask(n *image.NRGBA, w, h int, lat *checkerLattice) []bool {
	cand := make([]bool, w*h)
	for y := 0; y < h; y++ {
		row := y * n.Stride
		for x := 0; x < w; x++ {
			cand[y*w+x] = chMatch(n.Pix[row+x*4:row+x*4+4], lat.expected(x, y), checkerTol)
		}
	}
	mask := make([]bool, w*h)
	queue := make([]int, 0, w*h/4)
	push := func(i int) {
		if cand[i] && !mask[i] {
			mask[i] = true
			queue = append(queue, i)
		}
	}
	for x := 0; x < w; x++ {
		push(x)
		push((h-1)*w + x)
	}
	for y := 0; y < h; y++ {
		push(y * w)
		push(y*w + w - 1)
	}
	for len(queue) > 0 {
		i := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x, y := i%w, i/w
		if x > 0 {
			push(i - 1)
		}
		if x < w-1 {
			push(i + 1)
		}
		if y > 0 {
			push(i - w)
		}
		if y < h-1 {
			push(i + w)
		}
	}
	// Enclosed components (holes inside letters): accept when they span ≥2T² — a uniform content
	// region hitting one checker color only matches its own parity, i.e. isolated ≤T² squares.
	minHole := 2 * lat.period * lat.period
	comp := make([]int, 0, minHole*2)
	for start := range cand {
		if !cand[start] || mask[start] {
			continue
		}
		comp = comp[:0]
		mask[start] = true
		comp = append(comp, start)
		for head := 0; head < len(comp); head++ {
			i := comp[head]
			x, y := i%w, i/w
			if x > 0 && cand[i-1] && !mask[i-1] {
				mask[i-1] = true
				comp = append(comp, i-1)
			}
			if x < w-1 && cand[i+1] && !mask[i+1] {
				mask[i+1] = true
				comp = append(comp, i+1)
			}
			if y > 0 && cand[i-w] && !mask[i-w] {
				mask[i-w] = true
				comp = append(comp, i-w)
			}
			if y < h-1 && cand[i+w] && !mask[i+w] {
				mask[i+w] = true
				comp = append(comp, i+w)
			}
		}
		if len(comp) < minHole {
			for _, i := range comp {
				mask[i] = false
			}
		}
	}
	// One halo pass: anti-aliased seam pixels (cell boundaries, faint content edges) adjacent to
	// stripped ones, matched at a looser tolerance against EITHER lattice color.
	halo := make([]int, 0, w*h/16)
	for i, m := range mask {
		if m {
			continue
		}
		x, y := i%w, i/w
		near := (x > 0 && mask[i-1]) || (x < w-1 && mask[i+1]) || (y > 0 && mask[i-w]) || (y < h-1 && mask[i+w])
		if near {
			p := n.Pix[y*n.Stride+x*4 : y*n.Stride+x*4+4]
			if chMatch(p, lat.c[0], checkerEdgeTol) || chMatch(p, lat.c[1], checkerEdgeTol) {
				halo = append(halo, i)
			}
		}
	}
	for _, i := range halo {
		mask[i] = true
	}
	var covered int
	for _, m := range mask {
		if m {
			covered++
		}
	}
	// Coverage floor: a lattice that owns the perimeter but almost nothing else is suspicious.
	if covered*20 < w*h {
		return nil
	}
	return mask
}
