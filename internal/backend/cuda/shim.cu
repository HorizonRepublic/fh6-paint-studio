// shim.cu — CUDA backend for Forza Painter, compiled by nvcc (+MSVC host) into
// forzacuda.dll. The Go side loads the DLL and calls these extern "C" functions
// via syscall (no cgo), so the Go build stays pure (CGO_ENABLED=0).
//
// This MIRRORS internal/backend/cpu/cpu.go (the golden reference). Accumulators
// use double to match the CPU's float64 math within float32 tolerance. The
// candidate wire format is a flat float32 array, 11 floats per candidate:
//   [kind, p0,p1,p2,p3,p4,p5, R,G,B,A]
// Results are 5 floats per candidate: [score, oR,oG,oB,oA].
//
// Shape kinds (match model.ShapeKind): 0=ellipse 1=rectangle 2=triangle 3=line.

#include <cuda_runtime.h>
#include <cuda_fp16.h>
#include <math.h>
#include <float.h>

#define DEG2RAD 0.017453292519943295
#define BLOCK 128
#define NACC 19           // accumulators per candidate (see eval kernel)
#define REJECTED FLT_MAX

#ifdef _WIN32
#define API extern "C" __declspec(dllexport)
#else
#define API extern "C"
#endif

// ---- device/host geometry (ports of internal/raster/raster.go) ----

__host__ __device__ inline double clamp01d(double v) { return v < 0 ? 0 : (v > 1 ? 1 : v); }
__host__ __device__ inline int clampI(int v, int lo, int hi) { return v < lo ? lo : (v > hi ? hi : v); }
__host__ __device__ inline double fmaxd(double a, double b) { return a > b ? a : b; }

// ellipse: P=[cx,cy,rx,ry,thetaDeg]
__host__ __device__ bool ellipseInside(const float* P, int x, int y) {
    double cx = P[0], cy = P[1];
    double rx = fmaxd(1.0, (double)P[2]), ry = fmaxd(1.0, (double)P[3]);
    double t = (double)P[4] * DEG2RAD, c = cos(t), s = sin(t);
    double dx = x + 0.5 - cx, dy = y + 0.5 - cy;
    double xr = dx * c + dy * s, yr = -dx * s + dy * c;
    return xr * xr / (rx * rx) + yr * yr / (ry * ry) <= 1.0;
}
__host__ __device__ void ellipseBBox(const float* P, int w, int h, int* xMin, int* yMin, int* xMax, int* yMax) {
    double cx = P[0], cy = P[1];
    double rx = fmaxd(1.0, (double)P[2]), ry = fmaxd(1.0, (double)P[3]);
    double t = (double)P[4] * DEG2RAD, c = cos(t), s = sin(t);
    double ex = sqrt(rx * rx * c * c + ry * ry * s * s);
    double ey = sqrt(rx * rx * s * s + ry * ry * c * c);
    *xMin = clampI((int)floor(cx - ex - 1), 0, w - 1);
    *xMax = clampI((int)ceil(cx + ex + 1), 0, w - 1);
    *yMin = clampI((int)floor(cy - ey - 1), 0, h - 1);
    *yMax = clampI((int)ceil(cy + ey + 1), 0, h - 1);
}

// rectangle: P=[cx,cy,halfW,halfH,thetaDeg]
__host__ __device__ bool rectInside(const float* P, int x, int y) {
    double cx = P[0], cy = P[1];
    double hw = fmaxd(0.5, (double)P[2]), hh = fmaxd(0.5, (double)P[3]);
    double t = (double)P[4] * DEG2RAD, c = cos(t), s = sin(t);
    double dx = x + 0.5 - cx, dy = y + 0.5 - cy;
    double xr = dx * c + dy * s, yr = -dx * s + dy * c;
    return fabs(xr) <= hw && fabs(yr) <= hh;
}
__host__ __device__ void rectBBox(const float* P, int w, int h, int* xMin, int* yMin, int* xMax, int* yMax) {
    double cx = P[0], cy = P[1];
    double hw = fmaxd(0.5, (double)P[2]), hh = fmaxd(0.5, (double)P[3]);
    double t = (double)P[4] * DEG2RAD, c = cos(t), s = sin(t);
    double ex = fabs(hw * c) + fabs(hh * s);
    double ey = fabs(hw * s) + fabs(hh * c);
    *xMin = clampI((int)floor(cx - ex - 1), 0, w - 1);
    *xMax = clampI((int)ceil(cx + ex + 1), 0, w - 1);
    *yMin = clampI((int)floor(cy - ey - 1), 0, h - 1);
    *yMax = clampI((int)ceil(cy + ey + 1), 0, h - 1);
}

// triangle: P=[x1,y1,x2,y2,x3,y3]
__host__ __device__ inline double triSign(double ax, double ay, double bx, double by, double cx, double cy) {
    return (ax - cx) * (by - cy) - (bx - cx) * (ay - cy);
}
__host__ __device__ bool triangleInside(const float* P, int x, int y) {
    double px = x + 0.5, py = y + 0.5;
    double d1 = triSign(px, py, P[0], P[1], P[2], P[3]);
    double d2 = triSign(px, py, P[2], P[3], P[4], P[5]);
    double d3 = triSign(px, py, P[4], P[5], P[0], P[1]);
    bool hasNeg = d1 < 0 || d2 < 0 || d3 < 0;
    bool hasPos = d1 > 0 || d2 > 0 || d3 > 0;
    return !(hasNeg && hasPos);
}
__host__ __device__ void triangleBBox(const float* P, int w, int h, int* xMin, int* yMin, int* xMax, int* yMax) {
    double minX = fmin(P[0], fmin(P[2], P[4])), maxX = fmax(P[0], fmax(P[2], P[4]));
    double minY = fmin(P[1], fmin(P[3], P[5])), maxY = fmax(P[1], fmax(P[3], P[5]));
    *xMin = clampI((int)floor(minX), 0, w - 1);
    *xMax = clampI((int)ceil(maxX), 0, w - 1);
    *yMin = clampI((int)floor(minY), 0, h - 1);
    *yMax = clampI((int)ceil(maxY), 0, h - 1);
}

// line/capsule: P=[x1,y1,x2,y2,halfWidth]
__host__ __device__ bool lineInside(const float* P, int x, int y) {
    double x1 = P[0], y1 = P[1], x2 = P[2], y2 = P[3];
    double hwid = fmaxd(0.5, (double)P[4]);
    double px = x + 0.5, py = y + 0.5;
    double dx = x2 - x1, dy = y2 - y1, l2 = dx * dx + dy * dy, t = 0;
    if (l2 > 0) {
        t = ((px - x1) * dx + (py - y1) * dy) / l2;
        if (t < 0) t = 0;
        if (t > 1) t = 1;
    }
    double projX = x1 + t * dx, projY = y1 + t * dy;
    double ddx = px - projX, ddy = py - projY;
    return ddx * ddx + ddy * ddy <= hwid * hwid;
}
__host__ __device__ void lineBBox(const float* P, int w, int h, int* xMin, int* yMin, int* xMax, int* yMax) {
    double x1 = P[0], y1 = P[1], x2 = P[2], y2 = P[3], hwid = fmaxd(0.5, (double)P[4]);
    *xMin = clampI((int)floor(fmin(x1, x2) - hwid), 0, w - 1);
    *xMax = clampI((int)ceil(fmax(x1, x2) + hwid), 0, w - 1);
    *yMin = clampI((int)floor(fmin(y1, y2) - hwid), 0, h - 1);
    *yMax = clampI((int)ceil(fmax(y1, y2) + hwid), 0, h - 1);
}

__host__ __device__ bool inside(int kind, const float* P, int x, int y) {
    switch (kind) {
        case 1: return rectInside(P, x, y);
        case 2: return triangleInside(P, x, y);
        case 3: return lineInside(P, x, y);
        default: return ellipseInside(P, x, y);
    }
}
__host__ __device__ void bbox(int kind, const float* P, int w, int h, int* xMin, int* yMin, int* xMax, int* yMax) {
    switch (kind) {
        case 1: rectBBox(P, w, h, xMin, yMin, xMax, yMax); return;
        case 2: triangleBBox(P, w, h, xMin, yMin, xMax, yMax); return;
        case 3: lineBBox(P, w, h, xMin, yMin, xMax, yMax); return;
        default: ellipseBBox(P, w, h, xMin, yMin, xMax, yMax); return;
    }
}

// targetSamples is the progressive-sampling pixel cap (mirrors CPU c.sampleBudget,
// passed in as a kernel arg). A cap >= bbox area yields step 1 (full-res scoring).
__host__ __device__ int sampleStep(int xMin, int yMin, int xMax, int yMax, int targetSamples) {
    if (targetSamples < 1) targetSamples = 4000;
    long long area = (long long)(xMax - xMin + 1) * (long long)(yMax - yMin + 1); // long is 32-bit on MSVC
    if (area <= targetSamples) return 1;
    int step = (int)sqrt((double)area / (double)targetSamples);
    return step < 1 ? 1 : step;
}

// ---- fast per-shape inside test for the eval hot loop ----
// The eval kernel tests thousands of pixels per candidate; ellipse/rect recompute
// cos/sin per pixel in the generic inside(). SC precomputes the rotation (and other
// per-shape constants) ONCE per candidate, then insideC() reuses them. Results are
// bit-identical to inside() (same t -> same cos/sin), so the golden match is preserved.
struct SC {
    float cx, cy, c, s, a0, a1;          // ellipse: a0=rx^2 a1=ry^2; rect: a0=hw a1=hh
    float lx, ly, ldx, ldy, ll2, lhw;    // line/capsule
};

// prepShape computes the constants once (rotation in double for accuracy) and
// stores them as float; insideC then runs the per-pixel test entirely in float32
// (fp64 is ~1/64 throughput on consumer Blackwell, and this runs per sampled pixel).
// Boundary pixels can rarely flip vs the double reference — golden-diff tolerance
// (cuda_test.go) bounds the effect, and end-to-end error is unchanged.
__device__ void prepShape(int kind, const float* P, SC* sc) {
    if (kind == 1) { // rectangle
        sc->cx = P[0]; sc->cy = P[1];
        sc->a0 = (float)fmaxd(0.5, (double)P[2]); sc->a1 = (float)fmaxd(0.5, (double)P[3]);
        double cc, ss; sincos((double)P[4] * DEG2RAD, &ss, &cc);
        sc->c = (float)cc; sc->s = (float)ss;
    } else if (kind == 3) { // line
        sc->lx = P[0]; sc->ly = P[1];
        sc->ldx = P[2] - P[0]; sc->ldy = P[3] - P[1];
        sc->ll2 = sc->ldx * sc->ldx + sc->ldy * sc->ldy;
        sc->lhw = (float)fmaxd(0.5, (double)P[4]);
    } else if (kind != 2) { // ellipse (kind 0; triangle uses P directly)
        sc->cx = P[0]; sc->cy = P[1];
        float rx = (float)fmaxd(1.0, (double)P[2]), ry = (float)fmaxd(1.0, (double)P[3]);
        sc->a0 = rx * rx; sc->a1 = ry * ry;
        double cc, ss; sincos((double)P[4] * DEG2RAD, &ss, &cc);
        sc->c = (float)cc; sc->s = (float)ss;
    }
}

__device__ bool insideC(int kind, const SC* sc, const float* P, int x, int y) {
    float px = x + 0.5f, py = y + 0.5f;
    if (kind == 1) {
        float dx = px - sc->cx, dy = py - sc->cy;
        float xr = dx * sc->c + dy * sc->s, yr = -dx * sc->s + dy * sc->c;
        return fabsf(xr) <= sc->a0 && fabsf(yr) <= sc->a1;
    } else if (kind == 2) {
        // Triangle has no trig; the double sign test is cheap and avoids inlining
        // mistakes. Matches TriangleInside exactly.
        double d1 = triSign(px, py, P[0], P[1], P[2], P[3]);
        double d2 = triSign(px, py, P[2], P[3], P[4], P[5]);
        double d3 = triSign(px, py, P[4], P[5], P[0], P[1]);
        bool hasNeg = d1 < 0 || d2 < 0 || d3 < 0;
        bool hasPos = d1 > 0 || d2 > 0 || d3 > 0;
        return !(hasNeg && hasPos);
    } else if (kind == 3) {
        float t = 0;
        if (sc->ll2 > 0) {
            t = ((px - sc->lx) * sc->ldx + (py - sc->ly) * sc->ldy) / sc->ll2;
            if (t < 0) t = 0;
            if (t > 1) t = 1;
        }
        float ddx = px - (sc->lx + t * sc->ldx), ddy = py - (sc->ly + t * sc->ldy);
        return ddx * ddx + ddy * ddy <= sc->lhw * sc->lhw;
    }
    float dx = px - sc->cx, dy = py - sc->cy; // ellipse
    float xr = dx * sc->c + dy * sc->s, yr = -dx * sc->s + dy * sc->c;
    return xr * xr / sc->a0 + yr * yr / sc->a1 <= 1.0f;
}

// ---- radial-gradient coverage (mirrors raster.FalloffGlow / FalloffDisk) ----
// The native FH6 gradient kinds (KindGlow=4, KindDisk=5) contribute a per-pixel alpha = colour.A *
// falloff(t), where t is the elliptical normalised radius (0 centre, 1 footprint edge). The two
// profiles were calibrated live; constants must match internal/raster/gradient.go. This path is NOT
// bit-golden vs the CPU (float exp), so it is validated by SSE match-or-beat like the on-device search.
#define GRAD_GLOW_E 0.0820849986238988f // exp(-2.5)
__device__ inline float gradFalloff(int kind, float t2) {
    if (t2 >= 1.0f) return 0.0f;
    if (kind == 4) { // soft gaussian glow: 0.89 * (exp(-2.5 t²) - e) / (1 - e)
        float g = (expf(-2.5f * t2) - GRAD_GLOW_E) / (1.0f - GRAD_GLOW_E);
        return 0.89f * g;
    }
    // disk (kind 5): opaque core to 0.40, then 1 - smoothstep over the rim
    float t = sqrtf(t2);
    if (t <= 0.40f) return 1.0f;
    float u = (t - 0.40f) * (1.0f / 0.60f);
    return 1.0f - (3.0f * u * u - 2.0f * u * u * u);
}

// gradCovC: gradient coverage in the eval hot loop (reuses the precomputed SC rotation/axes²).
__device__ inline float gradCovC(int kind, const SC* sc, int x, int y) {
    float dx = (x + 0.5f) - sc->cx, dy = (y + 0.5f) - sc->cy;
    float xr = dx * sc->c + dy * sc->s, yr = -dx * sc->s + dy * sc->c;
    return gradFalloff(kind, xr * xr / sc->a0 + yr * yr / sc->a1);
}

// gradCovP: gradient coverage from raw params (apply kernel — one thread per pixel, no SC).
__device__ inline float gradCovP(int kind, const float* P, int x, int y) {
    float rx = fmaxf(1.0f, P[2]), ry = fmaxf(1.0f, P[3]);
    float th = (float)(P[4] * DEG2RAD), c = cosf(th), s = sinf(th);
    float dx = (x + 0.5f) - P[0], dy = (y + 0.5f) - P[1];
    float xr = dx * c + dy * s, yr = -dx * s + dy * c;
    return gradFalloff(kind, xr * xr / (rx * rx) + yr * yr / (ry * ry));
}

__device__ inline bool isGradKind(int kind) { return kind == 4 || kind == 5; }

// gaussianCovGradD: KindGlow coverage at pixel (x,y) AND its analytic gradient wrt the 5 ellipse
// params [cx,cy,rx,ry,thetaDeg] (slot 5 = 0). Device mirror of the FD-verified CPU reference
// raster.GaussianCovGrad — the SMOOTH (trainable) gaussian-splat gradient that makes a glow's
// geometry optimisable in the joint polish (#7 GaussianImage). cov = 0.89·norm·(exp(-2.5u) − E),
// u = (xr/rx)²+(yr/ry)² the squared elliptical radius in the rotated frame. Disk (kind 5) has a flat
// opaque core → zero geometry gradient (g stays 0); the caller still composites its falloff coverage.
// Inputs are the float params (mirrors CPU using float32 fp → float64 math); returns cov in double.
__device__ inline double gaussianCovGradD(int kind, const float* fp, int x, int y, double* g) {
    for (int i = 0; i < 6; i++) g[i] = 0.0;
    if (kind != 4) return (double)gradCovP(kind, fp, x, y); // disk/other: coverage only, geometry frozen
    double cx = (double)fp[0], cy = (double)fp[1];
    double rx = fmax(1.0, (double)fp[2]), ry = fmax(1.0, (double)fp[3]);
    double th = (double)fp[4] * DEG2RAD, c = cos(th), s = sin(th);
    double dx = (double)x + 0.5 - cx, dy = (double)y + 0.5 - cy;
    double xr = dx * c + dy * s, yr = -dx * s + dy * c;
    double u = xr * xr / (rx * rx) + yr * yr / (ry * ry);
    if (u >= 1.0) return 0.0;
    const double E = (double)GRAD_GLOW_E;       // exp(-2.5), matches gradFalloff
    const double norm = 1.0 / (1.0 - E), peak = 0.89;
    double cov  = peak * norm * (exp(-2.5 * u) - E);
    double dcov = peak * norm * (-2.5 * exp(-2.5 * u)); // dcov/du
    double dudxr = 2.0 * xr / (rx * rx);
    double dudyr = 2.0 * yr / (ry * ry);
    g[0] = dcov * (dudxr * (-c) + dudyr * (s));   // d/dcx
    g[1] = dcov * (dudxr * (-s) + dudyr * (-c));  // d/dcy
    if ((double)fp[2] > 1.0) g[2] = dcov * (-2.0 * xr * xr / (rx * rx * rx)); // d/drx (respect max(1,·) clamp)
    if ((double)fp[3] > 1.0) g[3] = dcov * (-2.0 * yr * yr / (ry * ry * ry)); // d/dry
    g[4] = dcov * (2.0 * DEG2RAD * xr * yr * (1.0 / (rx * rx) - 1.0 / (ry * ry))); // d/dthetaDeg
    return cov;
}

// ---- on-device candidate generation (build "B1") ----
// A counter-based hash RNG (no per-thread state array, no curand_init cost): each
// uniform draw is hash(seed, candidateIdx, ++ctr). Independent-enough streams for
// candidate sampling, fully deterministic per (seed, i). The generation stream
// DIFFERS from the Go math/rand path, so the on-device SEARCH is NOT golden-diffable
// vs the CPU — it is validated by bench SSE (must match-or-beat the host path).
__device__ inline unsigned int wanghash(unsigned int x) {
    x ^= x >> 16; x *= 0x7feb352dU; x ^= x >> 15; x *= 0x846ca68bU; x ^= x >> 16;
    return x;
}
__device__ inline float uf(unsigned long long seed, unsigned int i, unsigned int& ctr) {
    unsigned int a = (unsigned int)seed ^ 0x9e3779b9U;
    unsigned int b = (unsigned int)(seed >> 32);
    unsigned int h = wanghash(a + wanghash(i + 0x85ebca6bU) + wanghash((b ^ (++ctr)) * 0xc2b2ae35U));
    return (h >> 8) * (1.0f / 16777216.0f); // [0,1)
}
__device__ inline float clampf2(float v, float lo, float hi) { return v < lo ? lo : (v > hi ? hi : v); }

