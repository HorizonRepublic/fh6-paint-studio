// Shared geometry + SDF-gradient math for the Vulkan joint-polish shaders. Mirrors
// internal/engine/polish.go + polish_sdf.go (the golden reference). Geometry runs in
// float32 (GLSL has no double trig/sqrt); per-shape gradient sums accumulate in double
// in the backward shader. The polish golden-diff allows 1.5% rel, which absorbs this —
// the same float32-hot-path / double-final recipe the CUDA backend passes with.
//
// Kinds handled by the polish: 0=ellipse 1=rectangle 2=triangle (optGeo, trainable geometry),
// 3=line (hard inside, colour/alpha only), 4=glow and 5=disk (per-pixel radial alpha; the glow
// carries an analytic geometry gradient, the disk's geometry is frozen — same split as the CUDA
// shim and engine/polish.go).

#ifndef POLISH_COMMON_GLSL
#define POLISH_COMMON_GLSL

const float PDEG2RAD = 0.017453292519943295;

int pclampi(int v, int lo, int hi) { return v < lo ? lo : (v > hi ? hi : v); }
float psign(float v) { return v < 0.0 ? -1.0 : 1.0; }

// Hard inside test (line/non-optGeo + hard-loss render). Mirrors raster.Inside.
bool insideShape(int kind, float P[6], int x, int y) {
    float px = float(x) + 0.5, py = float(y) + 0.5;
    if (kind == 2) {
        float d1 = (px - P[4]) * (P[1] - P[5]) - (P[0] - P[4]) * (py - P[5]);
        float d2 = (px - P[0]) * (P[3] - P[1]) - (P[2] - P[0]) * (py - P[1]);
        float d3 = (px - P[2]) * (P[5] - P[3]) - (P[4] - P[2]) * (py - P[3]);
        bool hasNeg = d1 < 0.0 || d2 < 0.0 || d3 < 0.0;
        bool hasPos = d1 > 0.0 || d2 > 0.0 || d3 > 0.0;
        return !(hasNeg && hasPos);
    } else if (kind == 3) {
        float x1 = P[0], y1 = P[1], x2 = P[2], y2 = P[3], hwid = max(0.5, P[4]);
        float dx = x2 - x1, dy = y2 - y1, l2 = dx * dx + dy * dy, t = 0.0;
        if (l2 > 0.0) { t = ((px - x1) * dx + (py - y1) * dy) / l2; t = clamp(t, 0.0, 1.0); }
        float pjx = x1 + t * dx, pjy = y1 + t * dy, ddx = px - pjx, ddy = py - pjy;
        return ddx * ddx + ddy * ddy <= hwid * hwid;
    } else if (kind == 1) {
        float cx = P[0], cy = P[1], hw = max(0.5, P[2]), hh = max(0.5, P[3]);
        float th = P[4] * PDEG2RAD, c = cos(th), s = sin(th);
        float dx = px - cx, dy = py - cy, xr = dx * c + dy * s, yr = -dx * s + dy * c;
        return abs(xr) <= hw && abs(yr) <= hh;
    } else {
        float cx = P[0], cy = P[1], rx = max(1.0, P[2]), ry = max(1.0, P[3]);
        float th = P[4] * PDEG2RAD, c = cos(th), s = sin(th);
        float dx = px - cx, dy = py - cy, xr = dx * c + dy * s, yr = -dx * s + dy * c;
        return xr * xr / (rx * rx) + yr * yr / (ry * ry) <= 1.0;
    }
}

bool optGeoKind(int kind) { return kind == 0 || kind == 1 || kind == 2; }

// ---- radial-gradient coverage (mirrors raster.FalloffGlow/FalloffDisk, shim.cu gradFalloff) ----
#define PGRAD_GLOW_E 0.0820849986238988
bool isGradKind(int kind) { return kind == 4 || kind == 5; }

float gradFalloff(int kind, float t2) {
    if (t2 >= 1.0) return 0.0;
    if (kind == 4) {
        float g = (exp(-2.5 * t2) - float(PGRAD_GLOW_E)) / (1.0 - float(PGRAD_GLOW_E));
        return 0.89 * g;
    }
    float t = sqrt(t2);
    if (t <= 0.40) return 1.0;
    float u = (t - 0.40) * (1.0 / 0.60);
    return 1.0 - (3.0 * u * u - 2.0 * u * u * u);
}

float gradCovP(int kind, float P[6], int x, int y) {
    float rx = max(1.0, P[2]), ry = max(1.0, P[3]);
    float th = P[4] * PDEG2RAD, c = cos(th), sn = sin(th);
    float dx = (float(x) + 0.5) - P[0], dy = (float(y) + 0.5) - P[1];
    float xr = dx * c + dy * sn, yr = -dx * sn + dy * c;
    return gradFalloff(kind, xr * xr / (rx * rx) + yr * yr / (ry * ry));
}

