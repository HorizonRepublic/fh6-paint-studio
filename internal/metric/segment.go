package metric

import (
	"math"
	"sort"

	"fh6-paint-studio/internal/model"
)

// Segments is a colour-coherent partition of the target: every pixel carries the dense label of the
// region it belongs to, plus that region's mean colour and pixel count.
type Segments struct {
	Label []int32   // len w*h, values in [0, N)
	Mean  []float32 // 3*N linear RGB
	Size  []int32   // N
	N     int
	W, H  int
}

// LabelAt returns the label at (x, y), or -1 outside the canvas.
func (s *Segments) LabelAt(x, y int) int32 {
	if s == nil || x < 0 || y < 0 || x >= s.W || y >= s.H {
		return -1
	}
	return s.Label[y*s.W+x]
}

// Segment partitions the target into colour-coherent regions (Felzenszwalb-Huttenlocher graph
// segmentation on an 8-connected pixel grid). Unlike the existing target maps it answers a
// DIFFERENT question: HardEdgeMap says how structured a neighbourhood is, BoundaryDistance says how
// far the nearest edge is — both scalar and isotropic. A label map says WHICH side of an edge a
// pixel is on, which is what a shape needs to know to stay on one side of it.
//
// The scale k trades region count against size: the merge predicate lets a component absorb an edge
// of weight w only while w <= min over both components of (internal difference + k/size), so k is
// roughly "how much colour variation a small region may still swallow". minSize then absorbs the
// leftover specks, which are numerous on photographic content and useless as containment regions.
//
// Weights are Euclidean distance in sRGB-encoded channels, matching HardEdgeMap's reasoning: linear
// light crushes the darks, and a segmentation that cannot see a shadow boundary would hand back one
// region spanning a lit and an unlit surface.
func Segment(target []float32, w, h int, k float64, minSize int) *Segments {
	if w <= 0 || h <= 0 || len(target) < w*h*4 {
		return nil
	}
	px := smoothSRGB(target, w, h)

	type edge struct {
		a, b int32
		wt   float32
	}
	edges := make([]edge, 0, w*h*4)
	dist := func(i, j int) float32 {
		dr := px[i*3] - px[j*3]
		dg := px[i*3+1] - px[j*3+1]
		db := px[i*3+2] - px[j*3+2]
		return float32(math.Sqrt(float64(dr*dr + dg*dg + db*db)))
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if x+1 < w {
				edges = append(edges, edge{int32(i), int32(i + 1), dist(i, i+1)})
			}
			if y+1 < h {
				edges = append(edges, edge{int32(i), int32(i + w), dist(i, i+w)})
			}
			if x+1 < w && y+1 < h {
				edges = append(edges, edge{int32(i), int32(i + w + 1), dist(i, i+w+1)})
			}
			if x > 0 && y+1 < h {
				edges = append(edges, edge{int32(i), int32(i + w - 1), dist(i, i+w-1)})
			}
		}
	}
	// Bucket sort by quantized weight, ties broken by edge index: a comparison sort of ~8M edges is
	// the whole cost of the pass, and the order must not depend on the sort's internals — the label
	// map feeds candidate generation, where a reordering would read as a different seed.
	const buckets = 1 << 16
	var maxW float32
	for i := range edges {
		if edges[i].wt > maxW {
			maxW = edges[i].wt
		}
	}
	order := make([]int32, len(edges))
	if maxW > 0 {
		count := make([]int32, buckets+1)
		bk := make([]int32, len(edges))
		scale := float32(buckets-1) / maxW
		for i := range edges {
			b := int32(edges[i].wt * scale)
			bk[i] = b
			count[b+1]++
		}
		for b := 1; b <= buckets; b++ {
			count[b] += count[b-1]
		}
		for i := range edges {
			order[count[bk[i]]] = int32(i)
			count[bk[i]]++
		}
	} else {
		for i := range order {
			order[i] = int32(i)
		}
	}

	n := w * h
	parent := make([]int32, n)
	size := make([]int32, n)
	intDiff := make([]float32, n)
	for i := range parent {
		parent[i] = int32(i)
		size[i] = 1
	}
	var find func(int32) int32
	find = func(x int32) int32 {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	kf := float32(k)
	for _, oi := range order {
		e := edges[oi]
		a, b := find(e.a), find(e.b)
		if a == b {
			continue
		}
		ta := intDiff[a] + kf/float32(size[a])
		tb := intDiff[b] + kf/float32(size[b])
		thr := ta
		if tb < thr {
			thr = tb
		}
		if e.wt > thr {
			continue
		}
		if size[a] < size[b] {
			a, b = b, a
		}
		parent[b] = a
		size[a] += size[b]
		intDiff[a] = e.wt
	}
	// Absorb specks: a region below minSize cannot contain a shape, so leaving it in only fragments
	// its neighbours' containment. Merging along the sorted edges keeps it colour-greedy.
	if minSize > 1 {
		for _, oi := range order {
			e := edges[oi]
			a, b := find(e.a), find(e.b)
			if a == b || (size[a] >= int32(minSize) && size[b] >= int32(minSize)) {
				continue
			}
			if size[a] < size[b] {
				a, b = b, a
			}
			parent[b] = a
			size[a] += size[b]
		}
	}

	seg := &Segments{Label: make([]int32, n), W: w, H: h}
	dense := make(map[int32]int32, 1024)
	for i := 0; i < n; i++ {
		r := find(int32(i))
		id, ok := dense[r]
		if !ok {
			id = int32(len(dense))
			dense[r] = id
		}
		seg.Label[i] = id
	}
	seg.N = len(dense)
	seg.Size = make([]int32, seg.N)
	seg.Mean = make([]float32, 3*seg.N)
	for i := 0; i < n; i++ {
		l := seg.Label[i]
		seg.Size[l]++
		for c := 0; c < 3; c++ {
			seg.Mean[int(l)*3+c] += target[i*4+c]
		}
	}
	for l := 0; l < seg.N; l++ {
		if seg.Size[l] > 0 {
			for c := 0; c < 3; c++ {
				seg.Mean[l*3+c] /= float32(seg.Size[l])
			}
		}
	}
	return seg
}