// ---- device state ----
static float* d_target = nullptr;
static float* d_canvas = nullptr;
static float* d_weight = nullptr;
static float* d_cands  = nullptr;
static float* d_out    = nullptr;
static float* d_grid   = nullptr;
static int g_w = 0, g_h = 0, g_gw = 0, g_gh = 0, g_maxCands = 0;
static int g_sampleBudget = 4000; // progressive-sampling pixel cap (see sampleStep / fp_set_sample_budget)
static int g_warpEval = 1;        // 1 = evalKernelWarp (warp/candidate, faster); 0 = evalKernel (block, golden fallback)
static int g_gradients = 0;       // 1 = the batch may contain gradient kinds -> force evalKernel (block): only it carries the per-pixel-alpha gradient branch (the warp kernel does not)

// ---- on-device search state (fp_search_random) ----
// Separate scratch from d_cands/d_out so the search can hold a very high candidate volume
// (up to ~1M candidates/shape) independent of the 16k Evaluate chunk size. Grown
// lazily by ensureSearch(). d_orient/d_gridcdf are uploaded by the engine (orient
// once via fp_set_orient; the grid CDF every shape via fp_search_random).
static float* d_orient  = nullptr; // edge orientation (deg), len w*h, uploaded once
static float* d_boundDist = nullptr; // distance-to-boundary (px), len w*h, uploaded once (boundary-aware radius)
static float* d_gridcdf = nullptr; // cumulative error grid, len gw*gh, per-shape H2D
static float* d_kinds   = nullptr; // kind ids as float, len nKinds
static float* d_kindcdf = nullptr; // cumulative kind weights, len nKinds
static float* d_scand   = nullptr; // search candidate scratch (g_searchCap*11)
static float* d_sout    = nullptr; // search eval results   (g_searchCap*5)
static float* d_adj     = nullptr; // selection-adjusted score (g_searchCap)
static float* d_redVal  = nullptr; // argmin partials (REDBLK)
static int*   d_redIdx  = nullptr; // argmin partials (REDBLK)
static int*   d_bestIdx = nullptr; // argmin winner index
static float* d_best    = nullptr; // best candidate out (12 floats)
static int g_searchCap  = 0;
static float* d_seeds   = nullptr; // moment seed params (g_momentCap*6: cx,cy,rx,ry,theta,capR)
static int g_momentCap  = 0;
#define REDBLK 512

// ---- coarse-to-fine search (fp_set_coarse) ----
// Score the whole random batch at a CHEAP pixel budget just to FILTER, then re-score only
// the survivors at the FULL budget and argmin those. The survivors are the per-partition
// coarse-minima (n split into KPART tiny contiguous chunks): the true full-budget winner is
// almost always its chunk's coarse-min, so the winner is selected by a FULL-budget score
// (quality-safe — sidesteps the low-res-noise mis-pick that a uniform budget cut suffers)
// while the bulk pays only the coarse cost. Off by default (g_coarseSearch=0 -> unchanged).
static int g_coarseSearch = 0;
static int g_coarseBudget = 4000;
static int g_coarseFP16 = 0;       // run the coarse FILTER pass in FP16/half2 (faster; re-eval stays FP32)
#define MAXKPART 16384             // buffer cap for the survivor scratch
static int g_kpart = 2048;         // coarse survivors re-scored at full budget (runtime, <= MAXKPART)
static int*   d_cselIdx = nullptr; // partition-winner indices (MAXKPART)
static float* d_scand2  = nullptr; // gathered survivor geometry (MAXKPART*11)
static float* d_sout2   = nullptr; // survivor full-budget eval  (MAXKPART*5)
static float* d_adj2    = nullptr; // survivor selection score   (MAXKPART)

// ---- eval kernel: one block per candidate ----
// Accumulator layout (NACC=19): 0=W 1=n 2=nt 3..6=sT(rgba) 7..10=sC 11..14=sC2 15..18=sTC
__global__ void evalKernel(const float* __restrict__ cands, int n, const float* __restrict__ target, const float* __restrict__ canvas,
                           const float* __restrict__ weight, int W, int H, int sampleBudget, float* out) {
    int gid = blockIdx.x;
    if (gid >= n) return;
    const float* cc = cands + (long)gid * 11;
    int kind = (int)(cc[0] + 0.5f);
    float P[6] = { cc[1], cc[2], cc[3], cc[4], cc[5], cc[6] };
    double a = (double)cc[10];
    if (a < 1e-3) a = 1e-3;
    if (a > 1) a = 1;

    int xMin, yMin, xMax, yMax;
    bbox(kind, P, W, H, &xMin, &yMin, &xMax, &yMax);
    int step = sampleStep(xMin, yMin, xMax, yMax, sampleBudget);
    int cols = (xMax - xMin) / step + 1;
    int rows = (yMax - yMin) / step + 1;
    long long total = (long long)cols * rows; // long is 32-bit on MSVC — widen so large bboxes don't overflow

    SC sc;
    prepShape(kind, P, &sc);
    // Per-pixel accumulation in float32: on consumer Blackwell fp64 runs at ~1/64
    // throughput, and this 19-add inner loop is the dominant cost. The sums feed a
    // final ΔSSE formula computed in double (below); golden-diff tolerance guards
    // the precision trade-off (verified by cuda_test.go).
    bool isGrad = isGradKind(kind);
    float L[NACC];
    for (int k = 0; k < NACC; k++) L[k] = 0.0f;
    for (long long tt = threadIdx.x; tt < total; tt += blockDim.x) {
        int x = xMin + (int)(tt % cols) * step;
        int y = yMin + (int)(tt / cols) * step;
        if (isGrad) {
            // per-pixel alpha a(x)=A*cov; gradient accumulators (slots differ from the hard path):
            // L0=Σwa² L1=n L2=nt | L3-5=Σwa·r | L6-8=Σwa²·s | L9-11=Σwa·r·s | L12-14=Σwa²·s² |
            // L15=alpha ΔSSE (O_A=1, direct) | L16=Σwa² over transparent (spill).
            float cov = gradCovC(kind, &sc, x, y);
            if (cov <= 0.0f) continue;
            float ap = (float)a * cov;
            int gidx = y * W + x, gp = gidx * 4;
            float4 t = __ldg(reinterpret_cast<const float4*>(target + gp));
            float wgt = __ldg(weight + gidx);
            float wa = wgt * ap, wa2 = wa * ap;
            if (t.w < 0.5f) { L[2] += 1.0f; L[16] += wa2; continue; }
            float4 s = __ldg(reinterpret_cast<const float4*>(canvas + gp));
            float rr = t.x - s.x, rg = t.y - s.y, rb = t.z - s.z;
            L[0] += wa2; L[1] += 1.0f;
            L[3] += wa * rr; L[4] += wa * rg; L[5] += wa * rb;
            L[6] += wa2 * s.x; L[7] += wa2 * s.y; L[8] += wa2 * s.z;
            L[9] += wa * rr * s.x; L[10] += wa * rg * s.y; L[11] += wa * rb * s.z;
            L[12] += wa2 * s.x * s.x; L[13] += wa2 * s.y * s.y; L[14] += wa2 * s.z * s.z;
            float oms = 1.0f - s.w;
            L[15] += wgt * (ap * ap * oms * oms - 2.0f * ap * oms * (t.w - s.w));
            continue;
        }
        if (!insideC(kind, &sc, P, x, y)) continue;
        int idx = y * W + x, p = idx * 4;
        // Vectorized 16-byte loads through the read-only cache: target+canvas are RGBA-
        // contiguous (p=idx*4, 16-byte aligned), so one float4 load each replaces 4 scalar
        // loads -> fewer memory transactions on this memory-bound kernel. Same values/order
        // as the scalar path => golden-diff unchanged.
        float4 t = __ldg(reinterpret_cast<const float4*>(target + p));
        if (t.w < 0.5f) { L[2] += 1.0f; continue; }
        float wgt = __ldg(weight + idx);
        float4 s = __ldg(reinterpret_cast<const float4*>(canvas + p));
        float tr = t.x, tg = t.y, tb = t.z, ta = t.w;
        float sr = s.x, sg = s.y, sb = s.z, sa = s.w;
        L[0] += wgt; L[1] += 1.0f;
        L[3] += wgt * tr; L[4] += wgt * tg; L[5] += wgt * tb; L[6] += wgt * ta;
        L[7] += wgt * sr; L[8] += wgt * sg; L[9] += wgt * sb; L[10] += wgt * sa;
        L[11] += wgt * sr * sr; L[12] += wgt * sg * sg; L[13] += wgt * sb * sb; L[14] += wgt * sa * sa;
        L[15] += wgt * tr * sr; L[16] += wgt * tg * sg; L[17] += wgt * tb * sb; L[18] += wgt * ta * sa;
    }

    __shared__ float sh[BLOCK * NACC];
    for (int k = 0; k < NACC; k++) sh[threadIdx.x * NACC + k] = L[k];
    __syncthreads();
    for (int s = blockDim.x / 2; s > 0; s >>= 1) {
        if (threadIdx.x < s)
            for (int k = 0; k < NACC; k++) sh[threadIdx.x * NACC + k] += sh[(threadIdx.x + s) * NACC + k];
        __syncthreads();
    }
    if (threadIdx.x != 0) return;

    if (isGrad) {
        double saa = sh[0], nn = sh[1], nt = sh[2];
        if (nn < 1 || saa <= 0) { out[gid * 5] = REJECTED; return; }
        if (nt > 0 && nt > 1.5 * nn) { out[gid * 5] = REJECTED; return; }
        double sarR = sh[3], sarG = sh[4], sarB = sh[5];
        double saasR = sh[6], saasG = sh[7], saasB = sh[8];
        double sarsR = sh[9], sarsG = sh[10], sarsB = sh[11];
        double saassR = sh[12], saassG = sh[13], saassB = sh[14];
        double dAcc = sh[15], saaT = sh[16];
        double oR = clamp01d((sarR + saasR) / saa);
        double oG = clamp01d((sarG + saasG) / saa);
        double oB = clamp01d((sarB + saasB) / saa);
        double td = dAcc;
        td += 2 * sarsR + saassR - 2 * oR * (sarR + saasR) + oR * oR * saa;
        td += 2 * sarsG + saassG - 2 * oG * (sarG + saasG) + oG * oG * saa;
        td += 2 * sarsB + saassB - 2 * oB * (sarB + saasB) + oB * oB * saa;
        if (nt > 0) {
            double spillFrac = nt / (nn + nt);
            td += saaT * (1 + 2 * spillFrac) * (oR * oR + oG * oG + oB * oB + 1);
        }
        out[gid * 5 + 0] = (float)(td * (double)(step * step));
        out[gid * 5 + 1] = (float)oR;
        out[gid * 5 + 2] = (float)oG;
        out[gid * 5 + 3] = (float)oB;
        out[gid * 5 + 4] = cc[10];
        return;
    }

    double W_ = sh[0], nn = sh[1], nt = sh[2];
    if (nn < 1 || W_ <= 0) { out[gid * 5] = REJECTED; return; }
    if (nt > 0 && nt > 1.5 * nn) { out[gid * 5] = REJECTED; return; }
    double sTR = sh[3], sTG = sh[4], sTB = sh[5], sTA = sh[6];
    double sCR = sh[7], sCG = sh[8], sCB = sh[9], sCA = sh[10];
    double sCR2 = sh[11], sCG2 = sh[12], sCB2 = sh[13], sCA2 = sh[14];
    double sTCR = sh[15], sTCG = sh[16], sTCB = sh[17], sTCA = sh[18];
    double invW = 1.0 / W_, invA = 1.0 - a;
    double oR = clamp01d((sTR * invW - (sCR * invW) * invA) / a);
    double oG = clamp01d((sTG * invW - (sCG * invW) * invA) / a);
    double oB = clamp01d((sTB * invW - (sCB * invW) * invA) / a);
    double a2 = a * a, twoA = 2 * a;
    double dR = a2 * (W_ * oR * oR - 2 * oR * sCR + sCR2) - twoA * (oR * sTR - oR * sCR - sTCR + sCR2);
    double dG = a2 * (W_ * oG * oG - 2 * oG * sCG + sCG2) - twoA * (oG * sTG - oG * sCG - sTCG + sCG2);
    double dB = a2 * (W_ * oB * oB - 2 * oB * sCB + sCB2) - twoA * (oB * sTB - oB * sCB - sTCB + sCB2);
    double dA = a2 * (W_ - 2 * sCA + sCA2) - twoA * (sTA - sCA - sTCA + sCA2);
    double totalDelta = dR + dG + dB + dA;
    if (nt > 0) {
        double spillFrac = nt / (nn + nt);
        totalDelta += a2 * nt * (1 + 2 * spillFrac) * (oR * oR + oG * oG + oB * oB + 1);
    }
    out[gid * 5 + 0] = (float)(totalDelta * (double)(step * step));
    out[gid * 5 + 1] = (float)oR;
    out[gid * 5 + 2] = (float)oG;
    out[gid * 5 + 3] = (float)oB;
    out[gid * 5 + 4] = cc[10]; // pass-through input alpha (matches CPU)
}

// evalKernelWarp: one WARP (32 lanes) per candidate instead of one 128-thread block.
// The block reduction in evalKernel (shared mem + 7 __syncthreads) is pure overhead for
// the many SMALL shapes late in a run; a warp uses __shfl_down (no shared, no sync) and
// packs 4 candidates per 128-thread block -> far higher occupancy. The per-pixel
// accumulation + the final double ΔSSE math are IDENTICAL to evalKernel; only the
// reduction changes (shuffle vs shared-tree, both fp32) so the golden-diff still holds.
__global__ void evalKernelWarp(const float* cands, int n, const float* target, const float* canvas,
                               const float* weight, int W, int H, int sampleBudget, float* out) {
    int warpId = (blockIdx.x * blockDim.x + threadIdx.x) >> 5;
    int lane = threadIdx.x & 31;
    if (warpId >= n) return;
    const float* cc = cands + (long)warpId * 11;
    int kind = (int)(cc[0] + 0.5f);
    float P[6] = { cc[1], cc[2], cc[3], cc[4], cc[5], cc[6] };
    double a = (double)cc[10];
    if (a < 1e-3) a = 1e-3;
    if (a > 1) a = 1;

    int xMin, yMin, xMax, yMax;
    bbox(kind, P, W, H, &xMin, &yMin, &xMax, &yMax);
    int step = sampleStep(xMin, yMin, xMax, yMax, sampleBudget);
    int cols = (xMax - xMin) / step + 1;
    int rows = (yMax - yMin) / step + 1;
    long long total = (long long)cols * rows; // long is 32-bit on MSVC — widen so large bboxes don't overflow

    SC sc;
    prepShape(kind, P, &sc);
    float L[NACC];
    for (int k = 0; k < NACC; k++) L[k] = 0.0f;
    for (long long tt = lane; tt < total; tt += 32) {
        int x = xMin + (int)(tt % cols) * step;
        int y = yMin + (int)(tt / cols) * step;
        if (!insideC(kind, &sc, P, x, y)) continue;
        int idx = y * W + x, p = idx * 4;
        if (target[p + 3] < 0.5f) { L[2] += 1.0f; continue; }
        float wgt = weight[idx];
        float tr = target[p], tg = target[p + 1], tb = target[p + 2], ta = target[p + 3];
        float sr = canvas[p], sg = canvas[p + 1], sb = canvas[p + 2], sa = canvas[p + 3];
        L[0] += wgt; L[1] += 1.0f;
        L[3] += wgt * tr; L[4] += wgt * tg; L[5] += wgt * tb; L[6] += wgt * ta;
        L[7] += wgt * sr; L[8] += wgt * sg; L[9] += wgt * sb; L[10] += wgt * sa;
        L[11] += wgt * sr * sr; L[12] += wgt * sg * sg; L[13] += wgt * sb * sb; L[14] += wgt * sa * sa;
        L[15] += wgt * tr * sr; L[16] += wgt * tg * sg; L[17] += wgt * tb * sb; L[18] += wgt * ta * sa;
    }
    for (int k = 0; k < NACC; k++) {
        float v = L[k];
        for (int off = 16; off > 0; off >>= 1) v += __shfl_down_sync(0xffffffffu, v, off);
        L[k] = v;
    }
    if (lane != 0) return;

    double W_ = L[0], nn = L[1], nt = L[2];
    if (nn < 1 || W_ <= 0) { out[warpId * 5] = REJECTED; return; }
    if (nt > 0 && nt > 1.5 * nn) { out[warpId * 5] = REJECTED; return; }
    double sTR = L[3], sTG = L[4], sTB = L[5], sTA = L[6];
    double sCR = L[7], sCG = L[8], sCB = L[9], sCA = L[10];
    double sCR2 = L[11], sCG2 = L[12], sCB2 = L[13], sCA2 = L[14];
    double sTCR = L[15], sTCG = L[16], sTCB = L[17], sTCA = L[18];
    double invW = 1.0 / W_, invA = 1.0 - a;
    double oR = clamp01d((sTR * invW - (sCR * invW) * invA) / a);
    double oG = clamp01d((sTG * invW - (sCG * invW) * invA) / a);
    double oB = clamp01d((sTB * invW - (sCB * invW) * invA) / a);
    double a2 = a * a, twoA = 2 * a;
    double dR = a2 * (W_ * oR * oR - 2 * oR * sCR + sCR2) - twoA * (oR * sTR - oR * sCR - sTCR + sCR2);
    double dG = a2 * (W_ * oG * oG - 2 * oG * sCG + sCG2) - twoA * (oG * sTG - oG * sCG - sTCG + sCG2);
    double dB = a2 * (W_ * oB * oB - 2 * oB * sCB + sCB2) - twoA * (oB * sTB - oB * sCB - sTCB + sCB2);
    double dA = a2 * (W_ - 2 * sCA + sCA2) - twoA * (sTA - sCA - sTCA + sCA2);
    double totalDelta = dR + dG + dB + dA;
    if (nt > 0) {
        double spillFrac = nt / (nn + nt);
        totalDelta += a2 * nt * (1 + 2 * spillFrac) * (oR * oR + oG * oG + oB * oB + 1);
    }
    out[warpId * 5 + 0] = (float)(totalDelta * (double)(step * step));
    out[warpId * 5 + 1] = (float)oR;
    out[warpId * 5 + 2] = (float)oG;
    out[warpId * 5 + 3] = (float)oB;
    out[warpId * 5 + 4] = cc[10];
}