// gaussianCovGrad: KindGlow coverage AND its gradient wrt [cx,cy,rx,ry,thetaDeg]. Disk and any
// other kind return coverage only with a zero gradient (geometry frozen). Mirrors the FD-verified
// raster.GaussianCovGrad and shim.cu gaussianCovGradD.
float gaussianCovGrad(int kind, float P[6], int x, int y, out float g[6]) {
    for (int i = 0; i < 6; i++) g[i] = 0.0;
    if (kind != 4) return gradCovP(kind, P, x, y);
    float rx = max(1.0, P[2]), ry = max(1.0, P[3]);
    float th = P[4] * PDEG2RAD, c = cos(th), sn = sin(th);
    float dx = (float(x) + 0.5) - P[0], dy = (float(y) + 0.5) - P[1];
    float xr = dx * c + dy * sn, yr = -dx * sn + dy * c;
    float u = xr * xr / (rx * rx) + yr * yr / (ry * ry);
    if (u >= 1.0) return 0.0;
    float norm = 1.0 / (1.0 - float(PGRAD_GLOW_E));
    float cov = 0.89 * norm * (exp(-2.5 * u) - float(PGRAD_GLOW_E));
    float dcov = 0.89 * norm * (-2.5 * exp(-2.5 * u));
    float dudxr = 2.0 * xr / (rx * rx);
    float dudyr = 2.0 * yr / (ry * ry);
    g[0] = dcov * (dudxr * (-c) + dudyr * (sn));
    g[1] = dcov * (dudxr * (-sn) + dudyr * (-c));
    if (P[2] > 1.0) g[2] = dcov * (-2.0 * xr * xr / (rx * rx * rx));
    if (P[3] > 1.0) g[3] = dcov * (-2.0 * yr * yr / (ry * ry * ry));
    g[4] = dcov * (2.0 * PDEG2RAD * xr * yr * (1.0 / (rx * rx) - 1.0 / (ry * ry)));
    return cov;
}

// ellipseSDFGrad: signed distance (negative inside) + gradient wrt P[0..4] in g[0..4].
float ellipseSDFG(float P[6], float px, float py, out float g[6]) {
    float cx = P[0], cy = P[1], rx = max(1.0, P[2]), ry = max(1.0, P[3]);
    float th = P[4] * PDEG2RAD, cs = cos(th), sn = sin(th);
    float dx = px - cx, dy = py - cy;
    float xr = dx * cs + dy * sn, yr = -dx * sn + dy * cs;
    float u = xr / rx, v = yr / ry;
    float k = sqrt(u * u + v * v); if (k < 1e-9) k = 1e-9;
    float m = min(rx, ry);
    float dkdxr = xr / (rx * rx * k), dkdyr = yr / (ry * ry * k);
    float dkdcx = dkdxr * (-cs) + dkdyr * (sn);
    float dkdcy = dkdxr * (-sn) + dkdyr * (-cs);
    float dkdth = dkdxr * yr + dkdyr * (-xr);
    float dkdrx = -(xr * xr) / (rx * rx * rx * k);
    float dkdry = -(yr * yr) / (ry * ry * ry * k);
    float dmdrx = 0.0, dmdry = 0.0;
    if (rx <= ry) dmdrx = 1.0; else dmdry = 1.0;
    g[0] = m * dkdcx; g[1] = m * dkdcy;
    g[2] = m * dkdrx + (k - 1.0) * dmdrx; g[3] = m * dkdry + (k - 1.0) * dmdry;
    g[4] = m * dkdth * PDEG2RAD; g[5] = 0.0;
    return (k - 1.0) * m;
}

// rectSDFGrad: exact box SDF + gradient wrt P[0..4].
float rectSDFG(float P[6], float px, float py, out float g[6]) {
    float cx = P[0], cy = P[1], hw = max(0.5, P[2]), hh = max(0.5, P[3]);
    float th = P[4] * PDEG2RAD, cs = cos(th), sn = sin(th);
    float dx = px - cx, dy = py - cy;
    float xr = dx * cs + dy * sn, yr = -dx * sn + dy * cs;
    float sx = psign(xr), sy = psign(yr);
    float qx = abs(xr) - hw, qy = abs(yr) - hh;
    float dqx[5]; dqx[0] = sx * (-cs); dqx[1] = sx * (-sn); dqx[2] = -1.0; dqx[3] = 0.0; dqx[4] = sx * yr;
    float dqy[5]; dqy[0] = sy * (sn); dqy[1] = sy * (-cs); dqy[2] = 0.0; dqy[3] = -1.0; dqy[4] = sy * (-xr);
    float sdf;
    g[5] = 0.0;
    if (qx > 0.0 || qy > 0.0) {
        float mqx = max(qx, 0.0), mqy = max(qy, 0.0);
        sdf = sqrt(mqx * mqx + mqy * mqy);
        float inv = sdf > 1e-9 ? 1.0 / sdf : 0.0;
        for (int i = 0; i < 5; i++) {
            float t = 0.0;
            if (qx > 0.0) t += mqx * dqx[i];
            if (qy > 0.0) t += mqy * dqy[i];
            g[i] = t * inv;
        }
    } else {
        if (qx >= qy) { for (int i = 0; i < 5; i++) g[i] = dqx[i]; sdf = qx; }
        else          { for (int i = 0; i < 5; i++) g[i] = dqy[i]; sdf = qy; }
    }
    g[4] *= PDEG2RAD;
    return sdf;
}

