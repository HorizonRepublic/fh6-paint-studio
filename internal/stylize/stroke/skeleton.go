package stroke

// zhangSuenThin reduces a binary mask to a 1-pixel-wide skeleton (Zhang & Suen, CACM 1984): two
// sub-iterations of parallel deletion until a full pass deletes nothing. Returns a new mask.
func zhangSuenThin(mask []bool, w, h int) []bool {
	cur := make([]bool, len(mask))
	copy(cur, mask)
	at := func(m []bool, x, y int) int {
		if x < 0 || y < 0 || x >= w || y >= h || !m[y*w+x] {
			return 0
		}
		return 1
	}
	// neighbours clockwise from N: P2..P9
	nb := func(m []bool, x, y int) [8]int {
		return [8]int{
			at(m, x, y-1), at(m, x+1, y-1), at(m, x+1, y), at(m, x+1, y+1),
			at(m, x, y+1), at(m, x-1, y+1), at(m, x-1, y), at(m, x-1, y-1),
		}
	}
	transitions := func(p [8]int) int {
		c := 0
		for i := 0; i < 8; i++ {
			if p[i] == 0 && p[(i+1)%8] == 1 {
				c++
			}
		}
		return c
	}
	sum := func(p [8]int) int {
		s := 0
		for _, v := range p {
			s += v
		}
		return s
	}
	for {
		changed := false
		for step := 0; step < 2; step++ {
			var del []int
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					if !cur[y*w+x] {
						continue
					}
					p := nb(cur, x, y) // P2,P3,P4,P5,P6,P7,P8,P9
					b := sum(p)
					if b < 2 || b > 6 || transitions(p) != 1 {
						continue
					}
					// p[0]=P2(N) p[2]=P4(E) p[4]=P6(S) p[6]=P8(W)
					if step == 0 {
						if p[0]*p[2]*p[4] == 0 && p[2]*p[4]*p[6] == 0 {
							del = append(del, y*w+x)
						}
					} else {
						if p[0]*p[2]*p[6] == 0 && p[0]*p[4]*p[6] == 0 {
							del = append(del, y*w+x)
						}
					}
				}
			}
			if len(del) > 0 {
				changed = true
				for _, i := range del {
					cur[i] = false
				}
			}
		}
		if !changed {
			break
		}
	}
	return cur
}

// traceSkeleton walks a 1-px skeleton into ordered polylines (image coords). Branches run between nodes
// (endpoints with 1 neighbour or junctions with ≥3); interior pixels (2 neighbours) are the path. Pure
// loops with no node are traced as closed paths. Spurs shorter than minLen are dropped.
func traceSkeleton(skel []bool, w, h int, minLen int) [][][2]float64 {
	deg := make([]int, w*h)
	nbrs := func(x, y int) [][2]int {
		var out [][2]int
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				nx, ny := x+dx, y+dy
				if nx >= 0 && ny >= 0 && nx < w && ny < h && skel[ny*w+nx] {
					out = append(out, [2]int{nx, ny})
				}
			}
		}
		return out
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if skel[y*w+x] {
				deg[y*w+x] = len(nbrs(x, y))
			}
		}
	}
	used := make([]bool, w*h) // interior pixels consumed by a branch
	var polys [][][2]float64
	edgeSeen := map[int]bool{}
	firstStep := func(ax, ay, bx, by int) int {
		i, j := ay*w+ax, by*w+bx
		if i > j {
			i, j = j, i
		}
		return i*w*h + j
	}
	walk := func(sx, sy, fx, fy int) {
		poly := [][2]float64{{float64(sx), float64(sy)}}
		px, py := sx, sy
		cx, cy := fx, fy
		edgeSeen[firstStep(px, py, cx, cy)] = true
		for {
			poly = append(poly, [2]float64{float64(cx), float64(cy)})
			if deg[cy*w+cx] != 2 { // reached a node → stop
				break
			}
			used[cy*w+cx] = true
			var nx, ny int
			found := false
			for _, n := range nbrs(cx, cy) {
				if n[0] == px && n[1] == py {
					continue
				}
				nx, ny = n[0], n[1]
				found = true
				break
			}
			if !found {
				break
			}
			edgeSeen[firstStep(cx, cy, nx, ny)] = true
			px, py, cx, cy = cx, cy, nx, ny
			if cx == sx && cy == sy { // closed loop
				break
			}
		}
		if len(poly) >= 2 && (len(poly) >= minLen || deg[sy*w+sx] >= 3 || deg[cy*w+cx] >= 3) {
			polys = append(polys, poly)
		}
	}
	// branches from nodes
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !skel[y*w+x] || deg[y*w+x] == 2 {
				continue
			}
			for _, n := range nbrs(x, y) {
				if edgeSeen[firstStep(x, y, n[0], n[1])] {
					continue
				}
				walk(x, y, n[0], n[1])
			}
		}
	}
	// pure loops (no node, all deg==2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !skel[y*w+x] || used[y*w+x] || deg[y*w+x] != 2 {
				continue
			}
			ns := nbrs(x, y)
			if len(ns) == 0 {
				continue
			}
			walk(x, y, ns[0][0], ns[0][1])
		}
	}
	return polys
}