// evalKernelWarpFP16: a HALF-PRECISION variant of evalKernelWarp's accumulation, used ONLY for
// the coarse-to-fine FILTER pass (the FP32 re-eval picks + scores the winner, so this kernel
// never sets a shipped score — it only RANKS). The 16 channel accumulations pack into 8 __half2
// FMAs (~2x ALU throughput on Blackwell); since the eval is ALU-bound (occupancy tuning was a
// wash), cutting the per-pixel FMA work is the real lever. Per-lane sums are FP16; the warp
// reduction + final ΔSSE stay FP32/FP64 (precision where it's cheap, after the hot loop). NOT
// golden-diffed (filter-only, lossy by design) — validated END-TO-END (must not miss winners;
// raise -coarse-k if it does). Channels are packed [r,g]=lo/hi of *01, [b,a]=lo/hi of *23.
__global__ void evalKernelWarpFP16(const float* cands, int n, const float* target, const float* canvas,
                                   const float* weight, int W, int H, int sampleBudget, float* out) {
    int warpId = (blockIdx.x * blockDim.x + threadIdx.x) >> 5;
    int lane = threadIdx.x & 31;
    if (warpId >= n) return;
    const float* cc = cands + (long)warpId * 11;
    int kind = (int)(cc[0] + 0.5f);
    float P[6] = { cc[1], cc[2], cc[3], cc[4], cc[5], cc[6] };
    double a = (double)cc[10];
    if (a < 1e-3) a = 1e-3;
    if (a > 1) a = 1;
    int xMin, yMin, xMax, yMax;
    bbox(kind, P, W, H, &xMin, &yMin, &xMax, &yMax);
    int step = sampleStep(xMin, yMin, xMax, yMax, sampleBudget);
    int cols = (xMax - xMin) / step + 1;
    int rows = (yMax - yMin) / step + 1;
    long long total = (long long)cols * rows; // long is 32-bit on MSVC — widen so large bboxes don't overflow
    SC sc;
    prepShape(kind, P, &sc);
    __half2 sT01 = __float2half2_rn(0.f), sT23 = __float2half2_rn(0.f); // w*target
    __half2 sC01 = __float2half2_rn(0.f), sC23 = __float2half2_rn(0.f); // w*canvas
    __half2 sQ01 = __float2half2_rn(0.f), sQ23 = __float2half2_rn(0.f); // w*canvas^2
    __half2 sX01 = __float2half2_rn(0.f), sX23 = __float2half2_rn(0.f); // w*target*canvas
    float Lw = 0.f, Ln = 0.f, Lnt = 0.f;
    for (long long tt = lane; tt < total; tt += 32) {
        int x = xMin + (int)(tt % cols) * step;
        int y = yMin + (int)(tt / cols) * step;
        if (!insideC(kind, &sc, P, x, y)) continue;
        int idx = y * W + x, p = idx * 4;
        if (target[p + 3] < 0.5f) { Lnt += 1.0f; continue; }
        float wgt = weight[idx];
        __half2 wh = __float2half2_rn(wgt);
        __half2 t01 = __floats2half2_rn(target[p], target[p + 1]);
        __half2 t23 = __floats2half2_rn(target[p + 2], target[p + 3]);
        __half2 s01 = __floats2half2_rn(canvas[p], canvas[p + 1]);
        __half2 s23 = __floats2half2_rn(canvas[p + 2], canvas[p + 3]);
        Lw += wgt; Ln += 1.0f;
        sT01 = __hfma2(wh, t01, sT01); sT23 = __hfma2(wh, t23, sT23);
        __half2 ws01 = __hmul2(wh, s01), ws23 = __hmul2(wh, s23);
        sC01 = __hadd2(sC01, ws01); sC23 = __hadd2(sC23, ws23);
        sQ01 = __hfma2(ws01, s01, sQ01); sQ23 = __hfma2(ws23, s23, sQ23);
        sX01 = __hfma2(ws01, t01, sX01); sX23 = __hfma2(ws23, t23, sX23);
    }
    float L[NACC];
    L[0] = Lw; L[1] = Ln; L[2] = Lnt;
    L[3] = __low2float(sT01); L[4] = __high2float(sT01); L[5] = __low2float(sT23); L[6] = __high2float(sT23);
    L[7] = __low2float(sC01); L[8] = __high2float(sC01); L[9] = __low2float(sC23); L[10] = __high2float(sC23);
    L[11] = __low2float(sQ01); L[12] = __high2float(sQ01); L[13] = __low2float(sQ23); L[14] = __high2float(sQ23);
    L[15] = __low2float(sX01); L[16] = __high2float(sX01); L[17] = __low2float(sX23); L[18] = __high2float(sX23);
    for (int k = 0; k < NACC; k++) {
        float v = L[k];
        for (int off = 16; off > 0; off >>= 1) v += __shfl_down_sync(0xffffffffu, v, off);
        L[k] = v;
    }
    if (lane != 0) return;
    double W_ = L[0], nn = L[1], nt = L[2];
    if (nn < 1 || W_ <= 0) { out[warpId * 5] = REJECTED; return; }
    if (nt > 0 && nt > 1.5 * nn) { out[warpId * 5] = REJECTED; return; }
    double sTR = L[3], sTG = L[4], sTB = L[5], sTA = L[6];
    double sCR = L[7], sCG = L[8], sCB = L[9], sCA = L[10];
    double sCR2 = L[11], sCG2 = L[12], sCB2 = L[13], sCA2 = L[14];
    double sTCR = L[15], sTCG = L[16], sTCB = L[17], sTCA = L[18];
    double invW = 1.0 / W_, invA = 1.0 - a;
    double oR = clamp01d((sTR * invW - (sCR * invW) * invA) / a);
    double oG = clamp01d((sTG * invW - (sCG * invW) * invA) / a);
    double oB = clamp01d((sTB * invW - (sCB * invW) * invA) / a);
    double a2 = a * a, twoA = 2 * a;
    double dR = a2 * (W_ * oR * oR - 2 * oR * sCR + sCR2) - twoA * (oR * sTR - oR * sCR - sTCR + sCR2);
    double dG = a2 * (W_ * oG * oG - 2 * oG * sCG + sCG2) - twoA * (oG * sTG - oG * sCG - sTCG + sCG2);
    double dB = a2 * (W_ * oB * oB - 2 * oB * sCB + sCB2) - twoA * (oB * sTB - oB * sCB - sTCB + sCB2);
    double dA = a2 * (W_ - 2 * sCA + sCA2) - twoA * (sTA - sCA - sTCA + sCA2);
    double totalDelta = dR + dG + dB + dA;
    if (nt > 0) {
        double spillFrac = nt / (nn + nt);
        totalDelta += a2 * nt * (1 + 2 * spillFrac) * (oR * oR + oG * oG + oB * oB + 1);
    }
    out[warpId * 5 + 0] = (float)(totalDelta * (double)(step * step));
    out[warpId * 5 + 1] = (float)oR;
    out[warpId * 5 + 2] = (float)oG;
    out[warpId * 5 + 3] = (float)oB;
    out[warpId * 5 + 4] = cc[10];
}

// ---- apply kernel: 2D grid over the candidate bbox ----
__global__ void applyKernel(const float* cc, float* canvas, int W, int H,
                            int xMin, int yMin, int xMax, int yMax) {
    int x = xMin + blockIdx.x * blockDim.x + threadIdx.x;
    int y = yMin + blockIdx.y * blockDim.y + threadIdx.y;
    if (x > xMax || y > yMax) return;
    int kind = (int)(cc[0] + 0.5f);
    float P[6] = { cc[1], cc[2], cc[3], cc[4], cc[5], cc[6] };
    float a = cc[10];
    if (a < 0) a = 0;
    if (a > 1) a = 1;
    if (isGradKind(kind)) { // per-pixel falloff: aEff = A * cov(x)
        float cov = gradCovP(kind, P, x, y);
        if (cov <= 0.0f) return;
        a *= cov;
    } else if (!inside(kind, P, x, y)) {
        return;
    }
    float invA = 1.0f - a;
    int p = (y * W + x) * 4;
    canvas[p + 0] = canvas[p + 0] * invA + cc[7] * a;
    canvas[p + 1] = canvas[p + 1] * invA + cc[8] * a;
    canvas[p + 2] = canvas[p + 2] * invA + cc[9] * a;
    canvas[p + 3] = canvas[p + 3] * invA + a;
}

// ---- error-grid kernel: one block per cell ----
__global__ void gridKernel(const float* target, const float* canvas, const float* weight,
                           int W, int H, int GW, int GH, float* grid) {
    int cell = blockIdx.x;
    if (cell >= GW * GH) return;
    int gx = cell % GW, gy = cell / GW;
    int x0 = gx * W / GW, x1 = (gx + 1) * W / GW;
    int y0 = gy * H / GH, y1 = (gy + 1) * H / GH;
    int cols = x1 - x0, rows = y1 - y0;
    long long total = (long long)cols * rows; // long is 32-bit on MSVC — widen so large bboxes don't overflow
    double s = 0;
    for (long long tt = threadIdx.x; tt < total; tt += blockDim.x) {
        int x = x0 + (int)(tt % cols);
        int y = y0 + (int)(tt / cols);
        int idx = y * W + x, p = idx * 4;
        double wgt = (double)weight[idx];
        for (int k = 0; k < 4; k++) {
            double d = (double)target[p + k] - (double)canvas[p + k];
            s += wgt * d * d;
        }
    }
    __shared__ double sh[BLOCK];
    sh[threadIdx.x] = s;
    __syncthreads();
    for (int st = blockDim.x / 2; st > 0; st >>= 1) {
        if (threadIdx.x < st) sh[threadIdx.x] += sh[threadIdx.x + st];
        __syncthreads();
    }
    if (threadIdx.x == 0) grid[cell] = (float)sh[0];
}

// ---- search kernels (build "B1") ----

// genKernel ports candidates.go genCandidate + ErrorSampler.Sample to the device:
// one candidate per (grid-strided) thread. Center is importance-sampled from the
// error-grid CDF (binary search), kind from the weighted kind CDF, geometry from
// the per-kind anneal (maxR precomputed on host from progress), theta seeded along
// the local edge orientation (+jitter) when d_orient is present, alpha opaque or
// ~U(alphaMin,1). Color is left zero — evalKernel solves the optimal color.
// clampExtentsD shrinks an ellipse/rect's half-extents (a,b) uniformly so its rotated bounding box
// stays within [-pad, W+pad] x [-pad, H+pad]. Mirrors engine.clampExtents (host). Keeps shapes from
// ballooning outside the image rectangle (drawn in full in-game, clipped in the W×H preview).
__device__ void clampExtentsD(float cx, float cy, float* a, float* b, float thetaDeg, float W, float H, float pad) {
    float th = thetaDeg * 0.017453292519943295f; // pi/180
    float cs = cosf(th), sn = sinf(th);
    float hx = hypotf((*a) * cs, (*b) * sn);
    float hy = hypotf((*a) * sn, (*b) * cs);
    float allowX = fmaxf(1.f, fminf(cx, W - cx) + pad);
    float allowY = fmaxf(1.f, fminf(cy, H - cy) + pad);
    float scale = 1.f;
    if (hx > allowX) scale = fminf(scale, allowX / hx);
    if (hy > allowY) scale = fminf(scale, allowY / hy);
    if (scale < 1.f) { *a *= scale; *b *= scale; }
}

__global__ void genKernel(float* cands, int n, unsigned long long seed,
                          const float* kinds, const float* kindCDF, int nKinds,
                          float maxR, int allowAlpha, float alphaMin, float aspectMax,
                          const float* gridCDF, int gw, int gh, int W, int H,
                          const float* orient, const float* boundDist, float boundPad, float boundMix,
                          float canvasPad) {
    int stride = gridDim.x * blockDim.x;
    for (int i = blockIdx.x * blockDim.x + threadIdx.x; i < n; i += stride) {
        unsigned int ctr = 0;
        float* c = cands + (long)i * 11;
        // 1. importance-sampled center via the error-grid CDF
        float total = gridCDF[gw * gh - 1];
        float cx, cy;
        if (total <= 0.f) {
            cx = uf(seed, i, ctr) * W;
            cy = uf(seed, i, ctr) * H;
        } else {
            float u = uf(seed, i, ctr) * total;
            int lo = 0, hi = gw * gh - 1;
            while (lo < hi) { int mid = (lo + hi) >> 1; if (gridCDF[mid] < u) lo = mid + 1; else hi = mid; }
            int gx = lo % gw, gy = lo / gw;
            int x0 = gx * W / gw, x1 = (gx + 1) * W / gw;
            int y0 = gy * H / gh, y1 = (gy + 1) * H / gh;
            if (x1 <= x0) x1 = x0 + 1;
            if (y1 <= y0) y1 = y0 + 1;
            cx = x0 + uf(seed, i, ctr) * (x1 - x0);
            cy = y0 + uf(seed, i, ctr) * (y1 - y0);
        }
        cx = clampf2(cx, 0.f, W - 1.f);
        cy = clampf2(cy, 0.f, H - 1.f);
        // 1b. boundary-aware radius: cap this candidate's max size by its centre's distance
        // to the nearest target boundary, ramped by boundMix (host-computed per shape), so it
        // can't balloon across an edge. emaxR mirrors engine.boundaryRadiusCap exactly.
        float emaxR = maxR;
        if (boundDist && boundMix > 0.f) {
            int bidx = (int)cy * W + (int)cx;
            if (bidx >= 0 && bidx < W * H) {
                float lim = boundDist[bidx] + boundPad;
                if (lim < emaxR) emaxR = emaxR + (lim - emaxR) * boundMix;
            }
        }
        // 2. orientation-seeded angle (jitter ±20°), else uniform
        float theta = uf(seed, i, ctr) * 360.f;
        if (orient) {
            int idx = (int)cy * W + (int)cx;
            if (idx >= 0 && idx < W * H) theta = orient[idx] + (uf(seed, i, ctr) * 40.f - 20.f);
        }
        // 3. alpha
        float alpha = 1.f;
        if (allowAlpha) alpha = alphaMin + (1.f - alphaMin) * uf(seed, i, ctr);
        // 4. kind via weighted CDF
        int kind = (int)(kinds[nKinds - 1] + 0.5f);
        {
            float ku = uf(seed, i, ctr) * kindCDF[nKinds - 1];
            for (int k = 0; k < nKinds; k++) {
                if (ku < kindCDF[k]) { kind = (int)(kinds[k] + 0.5f); break; }
            }
        }
        c[0] = (float)kind;
        c[7] = 0.f; c[8] = 0.f; c[9] = 0.f; c[10] = alpha;
        // 5. per-kind geometry (mirrors randomShapeOfKind)
        if (kind == 1) { // rectangle: [cx,cy,halfW,halfH,theta,_]
            float hw = 1.f + uf(seed, i, ctr) * (emaxR - 1.f);
            float u2 = uf(seed, i, ctr);
            float hh = (aspectMax > 1.f) ? fmaxf(0.5f, hw / (1.f + u2 * (aspectMax - 1.f)))
                                         : 1.f + u2 * (emaxR - 1.f);
            c[1] = cx; c[2] = cy; c[3] = hw; c[4] = hh; c[5] = theta; c[6] = 0.f;
            if (canvasPad > 0.f) clampExtentsD(cx, cy, &c[3], &c[4], theta, (float)W, (float)H, canvasPad * fminf((float)W, (float)H));
        } else if (kind == 2) { // triangle: three vertices in a ±rr box about the center
            float rr = 4.f + uf(seed, i, ctr) * (emaxR - 4.f);
            c[1] = clampf2(cx + (-rr + 2.f * rr * uf(seed, i, ctr)), 0.f, W - 1.f);
            c[2] = clampf2(cy + (-rr + 2.f * rr * uf(seed, i, ctr)), 0.f, H - 1.f);
            c[3] = clampf2(cx + (-rr + 2.f * rr * uf(seed, i, ctr)), 0.f, W - 1.f);
            c[4] = clampf2(cy + (-rr + 2.f * rr * uf(seed, i, ctr)), 0.f, H - 1.f);
            c[5] = clampf2(cx + (-rr + 2.f * rr * uf(seed, i, ctr)), 0.f, W - 1.f);
            c[6] = clampf2(cy + (-rr + 2.f * rr * uf(seed, i, ctr)), 0.f, H - 1.f);
        } else { // ellipse: [cx,cy,rx,ry,theta,_]
            float rx = 2.f + uf(seed, i, ctr) * (emaxR - 2.f);
            float u2 = uf(seed, i, ctr);
            float ry = (aspectMax > 1.f) ? fmaxf(1.f, rx / (1.f + u2 * (aspectMax - 1.f)))
                                         : 2.f + u2 * (emaxR - 2.f);
            c[1] = cx; c[2] = cy; c[3] = rx; c[4] = ry; c[5] = theta; c[6] = 0.f;
            if (canvasPad > 0.f) clampExtentsD(cx, cy, &c[3], &c[4], theta, (float)W, (float)H, canvasPad * fminf((float)W, (float)H));
        }
    }
}

// ---- moment-seeding (on-device) ----
// momentFitDevice fits the covariance ellipse of the residual error in a grid window around
// pixel (px,py), mirroring the host momentEllipse/momentSeedFromGrid. The per-cell error is
// recovered from the cumulative gridCDF (cell i = cdf[i] - cdf[i-1]). Output in PIXEL coords.
__device__ void momentFitDevice(const float* gridCDF, int gw, int gh, int W, int H,
                                float px, float py, float radiusPx,
                                float* ocx, float* ocy, float* orx, float* ory, float* oth, int* valid) {
    float sx = (float)W / gw, sy = (float)H / gh;
    int gcx = (int)(px / sx), gcy = (int)(py / sy);
    int rcx = (int)(radiusPx / sx) + 1, rcy = (int)(radiusPx / sy) + 1;
    int gx0 = gcx - rcx, gy0 = gcy - rcy, gx1 = gcx + rcx, gy1 = gcy + rcy;
    if (gx0 < 0) gx0 = 0;
    if (gy0 < 0) gy0 = 0;
    if (gx1 > gw - 1) gx1 = gw - 1;
    if (gy1 > gh - 1) gy1 = gh - 1;
    double m00 = 0, m10 = 0, m01 = 0;
    for (int gy = gy0; gy <= gy1; gy++)
        for (int gx = gx0; gx <= gx1; gx++) {
            int idx = gy * gw + gx;
            float e = gridCDF[idx] - (idx > 0 ? gridCDF[idx - 1] : 0.f);
            if (e <= 0.f) continue;
            m00 += e; m10 += (double)gx * e; m01 += (double)gy * e;
        }
    if (m00 <= 1e-9) { *valid = 0; return; }
    double mcx = m10 / m00, mcy = m01 / m00;
    double u20 = 0, u02 = 0, u11 = 0;
    for (int gy = gy0; gy <= gy1; gy++)
        for (int gx = gx0; gx <= gx1; gx++) {
            int idx = gy * gw + gx;
            float e = gridCDF[idx] - (idx > 0 ? gridCDF[idx - 1] : 0.f);
            if (e <= 0.f) continue;
            double dx = gx - mcx, dy = gy - mcy;
            u20 += e * dx * dx; u02 += e * dy * dy; u11 += e * dx * dy;
        }
    u20 /= m00; u02 /= m00; u11 /= m00;
    double half = (u20 - u02) / 2;
    double d = sqrt(fmax(0.0, half * half + u11 * u11));
    double lmax = (u20 + u02) / 2 + d, lmin = (u20 + u02) / 2 - d;
    if (lmin < 0) lmin = 0;
    double major = 2.0 * sqrt(lmax), minor = 2.0 * sqrt(lmin);
    float s = (sx + sy) / 2; // isotropic axis scale (preserves the fitted angle)
    *ocx = (float)((mcx + 0.5) * sx);
    *ocy = (float)((mcy + 0.5) * sy);
    *orx = (float)(fmax(major, 0.5) * s);
    *ory = (float)(fmax(minor, 0.5) * s);
    *oth = (float)(0.5 * atan2(2 * u11, u20 - u02) * 57.295779513082323);
    *valid = 1;
}

