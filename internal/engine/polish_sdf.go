package engine

import "math"

// Per-primitive signed-distance-field gradients used by the joint polish (polish.go). Each returns the
// signed distance of pixel centre (px,py) to the shape (negative inside) plus the analytic gradient
// w.r.t. the shape parameters. Split out of polish.go as the self-contained geometry-math concern; the
// dispatch (sdfGrad/optimizableGeo) and the optimisation loop stay in polish.go (same package).

// ellipseSDFGrad returns the (approximate Euclidean) signed distance of pixel
// center (px,py) to the rotated ellipse P=[cx,cy,rx,ry,thetaDeg] (negative
// inside) and its gradient w.r.t. P[0..4]. sdf = (k-1)*min(rx,ry) with
// k = sqrt((xr/rx)^2 + (yr/ry)^2) in the ellipse-local rotated frame.
func ellipseSDFGrad(P [6]float64, px, py float64) (sdf float64, g [5]float64) {
	cx, cy := P[0], P[1]
	rx := math.Max(1, P[2])
	ry := math.Max(1, P[3])
	th := P[4] * polishDeg2Rad
	cs, sn := math.Cos(th), math.Sin(th)
	dx, dy := px-cx, py-cy
	xr := dx*cs + dy*sn
	yr := -dx*sn + dy*cs
	u, v := xr/rx, yr/ry
	k := math.Sqrt(u*u + v*v)
	if k < 1e-9 {
		k = 1e-9
	}
	m := rx
	if ry < rx {
		m = ry
	}
	sdf = (k - 1) * m

	dkdxr := xr / (rx * rx * k)
	dkdyr := yr / (ry * ry * k)
	// xr,yr partials: dxr/dcx=-cs, dxr/dcy=-sn, dyr/dcx=sn, dyr/dcy=-cs
	dkdcx := dkdxr*(-cs) + dkdyr*(sn)
	dkdcy := dkdxr*(-sn) + dkdyr*(-cs)
	// dxr/dth = yr ; dyr/dth = -xr (radians)
	dkdthRad := dkdxr*yr + dkdyr*(-xr)
	dkdrx := -(xr * xr) / (rx * rx * rx * k)
	dkdry := -(yr * yr) / (ry * ry * ry * k)
	dmdrx, dmdry := 0.0, 0.0
	if rx <= ry {
		dmdrx = 1
	} else {
		dmdry = 1
	}
	g[0] = m * dkdcx
	g[1] = m * dkdcy
	g[2] = m*dkdrx + (k-1)*dmdrx
	g[3] = m*dkdry + (k-1)*dmdry
	g[4] = m * dkdthRad * polishDeg2Rad
	return
}

// rectSDFGrad returns the exact signed distance of (px,py) to the rotated
// rectangle P=[cx,cy,hw,hh,thetaDeg] (negative inside) and its gradient w.r.t.
// P[0..4]. Box SDF: q=(|xr|-hw,|yr|-hh); sdf=length(max(q,0))+min(max(qx,qy),0).
func rectSDFGrad(P [6]float64, px, py float64) (sdf float64, g [5]float64) {
	cx, cy := P[0], P[1]
	hw := math.Max(0.5, P[2])
	hh := math.Max(0.5, P[3])
	th := P[4] * polishDeg2Rad
	cs, sn := math.Cos(th), math.Sin(th)
	dx, dy := px-cx, py-cy
	xr := dx*cs + dy*sn
	yr := -dx*sn + dy*cs
	sx := signf(xr)
	sy := signf(yr)
	qx := math.Abs(xr) - hw
	qy := math.Abs(yr) - hh
	// partials of qx,qy w.r.t cx,cy,theta(rad),hw,hh
	// d|xr|/dcx = sx*dxr/dcx; dxr/dcx=-cs, dxr/dcy=-sn, dxr/dth=yr
	dqx := [5]float64{sx * (-cs), sx * (-sn), -1, 0, sx * yr}   // wrt cx,cy,hw,hh,thRad
	dqy := [5]float64{sy * (sn), sy * (-cs), 0, -1, sy * (-xr)} // dyr/dcx=sn,dcy=-cs,dth=-xr
	if qx > 0 || qy > 0 {
		// outside: sdf = sqrt(mqx^2+mqy^2)
		mqx := math.Max(qx, 0)
		mqy := math.Max(qy, 0)
		sdf = math.Sqrt(mqx*mqx + mqy*mqy)
		inv := 0.0
		if sdf > 1e-9 {
			inv = 1 / sdf
		}
		for i := 0; i < 5; i++ {
			var t float64
			if qx > 0 {
				t += mqx * dqx[i]
			}
			if qy > 0 {
				t += mqy * dqy[i]
			}
			g[i] = t * inv
		}
	} else {
		// inside: sdf = max(qx,qy); gradient follows the larger one.
		if qx >= qy {
			g = dqx
			sdf = qx
		} else {
			g = dqy
			sdf = qy
		}
	}
	g[4] *= polishDeg2Rad // thetaRad -> thetaDeg
	return
}