// triangleSDFGrad: IQ 2D triangle SDF + gradient wrt all 6 vertex coords. Computed in
// DOUBLE (no trig here, only the one sqrt is float-then-promoted): the nearest-edge
// selection + winding sign are precision-sensitive on the medial axis, so float32 drifts
// past the gradient tolerance — double matches the float64 reference.
float triangleSDFG(float Pf[6], float pxf, float pyf, out float g[6]) {
    for (int i = 0; i < 6; i++) g[i] = 0.0;
    double ax = double(Pf[0]), ay = double(Pf[1]), bx = double(Pf[2]), by = double(Pf[3]), cx = double(Pf[4]), cy = double(Pf[5]);
    double px = double(pxf), py = double(pyf);
    double e0x = bx - ax, e0y = by - ay, e1x = cx - bx, e1y = cy - by, e2x = ax - cx, e2y = ay - cy;
    double v0x = px - ax, v0y = py - ay, v1x = px - bx, v1y = py - by, v2x = px - cx, v2y = py - cy;
    double d0 = max(e0x * e0x + e0y * e0y, 1e-12LF);
    double d1 = max(e1x * e1x + e1y * e1y, 1e-12LF);
    double d2 = max(e2x * e2x + e2y * e2y, 1e-12LF);
    double t0 = clamp((v0x * e0x + v0y * e0y) / d0, 0.0LF, 1.0LF);
    double t1 = clamp((v1x * e1x + v1y * e1y) / d1, 0.0LF, 1.0LF);
    double t2 = clamp((v2x * e2x + v2y * e2y) / d2, 0.0LF, 1.0LF);
    double pq0x = v0x - e0x * t0, pq0y = v0y - e0y * t0;
    double pq1x = v1x - e1x * t1, pq1y = v1y - e1y * t1;
    double pq2x = v2x - e2x * t2, pq2y = v2y - e2y * t2;
    double dd0 = pq0x * pq0x + pq0y * pq0y;
    double dd1 = pq1x * pq1x + pq1y * pq1y;
    double dd2 = pq2x * pq2x + pq2y * pq2y;
    double s = (e0x * e2y - e0y * e2x < 0.0LF) ? -1.0LF : 1.0LF;
    double w0 = s * (v0x * e0y - v0y * e0x);
    double w1 = s * (v1x * e1y - v1y * e1x);
    double w2 = s * (v2x * e2y - v2y * e2x);
    double ddmin = dd0; int actEdge = 0;
    if (dd1 < ddmin) { ddmin = dd1; actEdge = 1; }
    if (dd2 < ddmin) { ddmin = dd2; actEdge = 2; }
    double wmin = min(w0, min(w1, w2));
    double dist = double(sqrt(float(ddmin)));
    double sgn = wmin < 0.0LF ? -1.0LF : 1.0LF;
    if (dist < 1e-9LF) return float(-dist * sgn);
    double t, pqx, pqy; int sIdx, eIdx;
    if (actEdge == 0)      { t = t0; pqx = pq0x; pqy = pq0y; sIdx = 0; eIdx = 2; }
    else if (actEdge == 1) { t = t1; pqx = pq1x; pqy = pq1y; sIdx = 2; eIdx = 4; }
    else                  { t = t2; pqx = pq2x; pqy = pq2y; sIdx = 4; eIdx = 0; }
    double nx = pqx / dist, ny = pqy / dist;
    g[sIdx + 0] = float(sgn * (1.0LF - t) * nx); g[sIdx + 1] = float(sgn * (1.0LF - t) * ny);
    g[eIdx + 0] = float(sgn * t * nx);           g[eIdx + 1] = float(sgn * t * ny);
    return float(-dist * sgn);
}

float sdfGradKind(int kind, float P[6], float px, float py, out float g[6]) {
    if (kind == 1) return rectSDFG(P, px, py, g);
    if (kind == 2) return triangleSDFG(P, px, py, g);
    return ellipseSDFG(P, px, py, g);
}

float sigmoidCov(float sdf, float tau) {
    float z = sdf / tau;
    if (z > 40.0) return 0.0;
    if (z < -40.0) return 1.0;
    return 1.0 / (1.0 + exp(z));
}

#endif