// momentSeedKernel fits K covariance-ellipse seeds: each thread samples a centre from the
// error-grid CDF (same importance bias as the random search), caps its radius at the boundary,
// fits the residual blob, and writes the seed (cx,cy,rx,ry,theta,capR). An invalid fit (window
// with ~no error) falls back to a round seed at the sampled centre so the gen step always has one.
__global__ void momentSeedKernel(float* seeds, int K, unsigned long long seed,
                                 const float* gridCDF, int gw, int gh, int W, int H,
                                 float maxR, const float* boundDist, float boundPad, float boundMix) {
    int k = blockIdx.x * blockDim.x + threadIdx.x;
    if (k >= K) return;
    unsigned int ctr = 0;
    float total = gridCDF[gw * gh - 1];
    float cx, cy;
    if (total <= 0.f) { cx = uf(seed, k, ctr) * W; cy = uf(seed, k, ctr) * H; }
    else {
        float u = uf(seed, k, ctr) * total;
        int lo = 0, hi = gw * gh - 1;
        while (lo < hi) { int mid = (lo + hi) >> 1; if (gridCDF[mid] < u) lo = mid + 1; else hi = mid; }
        int gx = lo % gw, gy = lo / gw;
        int x0 = gx * W / gw, x1 = (gx + 1) * W / gw, y0 = gy * H / gh, y1 = (gy + 1) * H / gh;
        if (x1 <= x0) x1 = x0 + 1;
        if (y1 <= y0) y1 = y0 + 1;
        cx = x0 + uf(seed, k, ctr) * (x1 - x0);
        cy = y0 + uf(seed, k, ctr) * (y1 - y0);
    }
    cx = clampf2(cx, 0.f, W - 1.f);
    cy = clampf2(cy, 0.f, H - 1.f);
    float capR = maxR;
    if (boundDist && boundMix > 0.f) {
        int bidx = (int)cy * W + (int)cx;
        if (bidx >= 0 && bidx < W * H) {
            float lim = boundDist[bidx] + boundPad;
            if (lim < capR) capR = capR + (lim - capR) * boundMix;
        }
    }
    float scx, scy, srx, sry, sth; int valid;
    momentFitDevice(gridCDF, gw, gh, W, H, cx, cy, capR, &scx, &scy, &srx, &sry, &sth, &valid);
    float* s = seeds + (long)k * 6;
    if (!valid) { // round fallback at the sampled centre
        scx = cx; scy = cy; srx = fmaxf(4.f, capR * 0.3f); sry = srx; sth = uf(seed, k, ctr) * 360.f;
    }
    s[0] = scx; s[1] = scy; s[2] = srx; s[3] = sry; s[4] = sth; s[5] = capR;
}

// genMomentKernel builds the candidate pool from the K seeds (mirrors the host momentPool):
// candidate i belongs to seed i/perSeed; the first of each group is the EXACT moment ellipse,
// the rest are kind-weighted candidates LOCALISED (centre jitter, seed orientation/size/aspect)
// around the seed — the same localisation that lets a small pool match the 50k random search.
__global__ void genMomentKernel(float* cands, int n, int perSeed, int K, unsigned long long seed,
                                const float* seeds, const float* kinds, const float* kindCDF, int nKinds,
                                int allowAlpha, float alphaMin, int W, int H, float canvasPad) {
    float padPx = canvasPad * fminf((float)W, (float)H);
    int stride = gridDim.x * blockDim.x;
    for (int i = blockIdx.x * blockDim.x + threadIdx.x; i < n; i += stride) {
        unsigned int ctr = 0;
        float* c = cands + (long)i * 11;
        int sIdx = i / perSeed;
        if (sIdx >= K) sIdx = K - 1;
        const float* s = seeds + (long)sIdx * 6;
        float scx = s[0], scy = s[1], srx = s[2], sry = s[3], sth = s[4], capR = s[5];
        c[7] = 0.f; c[8] = 0.f; c[9] = 0.f;
        if (i % perSeed == 0) { // exact moment ellipse (opaque)
            c[0] = 0.f; c[10] = 1.f;
            c[1] = scx; c[2] = scy; c[3] = clampf2(srx, 2.f, capR); c[4] = clampf2(sry, 2.f, capR); c[5] = sth; c[6] = 0.f;
            if (canvasPad > 0.f) clampExtentsD(scx, scy, &c[3], &c[4], sth, (float)W, (float)H, padPx);
            continue;
        }
        float major = fmaxf(srx, sry), minor = fminf(srx, sry);
        float aspect = 1.f;
        if (minor > 0.5f) aspect = clampf2(major / minor, 1.f, 8.f);
        float localR = fminf(capR, fmaxf(4.f, major * 1.4f));
        float jit = localR * 0.5f;
        float jx = clampf2(scx + (uf(seed, i, ctr) * 2.f - 1.f) * jit, 0.f, W - 1.f);
        float jy = clampf2(scy + (uf(seed, i, ctr) * 2.f - 1.f) * jit, 0.f, H - 1.f);
        float theta = sth + (uf(seed, i, ctr) * 30.f - 15.f);
        float alpha = 1.f;
        if (allowAlpha) alpha = alphaMin + (1.f - alphaMin) * uf(seed, i, ctr);
        c[10] = alpha;
        int kind = (int)(kinds[nKinds - 1] + 0.5f);
        { float ku = uf(seed, i, ctr) * kindCDF[nKinds - 1];
          for (int kk = 0; kk < nKinds; kk++) if (ku < kindCDF[kk]) { kind = (int)(kinds[kk] + 0.5f); break; } }
        c[0] = (float)kind;
        if (kind == 1) { // rectangle
            float hw = 1.f + uf(seed, i, ctr) * (localR - 1.f);
            float u2 = uf(seed, i, ctr);
            float hh = (aspect > 1.f) ? fmaxf(0.5f, hw / (1.f + u2 * (aspect - 1.f))) : 1.f + u2 * (localR - 1.f);
            c[1] = jx; c[2] = jy; c[3] = hw; c[4] = hh; c[5] = theta; c[6] = 0.f;
            if (canvasPad > 0.f) clampExtentsD(jx, jy, &c[3], &c[4], theta, (float)W, (float)H, padPx);
        } else if (kind == 2) { // triangle
            float rr = 4.f + uf(seed, i, ctr) * (localR - 4.f);
            c[1] = clampf2(jx + (-rr + 2.f * rr * uf(seed, i, ctr)), 0.f, W - 1.f);
            c[2] = clampf2(jy + (-rr + 2.f * rr * uf(seed, i, ctr)), 0.f, H - 1.f);
            c[3] = clampf2(jx + (-rr + 2.f * rr * uf(seed, i, ctr)), 0.f, W - 1.f);
            c[4] = clampf2(jy + (-rr + 2.f * rr * uf(seed, i, ctr)), 0.f, H - 1.f);
            c[5] = clampf2(jx + (-rr + 2.f * rr * uf(seed, i, ctr)), 0.f, W - 1.f);
            c[6] = clampf2(jy + (-rr + 2.f * rr * uf(seed, i, ctr)), 0.f, H - 1.f);
        } else { // ellipse
            float rx = 2.f + uf(seed, i, ctr) * (localR - 2.f);
            float u2 = uf(seed, i, ctr);
            float ry = (aspect > 1.f) ? fmaxf(1.f, rx / (1.f + u2 * (aspect - 1.f))) : 2.f + u2 * (localR - 2.f);
            c[1] = jx; c[2] = jy; c[3] = rx; c[4] = ry; c[5] = theta; c[6] = 0.f;
            if (canvasPad > 0.f) clampExtentsD(jx, jy, &c[3], &c[4], theta, (float)W, (float)H, padPx);
        }
    }
}

// prepAdj writes the SELECTION score adj[i] = rawScore + compactPenalty (the penalty
// biases only WHICH candidate wins; the raw score is what gatherBest returns for the
// accept threshold — mirrors engine.pickBest). Rejected candidates keep FLT_MAX.
__global__ void prepAdj(const float* sout, const float* scand, int n,
                        int compact, int shapeCount, int W, int H, float* adj) {
    int stride = gridDim.x * blockDim.x;
    for (int i = blockIdx.x * blockDim.x + threadIdx.x; i < n; i += stride) {
        float sc = sout[(long)i * 5];
        if (compact && sc < REJECTED) {
            const float* cc = scand + (long)i * 11;
            int kind = (int)(cc[0] + 0.5f);
            float P[6] = { cc[1], cc[2], cc[3], cc[4], cc[5], cc[6] };
            int xmn, ymn, xmx, ymx;
            bbox(kind, P, W, H, &xmn, &ymn, &xmx, &ymx);
            int dw = xmx - xmn, dh = ymx - ymn;
            int span = dw > dh ? dw : dh;
            float hs = span * 0.5f;
            if (hs >= 96.f) sc += (shapeCount < 8) ? hs * hs * 0.1f : hs * 0.05f;
        }
        adj[i] = sc;
    }
}

// argmin: a custom 2-pass block reduction over adj[] (avoids a CUB dependency in the
// static-cudart DLL). Ties resolve to whichever index the reduction keeps — harmless
// (equal scores = equally good); the search is validated by SSE, not golden-diff.
__global__ void argminPass1(const float* adj, int n, float* outVal, int* outIdx) {
    __shared__ float sv[256];
    __shared__ int si[256];
    float bv = FLT_MAX; int bi = -1;
    for (int i = blockIdx.x * blockDim.x + threadIdx.x; i < n; i += gridDim.x * blockDim.x) {
        float v = adj[i];
        if (v < bv) { bv = v; bi = i; }
    }
    int t = threadIdx.x;
    sv[t] = bv; si[t] = bi;
    __syncthreads();
    for (int s = blockDim.x / 2; s > 0; s >>= 1) {
        if (t < s && sv[t + s] < sv[t]) { sv[t] = sv[t + s]; si[t] = si[t + s]; }
        __syncthreads();
    }
    if (t == 0) { outVal[blockIdx.x] = sv[0]; outIdx[blockIdx.x] = si[0]; }
}
__global__ void argminPass2(const float* inVal, const int* inIdx, int m, int* bestIdx) {
    __shared__ float sv[256];
    __shared__ int si[256];
    int t = threadIdx.x;
    float bv = FLT_MAX; int bi = -1;
    for (int i = t; i < m; i += blockDim.x) {
        if (inVal[i] < bv) { bv = inVal[i]; bi = inIdx[i]; }
    }
    sv[t] = bv; si[t] = bi;
    __syncthreads();
    for (int s = blockDim.x / 2; s > 0; s >>= 1) {
        if (t < s && sv[t + s] < sv[t]) { sv[t] = sv[t + s]; si[t] = si[t + s]; }
        __syncthreads();
    }
    if (t == 0) bestIdx[0] = si[0];
}
// gatherBest copies the winner's geometry (from d_scand) + its eval'd optimal color
// and RAW score (from d_sout) into out_best[12]: [score,kind,p0..p5,oR,oG,oB,alpha].
__global__ void gatherBest(const float* scand, const float* sout, const int* bestIdx, float* out_best) {
    int b = bestIdx[0];
    if (b < 0) { out_best[0] = REJECTED; for (int k = 1; k < 12; k++) out_best[k] = 0.f; return; }
    const float* cc = scand + (long)b * 11;
    const float* o = sout + (long)b * 5;
    out_best[0] = o[0];                                            // RAW score (not adj)
    out_best[1] = cc[0];                                           // kind
    out_best[2] = cc[1]; out_best[3] = cc[2]; out_best[4] = cc[3]; // P[0..2]
    out_best[5] = cc[4]; out_best[6] = cc[5]; out_best[7] = cc[6]; // P[3..5]
    out_best[8] = o[1]; out_best[9] = o[2]; out_best[10] = o[3];   // optimal oR,oG,oB
    out_best[11] = o[4];                                           // pass-through alpha
}

// coarsePartitionMin (coarse-to-fine): split n candidates into `parts` contiguous chunks,
// one block per chunk, and emit each chunk's min-adj index. Chunks are tiny (n/parts ~24),
// so a chunk's min is almost always the chunk's true full-budget best — collectively the
// `parts` survivors contain the global winner with very high probability.
__global__ void coarsePartitionMin(const float* adj, int n, int parts, int* selIdx) {
    int b = blockIdx.x;
    if (b >= parts) return;
    long lo = (long)b * n / parts;
    long hi = (long)(b + 1) * n / parts;
    __shared__ float sv[128];
    __shared__ int si[128];
    float bv = FLT_MAX; int bi = -1;
    for (long i = lo + threadIdx.x; i < hi; i += blockDim.x) {
        float v = adj[i];
        if (v < bv) { bv = v; bi = (int)i; }
    }
    int t = threadIdx.x;
    sv[t] = bv; si[t] = bi;
    __syncthreads();
    for (int s = blockDim.x / 2; s > 0; s >>= 1) {
        if (t < s && sv[t + s] < sv[t]) { sv[t] = sv[t + s]; si[t] = si[t + s]; }
        __syncthreads();
    }
    if (t == 0) selIdx[b] = (bi < 0 && lo < hi) ? (int)lo : si[0];
}

// gatherSubset copies the coarse survivors' geometry (indices in sel[]) into a compact
// buffer for the full-budget re-eval.
__global__ void gatherSubset(const float* scand, const int* sel, int k, float* scand2) {
    int j = blockIdx.x * blockDim.x + threadIdx.x;
    if (j >= k) return;
    int idx = sel[j];
    if (idx < 0) idx = 0;
    const float* src = scand + (long)idx * 11;
    float* dst = scand2 + (long)j * 11;
    for (int t = 0; t < 11; t++) dst[t] = src[t];
}

// ---- joint differentiable polish (CUDA port of internal/engine/polish.go) ----
// Refines ALL shapes together by gradient descent on a SOFT-rasterized render: soft
// coverage cov=sigmoid(-sdf/tau) makes the hard inside-test differentiable. Forward is
// N sequential per-shape composites (z-order via launch order on one stream); backward
// is N reverse per-shape passes accumulating dLoss/dparam and propagating dC*=(1-a).
// All math is DOUBLE to match the float64 CPU reference within golden-diff tolerance —
// EXCEPT the forward composite, which mirrors polish.go's float32 render/coverage path.
// Adam, tau-anneal, best-hard tracking and snap-to-hard stay on the host (cheap).

__device__ inline double sigmoidCovD(double sdf, double tau) {
    double z = sdf / tau;
    if (z > 40.0) return 0.0;
    if (z < -40.0) return 1.0;
    return 1.0 / (1.0 + exp(z));
}
// ellipseSDFGradD: signed distance (neg inside) to P=[cx,cy,rx,ry,thetaDeg] + grad wrt
// P[0..4] (g may be null). Mirrors polish.go ellipseSDFGrad.
__device__ double ellipseSDFGradD(const double* P, double px, double py, double* g) {
    double cx = P[0], cy = P[1];
    double rx = fmax(1.0, P[2]), ry = fmax(1.0, P[3]);
    double th = P[4] * DEG2RAD, cs = cos(th), sn = sin(th);
    double dx = px - cx, dy = py - cy;
    double xr = dx * cs + dy * sn, yr = -dx * sn + dy * cs;
    double u = xr / rx, v = yr / ry;
    double k = sqrt(u * u + v * v);
    if (k < 1e-9) k = 1e-9;
    double m = (ry < rx) ? ry : rx;
    double sdf = (k - 1.0) * m;
    if (g) {
        double dkdxr = xr / (rx * rx * k), dkdyr = yr / (ry * ry * k);
        double dkdcx = dkdxr * (-cs) + dkdyr * (sn);
        double dkdcy = dkdxr * (-sn) + dkdyr * (-cs);
        double dkdthRad = dkdxr * yr + dkdyr * (-xr);
        double dkdrx = -(xr * xr) / (rx * rx * rx * k);
        double dkdry = -(yr * yr) / (ry * ry * ry * k);
        double dmdrx = 0.0, dmdry = 0.0;
        if (rx <= ry) dmdrx = 1.0; else dmdry = 1.0;
        g[0] = m * dkdcx; g[1] = m * dkdcy;
        g[2] = m * dkdrx + (k - 1.0) * dmdrx;
        g[3] = m * dkdry + (k - 1.0) * dmdry;
        g[4] = m * dkdthRad * DEG2RAD;
    }
    return sdf;
}
// rectSDFGradD: exact signed distance to rotated box P=[cx,cy,hw,hh,thetaDeg] + grad.
// Mirrors polish.go rectSDFGrad.
__device__ double rectSDFGradD(const double* P, double px, double py, double* g) {
    double cx = P[0], cy = P[1];
    double hw = fmax(0.5, P[2]), hh = fmax(0.5, P[3]);
    double th = P[4] * DEG2RAD, cs = cos(th), sn = sin(th);
    double dx = px - cx, dy = py - cy;
    double xr = dx * cs + dy * sn, yr = -dx * sn + dy * cs;
    double sx = xr < 0 ? -1.0 : 1.0;
    double sy = yr < 0 ? -1.0 : 1.0;
    double qx = fabs(xr) - hw, qy = fabs(yr) - hh;
    double dqx[5] = { sx * (-cs), sx * (-sn), -1.0, 0.0, sx * yr };
    double dqy[5] = { sy * (sn), sy * (-cs), 0.0, -1.0, sy * (-xr) };
    double sdf;
    if (qx > 0.0 || qy > 0.0) {
        double mqx = fmax(qx, 0.0), mqy = fmax(qy, 0.0);
        sdf = sqrt(mqx * mqx + mqy * mqy);
        double inv = sdf > 1e-9 ? 1.0 / sdf : 0.0;
        if (g) for (int i = 0; i < 5; i++) {
            double t = 0.0;
            if (qx > 0.0) t += mqx * dqx[i];
            if (qy > 0.0) t += mqy * dqy[i];
            g[i] = t * inv;
        }
    } else {
        if (qx >= qy) { sdf = qx; if (g) for (int i = 0; i < 5; i++) g[i] = dqx[i]; }
        else { sdf = qy; if (g) for (int i = 0; i < 5; i++) g[i] = dqy[i]; }
    }
    if (g) g[4] *= DEG2RAD;
    return sdf;
}
// triangleSDFGradD: signed distance (neg inside) to triangle P=[x1,y1,x2,y2,x3,y3] + grad
// wrt all 6 vertex coords (g may be null). Mirrors polish.go triangleSDFGrad.
__device__ double triangleSDFGradD(const double* P, double px, double py, double* g) {
    if (g) { for (int i = 0; i < 6; i++) g[i] = 0.0; }
    double ax=P[0],ay=P[1], bx=P[2],by=P[3], cx=P[4],cy=P[5];
    double e0x=bx-ax,e0y=by-ay, e1x=cx-bx,e1y=cy-by, e2x=ax-cx,e2y=ay-cy;
    double v0x=px-ax,v0y=py-ay, v1x=px-bx,v1y=py-by, v2x=px-cx,v2y=py-cy;
    double d0=e0x*e0x+e0y*e0y; if(d0<1e-12)d0=1e-12;
    double d1=e1x*e1x+e1y*e1y; if(d1<1e-12)d1=1e-12;
    double d2=e2x*e2x+e2y*e2y; if(d2<1e-12)d2=1e-12;
    double t0=(v0x*e0x+v0y*e0y)/d0; t0=t0<0.0?0.0:(t0>1.0?1.0:t0);
    double t1=(v1x*e1x+v1y*e1y)/d1; t1=t1<0.0?0.0:(t1>1.0?1.0:t1);
    double t2=(v2x*e2x+v2y*e2y)/d2; t2=t2<0.0?0.0:(t2>1.0?1.0:t2);
    double pq0x=v0x-e0x*t0, pq0y=v0y-e0y*t0;
    double pq1x=v1x-e1x*t1, pq1y=v1y-e1y*t1;
    double pq2x=v2x-e2x*t2, pq2y=v2y-e2y*t2;
    double dd0=pq0x*pq0x+pq0y*pq0y, dd1=pq1x*pq1x+pq1y*pq1y, dd2=pq2x*pq2x+pq2y*pq2y;
    double s = (e0x*e2y-e0y*e2x < 0.0) ? -1.0 : 1.0;
    double w0=s*(v0x*e0y-v0y*e0x), w1=s*(v1x*e1y-v1y*e1x), w2=s*(v2x*e2y-v2y*e2x);
    double ddmin=dd0; int active=0;
    if(dd1<ddmin){ddmin=dd1;active=1;}
    if(dd2<ddmin){ddmin=dd2;active=2;}
    double wmin=w0; if(w1<wmin)wmin=w1; if(w2<wmin)wmin=w2;
    double dist=sqrt(ddmin);
    double sgn=(wmin<0.0)?-1.0:1.0;
    double sdf=-dist*sgn;
    if(!g || dist<1e-9) return sdf;
    double t,pqx,pqy; int sIdx,eIdx;
    if(active==0){t=t0;pqx=pq0x;pqy=pq0y;sIdx=0;eIdx=2;}
    else if(active==1){t=t1;pqx=pq1x;pqy=pq1y;sIdx=2;eIdx=4;}
    else{t=t2;pqx=pq2x;pqy=pq2y;sIdx=4;eIdx=0;}
    double nx=pqx/dist, ny=pqy/dist;
    g[sIdx+0]=sgn*(1.0-t)*nx; g[sIdx+1]=sgn*(1.0-t)*ny;
    g[eIdx+0]=sgn*t*nx;       g[eIdx+1]=sgn*t*ny;
    return sdf;
}
__device__ inline bool polishOptGeo(int kind) { return kind == 0 || kind == 1 || kind == 2; }
__device__ inline double polishSDFGrad(int kind, const double* P, double px, double py, double* g) {
    if (kind == 2) return triangleSDFGradD(P, px, py, g);
    double sdf = (kind == 1) ? rectSDFGradD(P, px, py, g) : ellipseSDFGradD(P, px, py, g);
    if (g) g[5] = 0.0; // ellipse/rect leave the 6th geo slot 0
    return sdf;
}

