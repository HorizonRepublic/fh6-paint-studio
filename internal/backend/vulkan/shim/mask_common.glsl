// Shared dictionary-mask sampling for the Vulkan shaders. The including shader must declare the
// two atlas bindings as `maskAtlas` (float[]) and `maskMeta` (int[]; slot 0 = count).
#ifndef MASK_COMMON_GLSL
#define MASK_COMMON_GLSL

// ---- dictionary-mask coverage (mirrors raster/mask.go and the CUDA shim) ----
// The bank words are captured coverage textures packed into one atlas; meta[mi*3] = {offset, w, h}.
// A word is placed by an affine (full extents Hx,Hy + skew), so the sampler inverts that placement
// and bilinearly samples the native UV.
#define MASKBASE 64
float maskSampleUV(int ofs, int mw, int mh, float u, float v) {
    if (u < 0.0 || u > 1.0 || v < 0.0 || v > 1.0) return 0.0;
    float tx = u * float(mw) - 0.5, ty = v * float(mh) - 0.5;
    int x0 = int(floor(tx)), y0 = int(floor(ty));
    float fx = tx - float(x0), fy = ty - float(y0);
    int x1 = (x0 + 1 < mw - 1) ? x0 + 1 : mw - 1;
    int y1 = (y0 + 1 < mh - 1) ? y0 + 1 : mh - 1;
    if (x1 < 0) x1 = 0;
    if (y1 < 0) y1 = 0;
    x0 = clamp(x0, 0, mw - 1);
    y0 = clamp(y0, 0, mh - 1);
    float c00 = maskAtlas[ofs + y0 * mw + x0], c10 = maskAtlas[ofs + y0 * mw + x1];
    float c01 = maskAtlas[ofs + y1 * mw + x0], c11 = maskAtlas[ofs + y1 * mw + x1];
    return (1.0 - fx) * (1.0 - fy) * c00 + fx * (1.0 - fy) * c10 + (1.0 - fx) * fy * c01 + fx * fy * c11;
}

float maskCovP(int kind, float P[6], int x, int y) {
    int mi = kind - MASKBASE;
    if (mi < 0 || mi >= maskMeta[0]) return 0.0; // meta[0] = count, entries start at 1
    float hx = P[2], hy = P[3];
    if (hx == 0.0 || hy == 0.0) return 0.0;
    float th = P[4] * 0.017453292519943295, skew = P[5];
    float c = cos(th), sn = sin(th);
    float dx = float(x) + 0.5 - P[0], dy = float(y) + 0.5 - P[1];
    float kx = dx * c + dy * sn, ky = -dx * sn + dy * c;
    float sx = kx - skew * ky;
    int b = 1 + mi * 3;
    return maskSampleUV(maskMeta[b], maskMeta[b + 1], maskMeta[b + 2], sx / hx + 0.5, ky / hy + 0.5);
}

void maskBBoxOf(float P[6], int W, int H, out int xMin, out int yMin, out int xMax, out int yMax) {
    float cx = P[0], cy = P[1], hx = P[2] * 0.5, hy = P[3] * 0.5;
    float th = P[4] * 0.017453292519943295, skew = P[5];
    float c = cos(th), sn = sin(th);
    float minX = 1e30, minY = 1e30, maxX = -1e30, maxY = -1e30;
    for (int i = 0; i < 4; i++) {
        float ex = ((i & 1) != 0) ? hx : -hx, ey = ((i & 2) != 0) ? hy : -hy;
        float kx = ex + skew * ey;
        float px = cx + kx * c - ey * sn, py = cy + kx * sn + ey * c;
        minX = min(minX, px); maxX = max(maxX, px);
        minY = min(minY, py); maxY = max(maxY, py);
    }
    xMin = clamp(int(floor(minX - 1.0)), 0, W - 1);
    xMax = clamp(int(ceil(maxX + 1.0)), 0, W - 1);
    yMin = clamp(int(floor(minY - 1.0)), 0, H - 1);
    yMax = clamp(int(ceil(maxY + 1.0)), 0, H - 1);
}

#endif