func signf(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// triangleSDFGrad returns the signed distance of (px,py) to the triangle
// P=[x1,y1,x2,y2,x3,y3] (negative inside) and its gradient w.r.t. all SIX vertex
// coords. sdf magnitude = distance to the nearest edge segment (IQ's 2D triangle
// SDF); sign from the min winding term (orientation-independent via s). Gradient:
// for the distance-active edge (start vertex S, end E, foot param t, pq=foot->p),
// d(sdf)/dS = sgn*(1-t)*n, d(sdf)/dE = sgn*t*n (n=pq/|pq|), third vertex = 0 — the
// foot-optimality (or clamp) makes the d(t) term vanish, so this holds for interior
// AND clamped feet. sgn is piecewise-constant (zero a.e. gradient). FD-verified.
func triangleSDFGrad(P [6]float64, px, py float64) (sdf float64, g [6]float64) {
	ax, ay := P[0], P[1]
	bx, by := P[2], P[3]
	cx, cy := P[4], P[5]
	e0x, e0y := bx-ax, by-ay // edge a->b
	e1x, e1y := cx-bx, cy-by // edge b->c
	e2x, e2y := ax-cx, ay-cy // edge c->a
	v0x, v0y := px-ax, py-ay
	v1x, v1y := px-bx, py-by
	v2x, v2y := px-cx, py-cy
	clamp01 := func(t float64) float64 {
		if t < 0 {
			return 0
		}
		if t > 1 {
			return 1
		}
		return t
	}
	d0 := e0x*e0x + e0y*e0y
	d1 := e1x*e1x + e1y*e1y
	d2 := e2x*e2x + e2y*e2y
	if d0 < 1e-12 {
		d0 = 1e-12
	}
	if d1 < 1e-12 {
		d1 = 1e-12
	}
	if d2 < 1e-12 {
		d2 = 1e-12
	}
	t0 := clamp01((v0x*e0x + v0y*e0y) / d0)
	t1 := clamp01((v1x*e1x + v1y*e1y) / d1)
	t2 := clamp01((v2x*e2x + v2y*e2y) / d2)
	pq0x, pq0y := v0x-e0x*t0, v0y-e0y*t0
	pq1x, pq1y := v1x-e1x*t1, v1y-e1y*t1
	pq2x, pq2y := v2x-e2x*t2, v2y-e2y*t2
	dd0 := pq0x*pq0x + pq0y*pq0y
	dd1 := pq1x*pq1x + pq1y*pq1y
	dd2 := pq2x*pq2x + pq2y*pq2y
	s := 1.0
	if e0x*e2y-e0y*e2x < 0 {
		s = -1
	}
	w0 := s * (v0x*e0y - v0y*e0x)
	w1 := s * (v1x*e1y - v1y*e1x)
	w2 := s * (v2x*e2y - v2y*e2x)
	// componentwise min (IQ): ddmin from nearest edge, wmin for the sign.
	ddmin, active := dd0, 0
	if dd1 < ddmin {
		ddmin, active = dd1, 1
	}
	if dd2 < ddmin {
		ddmin, active = dd2, 2
	}
	wmin := w0
	if w1 < wmin {
		wmin = w1
	}
	if w2 < wmin {
		wmin = w2
	}
	dist := math.Sqrt(ddmin)
	sgn := 1.0
	if wmin < 0 {
		sgn = -1
	}
	sdf = -dist * sgn
	if dist < 1e-9 {
		return // on an edge: gradient ~0 (sigmoid slope dominates)
	}
	var t, pqx, pqy float64
	var sIdx, eIdx int
	switch active {
	case 0:
		t, pqx, pqy, sIdx, eIdx = t0, pq0x, pq0y, 0, 2
	case 1:
		t, pqx, pqy, sIdx, eIdx = t1, pq1x, pq1y, 2, 4
	default:
		t, pqx, pqy, sIdx, eIdx = t2, pq2x, pq2y, 4, 0
	}
	nx, ny := pqx/dist, pqy/dist
	g[sIdx+0] = sgn * (1 - t) * nx
	g[sIdx+1] = sgn * (1 - t) * ny
	g[eIdx+0] = sgn * t * nx
	g[eIdx+1] = sgn * t * ny
	return
}