// ---- polish device state ----
static float*  d_pbase   = nullptr; // base canvas (bg fill / transparent), w*h*4
static float*  d_prender = nullptr; // soft render, w*h*4
static float*  d_pdC     = nullptr; // dLoss/dColor, w*h*4
static float*  d_pbelow  = nullptr; // per-shape "color below" snapshots (packed)
static double*    d_pP    = nullptr; // per-shape geometry P[0..5], N*6
static double*    d_pcol  = nullptr; // per-shape color R,G,B,A, N*4
static int*       d_pkind = nullptr; // per-shape kind, N
static int*       d_pbbx  = nullptr; // per-shape bbox xMin,yMin,xMax,yMax, N*4
static long long* d_pboff = nullptr; // per-shape below offset (prefix sum), N (64-bit: Win `long` is 32-bit)
static double* d_pgrad   = nullptr; // per-shape grad [gP0..5,gR,gG,gB,gA], N*10
static double* d_ploss   = nullptr; // scalar loss accumulator
// DETERMINISM buffers: the gradient/loss reductions used cross-block atomicAdd, whose
// non-deterministic float-sum ORDER jittered the Adam trajectory ~1.5% run-to-run (proven:
// greedy is bit-identical, polish varies) — which drowned sub-2% quality A/Bs. We instead
// stage each block's partial in its OWN slot (no atomic) and sum them in a FIXED order, so
// polish is now bit-reproducible for a given seed.
#define PMAXBLK 256                          // gridDim.x for polishBackwardReduce (grid-stride covers the rest)
static double* d_pgrad_partial = nullptr;    // per (shape,block) partial grad, N*PMAXBLK*10
static double* d_ploss_partial = nullptr;    // per-block partial loss, up to 1024 blocks
static float* d_pdcsnap = nullptr;           // tiled backward Pass A: dC-as-seen-by-shape, sized == d_pbelow
static int  g_pN = 0;
static long g_pbelowCap = 0;
static int  g_polishSTE = 0; // straight-through: hard forward coverage, soft surrogate gradient
static int  g_polishOKLab = 0; // perceptual loss: OKLab colour distance in loss/dcinit/hard (mirrors engine/oklab.go)

// False-edge additive term (mirrors engine/falseedge.go): λ·Σ relu(|∇L_recon|−|∇L_target|) over
// INTERIOR pixels (Sobel on Rec.601 luma, 1px border ring excluded so the adjoint scatter never
// leaves the canvas). Folded into fp_polish_loss / fp_polish_hard_loss / polishDCInit when λ>0.
static double g_polishFELambda = 0.0;
static float*  d_feTL = nullptr;       // target luma (computed once per lambda-set)
static float*  d_feRL = nullptr;       // recon luma scratch
static double* d_feDir = nullptr;      // 2*npix: active normalised gradient direction (cx,cy), 0 if inactive
static double* d_feAdj = nullptr;      // npix: dFE/dLuma adjoint
static double* d_fe_partial = nullptr; // per-block FE partials (deterministic fixed-order final sum)

// SSIM additive term (mirrors engine/ssimterm.go): λ·Σ(1−SSIM) over uniform 8×8 luma windows at
// VALID positions only (fully inside the canvas — no border clamping, so CPU==GPU exactly). The
// recon enters each window only via mean(x), mean(x²), mean(x·y): forward = horizontal+vertical
// 8-term box sums; backward = per-window moment-gradients box-correlated back + a per-pixel
// combine. All sums run in double with the SAME direct loop order as the CPU reference.
#define SSWIN 8
#define SSINVN (1.0 / (SSWIN * SSWIN))
#define SSC1 0.0001
#define SSC2 0.0009
static double g_polishSSIMLambda = 0.0;
static float*  d_ssTL = nullptr;       // target luma (computed once per lambda-set)
static float*  d_ssRL = nullptr;       // recon luma scratch
static double* d_ssMy = nullptr;       // target window means (mw*mh)
static double* d_ssMyy = nullptr;      // target window means of y² (mw*mh)
static double* d_ssH = nullptr;        // 3 planes mw*h: horizontal sums of x, x², x·y
static double* d_ssG = nullptr;        // 3 planes mw*mh: dL/dmx, dL/dmxx, dL/dmxy per window
static double* d_ssHG = nullptr;       // 3 planes w*mh: horizontally box-correlated G
static double* d_ssAdj = nullptr;      // npix: dSSIMterm/dLuma adjoint
static double* d_ss_partial = nullptr; // per-block partials (deterministic fixed-order final sum)

// d_linearToOKLab — OKLab from the linear working space (Ottosson 2020). Mirrors
// engine.linearToOKLab; double precision so the golden-diff stays tight.
__device__ inline void d_linearToOKLab(double r, double g, double b, double* L, double* A, double* B) {
    double l = 0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b;
    double m = 0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b;
    double s = 0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b;
    double lc = cbrt(l), mc = cbrt(m), sc = cbrt(s);
    *L = 0.2104542553 * lc + 0.7936177850 * mc - 0.0040720468 * sc;
    *A = 1.9779984951 * lc - 2.4285922050 * mc + 0.4505937099 * sc;
    *B = 0.0259040371 * lc + 0.7827717662 * mc - 0.8086757660 * sc;
}

// d_cbrtSq — cbrt(max(x,eps))^2, the clamped cube-root-derivative denominator (engine.cbrtSq).
__device__ inline double d_cbrtSq(double x) {
    if (x < 1e-7) x = 1e-7;
    double c = cbrt(x);
    return c * c;
}

// feLumaKernel — Rec.601 luma plane of an RGBA canvas (engine lumaOf / metric.Luma).
__global__ void feLumaKernel(const float* src, float* dst, int N) {
    int i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i >= N) return;
    int p = i * 4;
    dst[i] = 0.299f * src[p] + 0.587f * src[p + 1] + 0.114f * src[p + 2];
}

// d_sobelAt — Sobel gradient components at an INTERIOR pixel (no clamping; engine sobelAtFast).
__device__ inline void d_sobelAt(const float* luma, int w, int i, double* gx, double* gy) {
    double tl = luma[i - w - 1], tc = luma[i - w], tr = luma[i - w + 1];
    double ml = luma[i - 1], mr = luma[i + 1];
    double bl = luma[i + w - 1], bc = luma[i + w], br = luma[i + w + 1];
    *gx = (tr + 2.0 * mr + br) - (tl + 2.0 * ml + bl);
    *gy = (bl + 2.0 * bc + br) - (tl + 2.0 * tc + tr);
}

// feDirReduce — per interior pixel: relu(|∇recon|−|∇target|); active pixels write their normalised
// gradient direction into dir (zeroed otherwise) and the excess into the block's FE partial
// (NO atomic — fixed-order final sum, like polishLossReduce).
__global__ void feDirReduce(const float* rl, const float* tl, int w, int h, double* dir, double* fepartial) {
    __shared__ double sh[BLOCK];
    int N = w * h;
    double s = 0.0;
    for (int i = blockIdx.x * blockDim.x + threadIdx.x; i < N; i += gridDim.x * blockDim.x) {
        int x = i % w, y = i / w;
        double cx = 0.0, cy = 0.0;
        if (x > 0 && x < w - 1 && y > 0 && y < h - 1) {
            double gx, gy, tx, ty;
            d_sobelAt(rl, w, i, &gx, &gy);
            d_sobelAt(tl, w, i, &tx, &ty);
            double gr = sqrt(gx * gx + gy * gy);
            double d = gr - sqrt(tx * tx + ty * ty);
            if (d > 0.0 && gr >= 1e-12) {
                s += d;
                cx = gx / gr;
                cy = gy / gr;
            }
        }
        dir[i * 2] = cx;
        dir[i * 2 + 1] = cy;
    }
    sh[threadIdx.x] = s;
    __syncthreads();
    for (int st = blockDim.x / 2; st > 0; st >>= 1) {
        if (threadIdx.x < st) sh[threadIdx.x] += sh[threadIdx.x + st];
        __syncthreads();
    }
    if (threadIdx.x == 0) fepartial[blockIdx.x] = sh[0];
}

// feAdjKernel — gather form of the CPU scatter (engine feState.adjoint): each pixel collects the
// Sobel-stencil weights of its 8 neighbours' directions. dir is 0 on the border ring and on
// inactive pixels, so no bounds branching beyond the canvas edge is needed.
__global__ void feAdjKernel(const double* dir, int w, int h, double* adj) {
    int i = blockIdx.x * blockDim.x + threadIdx.x;
    int N = w * h;
    if (i >= N) return;
    int x = i % w, y = i / w;
    double a = 0.0;
    // neighbour q at (dx,dy) contributes W(-dx,-dy)(q): the inverse of the CPU scatter offsets.
    if (x + 1 < w && y + 1 < h) { double cx = dir[(i + w + 1) * 2], cy = dir[(i + w + 1) * 2 + 1]; a += -cx - cy; }
    if (y + 1 < h)              { double cy = dir[(i + w) * 2 + 1];                                a += -2.0 * cy; }
    if (x > 0 && y + 1 < h)     { double cx = dir[(i + w - 1) * 2], cy = dir[(i + w - 1) * 2 + 1]; a += cx - cy; }
    if (x + 1 < w)              { double cx = dir[(i + 1) * 2];                                    a += -2.0 * cx; }
    if (x > 0)                  { double cx = dir[(i - 1) * 2];                                    a += 2.0 * cx; }
    if (x + 1 < w && y > 0)     { double cx = dir[(i - w + 1) * 2], cy = dir[(i - w + 1) * 2 + 1]; a += -cx + cy; }
    if (y > 0)                  { double cy = dir[(i - w) * 2 + 1];                                a += 2.0 * cy; }
    if (x > 0 && y > 0)         { double cx = dir[(i - w - 1) * 2], cy = dir[(i - w - 1) * 2 + 1]; a += cx + cy; }
    adj[i] = a;
}

// feAddTotal — fixed-order sum of the FE partials, scaled by λ, added into lossOut (one thread).
__global__ void feAddTotal(const double* fepartial, int blocks, double lambda, double* lossOut) {
    if (threadIdx.x != 0) return;
    double s = 0.0;
    for (int b = 0; b < blocks; b++) s += fepartial[b];
    *lossOut += lambda * s;
}

// ssHPass — horizontal 8-term window sums of luma, luma² and luma·targetLuma (engine
// ssimState.hpass). One thread per (row, valid px); k ascends 0..7 like the CPU loop.
__global__ void ssHPass(const float* luma, const float* tl, int w, int h, int mw,
                        double* hx, double* hxx, double* hxy) {
    int i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i >= mw * h) return;
    int y = i / mw, px = i % mw;
    int row = y * w;
    double sx = 0.0, sxx = 0.0, sxy = 0.0;
    for (int k = 0; k < SSWIN; k++) {
        double v = (double)luma[row + px + k];
        sx += v;
        sxx += v * v;
        sxy += v * (double)tl[row + px + k];
    }
    hx[i] = sx; hxx[i] = sxx; hxy[i] = sxy;
}

// ssMyInit — target window means from the target's own h-pass (hx=Σy, hxx=Σy²).
__global__ void ssMyInit(const double* hx, const double* hxx, int mw, int mh, double* my, double* myy) {
    int i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i >= mw * mh) return;
    int gy = i / mw, gx = i % mw;
    double sy = 0.0, syy = 0.0;
    for (int k = 0; k < SSWIN; k++) {
        int row = (gy + k) * mw;
        sy += hx[row + gx];
        syy += hxx[row + gx];
    }
    my[i] = sy * SSINVN;
    myy[i] = syy * SSINVN;
}

// ssMapReduce — per valid window: vertical 8-sums of the h-pass, the SSIM pieces, Σ(1−S) into the
// block partial (NO atomic — fixed-order final sum, like feDirReduce); when g1 is non-null also
// the per-window moment-gradients for the backward (engine ssimState.adjoint, same formulas).
__global__ void ssMapReduce(const double* hx, const double* hxx, const double* hxy,
                            const double* my, const double* myy, int mw, int mh,
                            double* g1, double* g2, double* g3, double* partial) {
    __shared__ double sh[BLOCK];
    int nwin = mw * mh;
    double acc = 0.0;
    for (int i = blockIdx.x * blockDim.x + threadIdx.x; i < nwin; i += gridDim.x * blockDim.x) {
        int gy = i / mw, gx = i % mw;
        double sx = 0.0, sxx = 0.0, sxy = 0.0;
        for (int k = 0; k < SSWIN; k++) {
            int row = (gy + k) * mw;
            sx += hx[row + gx];
            sxx += hxx[row + gx];
            sxy += hxy[row + gx];
        }
        double mx = sx * SSINVN, mxx = sxx * SSINVN, mxy = sxy * SSINVN;
        double m2 = my[i], m2y = myy[i];
        double a1 = 2.0 * mx * m2 + SSC1;
        double a2 = 2.0 * (mxy - mx * m2) + SSC2;
        double b1 = mx * mx + m2 * m2 + SSC1;
        double b2 = (mxx - mx * mx) + (m2y - m2 * m2) + SSC2;
        double s = (a1 * a2) / (b1 * b2);
        acc += 1.0 - s;
        if (g1) {
            double denom = b1 * b2;
            double dsdmx = 2.0 * m2 * (a2 - a1) / denom - s * 2.0 * mx * (1.0 / b1 - 1.0 / b2);
            g1[i] = -dsdmx;
            g2[i] = s / b2;
            g3[i] = -2.0 * a1 / denom;
        }
    }
    sh[threadIdx.x] = acc;
    __syncthreads();
    for (int st = blockDim.x / 2; st > 0; st >>= 1) {
        if (threadIdx.x < st) sh[threadIdx.x] += sh[threadIdx.x + st];
        __syncthreads();
    }
    if (threadIdx.x == 0) partial[blockIdx.x] = sh[0];
}

// ssGH — horizontal box-correlation of the moment-gradients: HG(y,qx) = Σ G(y,px), px ∈
// [qx−7, qx] clamped to valid window columns (ascending, like the CPU loop).
__global__ void ssGH(const double* g1, const double* g2, const double* g3, int w, int mw, int mh,
                     double* hg1, double* hg2, double* hg3) {
    int i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i >= w * mh) return;
    int gy = i / w, qx = i % w;
    int p0 = qx - SSWIN + 1; if (p0 < 0) p0 = 0;
    int p1 = qx; if (p1 > mw - 1) p1 = mw - 1;
    int grow = gy * mw;
    double t1 = 0.0, t2 = 0.0, t3 = 0.0;
    for (int px = p0; px <= p1; px++) {
        t1 += g1[grow + px];
        t2 += g2[grow + px];
        t3 += g3[grow + px];
    }
    hg1[i] = t1; hg2[i] = t2; hg3[i] = t3;
}

// ssAdjKernel — vertical box-correlation + per-pixel combine: adj(q) = (T1 + 2x·T2 + y·T3)/N.
__global__ void ssAdjKernel(const double* hg1, const double* hg2, const double* hg3,
                            const float* rl, const float* tl, int w, int h, int mh, double* adj) {
    int i = blockIdx.x * blockDim.x + threadIdx.x;
    if (i >= w * h) return;
    int qy = i / w, qx = i % w;
    int p0 = qy - SSWIN + 1; if (p0 < 0) p0 = 0;
    int p1 = qy; if (p1 > mh - 1) p1 = mh - 1;
    double t1 = 0.0, t2 = 0.0, t3 = 0.0;
    for (int py = p0; py <= p1; py++) {
        int row = py * w;
        t1 += hg1[row + qx];
        t2 += hg2[row + qx];
        t3 += hg3[row + qx];
    }
    adj[i] = (t1 + 2.0 * (double)rl[i] * t2 + (double)tl[i] * t3) * SSINVN;
}