// smoothSRGB returns the sRGB-encoded RGB planes interleaved, pre-blurred with a 5-tap binomial
// kernel (sigma ~0.8). Without it the segmentation chases sensor noise and JPEG ringing.
func smoothSRGB(target []float32, w, h int) []float32 {
	src := make([]float32, w*h*3)
	for i := 0; i < w*h; i++ {
		for c := 0; c < 3; c++ {
			src[i*3+c] = model.LinearToSRGB(target[i*4+c])
		}
	}
	kern := [5]float32{1.0 / 16, 4.0 / 16, 6.0 / 16, 4.0 / 16, 1.0 / 16}
	tmp := make([]float32, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				var s float32
				for t := -2; t <= 2; t++ {
					xx := x + t
					if xx < 0 {
						xx = 0
					} else if xx >= w {
						xx = w - 1
					}
					s += kern[t+2] * src[(y*w+xx)*3+c]
				}
				tmp[(y*w+x)*3+c] = s
			}
		}
	}
	out := make([]float32, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				var s float32
				for t := -2; t <= 2; t++ {
					yy := y + t
					if yy < 0 {
						yy = 0
					} else if yy >= h {
						yy = h - 1
					}
					s += kern[t+2] * tmp[(yy*w+x)*3+c]
				}
				out[(y*w+x)*3+c] = s
			}
		}
	}
	return out
}

// RegionOrder returns region labels sorted by descending pixel count.
func (s *Segments) RegionOrder() []int32 {
	if s == nil {
		return nil
	}
	ids := make([]int32, s.N)
	for i := range ids {
		ids[i] = int32(i)
	}
	sort.SliceStable(ids, func(a, b int) bool { return s.Size[ids[a]] > s.Size[ids[b]] })
	return ids
}