// polishLossReduce sums the weighted 4-channel SSE of render vs the (eval-shared) target,
// writing each block's partial to lpartial[blockIdx.x] (NO atomic). polishLossFinal then sums
// the partials in fixed block order — deterministic (the old atomicAdd into a scalar jittered).
// oklab = the perceptual variant: w*(dL^2+da^2+db^2+dalpha^2) (engine.okLabPixelLoss).
__global__ void polishLossReduce(const float* render, const float* target, const float* weight,
                                 int N, double* lpartial, int oklab) {
    __shared__ double sh[BLOCK];
    double s = 0.0;
    for (int idx = blockIdx.x * blockDim.x + threadIdx.x; idx < N; idx += gridDim.x * blockDim.x) {
        double wt = (double)weight[idx];
        int p = idx * 4;
        if (oklab) {
            double rL, rA, rB, tL, tA, tB;
            d_linearToOKLab((double)render[p], (double)render[p + 1], (double)render[p + 2], &rL, &rA, &rB);
            d_linearToOKLab((double)target[p], (double)target[p + 1], (double)target[p + 2], &tL, &tA, &tB);
            double dL = rL - tL, dA = rA - tA, dB = rB - tB;
            double dAl = (double)render[p + 3] - (double)target[p + 3];
            s += wt * (dL * dL + dA * dA + dB * dB + dAl * dAl);
            continue;
        }
        for (int c = 0; c < 4; c++) {
            double d = (double)render[p + c] - (double)target[p + c];
            s += wt * d * d;
        }
    }
    sh[threadIdx.x] = s;
    __syncthreads();
    for (int st = blockDim.x / 2; st > 0; st >>= 1) {
        if (threadIdx.x < st) sh[threadIdx.x] += sh[threadIdx.x + st];
        __syncthreads();
    }
    if (threadIdx.x == 0) lpartial[blockIdx.x] = sh[0];
}

// polishLossFinal sums the `blocks` partial losses in fixed order into lossOut (one thread).
__global__ void polishLossFinal(const double* lpartial, int blocks, double* lossOut) {
    if (threadIdx.x != 0) return;
    double s = 0.0;
    for (int b = 0; b < blocks; b++) s += lpartial[b];
    *lossOut = s;
}

// polishDCInit sets dC = 2*weight*(render-target) per channel (the loss gradient
// w.r.t. the final composited color). Mirrors the head of polish.go polishBackward.
// oklab = 2*weight*J^T*dLab for the colour channels (engine.okLabPixelDC).
__global__ void polishDCInit(const float* render, const float* target, const float* weight,
                            int N, float* dC, int oklab, const double* feAdj, double feLambda,
                            const double* ssimAdj, double ssimLambda) {
    int idx = blockIdx.x * blockDim.x + threadIdx.x;
    if (idx >= N) return;
    double wt = (double)weight[idx];
    int p = idx * 4;
    double feR = 0.0, feG = 0.0, feB = 0.0;
    if (feAdj || ssimAdj) {
        double a = 0.0;
        if (feAdj) a = feLambda * feAdj[idx];
        if (ssimAdj) a += ssimLambda * ssimAdj[idx];
        feR = a * 0.299; feG = a * 0.587; feB = a * 0.114;
    }
    if (oklab) {
        double rr = (double)render[p], rg = (double)render[p + 1], rb = (double)render[p + 2];
        double l = 0.4122214708 * rr + 0.5363325363 * rg + 0.0514459929 * rb;
        double m = 0.2119034982 * rr + 0.6806995451 * rg + 0.1073969566 * rb;
        double s = 0.0883024619 * rr + 0.2817188376 * rg + 0.6299787005 * rb;
        double lc = cbrt(l), mc = cbrt(m), sc = cbrt(s);
        double rL = 0.2104542553 * lc + 0.7936177850 * mc - 0.0040720468 * sc;
        double rA = 1.9779984951 * lc - 2.4285922050 * mc + 0.4505937099 * sc;
        double rB = 0.0259040371 * lc + 0.7827717662 * mc - 0.8086757660 * sc;
        double tL, tA, tB;
        d_linearToOKLab((double)target[p], (double)target[p + 1], (double)target[p + 2], &tL, &tA, &tB);
        double dL = 2.0 * wt * (rL - tL), dA = 2.0 * wt * (rA - tA), dB = 2.0 * wt * (rB - tB);
        double gl = dL * 0.2104542553 + dA * 1.9779984951 + dB * 0.0259040371;
        double gm = dL * 0.7936177850 - dA * 2.4285922050 + dB * 0.7827717662;
        double gs = -dL * 0.0040720468 + dA * 0.4505937099 - dB * 0.8086757660;
        gl /= 3.0 * d_cbrtSq(l);
        gm /= 3.0 * d_cbrtSq(m);
        gs /= 3.0 * d_cbrtSq(s);
        dC[p + 0] = (float)(gl * 0.4122214708 + gm * 0.2119034982 + gs * 0.0883024619 + feR);
        dC[p + 1] = (float)(gl * 0.5363325363 + gm * 0.6806995451 + gs * 0.2817188376 + feG);
        dC[p + 2] = (float)(gl * 0.0514459929 + gm * 0.1073969566 + gs * 0.6299787005 + feB);
        dC[p + 3] = (float)(2.0 * wt * ((double)render[p + 3] - (double)target[p + 3]));
        return;
    }
    dC[p + 0] = (float)(2.0 * wt * ((double)render[p + 0] - (double)target[p + 0]) + feR);
    dC[p + 1] = (float)(2.0 * wt * ((double)render[p + 1] - (double)target[p + 1]) + feG);
    dC[p + 2] = (float)(2.0 * wt * ((double)render[p + 2] - (double)target[p + 2]) + feB);
    dC[p + 3] = (float)(2.0 * wt * ((double)render[p + 3] - (double)target[p + 3]));
}

// polishGradReduceAll sums each shape's PMAXBLK partial gradients in FIXED block order into
// pgrad[si] (one block per shape, 10 threads = one per gradient component). Unused block slots
// are zeroed by the cudaMemset in fp_polish_backward, so the fixed-stride sum is exact.
__global__ void polishGradReduceAll(const double* gpartial, double* pgrad) {
    int si = blockIdx.x;
    int i = threadIdx.x; // 0..9
    if (i >= 10) return;
    const double* base = gpartial + (long long)si * PMAXBLK * 10;
    double s = 0.0;
    for (int b = 0; b < PMAXBLK; b++) s += base[(long long)b * 10 + i];
    pgrad[(long long)si * 10 + i] = s;
}

// ===== TILED polish: one thread per PIXEL walks ALL shapes in a register loop (forward/hard)
// or reverse (backward dC), replacing the N sequential per-shape kernel launches with O(1)
// launches. Mirrors the per-shape kernels above byte-for-byte (incl. gradient kinds). =====

// polishForwardTiled — render = base then composite every shape in order (one dispatch).
__global__ void polishForwardTiled(const float* base, float* render, float* below,
                                   const double* pP, const double* pcol, const int* pkind,
                                   const int* pbbx, const long long* pboff, int n, int W, int H,
                                   double tau, int ste) {
    long long idx = (long long)blockIdx.x * blockDim.x + threadIdx.x;
    if (idx >= (long long)W * H) return;
    int x = (int)(idx % W), y = (int)(idx / W);
    long long p = idx * 4;
    float ar = base[p], ag = base[p + 1], ab = base[p + 2], aa = base[p + 3];
    for (int si = 0; si < n; si++) {
        const int* bb = pbbx + si * 4;
        if (x < bb[0] || x > bb[2] || y < bb[1] || y > bb[3]) continue;
        const double* P = pP + (long long)si * 6;
        const double* col = pcol + (long long)si * 4;
        int kind = pkind[si];
        int bw = bb[2] - bb[0] + 1;
        long long bIdx = pboff[si] + ((long long)(y - bb[1]) * bw + (x - bb[0])) * 4;
        below[bIdx + 0] = ar; below[bIdx + 1] = ag; below[bIdx + 2] = ab; below[bIdx + 3] = aa;
        double cov;
        if (polishOptGeo(kind)) {
            double sdf = polishSDFGrad(kind, P, x + 0.5, y + 0.5, nullptr);
            cov = ste ? (sdf <= 0.0 ? 1.0 : 0.0) : sigmoidCovD(sdf, tau);
        } else {
            float fp[6]; for (int i = 0; i < 6; i++) fp[i] = (float)P[i];
            cov = isGradKind(kind) ? (double)gradCovP(kind, fp, x, y) : (inside(kind, fp, x, y) ? 1.0 : 0.0);
        }
        float covf = (float)cov;
        if (covf > 0.0f) {
            float A = (float)col[3];
            float a = A * covf, ia = 1.0f - a;
            ar = ar * ia + (float)col[0] * a;
            ag = ag * ia + (float)col[1] * a;
            ab = ab * ia + (float)col[2] * a;
            aa = aa * ia + a;
        }
    }
    render[p + 0] = ar; render[p + 1] = ag; render[p + 2] = ab; render[p + 3] = aa;
}

// polishHardTiled — HARD-coverage deliverable render (no below snapshot), one dispatch.
__global__ void polishHardTiled(const float* base, float* render, const double* pP,
                                const double* pcol, const int* pkind, const int* pbbx,
                                int n, int W, int H) {
    long long idx = (long long)blockIdx.x * blockDim.x + threadIdx.x;
    if (idx >= (long long)W * H) return;
    int x = (int)(idx % W), y = (int)(idx / W);
    long long p = idx * 4;
    float ar = base[p], ag = base[p + 1], ab = base[p + 2], aa = base[p + 3];
    for (int si = 0; si < n; si++) {
        const int* bb = pbbx + si * 4;
        if (x < bb[0] || x > bb[2] || y < bb[1] || y > bb[3]) continue;
        const double* P = pP + (long long)si * 6;
        const double* col = pcol + (long long)si * 4;
        int kind = pkind[si];
        float fp[6]; for (int i = 0; i < 6; i++) fp[i] = (float)P[i];
        float a = (float)col[3];
        a = a < 0.0f ? 0.0f : (a > 1.0f ? 1.0f : a);
        if (isGradKind(kind)) {
            float cov = gradCovP(kind, fp, x, y);
            if (cov <= 0.0f) continue;
            a *= cov;
        } else if (!inside(kind, fp, x, y)) {
            continue;
        }
        float ia = 1.0f - a;
        ar = ar * ia + (float)col[0] * a;
        ag = ag * ia + (float)col[1] * a;
        ab = ab * ia + (float)col[2] * a;
        aa = aa * ia + a;
    }
    render[p + 0] = ar; render[p + 1] = ag; render[p + 2] = ab; render[p + 3] = aa;
}

// polishDCWalk — backward Pass A: one thread per PIXEL walks shapes in REVERSE, snapshotting
// the upstream gradient dC AS SEEN BY each shape into dcsnap (below-layout) and propagating
// dC *= 1-a in a register. The sequential top->bottom dC dependency becomes a per-pixel
// register loop -> the N sequential per-shape launches collapse to ONE. covEff/geoActive are
// computed identically to polishBackwardReduce (same coverage fns) so their snapshot/skip
// conditions match exactly (else Pass B could read an unwritten dcsnap slot).
__global__ void polishDCWalk(const float* dC, float* dcsnap, const double* pP, const double* pcol,
                             const int* pkind, const int* pbbx, const long long* pboff, int n,
                             int W, int H, double tau, int ste) {
    long long idx = (long long)blockIdx.x * blockDim.x + threadIdx.x;
    if (idx >= (long long)W * H) return;
    int x = (int)(idx % W), y = (int)(idx / W);
    long long p = idx * 4;
    float d0 = dC[p], d1 = dC[p + 1], d2 = dC[p + 2], d3 = dC[p + 3];
    for (int si = n - 1; si >= 0; si--) {
        const int* bb = pbbx + si * 4;
        if (x < bb[0] || x > bb[2] || y < bb[1] || y > bb[3]) continue;
        const double* P = pP + (long long)si * 6;
        const double* col = pcol + (long long)si * 4;
        int kind = pkind[si];
        double covEff = 0.0; bool geoActive = false;
        if (isGradKind(kind)) {
            float fp[6]; for (int i = 0; i < 6; i++) fp[i] = (float)P[i];
            double gg[6];
            double cov = gaussianCovGradD(kind, fp, x, y, gg); // same cov source as Pass B
            covEff = cov;
            if (kind == 4) geoActive = cov > 1e-9;
        } else if (polishOptGeo(kind)) {
            double sdf = polishSDFGrad(kind, P, x + 0.5, y + 0.5, nullptr);
            double covS = sigmoidCovD(sdf, tau);
            if (ste) { if (sdf <= 0.0) covEff = 1.0; geoActive = covS > 1e-12; }
            else { covEff = covS; geoActive = covS > 0.0; }
        } else {
            float fp[6]; for (int i = 0; i < 6; i++) fp[i] = (float)P[i];
            covEff = inside(kind, fp, x, y) ? 1.0 : 0.0;
        }
        bool colorActive = covEff > 0.0;
        if (!geoActive && !colorActive) continue;
        int bw = bb[2] - bb[0] + 1;
        long long bIdx = pboff[si] + ((long long)(y - bb[1]) * bw + (x - bb[0])) * 4;
        dcsnap[bIdx + 0] = d0; dcsnap[bIdx + 1] = d1; dcsnap[bIdx + 2] = d2; dcsnap[bIdx + 3] = d3;
        if (colorActive) {
            double a = col[3] * covEff, ia = 1.0 - a;
            d0 = (float)(d0 * ia); d1 = (float)(d1 * ia); d2 = (float)(d2 * ia); d3 = (float)(d3 * ia);
        }
    }
}

// polishBackwardReduce — backward Pass B: 2D grid (blockIdx.y=shape, blockIdx.x=block-in-shape,
// gridDim.x=PMAXBLK). Each block grid-strides its shape's bbox reading dcsnap (NOT dC — Pass A
// already propagated) and accumulates the 10-double gradient, writing its OWN gpartial slot.
// All shapes run in ONE launch (no dC dependency between them now). The per-block pixel chunks
// are IDENTICAL to the old adaptive per-shape launch (grid-stride with PMAXBLK extra-but-empty
// blocks), so polishGradReduceAll sums the same partials -> bit-exact gradient.
__global__ void polishBackwardReduce(const float* below, const float* dcsnap, const double* pP,
                                     const double* pcol, const int* pkind, const int* pbbx,
                                     const long long* pboff, int n, int W, double tau,
                                     double* gpartial, int ste) {
    int si = blockIdx.y;
    if (si >= n) return;
    const int* bb = pbbx + si * 4;
    int xMin = bb[0], yMin = bb[1], xMax = bb[2], yMax = bb[3];
    int bw = xMax - xMin + 1, bh = yMax - yMin + 1;
    long long total = (xMax < xMin || yMax < yMin) ? 0 : (long long)bw * bh;
    const double* P = pP + (long long)si * 6;
    const double* col = pcol + (long long)si * 4;
    int kind = pkind[si];
    bool opt = polishOptGeo(kind);
    double R = col[0], G = col[1], B = col[2], A = col[3];
    long long off = pboff[si];
    float fp[6]; for (int i = 0; i < 6; i++) fp[i] = (float)P[i];
    double L[10]; for (int i = 0; i < 10; i++) L[i] = 0.0;
    long long gtid = (long long)blockIdx.x * blockDim.x + threadIdx.x;
    long long gstride = (long long)gridDim.x * blockDim.x;
    for (long long tt = gtid; tt < total; tt += gstride) {
        int x = xMin + (int)(tt % bw);
        int y = yMin + (int)(tt / bw);
        long long li = tt * 4;
        double covEff = 0.0, covS = 0.0, dcovdsdf = 0.0, sdfg[6];
        bool geoActive = false;
        if (isGradKind(kind)) {
            double gg[6];
            double cov = gaussianCovGradD(kind, fp, x, y, gg);
            covEff = cov;
            if (kind == 4) { covS = cov; for (int i = 0; i < 6; i++) sdfg[i] = gg[i]; dcovdsdf = 1.0; geoActive = cov > 1e-9; }
        } else if (opt) {
            double sdf = polishSDFGrad(kind, P, x + 0.5, y + 0.5, sdfg);
            covS = sigmoidCovD(sdf, tau);
            dcovdsdf = -covS * (1.0 - covS) / tau;
            if (ste) { if (sdf <= 0.0) covEff = 1.0; geoActive = covS > 1e-12; }
            else { covEff = covS; geoActive = covS > 0.0; }
        } else {
            covEff = inside(kind, fp, x, y) ? 1.0 : 0.0;
        }
        bool colorActive = covEff > 0.0;
        if (!geoActive && !colorActive) continue;
        double d0 = dcsnap[off + li + 0], d1 = dcsnap[off + li + 1], d2 = dcsnap[off + li + 2], d3 = dcsnap[off + li + 3];
        double cb0 = below[off + li + 0], cb1 = below[off + li + 1], cb2 = below[off + li + 2], cb3 = below[off + li + 3];
        double da = d0 * (R - cb0) + d1 * (G - cb1) + d2 * (B - cb2) + d3 * (1.0 - cb3);
        if (colorActive) {
            double a = A * covEff;
            L[6] += d0 * a; L[7] += d1 * a; L[8] += d2 * a; L[9] += da * covEff;
        }
        if (geoActive) {
            double dsdf = (da * A) * dcovdsdf;
            L[0] += dsdf * sdfg[0]; L[1] += dsdf * sdfg[1]; L[2] += dsdf * sdfg[2];
            L[3] += dsdf * sdfg[3]; L[4] += dsdf * sdfg[4]; L[5] += dsdf * sdfg[5];
        }
    }
    __shared__ double sh[BLOCK * 10];
    for (int i = 0; i < 10; i++) sh[threadIdx.x * 10 + i] = L[i];
    __syncthreads();
    for (int st = blockDim.x / 2; st > 0; st >>= 1) {
        if (threadIdx.x < st)
            for (int i = 0; i < 10; i++) sh[threadIdx.x * 10 + i] += sh[(threadIdx.x + st) * 10 + i];
        __syncthreads();
    }
    if (threadIdx.x == 0) {
        long long base = ((long long)si * PMAXBLK + blockIdx.x) * 10;
        for (int i = 0; i < 10; i++) gpartial[base + i] = sh[i];
    }
}

// ---- extern C API ----

API int fp_init(const float* target, const float* weight, int w, int h, int maxCands, int gridSize) {
    g_w = w; g_h = h; g_gw = gridSize; g_gh = gridSize; g_maxCands = maxCands;
    size_t npix = (size_t)w * h;
    if (cudaMalloc(&d_target, npix * 4 * sizeof(float)) != cudaSuccess) return 1;
    if (cudaMalloc(&d_canvas, npix * 4 * sizeof(float)) != cudaSuccess) return 2;
    if (cudaMalloc(&d_weight, npix * sizeof(float)) != cudaSuccess) return 3;
    if (cudaMalloc(&d_cands, (size_t)maxCands * 11 * sizeof(float)) != cudaSuccess) return 4;
    if (cudaMalloc(&d_out, (size_t)maxCands * 5 * sizeof(float)) != cudaSuccess) return 5;
    if (cudaMalloc(&d_grid, (size_t)gridSize * gridSize * sizeof(float)) != cudaSuccess) return 6;
    cudaMemcpy(d_target, target, npix * 4 * sizeof(float), cudaMemcpyHostToDevice);
    cudaMemcpy(d_weight, weight, npix * sizeof(float), cudaMemcpyHostToDevice);
    cudaMemset(d_canvas, 0, npix * 4 * sizeof(float));
    return cudaGetLastError() == cudaSuccess ? 0 : 7;
}

API void fp_eval(const float* cands, int n, float* out) {
    if (n <= 0) return;
    if (n > g_maxCands) n = g_maxCands;
    cudaMemcpy(d_cands, cands, (size_t)n * 11 * sizeof(float), cudaMemcpyHostToDevice);
    if (g_warpEval && !g_gradients)
        evalKernelWarp<<<(n + 3) / 4, 128>>>(d_cands, n, d_target, d_canvas, d_weight, g_w, g_h, g_sampleBudget, d_out);
    else
        evalKernel<<<n, BLOCK>>>(d_cands, n, d_target, d_canvas, d_weight, g_w, g_h, g_sampleBudget, d_out);
    cudaMemcpy(out, d_out, (size_t)n * 5 * sizeof(float), cudaMemcpyDeviceToHost);
}

// fp_last_error returns AND CLEARS the sticky CUDA error (cudaGetLastError). The Go side calls it
// after a run of kernels so a launch/exec fault (OOM, illegal address) surfaces as a Go error instead
// of leaving the eval output silently garbage. 0 = cudaSuccess. Absent on DLLs predating this export.
API int fp_last_error() { return (int)cudaGetLastError(); }

// fp_set_sample_budget sets the progressive-sampling pixel cap used by the eval
// kernel (mirrors CPU SetSampleBudget). n<1 resets to the default 4000. A value
// >= image area makes scoring effectively full-resolution.
API void fp_set_sample_budget(int n) {
    g_sampleBudget = (n < 1) ? 4000 : n;
}

// fp_set_warp_eval toggles the eval kernel: 1 = warp-per-candidate (faster), 0 =
// block-per-candidate (the original golden kernel). For comparison + a safe fallback.
API void fp_set_warp_eval(int on) { g_warpEval = on ? 1 : 0; }

// fp_set_gradients tells fp_eval the batch may contain the native gradient kinds (KindGlow/KindDisk),
// so it routes to the block kernel (evalKernel) whose per-candidate branch evaluates their per-pixel
// falloff. Hard candidates in the same batch take evalKernel's unchanged path. The apply kernel keys
// off the candidate's kind directly, so it needs no flag.
API void fp_set_gradients(int on) { g_gradients = on ? 1 : 0; }

// fp_set_coarse enables coarse-to-fine search (see g_coarseSearch): budget = the cheap
// pixel cap for the filter pass (<1 -> 4000). Allocates the survivor scratch on first
// enable. Pass enable=0 to restore the single-pass full-budget search.
API void fp_set_coarse(int enable, int budget, int kpart) {
    g_coarseSearch = enable ? 1 : 0;
    g_coarseBudget = (budget < 1) ? 4000 : budget;
    if (kpart < 256) kpart = 2048;
    if (kpart > MAXKPART) kpart = MAXKPART;
    g_kpart = kpart;
    if (enable && !d_cselIdx) {
        cudaMalloc(&d_cselIdx, MAXKPART * sizeof(int));
        cudaMalloc(&d_scand2, (size_t)MAXKPART * 11 * sizeof(float));
        cudaMalloc(&d_sout2, (size_t)MAXKPART * 5 * sizeof(float));
        cudaMalloc(&d_adj2, (size_t)MAXKPART * sizeof(float));
    }
}

// fp_set_coarse_fp16 toggles FP16/half2 accumulation for the coarse FILTER pass (re-eval stays
// FP32). Faster but lossy — only safe because the FP32 re-eval picks the winner. No-op if the
// DLL predates the export.
API void fp_set_coarse_fp16(int on) { g_coarseFP16 = on ? 1 : 0; }

// fp_set_polish_ste toggles straight-through coverage in the polish kernels: 1 = HARD
// forward composite (the exact deliverable) with the soft surrogate gradient, 0 = soft.
API void fp_set_polish_ste(int on) { g_polishSTE = on ? 1 : 0; }

// fp_set_polish_oklab toggles the perceptual OKLab colour metric in the polish loss, hard loss
// and backward seed together (one flag, so the optimisation stays self-consistent).
API void fp_set_polish_oklab(int on) { g_polishOKLab = on ? 1 : 0; }

// fp_set_polish_false_edge sets the false-edge λ (pointer: Win64 syscall path keeps doubles out of
// XMM) and prepares the FE planes + target luma. λ<=0 disables the term (buffers stay for reuse).
API void fp_set_polish_false_edge(const double* lambdaPtr) {
    g_polishFELambda = lambdaPtr[0];
    if (g_polishFELambda <= 0.0) return;
    int npix = g_w * g_h;
    if (!d_feTL) {
        cudaMalloc(&d_feTL, (size_t)npix * sizeof(float));
        cudaMalloc(&d_feRL, (size_t)npix * sizeof(float));
        cudaMalloc(&d_feDir, (size_t)npix * 2 * sizeof(double));
        cudaMalloc(&d_feAdj, (size_t)npix * sizeof(double));
        cudaMalloc(&d_fe_partial, 1024 * sizeof(double));
    }
    feLumaKernel<<<(npix + BLOCK - 1) / BLOCK, BLOCK>>>(d_target, d_feTL, npix);
}

// feAccumulateLoss adds λ·FE(d_prender) into d_ploss (deterministic fixed-order partial sum).
static void feAccumulateLoss() {
    int npix = g_w * g_h;
    int blocks = (npix + BLOCK - 1) / BLOCK;
    feLumaKernel<<<blocks, BLOCK>>>(d_prender, d_feRL, npix);
    if (blocks > 1024) blocks = 1024;
    feDirReduce<<<blocks, BLOCK>>>(d_feRL, d_feTL, g_w, g_h, d_feDir, d_fe_partial);
    feAddTotal<<<1, 1>>>(d_fe_partial, blocks, g_polishFELambda, d_ploss);
}

// fp_set_polish_ssim sets the SSIM λ (pointer: Win64 syscall ABI) and prepares the target-side
// window moments. λ<=0 disables the term (buffers stay for reuse); a canvas smaller than one
// window degrades to λ=0 (the CPU reference's nil-state contract).
API void fp_set_polish_ssim(const double* lambdaPtr) {
    g_polishSSIMLambda = lambdaPtr[0];
    if (g_polishSSIMLambda <= 0.0) return;
    int mw = g_w - SSWIN + 1, mh = g_h - SSWIN + 1;
    if (mw < 1 || mh < 1) { g_polishSSIMLambda = 0.0; return; }
    int npix = g_w * g_h;
    if (!d_ssTL) {
        cudaMalloc(&d_ssTL, (size_t)npix * sizeof(float));
        cudaMalloc(&d_ssRL, (size_t)npix * sizeof(float));
        cudaMalloc(&d_ssMy, (size_t)mw * mh * sizeof(double));
        cudaMalloc(&d_ssMyy, (size_t)mw * mh * sizeof(double));
        cudaMalloc(&d_ssH, (size_t)mw * g_h * 3 * sizeof(double));
        cudaMalloc(&d_ssG, (size_t)mw * mh * 3 * sizeof(double));
        cudaMalloc(&d_ssHG, (size_t)g_w * mh * 3 * sizeof(double));
        cudaMalloc(&d_ssAdj, (size_t)npix * sizeof(double));
        cudaMalloc(&d_ss_partial, 1024 * sizeof(double));
    }
    feLumaKernel<<<(npix + BLOCK - 1) / BLOCK, BLOCK>>>(d_target, d_ssTL, npix);
    size_t hplane = (size_t)mw * g_h;
    int nh = mw * g_h;
    ssHPass<<<(nh + BLOCK - 1) / BLOCK, BLOCK>>>(d_ssTL, d_ssTL, g_w, g_h, mw, d_ssH, d_ssH + hplane, d_ssH + 2 * hplane);
    int nwin = mw * mh;
    ssMyInit<<<(nwin + BLOCK - 1) / BLOCK, BLOCK>>>(d_ssH, d_ssH + hplane, mw, mh, d_ssMy, d_ssMyy);
}

// ssimForward runs luma + the box-sum passes on d_prender; with G buffers when the backward
// needs the moment-gradients. Returns the partial-block count for the loss add.
static int ssimForward(bool forBackward) {
    int npix = g_w * g_h;
    int mw = g_w - SSWIN + 1, mh = g_h - SSWIN + 1;
    size_t hplane = (size_t)mw * g_h;
    size_t gplane = (size_t)mw * mh;
    int blocks = (npix + BLOCK - 1) / BLOCK;
    feLumaKernel<<<blocks, BLOCK>>>(d_prender, d_ssRL, npix);
    int nh = mw * g_h;
    ssHPass<<<(nh + BLOCK - 1) / BLOCK, BLOCK>>>(d_ssRL, d_ssTL, g_w, g_h, mw, d_ssH, d_ssH + hplane, d_ssH + 2 * hplane);
    int nwin = mw * mh;
    int rblocks = (nwin + BLOCK - 1) / BLOCK;
    if (rblocks > 1024) rblocks = 1024;
    ssMapReduce<<<rblocks, BLOCK>>>(d_ssH, d_ssH + hplane, d_ssH + 2 * hplane, d_ssMy, d_ssMyy, mw, mh,
                                    forBackward ? d_ssG : nullptr,
                                    forBackward ? d_ssG + gplane : nullptr,
                                    forBackward ? d_ssG + 2 * gplane : nullptr, d_ss_partial);
    return rblocks;
}

// ssimAccumulateLoss adds λ·Σ(1−SSIM)(d_prender) into d_ploss (fixed-order partial sum).
static void ssimAccumulateLoss() {
    int rblocks = ssimForward(false);
    feAddTotal<<<1, 1>>>(d_ss_partial, rblocks, g_polishSSIMLambda, d_ploss);
}

// fp_set_orient uploads the per-pixel edge-orientation map (len w*h, degrees) used
// by genKernel to seed elongated shapes along local edges. Called once by the engine
// before the greedy loop (the map is fixed for a run).
API void fp_set_orient(const float* orient) {
    if (!d_orient) cudaMalloc(&d_orient, (size_t)g_w * g_h * sizeof(float));
    cudaMemcpy(d_orient, orient, (size_t)g_w * g_h * sizeof(float), cudaMemcpyHostToDevice);
}

// fp_set_boundary_dist uploads the per-pixel distance-to-boundary field (len w*h, px)
// used by genKernel to cap candidate radii near edges (boundary-aware radius). Uploaded
// once per run (the field is fixed). NULL clears it (cap disabled).
API void fp_set_boundary_dist(const float* dist) {
    if (!dist) { if (d_boundDist) { cudaFree(d_boundDist); d_boundDist = nullptr; } return; }
    if (!d_boundDist) cudaMalloc(&d_boundDist, (size_t)g_w * g_h * sizeof(float));
    cudaMemcpy(d_boundDist, dist, (size_t)g_w * g_h * sizeof(float), cudaMemcpyHostToDevice);
}

// ensureSearch (re)allocates the search scratch to hold at least n candidates.
static void ensureSearch(int n) {
    if (n <= g_searchCap) return;
    if (d_scand) cudaFree(d_scand);
    if (d_sout) cudaFree(d_sout);
    if (d_adj) cudaFree(d_adj);
    cudaMalloc(&d_scand, (size_t)n * 11 * sizeof(float));
    cudaMalloc(&d_sout, (size_t)n * 5 * sizeof(float));
    cudaMalloc(&d_adj, (size_t)n * sizeof(float));
    g_searchCap = n;
}

// fp_search_random runs the WHOLE random phase for one shape on-device — generate n
// candidates, score them (reusing the golden-verified evalKernel against the current
// d_canvas), apply the selection penalty, and argmin — returning ONLY the single best
// candidate to the host (12 floats). This replaces the host RandomShapes->pack->fp_eval
// (16k-chunk D2H)->argmin loop, removing the per-chunk host transfer that capped throughput,
// so we can afford a very high candidate volume (hundreds of thousands per shape).
//
// Scalars are passed via memory (ip = int args, fp = float args) NOT as register
// scalars: the Go side calls this through syscall, which only loads integer/pointer
// registers — float scalar args (XMM regs in the Win64 ABI) would be garbage. seed is
// a 64-bit integer arg (safe in a register). kinds/kindCDF/gridCDF/out_best are host
// pointers; the arrays are uploaded H2D here.
//   ip = [n, nKinds, gw, gh, compact, shapeCount, allowAlpha]
//   fp = [maxR, alphaMin, aspectMax, boundPad, boundMix, canvasPad]
API void fp_search_random(unsigned long long seed, const int* ip, const float* fp,
                          const float* kinds, const float* kindCDF,
                          const float* gridCDF, float* out_best) {
    int n = ip[0], nKinds = ip[1], gw = ip[2], gh = ip[3];
    int compact = ip[4], shapeCount = ip[5], allowAlpha = ip[6];
    float maxR = fp[0], alphaMin = fp[1], aspectMax = fp[2];
    float boundPad = fp[3], boundMix = fp[4], canvasPad = fp[5];
    if (n < 1 || nKinds < 1) { out_best[0] = REJECTED; return; }
    ensureSearch(n);
    if (!d_kinds) { cudaMalloc(&d_kinds, 8 * sizeof(float)); cudaMalloc(&d_kindcdf, 8 * sizeof(float)); }
    if (!d_gridcdf) cudaMalloc(&d_gridcdf, (size_t)gw * gh * sizeof(float));
    if (!d_redVal) {
        cudaMalloc(&d_redVal, REDBLK * sizeof(float));
        cudaMalloc(&d_redIdx, REDBLK * sizeof(int));
        cudaMalloc(&d_bestIdx, sizeof(int));
        cudaMalloc(&d_best, 12 * sizeof(float));
    }
    cudaMemcpy(d_kinds, kinds, nKinds * sizeof(float), cudaMemcpyHostToDevice);
    cudaMemcpy(d_kindcdf, kindCDF, nKinds * sizeof(float), cudaMemcpyHostToDevice);
    cudaMemcpy(d_gridcdf, gridCDF, (size_t)gw * gh * sizeof(float), cudaMemcpyHostToDevice);

    int gblocks = (n + 255) / 256;
    if (gblocks > 65535) gblocks = 65535;
    genKernel<<<gblocks, 256>>>(d_scand, n, seed, d_kinds, d_kindcdf, nKinds,
                                maxR, allowAlpha, alphaMin, aspectMax, d_gridcdf, gw, gh, g_w, g_h, d_orient,
                                d_boundDist, boundPad, boundMix, canvasPad);
    int kpart = g_kpart;
    bool useCoarse = g_coarseSearch && d_scand2 && n > 4 * kpart;
    int firstBudget = useCoarse ? g_coarseBudget : g_sampleBudget;
    // Pass 1: score all n candidates (at the CHEAP coarse budget when filtering, else FULL).
    // When filtering, the FP16 variant (ranking-only, never the shipped score) cuts the ALU-bound
    // accumulation ~2x; the FP32 re-eval below picks + scores the winner exactly.
    if (useCoarse && g_coarseFP16)
        evalKernelWarpFP16<<<(n + 3) / 4, 128>>>(d_scand, n, d_target, d_canvas, d_weight, g_w, g_h, firstBudget, d_sout);
    else if (g_warpEval)
        evalKernelWarp<<<(n + 3) / 4, 128>>>(d_scand, n, d_target, d_canvas, d_weight, g_w, g_h, firstBudget, d_sout);
    else
        evalKernel<<<n, BLOCK>>>(d_scand, n, d_target, d_canvas, d_weight, g_w, g_h, firstBudget, d_sout);
    prepAdj<<<gblocks, 256>>>(d_sout, d_scand, n, compact, shapeCount, g_w, g_h, d_adj);
    if (useCoarse) {
        // Filter to kpart partition-minima, then re-score those at the FULL budget and argmin
        // them: the winner is chosen by a full-budget score (no low-res mis-pick), the bulk
        // paid only the coarse cost.
        coarsePartitionMin<<<kpart, 128>>>(d_adj, n, kpart, d_cselIdx);
        gatherSubset<<<(kpart + 255) / 256, 256>>>(d_scand, d_cselIdx, kpart, d_scand2);
        if (g_warpEval)
            evalKernelWarp<<<(kpart + 3) / 4, 128>>>(d_scand2, kpart, d_target, d_canvas, d_weight, g_w, g_h, g_sampleBudget, d_sout2);
        else
            evalKernel<<<kpart, BLOCK>>>(d_scand2, kpart, d_target, d_canvas, d_weight, g_w, g_h, g_sampleBudget, d_sout2);
        prepAdj<<<(kpart + 255) / 256, 256>>>(d_sout2, d_scand2, kpart, compact, shapeCount, g_w, g_h, d_adj2);
        argminPass1<<<REDBLK, 256>>>(d_adj2, kpart, d_redVal, d_redIdx);
        argminPass2<<<1, 256>>>(d_redVal, d_redIdx, REDBLK, d_bestIdx);
        gatherBest<<<1, 1>>>(d_scand2, d_sout2, d_bestIdx, d_best);
    } else {
        argminPass1<<<REDBLK, 256>>>(d_adj, n, d_redVal, d_redIdx);
        argminPass2<<<1, 256>>>(d_redVal, d_redIdx, REDBLK, d_bestIdx);
        gatherBest<<<1, 1>>>(d_scand, d_sout, d_bestIdx, d_best);
    }
    cudaMemcpy(out_best, d_best, 12 * sizeof(float), cudaMemcpyDeviceToHost); // syncs the stream
}

// fp_search_moment runs the on-device MOMENT-seeded search for one shape: fit K covariance-
// ellipse seeds from the residual grid, generate a localised refine pool around them, then score
// + argmin (same eval/coarse path as fp_search_random). Fully self-contained and additive — it
// shares no code with fp_search_random (the random path stays byte-identical), so either search
// can be removed independently. ip[7] = K (seed centres); the pool is perSeed = n/K per seed.
API void fp_search_moment(unsigned long long seed, const int* ip, const float* fp,
                          const float* kinds, const float* kindCDF,
                          const float* gridCDF, float* out_best) {
    int n = ip[0], nKinds = ip[1], gw = ip[2], gh = ip[3];
    int compact = ip[4], shapeCount = ip[5], allowAlpha = ip[6], K = ip[7];
    float maxR = fp[0], alphaMin = fp[1];
    float boundPad = fp[3], boundMix = fp[4], canvasPad = fp[5];
    if (n < 1 || nKinds < 1 || K < 1) { out_best[0] = REJECTED; return; }
    ensureSearch(n);
    if (!d_kinds) { cudaMalloc(&d_kinds, 8 * sizeof(float)); cudaMalloc(&d_kindcdf, 8 * sizeof(float)); }
    if (!d_gridcdf) cudaMalloc(&d_gridcdf, (size_t)gw * gh * sizeof(float));
    if (!d_redVal) {
        cudaMalloc(&d_redVal, REDBLK * sizeof(float));
        cudaMalloc(&d_redIdx, REDBLK * sizeof(int));
        cudaMalloc(&d_bestIdx, sizeof(int));
        cudaMalloc(&d_best, 12 * sizeof(float));
    }
    if (g_momentCap < K) { cudaFree(d_seeds); cudaMalloc(&d_seeds, (size_t)K * 6 * sizeof(float)); g_momentCap = K; }
    cudaMemcpy(d_kinds, kinds, nKinds * sizeof(float), cudaMemcpyHostToDevice);
    cudaMemcpy(d_kindcdf, kindCDF, nKinds * sizeof(float), cudaMemcpyHostToDevice);
    cudaMemcpy(d_gridcdf, gridCDF, (size_t)gw * gh * sizeof(float), cudaMemcpyHostToDevice);

    int perSeed = n / K;
    if (perSeed < 1) perSeed = 1;
    int nGen = perSeed * K; // candidates actually generated (<= n)
    momentSeedKernel<<<(K + 127) / 128, 128>>>(d_seeds, K, seed, d_gridcdf, gw, gh, g_w, g_h, maxR, d_boundDist, boundPad, boundMix);
    int gblocks = (nGen + 255) / 256;
    if (gblocks > 65535) gblocks = 65535;
    genMomentKernel<<<gblocks, 256>>>(d_scand, nGen, perSeed, K, seed, d_seeds, d_kinds, d_kindcdf, nKinds,
                                      allowAlpha, alphaMin, g_w, g_h, canvasPad);

    // Score + argmin (coarse-to-fine auto-applies only at high pool sizes; off for the small moment pool).
    int kpart = g_kpart;
    bool useCoarse = g_coarseSearch && d_scand2 && nGen > 4 * kpart;
    int firstBudget = useCoarse ? g_coarseBudget : g_sampleBudget;
    if (useCoarse && g_coarseFP16)
        evalKernelWarpFP16<<<(nGen + 3) / 4, 128>>>(d_scand, nGen, d_target, d_canvas, d_weight, g_w, g_h, firstBudget, d_sout);
    else if (g_warpEval)
        evalKernelWarp<<<(nGen + 3) / 4, 128>>>(d_scand, nGen, d_target, d_canvas, d_weight, g_w, g_h, firstBudget, d_sout);
    else
        evalKernel<<<nGen, BLOCK>>>(d_scand, nGen, d_target, d_canvas, d_weight, g_w, g_h, firstBudget, d_sout);
    prepAdj<<<gblocks, 256>>>(d_sout, d_scand, nGen, compact, shapeCount, g_w, g_h, d_adj);
    if (useCoarse) {
        coarsePartitionMin<<<kpart, 128>>>(d_adj, nGen, kpart, d_cselIdx);
        gatherSubset<<<(kpart + 255) / 256, 256>>>(d_scand, d_cselIdx, kpart, d_scand2);
        if (g_warpEval)
            evalKernelWarp<<<(kpart + 3) / 4, 128>>>(d_scand2, kpart, d_target, d_canvas, d_weight, g_w, g_h, g_sampleBudget, d_sout2);
        else
            evalKernel<<<kpart, BLOCK>>>(d_scand2, kpart, d_target, d_canvas, d_weight, g_w, g_h, g_sampleBudget, d_sout2);
        prepAdj<<<(kpart + 255) / 256, 256>>>(d_sout2, d_scand2, kpart, compact, shapeCount, g_w, g_h, d_adj2);
        argminPass1<<<REDBLK, 256>>>(d_adj2, kpart, d_redVal, d_redIdx);
        argminPass2<<<1, 256>>>(d_redVal, d_redIdx, REDBLK, d_bestIdx);
        gatherBest<<<1, 1>>>(d_scand2, d_sout2, d_bestIdx, d_best);
    } else {
        argminPass1<<<REDBLK, 256>>>(d_adj, nGen, d_redVal, d_redIdx);
        argminPass2<<<1, 256>>>(d_redVal, d_redIdx, REDBLK, d_bestIdx);
        gatherBest<<<1, 1>>>(d_scand, d_sout, d_bestIdx, d_best);
    }
    cudaMemcpy(out_best, d_best, 12 * sizeof(float), cudaMemcpyDeviceToHost); // syncs the stream
}

// ---- polish DLL API ----
// The host (Go) drives the iteration loop (Adam, tau anneal, best-hard, snap) and
// computes each shape's expanded bbox + below-buffer prefix offsets, mirroring
// internal/engine/polish.go. The DLL does the heavy per-pixel forward/loss/backward.
// Scalars that would land in XMM under the Win64 ABI (tau is double) are passed by
// pointer, since the Go syscall path only loads integer/pointer registers.

API void fp_polish_setup(const float* base, int n) {
    g_pN = n;
    size_t npix = (size_t)g_w * g_h;
    if (!d_pbase)   cudaMalloc(&d_pbase, npix * 4 * sizeof(float));
    if (!d_prender) cudaMalloc(&d_prender, npix * 4 * sizeof(float));
    if (!d_pdC)     cudaMalloc(&d_pdC, npix * 4 * sizeof(float));
    if (!d_ploss)   cudaMalloc(&d_ploss, sizeof(double));
    if (!d_ploss_partial) cudaMalloc(&d_ploss_partial, 1024 * sizeof(double)); // max 1024 loss blocks
    cudaFree(d_pP); cudaFree(d_pcol); cudaFree(d_pkind); cudaFree(d_pbbx); cudaFree(d_pboff); cudaFree(d_pgrad);
    cudaFree(d_pgrad_partial);
    cudaMalloc(&d_pP, (size_t)n * 6 * sizeof(double));
    cudaMalloc(&d_pcol, (size_t)n * 4 * sizeof(double));
    cudaMalloc(&d_pkind, (size_t)n * sizeof(int));
    cudaMalloc(&d_pbbx, (size_t)n * 4 * sizeof(int));
    cudaMalloc(&d_pboff, (size_t)n * sizeof(long long));
    cudaMalloc(&d_pgrad, (size_t)n * 10 * sizeof(double));
    cudaMalloc(&d_pgrad_partial, (size_t)n * PMAXBLK * 10 * sizeof(double)); // per (shape,block) partial grad
    cudaMemcpy(d_pbase, base, npix * 4 * sizeof(float), cudaMemcpyHostToDevice);
}

API void fp_polish_upload(const double* P, const double* col, const int* kinds,
                          const int* bbx, const long long* boff, long long belowTotal) {
    int n = g_pN;
    cudaMemcpy(d_pP, P, (size_t)n * 6 * sizeof(double), cudaMemcpyHostToDevice);
    cudaMemcpy(d_pcol, col, (size_t)n * 4 * sizeof(double), cudaMemcpyHostToDevice);
    cudaMemcpy(d_pkind, kinds, (size_t)n * sizeof(int), cudaMemcpyHostToDevice);
    cudaMemcpy(d_pbbx, bbx, (size_t)n * 4 * sizeof(int), cudaMemcpyHostToDevice);
    cudaMemcpy(d_pboff, boff, (size_t)n * sizeof(long long), cudaMemcpyHostToDevice);
    if (belowTotal > g_pbelowCap) {
        cudaFree(d_pbelow);
        cudaMalloc(&d_pbelow, (size_t)belowTotal * sizeof(float));
        cudaFree(d_pdcsnap);
        cudaMalloc(&d_pdcsnap, (size_t)belowTotal * sizeof(float)); // tiled backward Pass A snapshot
        g_pbelowCap = belowTotal;
    }
}

// fp_polish_forward — ONE tiled dispatch (thread-per-pixel walks all shapes). The kernel inits
// render=base per pixel, so no base->render copy and no N per-shape launches.
API void fp_polish_forward(const int* bbxHost, const double* tauPtr) {
    (void)bbxHost; // bbx is on-device (d_pbbx)
    double tau = tauPtr[0];
    long long npix = (long long)g_w * g_h;
    int blocks = (int)((npix + BLOCK - 1) / BLOCK);
    polishForwardTiled<<<blocks, BLOCK>>>(d_pbase, d_prender, d_pbelow, d_pP, d_pcol, d_pkind, d_pbbx, d_pboff, g_pN, g_w, g_h, tau, g_polishSTE);
}

API void fp_polish_loss(double* out) {
    int npix = g_w * g_h;
    int blocks = (npix + BLOCK - 1) / BLOCK;
    if (blocks > 1024) blocks = 1024;
    polishLossReduce<<<blocks, BLOCK>>>(d_prender, d_target, d_weight, npix, d_ploss_partial, g_polishOKLab);
    polishLossFinal<<<1, 1>>>(d_ploss_partial, blocks, d_ploss);
    if (g_polishFELambda > 0.0) feAccumulateLoss();
    if (g_polishSSIMLambda > 0.0) ssimAccumulateLoss();
    cudaMemcpy(out, d_ploss, sizeof(double), cudaMemcpyDeviceToHost);
}

// fp_polish_hard_loss renders all shapes with HARD coverage (the shipped deliverable)
// into d_prender and returns its weighted SSE vs target — the GPU port of polish.go
// polishHardLoss, used for best-hard tracking. Caller must upload the CURRENT (post-Adam)
// params first so d_pP/d_pcol/d_pbbx match. Clobbers d_prender (the next forward rebuilds
// it from d_pbase, and this is only called after backward, so the soft chain is unaffected).
API void fp_polish_hard_loss(const int* bbxHost, double* out) {
    (void)bbxHost; // bbx is on-device (d_pbbx)
    long long npix = (long long)g_w * g_h;
    int hblocks = (int)((npix + BLOCK - 1) / BLOCK);
    polishHardTiled<<<hblocks, BLOCK>>>(d_pbase, d_prender, d_pP, d_pcol, d_pkind, d_pbbx, g_pN, g_w, g_h);
    int npx = g_w * g_h;
    int blocks = (npx + BLOCK - 1) / BLOCK;
    if (blocks > 1024) blocks = 1024;
    polishLossReduce<<<blocks, BLOCK>>>(d_prender, d_target, d_weight, npx, d_ploss_partial, g_polishOKLab);
    polishLossFinal<<<1, 1>>>(d_ploss_partial, blocks, d_ploss);
    if (g_polishFELambda > 0.0) feAccumulateLoss();
    if (g_polishSSIMLambda > 0.0) ssimAccumulateLoss();
    cudaMemcpy(out, d_ploss, sizeof(double), cudaMemcpyDeviceToHost);
}

// fp_polish_backward — dcinit + Pass A (per-pixel reverse dC walk -> dcsnap) + Pass B (per-shape
// gradient reduce, ALL shapes in one 2D launch) + the fixed-order grad reduce. The N sequential
// per-shape backward launches (stream-ordered for the dC chain) collapse to ONE walk + ONE reduce.
API void fp_polish_backward(const int* bbxHost, const double* tauPtr) {
    (void)bbxHost; // bbx is on-device (d_pbbx)
    double tau = tauPtr[0];
    int npix = g_w * g_h;
    // gradient partials default to 0 (empty (shape,block) slots write 0 themselves, but keep the
    // memset defensive for degenerate shapes / the fixed-order sum).
    cudaMemset(d_pgrad_partial, 0, (size_t)g_pN * PMAXBLK * 10 * sizeof(double));
    const double* feAdj = nullptr;
    if (g_polishFELambda > 0.0) {
        int blocks = (npix + BLOCK - 1) / BLOCK;
        feLumaKernel<<<blocks, BLOCK>>>(d_prender, d_feRL, npix);
        int rblocks = blocks > 1024 ? 1024 : blocks;
        feDirReduce<<<rblocks, BLOCK>>>(d_feRL, d_feTL, g_w, g_h, d_feDir, d_fe_partial);
        feAdjKernel<<<blocks, BLOCK>>>(d_feDir, g_w, g_h, d_feAdj);
        feAdj = d_feAdj;
    }
    const double* ssAdjPtr = nullptr;
    if (g_polishSSIMLambda > 0.0) {
        ssimForward(true);
        int mw = g_w - SSWIN + 1, mh = g_h - SSWIN + 1;
        size_t gplane = (size_t)mw * mh;
        size_t hgplane = (size_t)g_w * mh;
        int nhg = g_w * mh;
        ssGH<<<(nhg + BLOCK - 1) / BLOCK, BLOCK>>>(d_ssG, d_ssG + gplane, d_ssG + 2 * gplane, g_w, mw, mh,
                                                   d_ssHG, d_ssHG + hgplane, d_ssHG + 2 * hgplane);
        ssAdjKernel<<<(npix + BLOCK - 1) / BLOCK, BLOCK>>>(d_ssHG, d_ssHG + hgplane, d_ssHG + 2 * hgplane,
                                                           d_ssRL, d_ssTL, g_w, g_h, mh, d_ssAdj);
        ssAdjPtr = d_ssAdj;
    }
    polishDCInit<<<(npix + BLOCK - 1) / BLOCK, BLOCK>>>(d_prender, d_target, d_weight, npix, d_pdC, g_polishOKLab, feAdj, g_polishFELambda, ssAdjPtr, g_polishSSIMLambda);
    // Pass A: one thread per pixel walks shapes in reverse, snapshotting dcsnap + propagating dC.
    int pblocks = (int)(((long long)npix + BLOCK - 1) / BLOCK);
    polishDCWalk<<<pblocks, BLOCK>>>(d_pdC, d_pdcsnap, d_pP, d_pcol, d_pkind, d_pbbx, d_pboff, g_pN, g_w, g_h, tau, g_polishSTE);
    // Pass B: 2D grid (x=block-in-shape up to PMAXBLK, y=shape) — all shapes' gradients at once.
    dim3 grd(PMAXBLK, g_pN);
    polishBackwardReduce<<<grd, BLOCK>>>(d_pbelow, d_pdcsnap, d_pP, d_pcol, d_pkind, d_pbbx, d_pboff, g_pN, g_w, tau, d_pgrad_partial, g_polishSTE);
    // Deterministic reduction: sum each shape's PMAXBLK partials in fixed block order -> pgrad.
    polishGradReduceAll<<<g_pN, 10>>>(d_pgrad_partial, d_pgrad);
}

API void fp_polish_read_grad(double* dst) {
    cudaMemcpy(dst, d_pgrad, (size_t)g_pN * 10 * sizeof(double), cudaMemcpyDeviceToHost);
}

// fp_polish_sync blocks until all queued polish kernels complete. Used by the host loop
// to attribute async GPU time to the correct phase (forward/backward) when profiling.
API void fp_polish_sync() { cudaDeviceSynchronize(); }

API void fp_polish_read_render(float* dst) {
    cudaMemcpy(dst, d_prender, (size_t)g_w * g_h * 4 * sizeof(float), cudaMemcpyDeviceToHost);
}

API void fp_polish_free() {
    cudaFree(d_pbase); cudaFree(d_prender); cudaFree(d_pdC); cudaFree(d_pbelow);
    cudaFree(d_pP); cudaFree(d_pcol); cudaFree(d_pkind); cudaFree(d_pbbx);
    cudaFree(d_pboff); cudaFree(d_pgrad); cudaFree(d_ploss);
    cudaFree(d_pgrad_partial); cudaFree(d_ploss_partial); cudaFree(d_pdcsnap);
    cudaFree(d_feTL); cudaFree(d_feRL); cudaFree(d_feDir); cudaFree(d_feAdj); cudaFree(d_fe_partial);
    d_feTL = d_feRL = nullptr; d_feDir = d_feAdj = d_fe_partial = nullptr;
    g_polishFELambda = 0.0;
    cudaFree(d_ssTL); cudaFree(d_ssRL); cudaFree(d_ssMy); cudaFree(d_ssMyy);
    cudaFree(d_ssH); cudaFree(d_ssG); cudaFree(d_ssHG); cudaFree(d_ssAdj); cudaFree(d_ss_partial);
    d_ssTL = d_ssRL = nullptr; d_ssMy = d_ssMyy = d_ssH = d_ssG = d_ssHG = d_ssAdj = d_ss_partial = nullptr;
    g_polishSSIMLambda = 0.0;
    d_pbase = d_prender = d_pdC = d_pbelow = nullptr;
    d_pP = d_pcol = d_pgrad = d_ploss = nullptr;
    d_pgrad_partial = d_ploss_partial = nullptr; d_pdcsnap = nullptr;
    d_pkind = nullptr; d_pbbx = nullptr; d_pboff = nullptr;
    g_pN = 0; g_pbelowCap = 0;
}

API void fp_apply(const float* cand) {
    int kind = (int)(cand[0] + 0.5f);
    float P[6] = { cand[1], cand[2], cand[3], cand[4], cand[5], cand[6] };
    int xMin, yMin, xMax, yMax;
    bbox(kind, P, g_w, g_h, &xMin, &yMin, &xMax, &yMax);
    cudaMemcpy(d_cands, cand, 11 * sizeof(float), cudaMemcpyHostToDevice);
    dim3 blk(16, 16);
    dim3 grd((xMax - xMin) / 16 + 1, (yMax - yMin) / 16 + 1);
    applyKernel<<<grd, blk>>>(d_cands, d_canvas, g_w, g_h, xMin, yMin, xMax, yMax);
    cudaDeviceSynchronize();
}

API void fp_error_grid(float* out) {
    gridKernel<<<g_gw * g_gh, BLOCK>>>(d_target, d_canvas, d_weight, g_w, g_h, g_gw, g_gh, d_grid);
    cudaMemcpy(out, d_grid, (size_t)g_gw * g_gh * sizeof(float), cudaMemcpyDeviceToHost);
}

API void fp_read_canvas(float* dst) {
    cudaMemcpy(dst, d_canvas, (size_t)g_w * g_h * 4 * sizeof(float), cudaMemcpyDeviceToHost);
}

API void fp_reset(const float* canvas) {
    cudaMemcpy(d_canvas, canvas, (size_t)g_w * g_h * 4 * sizeof(float), cudaMemcpyHostToDevice);
}

API void fp_free() {
    cudaFree(d_target); cudaFree(d_canvas); cudaFree(d_weight);
    cudaFree(d_cands); cudaFree(d_out); cudaFree(d_grid);
    d_target = d_canvas = d_weight = d_cands = d_out = d_grid = nullptr;
    // search scratch (build "B1")
    cudaFree(d_orient); cudaFree(d_gridcdf); cudaFree(d_kinds); cudaFree(d_kindcdf);
    cudaFree(d_scand); cudaFree(d_sout); cudaFree(d_adj);
    cudaFree(d_redVal); cudaFree(d_redIdx); cudaFree(d_bestIdx); cudaFree(d_best);
    cudaFree(d_seeds);
    d_orient = d_gridcdf = d_kinds = d_kindcdf = d_scand = d_sout = d_adj = nullptr;
    d_redVal = d_best = d_seeds = nullptr;
    d_redIdx = d_bestIdx = nullptr;
    g_searchCap = 0;
    g_momentCap = 0;
}
