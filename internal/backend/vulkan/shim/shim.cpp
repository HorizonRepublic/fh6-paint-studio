// shim.cpp — Vulkan compute backend for FH6 Paint Studio, compiled into fh6vk.dll.
// The Go side (internal/backend/vulkan) loads this DLL via x/sys/windows + syscall —
// no cgo — and calls these extern "C" fp_* functions. The C API names + wire formats
// MIRROR shim.cu (the CUDA backend) so the Go host templates off cuda.go:
//   candidate: [kind, p0..p5, R,G,B,A]  (11 floats)
//   result:    [score, oR,oG,oB,oA]     (5 floats)
// This MIRRORS internal/backend/cpu/cpu.go (the golden reference) via eval.comp.
//
// Phase 1 scope: core Backend (eval + apply). On-device search and joint polish
// (the optional capabilities) come in later phases. Buffers are HOST_VISIBLE|COHERENT
// for simplicity/correctness; device-local + staging is a Phase-4 perf optimisation.

#define _CRT_SECURE_NO_WARNINGS
#include "volk.h"
#include <algorithm>
#include <cmath>
#include <cstdint>
#include <cstring>
#include <cstdlib>
#include <cstdio>
#include <chrono>
#include <vector>

#include "eval.spv.h"   // unsigned int eval_spv[]  (glslangValidator --vn)
#include "apply.spv.h"  // unsigned int apply_spv[]
#include "grid.spv.h"   // unsigned int grid_spv[]
#include "gen.spv.h"     // gen_spv (on-device random candidate generator)
#include "prepadj.spv.h" // prepadj_spv
#include "argmin.spv.h"  // argmin_spv
#include "mutate.spv.h"  // mutate_spv (on-device hill-climb mutation batch)
#include "coarse_min.spv.h"    // coarse_min_spv (partition argmin over the cheap pass)
#include "coarse_gather.spv.h" // coarse_gather_spv (compact the survivors)
// fp32 variants of every REAL-typed shader, for devices without shaderFloat64 (see buildContext).
#include "eval_f32.spv.h"
#include "grid_f32.spv.h"
#include "momentseed_f32.spv.h"
#include "polish_forward_tiled_f32.spv.h"
#include "polish_hard_tiled_f32.spv.h"
#include "polish_dcinit_f32.spv.h"
#include "polish_loss_f32.spv.h"
#include "polish_dcwalk_tiled_f32.spv.h"
#include "polish_backward_reduce_f32.spv.h"
#include "polish_backward_combine_f32.spv.h"
#include "fe_dir_f32.spv.h"
#include "fe_adj_f32.spv.h"
#include "ssim_h_f32.spv.h"
#include "ssim_myinit_f32.spv.h"
#include "ssim_map_f32.spv.h"
#include "ssim_gh_f32.spv.h"
#include "ssim_adj_f32.spv.h"
#include "eagle_scharr_f32.spv.h"
#include "eagle_var_f32.spv.h"
#include "eagle_boxx_f32.spv.h"
#include "eagle_boxy_f32.spv.h"
#include "eagle_hpfin_f32.spv.h"
#include "eagle_loss_f32.spv.h"
#include "eagle_sign_f32.spv.h"
#include "eagle_um_f32.spv.h"
#include "eagle_varadj_f32.spv.h"
#include "eagle_scharradj_f32.spv.h"
#include "momentseed.spv.h" // momentseed_spv (covariance-ellipse seeds)
#include "genmoment.spv.h"  // genmoment_spv (localised pool around seeds)
#include "polish_forward_tiled.spv.h" // pt_forward_spv (one-dispatch tiled forward)
#include "polish_hard_tiled.spv.h"    // pt_hard_spv    (one-dispatch tiled hard)
#include "polish_dcwalk_tiled.spv.h"    // pt_dcwalk_spv  (tiled backward Pass A: dC reverse walk)
#include "conv3x3.spv.h"    // conv3x3_spv    (one 3x3 layer of the candidate proposer)
#include "prop_input.spv.h" // prop_input_spv (target+canvas -> the proposer's stored input planes)
#include "prop_orient.spv.h"// prop_orient_spv(target planes -> the derived orientation planes)
#include "prop_head.spv.h"  // prop_head_spv  (pooled features -> the proposal map)
#include "tile_count.spv.h"           // tb_count_spv   (shape binning: count per tile)
#include "tile_scan.spv.h"            // tb_scan_spv    (shape binning: prefix sum)
#include "tile_fill.spv.h"            // tb_fill_spv    (shape binning: fill per-tile lists)
#include "polish_backward_reduce.spv.h" // pt_breduce_spv (tiled backward Pass B: sliced per-shape reduce)
#include "polish_backward_combine.spv.h" // pt_bcombine_spv (tiled backward Pass C: slice-partial sum)
#include "polish_dcinit.spv.h"   // p_dcinit_spv
#include "polish_loss.spv.h"     // p_loss_spv
#include "fe_luma.spv.h"         // fe_luma_spv
#include "fe_dir.spv.h"          // fe_dir_spv
#include "fe_adj.spv.h"          // fe_adj_spv
#include "ssim_h.spv.h"          // ssim_h_spv
#include "ssim_myinit.spv.h"     // ssim_myinit_spv
#include "ssim_map.spv.h"        // ssim_map_spv
#include "ssim_gh.spv.h"         // ssim_gh_spv
#include "ssim_adj.spv.h"        // ssim_adj_spv
#include "eagle_scharr.spv.h"    // eagle_scharr_spv
#include "eagle_var.spv.h"       // eagle_var_spv
#include "eagle_boxx.spv.h"      // eagle_boxx_spv
#include "eagle_boxy.spv.h"      // eagle_boxy_spv
#include "eagle_hpfin.spv.h"     // eagle_hpfin_spv
#include "eagle_loss.spv.h"      // eagle_loss_spv
#include "eagle_sign.spv.h"      // eagle_sign_spv
#include "eagle_um.spv.h"        // eagle_um_spv
#include "eagle_varadj.spv.h"    // eagle_varadj_spv
#include "eagle_scharradj.spv.h" // eagle_scharradj_spv

#ifdef _WIN32
#define API extern "C" __declspec(dllexport)
#else
#define API extern "C"
#endif

// The proposer's lifetime helpers are defined below the anonymous namespace; teardown(), which
// lives inside it, has to see them.
void freeProposer();
void proposerTeardown();

namespace {

struct Buf { VkBuffer buf = VK_NULL_HANDLE; VkDeviceMemory mem = VK_NULL_HANDLE; void* map = nullptr; VkDeviceSize size = 0; };

// ---- global single device context (engine drives one backend serially) ----
VkInstance       g_instance = VK_NULL_HANDLE;
VkDebugUtilsMessengerEXT g_dbgMsgr = VK_NULL_HANDLE; // FH6VK_VALIDATE=1 only
VkPhysicalDevice g_phys     = VK_NULL_HANDLE;
VkDevice         g_device   = VK_NULL_HANDLE;
VkQueue          g_queue    = VK_NULL_HANDLE;
uint32_t         g_qfam     = 0;
VkCommandPool    g_cmdPool  = VK_NULL_HANDLE;
VkCommandBuffer  g_cmd      = VK_NULL_HANDLE;
VkFence          g_fence    = VK_NULL_HANDLE;

VkDescriptorSetLayout g_evalDSL  = VK_NULL_HANDLE;
VkDescriptorSetLayout g_applyDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_evalPL   = VK_NULL_HANDLE;
VkPipelineLayout      g_applyPL  = VK_NULL_HANDLE;
VkPipeline            g_evalPipe = VK_NULL_HANDLE;
VkPipeline            g_applyPipe = VK_NULL_HANDLE;
VkDescriptorPool      g_descPool = VK_NULL_HANDLE;
VkDescriptorSet       g_evalSet  = VK_NULL_HANDLE;
VkDescriptorSet       g_applySet = VK_NULL_HANDLE;
VkDescriptorSetLayout g_gridDSL  = VK_NULL_HANDLE;
VkPipelineLayout      g_gridPL   = VK_NULL_HANDLE;
VkPipeline            g_gridPipe = VK_NULL_HANDLE;
VkDescriptorSet       g_gridSet  = VK_NULL_HANDLE;

Buf g_target, g_weight, g_canvas, g_cands, g_out, g_staging, g_gridBuf;
int g_w = 0, g_h = 0, g_maxCands = 0, g_grid = 0;
int g_sampleBudget = 4000;
int g_lastError = 0;
// Sticky device-loss flag: once a submit or fence wait fails (TDR / driver reset / OOM kill),
// the device never recovers inside this fp_init generation. Every later submit short-circuits
// and re-arms g_lastError so ALL callers see the fault, not just the one that hit it first.
int g_fatal = 0;
int g_memBudgetExt = 0; // VK_EXT_memory_budget enabled on the device (see buildContext)
uint32_t g_maxGroupsX = 65535; // device limit on 1-D dispatch workgroups (guaranteed floor 65535)
// Measured seconds-per-round EMA for the hill-climb submit chunking (fp_search_mutate's TDR
// guard). Survives fp_init on purpose: the card does not change between runs.
double g_mutRoundCost = 0.0;

// ---- built-in GPU profiler (FH6VK_PROF=1) ----
// Every submit here is synchronous — submitWait blocks on the fence — so the host time spent
// inside submitWait IS the GPU time of that submit to within ~50us of fixed overhead. Each API
// entry names its scope; submitWait accumulates seconds + submit counts per scope. The table
// (fp_prof_dump) therefore shows real per-phase GPU seconds AND the round-trip counts that
// wall-clock profiling can only guess at. Off (the default) costs one integer compare per submit.
enum ProfScope {
    PROF_OTHER = 0, PROF_INIT, PROF_EVAL, PROF_SEARCH, PROF_MUTATE, PROF_APPLY, PROF_GRID,
    PROF_RESET, PROF_READCANVAS, PROF_PSETUP, PROF_PUPLOAD, PROF_PFWD, PROF_PLOSS, PROF_PBWD,
    PROF_PHARD, PROF_PREADRENDER, PROF_TERMSET, PROF_N
};
const char* const g_profNames[PROF_N] = {
    "other", "init", "eval", "search", "mutate", "apply", "error_grid",
    "reset", "read_canvas", "polish_setup", "polish_upload", "polish_forward", "polish_loss",
    "polish_backward", "polish_hard", "polish_read_render", "term_setup",
};
int g_profOn = -1; // resolved from FH6VK_PROF on first submit
int g_profScope = PROF_OTHER;
double g_profSec[PROF_N] = {};
long long g_profCnt[PROF_N] = {};
// fp64-vs-fp32 pipeline family (see buildContext). g_rs is sizeof(REAL) for the buffers whose
// element type follows the shaders' REAL macro.
int g_fp64 = 1;
size_t g_rs = 8;
#define RSPV(base) (g_fp64 ? base##_spv : base##_f32_spv), (g_fp64 ? sizeof(base##_spv) : sizeof(base##_f32_spv))

struct EvalPC  { int32_t n, W, H, sampleBudget; int32_t agN; float ag[6]; int32_t gradOn; int32_t first; };
// DICTIONARY MASKS: the bank words are captured coverage textures packed into one atlas, with a
// meta table {count, then (offset,w,h) per word}. Kept as plain storage buffers so every shader
// that scores or composites a word reads the same data the CUDA constant/global pair holds.
Buf g_maskAtlas, g_maskMeta;
int g_masksOn = 0; // agN/ag = analytic-alpha grid (fp_set_alpha_grid), agN=0 off; gradOn = per-pixel-alpha scoring for the radial gradients
int g_gradOn = 0;
struct ApplyPC { int32_t kind; float p0, p1, p2, p3, p4, p5; float cr, cg, cb, ca; int32_t W, H; };
struct GridPC  { int32_t W, H, gw, gh, cx0, cy0, cx1, cy1; };

// ---- incremental error grid ----
// fp_error_grid used to re-reduce the ENTIRE frame (36 B/px) once per placed shape, when fp_apply
// had only written inside one bbox. fp_apply unions a conservative host-side bbox into this dirty
// rect and the grid pass recomputes only the cells it touches; every other cell keeps its value in
// the persistent grid buffer. Conservative on purpose: the host bbox is the shader's formula
// widened by two pixels (float-library divergence headroom), bank words and anything exotic mark
// the whole frame, and a missed cell would be silent quality corruption, so err wide.
int  g_dirtyX0 = 0, g_dirtyY0 = 0, g_dirtyX1 = -1, g_dirtyY1 = -1;
bool g_dirtyFull = true;

void dirtyUnion(int x0, int y0, int x1, int y1) {
    if (g_dirtyX1 < g_dirtyX0) { g_dirtyX0 = x0; g_dirtyY0 = y0; g_dirtyX1 = x1; g_dirtyY1 = y1; return; }
    if (x0 < g_dirtyX0) g_dirtyX0 = x0;
    if (y0 < g_dirtyY0) g_dirtyY0 = y0;
    if (x1 > g_dirtyX1) g_dirtyX1 = x1;
    if (y1 > g_dirtyY1) g_dirtyY1 = y1;
}

// shapeRect computes the conservative CLAMPED pixel bbox for kinds 0..5 (a strict superset of
// the shaders' own per-kind bboxes — verified per kind against apply.comp). false = a mask kind,
// whose reach only the atlas knows: treat as whole-frame. Shared by the dirty-rect tracking and
// fp_apply's dispatch sizing.
bool shapeRect(int kind, const float* P, int& x0o, int& y0o, int& x1o, int& y1o) {
    if (kind > 5) return false;
    double minX, maxX, minY, maxY;
    if (kind == 2) {
        minX = std::min((double)P[0], std::min((double)P[2], (double)P[4]));
        maxX = std::max((double)P[0], std::max((double)P[2], (double)P[4]));
        minY = std::min((double)P[1], std::min((double)P[3], (double)P[5]));
        maxY = std::max((double)P[1], std::max((double)P[3], (double)P[5]));
    } else if (kind == 3) {
        double hw = std::max(0.5, (double)P[4]);
        minX = std::min((double)P[0], (double)P[2]) - hw; maxX = std::max((double)P[0], (double)P[2]) + hw;
        minY = std::min((double)P[1], (double)P[3]) - hw; maxY = std::max((double)P[1], (double)P[3]) + hw;
    } else {
        double cx = P[0], cy = P[1];
        double a = std::max(1.0, (double)P[2]), b = std::max(1.0, (double)P[3]);
        double sh = std::fabs((double)P[5]);
        // One formula covers rect and (sheared) ellipse conservatively: the rotated bbox of the
        // shear-widened extents is an upper bound for both shader variants.
        double th = (double)P[4] * 0.017453292519943295, c = std::fabs(std::cos(th)), s = std::fabs(std::sin(th));
        double shx = a + sh * b + std::sqrt(a * a + sh * sh * b * b);
        double ex = shx * c + b * s + 2.0, ey = shx * s + b * c + 2.0;
        minX = cx - ex; maxX = cx + ex; minY = cy - ey; maxY = cy + ey;
    }
    int x0 = (int)std::floor(minX) - 2, y0 = (int)std::floor(minY) - 2;
    int x1 = (int)std::ceil(maxX) + 2, y1 = (int)std::ceil(maxY) + 2;
    if (x0 < 0) x0 = 0;
    if (y0 < 0) y0 = 0;
    if (x1 > g_w - 1) x1 = g_w - 1;
    if (y1 > g_h - 1) y1 = g_h - 1;
    x0o = x0; y0o = y0; x1o = x1; y1o = y1;
    return true;
}

void applyDirty(int kind, const float* P) {
    if (g_dirtyFull) return;
    int x0, y0, x1, y1;
    if (!shapeRect(kind, P, x0, y0, x1, y1)) { g_dirtyFull = true; return; }
    if (x0 > x1 || y0 > y1) return;
    dirtyUnion(x0, y0, x1, y1);
}

// ---- on-device random search (fp_search_random) ----
// GenPC is capped at 120 bytes: the guaranteed maxPushConstantsSize is 128, AMD/Intel report
// exactly that, and this block once grew to 164 (proposer + coherence fields) — which failed
// fp_init on every non-NVIDIA card. The proposer scalars live in the g_genCfg SSBO instead
// (binding 11 of gen.comp); do NOT grow this struct past 32 words.
struct GenPC  { uint32_t seedLo, seedHi; int32_t n, nKinds, gw, gh, W, H, allowAlpha, hasOrient, hasBound, hasGate, hasRampGlow, bigKinds, bigKind, hasCoh;
                float maxR, alphaMin, aspectMax, boundPad, boundMix, canvasPad, glowTau, glowProb, rampThresh, rampTau, rampProb, bigTau, bigProb, aspectCap; };
// GenCfg mirrors gen.comp's binding-11 block (the proposer scalars moved out of the PC).
struct GenCfg { int32_t hasProp, propW, propH, propHeads, propOff, propConfGate;
                float propFrac, propPatch, propJitter, propStrideF, propConfTau; };
static_assert(sizeof(GenPC) <= 128, "GenPC exceeds the guaranteed push-constant limit");
struct PrepPC { int32_t n, compact, shapeCount, W, H; };
struct ArgPC  { int32_t n, keep, inlineAdj, compact, shapeCount, W, H; };
// keep != 0: the argmin leaves best[] alone unless the batch winner's raw score beats it —
// the on-device hill climb's cross-round accept rule. The one-shot searches pass 0.
struct MutPC  { uint32_t seedLo, seedHi; int32_t n, W, H, allowAlpha; float moveStep, radiusStep, alphaMin, canvasPad; };

VkDescriptorSetLayout g_genDSL = VK_NULL_HANDLE, g_prepDSL = VK_NULL_HANDLE, g_argDSL = VK_NULL_HANDLE, g_mutDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_genPL = VK_NULL_HANDLE, g_prepPL = VK_NULL_HANDLE, g_argPL = VK_NULL_HANDLE, g_mutPL = VK_NULL_HANDLE;
VkPipeline            g_genPipe = VK_NULL_HANDLE, g_prepPipe = VK_NULL_HANDLE, g_argPipe = VK_NULL_HANDLE, g_mutPipe = VK_NULL_HANDLE;
VkDescriptorSet       g_genSet = VK_NULL_HANDLE, g_sevalSet = VK_NULL_HANDLE, g_prepSet = VK_NULL_HANDLE, g_argSet = VK_NULL_HANDLE, g_mutSet = VK_NULL_HANDLE;
Buf g_scand, g_sout, g_adj, g_best, g_kindsB, g_kindcdf, g_gridcdf, g_orient, g_bound, g_kgate, g_rampglow, g_coh, g_genCfg;

// ---- coarse-to-fine filter (fp_set_coarse) ----
// Pass 1 scores the WHOLE candidate pool at a cheap pixel cap, a partition argmin keeps kpart
// survivors, and only those are re-scored at the full sample budget. The winner is always
// full-budget scored, so the filter trades nothing the argmin can see — the CUDA backend ran this
// for months as the dominant eval lever, and the shaders sat here compiled-but-unwired since the
// 2026-08-03 port note. selection uses the ADJUSTED score, matching the one-pass path.
struct CminPC { int32_t n, parts; };
struct CgatPC { int32_t k; };
VkDescriptorSetLayout g_cminDSL = VK_NULL_HANDLE, g_cgatDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_cminPL = VK_NULL_HANDLE, g_cgatPL = VK_NULL_HANDLE;
VkPipeline            g_cminPipe = VK_NULL_HANDLE, g_cgatPipe = VK_NULL_HANDLE;
VkDescriptorSet       g_cminSet = VK_NULL_HANDLE, g_cgatSet = VK_NULL_HANDLE;
VkDescriptorSet       g_seval2Set = VK_NULL_HANDLE, g_prep2Set = VK_NULL_HANDLE, g_arg2Set = VK_NULL_HANDLE;
Buf g_scand2, g_sout2, g_adj2, g_sel;
// Batch placement (fp_set_batch / fp_search_survivors): the refined survivor pool copied out to
// the host so it can place several disjoint winners per round. See cmdExportSurvivors.
Buf g_survBuf;
int g_survCap = 0, g_survN = 0, g_batchOn = 0;
int g_coarseOn = 0, g_coarseBudget = 4000, g_kpart = 2048, g_coarseCap = 0;
int g_searchCap = 0, g_hasOrient = 0, g_hasBound = 0, g_hasGate = 0, g_hasRampGlow = 0, g_hasCoh = 0;
float g_aspectCap = 0.f;
float g_glowTau = 0.f, g_glowProb = 0.f; // deep-smooth glow swap (fp_set_glow_swap)
float g_rampGlowThresh = 0.f, g_rampGlowTau = 0.f, g_rampGlowProb = 0.f; // hotter glow swap in gradient zones (fp_set_ramp_glow)
float g_bigGlowTau = 0.f, g_bigGlowProb = 0.f;                          // size-conditioned glow swap (fp_set_big_glow)
int   g_bigGlowKinds = 0, g_bigGlowKind = 4;                            // ... which source kinds it eats, and what it emits (4 glow / 5 disk)
int   g_alphaGridN = 0;                  // analytic-alpha grid size (fp_set_alpha_grid), 0 = off
float g_alphaGrid[6] = {};               // grid values (eval epilogue picks the ΔSSE-min alpha)
bool g_searchSetsDirty = true;

// ---- neural candidate proposer (fp_set_proposer) ----
// The engine's cost is dominated by exactly scoring candidates -- 96% of a polish-free run -- and it
// is linear in how many are scored. The proposer replaces a large random draw with a handful of
// learned ones. It is safe by construction: every proposal is still scored by the same exact eval,
// so a bad network costs speed and never correctness.
//
// The trunk runs ONCE over the canvas and every candidate location reads its own proposal out of the
// resulting map; a forward pass per candidate would cost more than it saves. That equivalence
// between per-patch and whole-canvas evaluation only holds because every operator is translation-
// equivariant, which is why the network carries folded BatchNorm rather than GroupNorm and was
// trained on context windows wider than its receptive field.
struct ConvPC { int32_t inC, outC, inW, inH, outW, outH, stride, act; };
struct PropInPC { int32_t w, h, ow, oh; float scale; };
struct PropOrPC { int32_t w, h; };
struct PropHeadPC { int32_t chan, fw, fh, pw, ph, heads, pool; float progress; };
struct PropLayer { int inC, outC, stride; Buf w, b; };

VkDescriptorSetLayout g_pcDSL = VK_NULL_HANDLE, g_piDSL = VK_NULL_HANDLE, g_phDSL = VK_NULL_HANDLE,
                      g_poDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_pcPL = VK_NULL_HANDLE, g_piPL = VK_NULL_HANDLE, g_phPL = VK_NULL_HANDLE,
                      g_poPL = VK_NULL_HANDLE;
VkPipeline            g_pcPipe = VK_NULL_HANDLE, g_piPipe = VK_NULL_HANDLE, g_phPipe = VK_NULL_HANDLE,
                      g_poPipe = VK_NULL_HANDLE;
VkDescriptorSet       g_piSet = VK_NULL_HANDLE, g_phSet = VK_NULL_HANDLE,
                      g_poSet = VK_NULL_HANDLE;
// ONE conv set per trunk layer. There used to be two, ping-ponged — but a descriptor set may not be
// rewritten while a command buffer that binds it is still pending, and every write happened INSIDE
// the recording. Six layers over two sets meant every dispatch read the LAST write, so layer 0 ran
// with layer 4's weights over an uninitialised buffer. The whole recorded AI-proposer verdict was
// measured on that.
std::vector<VkDescriptorSet> g_pcSets;
VkDescriptorPool g_propDPool = VK_NULL_HANDLE; // was a local: nothing ever destroyed it
std::vector<PropLayer> g_propLayers;
Buf g_propIn, g_propA, g_propB, g_propMap, g_propKinds;
Buf g_pMixW, g_pMixB, g_pGeoW, g_pGeoB, g_pAlpW, g_pAlpB, g_pCnfW, g_pCnfB;
int g_propHeads = 0, g_propPool = 8, g_propPatchSrc = 256, g_propChan = 0;
// Input planes the trunk expects, taken from the first layer's in_ch rather than assumed: 6 planes
// come straight from the target and the canvas, anything beyond that is derived on device.
int g_propInC = 6;
int g_propHasConf = 0;   // v3 blobs carry a confidence head; older ones do not
int g_propConfGate = 0;  // caller's request to gate on it (ignored when the blob has no head)
float g_propConfTau = 0.f; // how much predicted advantage the gate demands before it lets a proposal through
int g_propCtxSrc = 512, g_propInDim = 128;   // training context window and the network's input size
int g_propNW = 0, g_propNH = 0;              // the downscaled canvas the network actually sees
float g_propScale = 1.f;                     // canvas pixels per network pixel
int g_propTrainDim = 571; // short side of the canvases the network was trained on
int g_propW = 0, g_propH = 0;      // proposal-map dimensions
int g_hasProposer = 0, g_propOn = 0;
float g_propFrac = 0.5f;   // share of the batch drawn from the network; the rest stays random
float g_propJitter = 0.05f; // spread around each proposal, in patch widths (see gen.comp jit())
float g_propProgress = 0.f;

// ---- on-device moment-seeded search (fp_search_moment) ----
struct MomSeedPC { uint32_t seedLo, seedHi; int32_t K, gw, gh, W, H, hasBound; float maxR, boundPad, boundMix; };
struct GenMomPC  { uint32_t seedLo, seedHi; int32_t n, perSeed, K, nKinds, allowAlpha, W, H, hasGate, hasRampGlow, bigKinds, bigKind; float alphaMin, canvasPad, glowTau, glowProb, rampThresh, rampTau, rampProb, bigTau, bigProb; };
VkDescriptorSetLayout g_msDSL = VK_NULL_HANDLE, g_gmDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_msPL = VK_NULL_HANDLE, g_gmPL = VK_NULL_HANDLE;
VkPipeline            g_msPipe = VK_NULL_HANDLE, g_gmPipe = VK_NULL_HANDLE;
VkDescriptorSet       g_msSet = VK_NULL_HANDLE, g_gmSet = VK_NULL_HANDLE;
Buf g_seeds;
int g_momentCap = 0;

// ---- joint-polish state (built lazily by fp_polish_setup, freed by fp_polish_free) ----
struct PolishPC { int32_t shapeIdx, w, h, xMin, yMin, xMax, yMax, boff, ste, npix; float tau; int32_t oklab; float feLambda; float ssimLambda; float eagleLambda; float ldLambda; };
const int PLOSS_GROUPS = 64; // loss reduction workgroups (host sums the partials)

VkDescriptorSetLayout g_pDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_pPL  = VK_NULL_HANDLE;
VkPipeline g_pDcinit = VK_NULL_HANDLE, g_pLoss = VK_NULL_HANDLE; // shared-DSL pipelines (forward/hard/backward are tiled, below)
VkDescriptorPool g_pPool = VK_NULL_HANDLE;
VkDescriptorSet  g_pSet  = VK_NULL_HANDLE;
Buf g_pbase, g_prender, g_pbelow, g_pdC, g_pP, g_pcol, g_pkinds, g_ppgrad, g_ppartials, g_pbwPart;
int g_pn = 0, g_pste = 0, g_poklab = 0;
bool g_pkindsUp = false; // kinds are invariant per setup; uploaded on the first upload only
VkDeviceSize g_belowCap = 0;

// ---- false-edge additive polish term (mirrors engine/falseedge.go + shim.cu): its own small
// DSL (0=src4 1=targetLuma 2=reconLuma 3=dir 4=adj 5=partials) with two sets — setT computes the
// fixed target-luma plane once at set-lambda, setR runs per evaluation on the current render. ----
struct FePC { int32_t w, h; float feLambda; int32_t hasTW; float ldLambda; };
double g_pfelambda = 0.0;
// Lost-detail lambda (lostdetail.go): rides the FE passes, see fe_dir.comp.
double g_pldlambda = 0.0;
Buf g_feTL, g_feRL, g_feDir, g_feAdj, g_feParts;
VkDescriptorSetLayout g_feDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_fePL  = VK_NULL_HANDLE;
VkPipeline g_feLumaP = VK_NULL_HANDLE, g_feDirP = VK_NULL_HANDLE, g_feAdjP = VK_NULL_HANDLE;
VkDescriptorPool g_fePool = VK_NULL_HANDLE;
VkDescriptorSet  g_feSetR = VK_NULL_HANDLE, g_feSetT = VK_NULL_HANDLE;
const int FE_GROUPS = 64;

// ---- SSIM additive polish term (mirrors engine/ssimterm.go + shim.cu): its own DSL
// (0=src4 1=targetLuma 2=lumaOut 3=H[3 planes] 4=MY[2] 5=G[3] 6=HG[3] 7=adj 8=partials), two
// sets — setT runs the target through luma+h-pass+moments once at set-lambda (binding 2 = the
// target-luma plane there), setR runs per evaluation on the current render. The luma pipe is
// the fe_luma shader rebuilt against this layout (bindings 0/2 line up; PC is prefix-compatible). ----
struct SsimPC { int32_t w, h, mw, mh, writeG; float lambda; };
double g_psslambda = 0.0;
// EAGLE additive polish term (engine eagleterm.go / shim.cu eg*): 18-binding DSL shared by 10
// small pipelines; partials host-visible (lambda*sum added host-side like FE/SSIM).
struct EagPC { int32_t w, h, dir, mode; float lambda; int32_t hasTW; };
double g_peglambda = 0.0;
Buf g_ssTL, g_ssRL, g_ssH, g_ssMY, g_ssG, g_ssHG, g_ssAdj, g_ssParts;
Buf g_egTL, g_egRL, g_egTHx, g_egTHy, g_egGx, g_egGy, g_egVx, g_egVy, g_egMx, g_egMy;
Buf g_egHx, g_egHy, g_egT1, g_egT2, g_egSx, g_egSy, g_egAdj, g_egParts;
Buf g_termW;            // per-pixel FE/EAGLE term weight (fp_set_term_weight); read when hasTW
int g_hasTermW = 0;
bool ensureTermW();
VkDescriptorSetLayout g_egDSL = VK_NULL_HANDLE;
VkPipelineLayout g_egPL = VK_NULL_HANDLE;
VkDescriptorPool g_egPool = VK_NULL_HANDLE;
VkDescriptorSet g_egSetR = VK_NULL_HANDLE, g_egSetT = VK_NULL_HANDLE;
VkPipeline g_egLumaP = VK_NULL_HANDLE, g_egScharrP = VK_NULL_HANDLE, g_egVarP = VK_NULL_HANDLE,
           g_egBoxXP = VK_NULL_HANDLE, g_egBoxYP = VK_NULL_HANDLE, g_egHpP = VK_NULL_HANDLE,
           g_egLossP = VK_NULL_HANDLE, g_egSignP = VK_NULL_HANDLE, g_egUmP = VK_NULL_HANDLE,
           g_egVarAdjP = VK_NULL_HANDLE, g_egScharrAdjP = VK_NULL_HANDLE;
const int EG_GROUPS = 64;
VkDescriptorSetLayout g_ssDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_ssPL  = VK_NULL_HANDLE;
VkPipeline g_ssLumaP = VK_NULL_HANDLE, g_ssHP = VK_NULL_HANDLE, g_ssMyP = VK_NULL_HANDLE,
           g_ssMapP = VK_NULL_HANDLE, g_ssGHP = VK_NULL_HANDLE, g_ssAdjP = VK_NULL_HANDLE;
VkDescriptorPool g_ssPool = VK_NULL_HANDLE;
VkDescriptorSet  g_ssSetR = VK_NULL_HANDLE, g_ssSetT = VK_NULL_HANDLE;
const int SS_GROUPS = 64;
const int SSWIN = 8;

// ---- tiled polish forward/hard: ONE dispatch, no barriers (its own DSL/PL/pool/set so it
// never touches the shared 10-binding per-shape polish set). 8 bindings:
// 0=P 1=col 2=kinds 3=render 4=below 5=bbx 6=boff 7=base. bbx/boff live on-device. ----
// slices: dispatch height of the backward reduce — the per-shape slice cap (see
// polish_backward_reduce.comp). The forward/hard/walk shaders declare only the prefix.
struct TiledPC { int32_t n, w, h, ste; float tau; int32_t tilesX, tile, binned; int32_t slices, first; };
// PBSLICES bounds one shape's reduce slices. 64 keeps the worst canvas-sized bbox near the
// measured 4096 px/workgroup sweet spot without drowning small stacks in empty workgroups.
constexpr int32_t PBSLICES = 64;
// MUST match polish_backward_reduce.comp's SLICE_PX (and combine's copy of it).
constexpr int32_t PBSLICE_PX = 4096;
// Widest sliceCount over the current boxes, recomputed on every fp_polish_upload. See there.
int g_pMaxSlices = PBSLICES;
// SHAPE BINNING (mirrors the CUDA shim). The polish passes are thread-per-pixel and would
// otherwise walk the whole shape list at every pixel just to bbox-test it away. Binning once per
// upload leaves each pixel with the handful that can reach its tile; the list is ascending, so the
// composite order is untouched. Cap exceeded -> binned=0 and the shaders fall back to a full scan.
#define PTILE 32
struct TilePC { int32_t n, tilesX, nTiles, tile, cap; };
VkDescriptorSetLayout g_tbDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_tbPL  = VK_NULL_HANDLE;
VkPipeline g_tbCount = VK_NULL_HANDLE, g_tbScan = VK_NULL_HANDLE, g_tbFill = VK_NULL_HANDLE;
VkDescriptorPool g_tbPool = VK_NULL_HANDLE;
VkDescriptorSet  g_tbSet  = VK_NULL_HANDLE;
Buf g_tileCount, g_tileOff, g_tileList;
int g_tilesX = 0, g_tilesY = 0, g_nTiles = 0, g_binned = 0;
VkDeviceSize g_tileListCap = 0;
VkDescriptorSetLayout g_ptDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_ptPL  = VK_NULL_HANDLE;
VkPipeline g_ptFwd = VK_NULL_HANDLE, g_ptHard = VK_NULL_HANDLE;
VkDescriptorPool g_ptPool = VK_NULL_HANDLE;
VkDescriptorSet  g_ptSet  = VK_NULL_HANDLE;
Buf g_pbbxBuf, g_pboffBuf; // device-local n*4 int / n int (int32)

// ---- tiled polish backward: dcinit (shared DSL) + Pass A (per-pixel reverse dC walk) +
// Pass B (per-shape gradient reduce, N workgroups in one dispatch). 9-binding DSL:
// 0=P 1=col 2=kinds 3=dC 4=below 5=dcsnap 6=pgrad 7=bbx 8=boff. ZERO per-shape barriers. ----
VkDescriptorSetLayout g_pbDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_pbPL  = VK_NULL_HANDLE;
VkPipeline g_pbWalk = VK_NULL_HANDLE, g_pbReduce = VK_NULL_HANDLE, g_pbCombine = VK_NULL_HANDLE;
VkDescriptorPool g_pbPool = VK_NULL_HANDLE;
VkDescriptorSet  g_pbSet  = VK_NULL_HANDLE;
Buf g_pdcsnap; // device-local, sized == g_pbelow (per pixel-shape dC snapshot)

uint32_t findMemType(uint32_t bits, VkMemoryPropertyFlags want) {
    VkPhysicalDeviceMemoryProperties mp;
    vkGetPhysicalDeviceMemoryProperties(g_phys, &mp);
    for (uint32_t i = 0; i < mp.memoryTypeCount; i++)
        if ((bits & (1u << i)) && (mp.memoryTypes[i].propertyFlags & want) == want) return i;
    return UINT32_MAX;
}

const VkMemoryPropertyFlags HOSTVIS = VK_MEMORY_PROPERTY_HOST_VISIBLE_BIT | VK_MEMORY_PROPERTY_HOST_COHERENT_BIT;

void destroyBuf(Buf& b);

bool createBufEx(VkDeviceSize size, VkBufferUsageFlags usage, VkMemoryPropertyFlags props, bool doMap, Buf& b) {
    // b.size is committed only on SUCCESS. A failure that left {buf=NULL, size=N} behind made
    // "need <= b.size" caches (ensureStaging, g_belowCap) believe the allocation exists, which
    // turned a survivable OOM into null-descriptor writes and host null-pointer memcpys.
    b.size = 0;
    VkBufferCreateInfo bci{VK_STRUCTURE_TYPE_BUFFER_CREATE_INFO};
    bci.size = size; bci.usage = usage; bci.sharingMode = VK_SHARING_MODE_EXCLUSIVE;
    if (vkCreateBuffer(g_device, &bci, nullptr, &b.buf) != VK_SUCCESS) return false;
    VkMemoryRequirements mr; vkGetBufferMemoryRequirements(g_device, b.buf, &mr);
    uint32_t mt = findMemType(mr.memoryTypeBits, props);
    if (mt == UINT32_MAX) { destroyBuf(b); return false; }
    VkMemoryAllocateInfo mai{VK_STRUCTURE_TYPE_MEMORY_ALLOCATE_INFO};
    mai.allocationSize = mr.size; mai.memoryTypeIndex = mt;
    if (vkAllocateMemory(g_device, &mai, nullptr, &b.mem) != VK_SUCCESS) { destroyBuf(b); return false; }
    if (vkBindBufferMemory(g_device, b.buf, b.mem, 0) != VK_SUCCESS) { destroyBuf(b); return false; }
    if (doMap && vkMapMemory(g_device, b.mem, 0, size, 0, &b.map) != VK_SUCCESS) { destroyBuf(b); return false; }
    b.size = size;
    return true;
}

// host-visible coherent storage buffer (CPU-accessible: cands/out/staging)
bool createHost(VkDeviceSize size, Buf& b) {
    return createBufEx(size, VK_BUFFER_USAGE_STORAGE_BUFFER_BIT | VK_BUFFER_USAGE_TRANSFER_SRC_BIT | VK_BUFFER_USAGE_TRANSFER_DST_BIT, HOSTVIS, true, b);
}

void destroyBuf(Buf& b) {
    if (b.map) { vkUnmapMemory(g_device, b.mem); b.map = nullptr; }
    if (b.buf) { vkDestroyBuffer(g_device, b.buf, nullptr); b.buf = VK_NULL_HANDLE; }
    if (b.mem) { vkFreeMemory(g_device, b.mem, nullptr); b.mem = VK_NULL_HANDLE; }
    b.size = 0;
}

VkShaderModule loadShader(const unsigned int* code, size_t bytes) {
    VkShaderModuleCreateInfo ci{VK_STRUCTURE_TYPE_SHADER_MODULE_CREATE_INFO};
    ci.codeSize = bytes; ci.pCode = code;
    VkShaderModule m = VK_NULL_HANDLE;
    vkCreateShaderModule(g_device, &ci, nullptr, &m);
    return m;
}

void polishTeardown() {
    if (g_device) vkDeviceWaitIdle(g_device);
    destroyBuf(g_pbase); destroyBuf(g_prender); destroyBuf(g_pbelow); destroyBuf(g_pdC);
    destroyBuf(g_tileCount); destroyBuf(g_tileOff); destroyBuf(g_tileList);
    g_tileListCap = 0; g_binned = 0; g_nTiles = 0;
    if (g_tbCount) { vkDestroyPipeline(g_device, g_tbCount, nullptr); g_tbCount = VK_NULL_HANDLE; }
    if (g_tbScan)  { vkDestroyPipeline(g_device, g_tbScan, nullptr);  g_tbScan = VK_NULL_HANDLE; }
    if (g_tbFill)  { vkDestroyPipeline(g_device, g_tbFill, nullptr);  g_tbFill = VK_NULL_HANDLE; }
    if (g_tbPool)  { vkDestroyDescriptorPool(g_device, g_tbPool, nullptr); g_tbPool = VK_NULL_HANDLE; g_tbSet = VK_NULL_HANDLE; }
    if (g_tbPL)    { vkDestroyPipelineLayout(g_device, g_tbPL, nullptr); g_tbPL = VK_NULL_HANDLE; }
    if (g_tbDSL)   { vkDestroyDescriptorSetLayout(g_device, g_tbDSL, nullptr); g_tbDSL = VK_NULL_HANDLE; }
    destroyBuf(g_pP); destroyBuf(g_pcol); destroyBuf(g_pkinds); destroyBuf(g_ppgrad); destroyBuf(g_ppartials);
    destroyBuf(g_pbwPart);
    if (g_pDcinit) { vkDestroyPipeline(g_device, g_pDcinit, nullptr); g_pDcinit = VK_NULL_HANDLE; }
    if (g_pLoss)   { vkDestroyPipeline(g_device, g_pLoss, nullptr);   g_pLoss = VK_NULL_HANDLE; }
    if (g_pPool)   { vkDestroyDescriptorPool(g_device, g_pPool, nullptr); g_pPool = VK_NULL_HANDLE; g_pSet = VK_NULL_HANDLE; }
    if (g_pPL)     { vkDestroyPipelineLayout(g_device, g_pPL, nullptr); g_pPL = VK_NULL_HANDLE; }
    if (g_pDSL)    { vkDestroyDescriptorSetLayout(g_device, g_pDSL, nullptr); g_pDSL = VK_NULL_HANDLE; }
    destroyBuf(g_pbbxBuf); destroyBuf(g_pboffBuf);
    if (g_ptFwd)  { vkDestroyPipeline(g_device, g_ptFwd, nullptr);  g_ptFwd = VK_NULL_HANDLE; }
    if (g_ptHard) { vkDestroyPipeline(g_device, g_ptHard, nullptr); g_ptHard = VK_NULL_HANDLE; }
    if (g_ptPool) { vkDestroyDescriptorPool(g_device, g_ptPool, nullptr); g_ptPool = VK_NULL_HANDLE; g_ptSet = VK_NULL_HANDLE; }
    if (g_ptPL)   { vkDestroyPipelineLayout(g_device, g_ptPL, nullptr); g_ptPL = VK_NULL_HANDLE; }
    if (g_ptDSL)  { vkDestroyDescriptorSetLayout(g_device, g_ptDSL, nullptr); g_ptDSL = VK_NULL_HANDLE; }
    destroyBuf(g_pdcsnap);
    if (g_pbWalk)   { vkDestroyPipeline(g_device, g_pbWalk, nullptr);   g_pbWalk = VK_NULL_HANDLE; }
    if (g_pbReduce) { vkDestroyPipeline(g_device, g_pbReduce, nullptr); g_pbReduce = VK_NULL_HANDLE; }
    if (g_pbCombine){ vkDestroyPipeline(g_device, g_pbCombine, nullptr); g_pbCombine = VK_NULL_HANDLE; }
    if (g_pbPool)   { vkDestroyDescriptorPool(g_device, g_pbPool, nullptr); g_pbPool = VK_NULL_HANDLE; g_pbSet = VK_NULL_HANDLE; }
    if (g_pbPL)     { vkDestroyPipelineLayout(g_device, g_pbPL, nullptr); g_pbPL = VK_NULL_HANDLE; }
    if (g_pbDSL)    { vkDestroyDescriptorSetLayout(g_device, g_pbDSL, nullptr); g_pbDSL = VK_NULL_HANDLE; }
    destroyBuf(g_feTL); destroyBuf(g_feRL); destroyBuf(g_feDir); destroyBuf(g_feAdj); destroyBuf(g_feParts);
    if (g_feLumaP) { vkDestroyPipeline(g_device, g_feLumaP, nullptr); g_feLumaP = VK_NULL_HANDLE; }
    if (g_feDirP)  { vkDestroyPipeline(g_device, g_feDirP, nullptr);  g_feDirP = VK_NULL_HANDLE; }
    if (g_feAdjP)  { vkDestroyPipeline(g_device, g_feAdjP, nullptr);  g_feAdjP = VK_NULL_HANDLE; }
    if (g_fePool)  { vkDestroyDescriptorPool(g_device, g_fePool, nullptr); g_fePool = VK_NULL_HANDLE; g_feSetR = g_feSetT = VK_NULL_HANDLE; }
    if (g_fePL)    { vkDestroyPipelineLayout(g_device, g_fePL, nullptr); g_fePL = VK_NULL_HANDLE; }
    if (g_feDSL)   { vkDestroyDescriptorSetLayout(g_device, g_feDSL, nullptr); g_feDSL = VK_NULL_HANDLE; }
    g_pfelambda = 0.0;
    g_pldlambda = 0.0;
    destroyBuf(g_ssTL); destroyBuf(g_ssRL); destroyBuf(g_ssH); destroyBuf(g_ssMY);
    destroyBuf(g_ssG); destroyBuf(g_ssHG); destroyBuf(g_ssAdj); destroyBuf(g_ssParts);
    if (g_ssLumaP) { vkDestroyPipeline(g_device, g_ssLumaP, nullptr); g_ssLumaP = VK_NULL_HANDLE; }
    if (g_ssHP)    { vkDestroyPipeline(g_device, g_ssHP, nullptr);    g_ssHP = VK_NULL_HANDLE; }
    if (g_ssMyP)   { vkDestroyPipeline(g_device, g_ssMyP, nullptr);   g_ssMyP = VK_NULL_HANDLE; }
    if (g_ssMapP)  { vkDestroyPipeline(g_device, g_ssMapP, nullptr);  g_ssMapP = VK_NULL_HANDLE; }
    if (g_ssGHP)   { vkDestroyPipeline(g_device, g_ssGHP, nullptr);   g_ssGHP = VK_NULL_HANDLE; }
    if (g_ssAdjP)  { vkDestroyPipeline(g_device, g_ssAdjP, nullptr);  g_ssAdjP = VK_NULL_HANDLE; }
    if (g_ssPool)  { vkDestroyDescriptorPool(g_device, g_ssPool, nullptr); g_ssPool = VK_NULL_HANDLE; g_ssSetR = g_ssSetT = VK_NULL_HANDLE; }
    if (g_ssPL)    { vkDestroyPipelineLayout(g_device, g_ssPL, nullptr); g_ssPL = VK_NULL_HANDLE; }
    if (g_ssDSL)   { vkDestroyDescriptorSetLayout(g_device, g_ssDSL, nullptr); g_ssDSL = VK_NULL_HANDLE; }
    g_psslambda = 0.0;
    destroyBuf(g_egTL); destroyBuf(g_egRL); destroyBuf(g_egTHx); destroyBuf(g_egTHy);
    destroyBuf(g_egGx); destroyBuf(g_egGy); destroyBuf(g_egVx); destroyBuf(g_egVy);
    destroyBuf(g_egMx); destroyBuf(g_egMy); destroyBuf(g_egHx); destroyBuf(g_egHy);
    destroyBuf(g_egT1); destroyBuf(g_egT2); destroyBuf(g_egSx); destroyBuf(g_egSy);
    destroyBuf(g_egAdj); destroyBuf(g_egParts);
    destroyBuf(g_termW); g_hasTermW = 0;
    VkPipeline* egp[11] = { &g_egLumaP, &g_egScharrP, &g_egVarP, &g_egBoxXP, &g_egBoxYP, &g_egHpP,
                            &g_egLossP, &g_egSignP, &g_egUmP, &g_egVarAdjP, &g_egScharrAdjP };
    for (int i = 0; i < 11; i++) if (*egp[i]) { vkDestroyPipeline(g_device, *egp[i], nullptr); *egp[i] = VK_NULL_HANDLE; }
    if (g_egPool) { vkDestroyDescriptorPool(g_device, g_egPool, nullptr); g_egPool = VK_NULL_HANDLE; g_egSetR = g_egSetT = VK_NULL_HANDLE; }
    if (g_egPL)   { vkDestroyPipelineLayout(g_device, g_egPL, nullptr); g_egPL = VK_NULL_HANDLE; }
    if (g_egDSL)  { vkDestroyDescriptorSetLayout(g_device, g_egDSL, nullptr); g_egDSL = VK_NULL_HANDLE; }
    g_peglambda = 0.0;
    g_pn = 0; g_belowCap = 0; g_pMaxSlices = PBSLICES;
}

void teardown() {
    if (g_device) vkDeviceWaitIdle(g_device);
    polishTeardown();
    // The proposer owned four pipelines, four layouts, four DSLs and a descriptor pool, and NONE of
    // them were destroyed — they outlived vkDestroyDevice below, so the second run in a process
    // bound handles from a dead device.
    freeProposer();
    proposerTeardown();
    destroyBuf(g_target); destroyBuf(g_weight); destroyBuf(g_canvas); destroyBuf(g_cands); destroyBuf(g_out); destroyBuf(g_staging); destroyBuf(g_gridBuf);
    if (g_evalPipe)  { vkDestroyPipeline(g_device, g_evalPipe, nullptr);  g_evalPipe = VK_NULL_HANDLE; }
    if (g_applyPipe) { vkDestroyPipeline(g_device, g_applyPipe, nullptr); g_applyPipe = VK_NULL_HANDLE; }
    if (g_gridPipe)  { vkDestroyPipeline(g_device, g_gridPipe, nullptr);  g_gridPipe = VK_NULL_HANDLE; }
    if (g_gridPL)    { vkDestroyPipelineLayout(g_device, g_gridPL, nullptr); g_gridPL = VK_NULL_HANDLE; }
    if (g_gridDSL)   { vkDestroyDescriptorSetLayout(g_device, g_gridDSL, nullptr); g_gridDSL = VK_NULL_HANDLE; }
    destroyBuf(g_scand); destroyBuf(g_sout); destroyBuf(g_adj); destroyBuf(g_best);
    destroyBuf(g_kindsB); destroyBuf(g_kindcdf); destroyBuf(g_gridcdf); destroyBuf(g_orient); destroyBuf(g_bound); destroyBuf(g_kgate); destroyBuf(g_rampglow); destroyBuf(g_coh); destroyBuf(g_genCfg);
    if (g_genPipe)  { vkDestroyPipeline(g_device, g_genPipe, nullptr);  g_genPipe = VK_NULL_HANDLE; }
    if (g_prepPipe) { vkDestroyPipeline(g_device, g_prepPipe, nullptr); g_prepPipe = VK_NULL_HANDLE; }
    if (g_argPipe)  { vkDestroyPipeline(g_device, g_argPipe, nullptr);  g_argPipe = VK_NULL_HANDLE; }
    if (g_mutPipe)  { vkDestroyPipeline(g_device, g_mutPipe, nullptr);  g_mutPipe = VK_NULL_HANDLE; }
    destroyBuf(g_scand2); destroyBuf(g_sout2); destroyBuf(g_adj2); destroyBuf(g_sel);
    destroyBuf(g_survBuf); g_survCap = 0; g_survN = 0;
    if (g_cminPipe) { vkDestroyPipeline(g_device, g_cminPipe, nullptr); g_cminPipe = VK_NULL_HANDLE; }
    if (g_cgatPipe) { vkDestroyPipeline(g_device, g_cgatPipe, nullptr); g_cgatPipe = VK_NULL_HANDLE; }
    if (g_cminPL)   { vkDestroyPipelineLayout(g_device, g_cminPL, nullptr); g_cminPL = VK_NULL_HANDLE; }
    if (g_cgatPL)   { vkDestroyPipelineLayout(g_device, g_cgatPL, nullptr); g_cgatPL = VK_NULL_HANDLE; }
    if (g_cminDSL)  { vkDestroyDescriptorSetLayout(g_device, g_cminDSL, nullptr); g_cminDSL = VK_NULL_HANDLE; }
    if (g_cgatDSL)  { vkDestroyDescriptorSetLayout(g_device, g_cgatDSL, nullptr); g_cgatDSL = VK_NULL_HANDLE; }
    g_coarseOn = 0; g_coarseBudget = 4000; g_kpart = 2048; g_coarseCap = 0;
    if (g_genPL)  { vkDestroyPipelineLayout(g_device, g_genPL, nullptr);  g_genPL = VK_NULL_HANDLE; }
    if (g_prepPL) { vkDestroyPipelineLayout(g_device, g_prepPL, nullptr); g_prepPL = VK_NULL_HANDLE; }
    if (g_argPL)  { vkDestroyPipelineLayout(g_device, g_argPL, nullptr);  g_argPL = VK_NULL_HANDLE; }
    if (g_mutPL)  { vkDestroyPipelineLayout(g_device, g_mutPL, nullptr);  g_mutPL = VK_NULL_HANDLE; }
    if (g_genDSL)  { vkDestroyDescriptorSetLayout(g_device, g_genDSL, nullptr);  g_genDSL = VK_NULL_HANDLE; }
    if (g_prepDSL) { vkDestroyDescriptorSetLayout(g_device, g_prepDSL, nullptr); g_prepDSL = VK_NULL_HANDLE; }
    if (g_argDSL)  { vkDestroyDescriptorSetLayout(g_device, g_argDSL, nullptr);  g_argDSL = VK_NULL_HANDLE; }
    if (g_mutDSL)  { vkDestroyDescriptorSetLayout(g_device, g_mutDSL, nullptr);  g_mutDSL = VK_NULL_HANDLE; }
    destroyBuf(g_seeds);
    if (g_msPipe) { vkDestroyPipeline(g_device, g_msPipe, nullptr); g_msPipe = VK_NULL_HANDLE; }
    if (g_gmPipe) { vkDestroyPipeline(g_device, g_gmPipe, nullptr); g_gmPipe = VK_NULL_HANDLE; }
    if (g_msPL) { vkDestroyPipelineLayout(g_device, g_msPL, nullptr); g_msPL = VK_NULL_HANDLE; }
    if (g_gmPL) { vkDestroyPipelineLayout(g_device, g_gmPL, nullptr); g_gmPL = VK_NULL_HANDLE; }
    if (g_msDSL) { vkDestroyDescriptorSetLayout(g_device, g_msDSL, nullptr); g_msDSL = VK_NULL_HANDLE; }
    if (g_gmDSL) { vkDestroyDescriptorSetLayout(g_device, g_gmDSL, nullptr); g_gmDSL = VK_NULL_HANDLE; }
    g_searchCap = 0; g_hasOrient = 0; g_hasBound = 0; g_hasGate = 0; g_hasCoh = 0; g_aspectCap = 0.f; g_glowTau = 0.f; g_glowProb = 0.f; g_hasRampGlow = 0; g_rampGlowThresh = g_rampGlowTau = g_rampGlowProb = 0.f; g_bigGlowTau = g_bigGlowProb = 0.f; g_bigGlowKinds = 0; g_bigGlowKind = 4; g_alphaGridN = 0; g_searchSetsDirty = true; g_momentCap = 0;
    if (g_evalPL)    { vkDestroyPipelineLayout(g_device, g_evalPL, nullptr);  g_evalPL = VK_NULL_HANDLE; }
    if (g_applyPL)   { vkDestroyPipelineLayout(g_device, g_applyPL, nullptr); g_applyPL = VK_NULL_HANDLE; }
    if (g_descPool)  { vkDestroyDescriptorPool(g_device, g_descPool, nullptr); g_descPool = VK_NULL_HANDLE; g_evalSet = g_applySet = VK_NULL_HANDLE; }
    if (g_evalDSL)   { vkDestroyDescriptorSetLayout(g_device, g_evalDSL, nullptr);  g_evalDSL = VK_NULL_HANDLE; }
    if (g_applyDSL)  { vkDestroyDescriptorSetLayout(g_device, g_applyDSL, nullptr); g_applyDSL = VK_NULL_HANDLE; }
    if (g_fence)     { vkDestroyFence(g_device, g_fence, nullptr); g_fence = VK_NULL_HANDLE; }
    if (g_cmdPool)   { vkDestroyCommandPool(g_device, g_cmdPool, nullptr); g_cmdPool = VK_NULL_HANDLE; g_cmd = VK_NULL_HANDLE; }
    if (g_device)    { vkDestroyDevice(g_device, nullptr); g_device = VK_NULL_HANDLE; }
    if (g_dbgMsgr)   { vkDestroyDebugUtilsMessengerEXT(g_instance, g_dbgMsgr, nullptr); g_dbgMsgr = VK_NULL_HANDLE; }
    if (g_instance)  { vkDestroyInstance(g_instance, nullptr); g_instance = VK_NULL_HANDLE; }
    g_phys = VK_NULL_HANDLE; g_queue = VK_NULL_HANDLE;
}

// pickDevice: FH6VK_DEVICE env overrides; else prefer a discrete GPU; else index 0.
VkPhysicalDevice pickDevice(const std::vector<VkPhysicalDevice>& devs) {
    const char* env = getenv("FH6VK_DEVICE");
    if (env && *env) {
        int idx = atoi(env);
        if (idx >= 0 && idx < (int)devs.size()) return devs[idx];
    }
    for (auto d : devs) {
        VkPhysicalDeviceProperties p; vkGetPhysicalDeviceProperties(d, &p);
        if (p.deviceType == VK_PHYSICAL_DEVICE_TYPE_DISCRETE_GPU) return d;
    }
    return devs[0];
}

bool buildContext() {
    if (volkInitialize() != VK_SUCCESS) { g_lastError = 1001; return false; }
    VkApplicationInfo ai{VK_STRUCTURE_TYPE_APPLICATION_INFO}; ai.apiVersion = VK_API_VERSION_1_2;
    VkInstanceCreateInfo ici{VK_STRUCTURE_TYPE_INSTANCE_CREATE_INFO}; ici.pApplicationInfo = &ai;
    // FH6VK_VALIDATE=1 turns on VK_LAYER_KHRONOS_validation + VK_EXT_debug_utils and prints every
    // message to stderr. Off by default and only enabled when the layer is actually present, so a
    // machine without the SDK behaves exactly as before. This is the only way a descriptor-set or
    // synchronisation mistake in a shader path shows up as a diagnostic rather than as silent
    // garbage in the output.
    const char* vEnv = getenv("FH6VK_VALIDATE");
    const char* vLayer = "VK_LAYER_KHRONOS_validation";
    const char* vExt = VK_EXT_DEBUG_UTILS_EXTENSION_NAME;
    bool wantVal = vEnv && vEnv[0] == '1';
    if (wantVal) {
        uint32_t nl = 0; vkEnumerateInstanceLayerProperties(&nl, nullptr);
        std::vector<VkLayerProperties> lp(nl ? nl : 1);
        if (nl) vkEnumerateInstanceLayerProperties(&nl, lp.data());
        bool have = false;
        for (uint32_t i = 0; i < nl; i++) if (!strcmp(lp[i].layerName, vLayer)) { have = true; break; }
        if (have) {
            ici.enabledLayerCount = 1; ici.ppEnabledLayerNames = &vLayer;
            ici.enabledExtensionCount = 1; ici.ppEnabledExtensionNames = &vExt;
        } else {
            fprintf(stderr, "fh6vk: FH6VK_VALIDATE=1 but %s is not installed\n", vLayer);
            wantVal = false;
        }
    }
    if (vkCreateInstance(&ici, nullptr, &g_instance) != VK_SUCCESS) { g_lastError = 1002; return false; }
    volkLoadInstance(g_instance);
    if (wantVal && vkCreateDebugUtilsMessengerEXT) {
        VkDebugUtilsMessengerCreateInfoEXT dci{VK_STRUCTURE_TYPE_DEBUG_UTILS_MESSENGER_CREATE_INFO_EXT};
        dci.messageSeverity = VK_DEBUG_UTILS_MESSAGE_SEVERITY_WARNING_BIT_EXT | VK_DEBUG_UTILS_MESSAGE_SEVERITY_ERROR_BIT_EXT;
        dci.messageType = VK_DEBUG_UTILS_MESSAGE_TYPE_GENERAL_BIT_EXT | VK_DEBUG_UTILS_MESSAGE_TYPE_VALIDATION_BIT_EXT |
                          VK_DEBUG_UTILS_MESSAGE_TYPE_PERFORMANCE_BIT_EXT;
        dci.pfnUserCallback = [](VkDebugUtilsMessageSeverityFlagBitsEXT, VkDebugUtilsMessageTypeFlagsEXT,
                                 const VkDebugUtilsMessengerCallbackDataEXT* d, void*) -> VkBool32 {
            fprintf(stderr, "fh6vk/validation: %s\n", d && d->pMessage ? d->pMessage : "(null)");
            return VK_FALSE;
        };
        vkCreateDebugUtilsMessengerEXT(g_instance, &dci, nullptr, &g_dbgMsgr);
    }

    uint32_t nd = 0; vkEnumeratePhysicalDevices(g_instance, &nd, nullptr);
    if (!nd) { g_lastError = 1003; return false; }
    std::vector<VkPhysicalDevice> devs(nd); vkEnumeratePhysicalDevices(g_instance, &nd, devs.data());
    g_phys = pickDevice(devs);

    uint32_t qn = 0; vkGetPhysicalDeviceQueueFamilyProperties(g_phys, &qn, nullptr);
    std::vector<VkQueueFamilyProperties> qf(qn); vkGetPhysicalDeviceQueueFamilyProperties(g_phys, &qn, qf.data());
    g_qfam = UINT32_MAX;
    for (uint32_t i = 0; i < qn; i++) if (qf[i].queueFlags & VK_QUEUE_COMPUTE_BIT) { g_qfam = i; break; }
    if (g_qfam == UINT32_MAX) { g_lastError = 1004; return false; }

    float prio = 1.0f;
    VkDeviceQueueCreateInfo qci{VK_STRUCTURE_TYPE_DEVICE_QUEUE_CREATE_INFO};
    qci.queueFamilyIndex = g_qfam; qci.queueCount = 1; qci.pQueuePriorities = &prio;
    // fp64 is preferred, not required. Requesting shaderFloat64 unconditionally made
    // vkCreateDevice fail outright on iGPUs that do not expose it — the app would not start at
    // all. Every REAL-typed shader now compiles in an fp32 variant too; a device without fp64
    // gets those pipelines and slightly different rounding instead of a startup error.
    // FH6VK_FORCE_FP32=1 selects the fp32 pipelines on capable hardware for testing.
    VkPhysicalDeviceFeatures have{};
    vkGetPhysicalDeviceFeatures(g_phys, &have);
    VkPhysicalDeviceProperties props{};
    vkGetPhysicalDeviceProperties(g_phys, &props);
    g_maxGroupsX = props.limits.maxComputeWorkGroupCount[0];
    g_fp64 = have.shaderFloat64 == VK_TRUE ? 1 : 0;
    const char* ffp = getenv("FH6VK_FORCE_FP32");
    if (ffp && *ffp == '1') g_fp64 = 0;
    g_rs = g_fp64 ? 8 : 4;
    VkPhysicalDeviceFeatures feats{};
    feats.shaderFloat64 = g_fp64 ? VK_TRUE : VK_FALSE;
    // VK_EXT_memory_budget (when the driver has it) powers fp_mem_info: live per-heap budget and
    // usage, which the engine's polish VRAM ladder reads before committing ~1.5GB of allocations.
    g_memBudgetExt = 0;
    const char* devExts[1]; uint32_t devExtN = 0;
    uint32_t extn = 0; vkEnumerateDeviceExtensionProperties(g_phys, nullptr, &extn, nullptr);
    if (extn) {
        std::vector<VkExtensionProperties> eps(extn);
        vkEnumerateDeviceExtensionProperties(g_phys, nullptr, &extn, eps.data());
        for (uint32_t i = 0; i < extn; i++) {
            if (!strcmp(eps[i].extensionName, "VK_EXT_memory_budget")) {
                devExts[devExtN++] = "VK_EXT_memory_budget"; g_memBudgetExt = 1; break;
            }
        }
    }
    VkDeviceCreateInfo dci{VK_STRUCTURE_TYPE_DEVICE_CREATE_INFO};
    dci.queueCreateInfoCount = 1; dci.pQueueCreateInfos = &qci; dci.pEnabledFeatures = &feats;
    dci.enabledExtensionCount = devExtN; dci.ppEnabledExtensionNames = devExtN ? devExts : nullptr;
    if (vkCreateDevice(g_phys, &dci, nullptr, &g_device) != VK_SUCCESS) { g_lastError = 1005; return false; }
    volkLoadDevice(g_device);
    vkGetDeviceQueue(g_device, g_qfam, 0, &g_queue);

    VkCommandPoolCreateInfo cpci{VK_STRUCTURE_TYPE_COMMAND_POOL_CREATE_INFO};
    cpci.flags = VK_COMMAND_POOL_CREATE_RESET_COMMAND_BUFFER_BIT; cpci.queueFamilyIndex = g_qfam;
    if (vkCreateCommandPool(g_device, &cpci, nullptr, &g_cmdPool) != VK_SUCCESS) { g_lastError = 1006; return false; }
    VkCommandBufferAllocateInfo cbai{VK_STRUCTURE_TYPE_COMMAND_BUFFER_ALLOCATE_INFO};
    cbai.commandPool = g_cmdPool; cbai.level = VK_COMMAND_BUFFER_LEVEL_PRIMARY; cbai.commandBufferCount = 1;
    if (vkAllocateCommandBuffers(g_device, &cbai, &g_cmd) != VK_SUCCESS) { g_lastError = 1007; return false; }
    VkFenceCreateInfo fci{VK_STRUCTURE_TYPE_FENCE_CREATE_INFO};
    if (vkCreateFence(g_device, &fci, nullptr, &g_fence) != VK_SUCCESS) { g_lastError = 1008; return false; }

    // descriptor set layouts: eval = 5 storage buffers, apply = 1
    auto makeDSL = [](uint32_t count, VkDescriptorSetLayout& dsl) -> bool {
        std::vector<VkDescriptorSetLayoutBinding> bs(count);
        for (uint32_t i = 0; i < count; i++) {
            bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
            bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
        }
        VkDescriptorSetLayoutCreateInfo ci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
        ci.bindingCount = count; ci.pBindings = bs.data();
        return vkCreateDescriptorSetLayout(g_device, &ci, nullptr, &dsl) == VK_SUCCESS;
    };
    if (!makeDSL(7, g_evalDSL) || !makeDSL(3, g_applyDSL)) { g_lastError = 1009; return false; }

    auto makePL = [](VkDescriptorSetLayout dsl, uint32_t pcSize, VkPipelineLayout& pl) -> bool {
        VkPushConstantRange pr{VK_SHADER_STAGE_COMPUTE_BIT, 0, pcSize};
        VkPipelineLayoutCreateInfo ci{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO};
        ci.setLayoutCount = 1; ci.pSetLayouts = &dsl; ci.pushConstantRangeCount = 1; ci.pPushConstantRanges = &pr;
        return vkCreatePipelineLayout(g_device, &ci, nullptr, &pl) == VK_SUCCESS;
    };
    if (!makePL(g_evalDSL, sizeof(EvalPC), g_evalPL) || !makePL(g_applyDSL, sizeof(ApplyPC), g_applyPL)) { g_lastError = 1010; return false; }

    auto makePipe = [](VkPipelineLayout pl, const unsigned int* spv, size_t bytes, VkPipeline& pipe) -> bool {
        VkShaderModule sm = loadShader(spv, bytes);
        if (!sm) return false;
        VkComputePipelineCreateInfo ci{VK_STRUCTURE_TYPE_COMPUTE_PIPELINE_CREATE_INFO};
        ci.stage.sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO;
        ci.stage.stage = VK_SHADER_STAGE_COMPUTE_BIT; ci.stage.module = sm; ci.stage.pName = "main";
        ci.layout = pl;
        VkResult r = vkCreateComputePipelines(g_device, VK_NULL_HANDLE, 1, &ci, nullptr, &pipe);
        vkDestroyShaderModule(g_device, sm, nullptr);
        return r == VK_SUCCESS;
    };
    if (!makePipe(g_evalPL, RSPV(eval), g_evalPipe) ||
        !makePipe(g_applyPL, apply_spv, sizeof(apply_spv), g_applyPipe)) { g_lastError = 1011; return false; }
    // grid: error-grid reduction (4 storage buffers: target, canvas, weight, grid)
    if (!makeDSL(4, g_gridDSL)) { g_lastError = 1009; return false; }
    if (!makePL(g_gridDSL, sizeof(GridPC), g_gridPL)) { g_lastError = 1010; return false; }
    if (!makePipe(g_gridPL, RSPV(grid), g_gridPipe)) { g_lastError = 1011; return false; }
    // on-device search: gen (12 bindings — 11 data + the GenCfg SSBO), prepadj (3), argmin (4),
    // mutate (2); eval is reused for scoring.
    if (!makeDSL(12, g_genDSL) || !makeDSL(3, g_prepDSL) || !makeDSL(4, g_argDSL) || !makeDSL(2, g_mutDSL)) { g_lastError = 1009; return false; }
    if (!makePL(g_genDSL, sizeof(GenPC), g_genPL) || !makePL(g_prepDSL, sizeof(PrepPC), g_prepPL) || !makePL(g_argDSL, sizeof(ArgPC), g_argPL) || !makePL(g_mutDSL, sizeof(MutPC), g_mutPL)) { g_lastError = 1010; return false; }
    if (!makePipe(g_genPL, gen_spv, sizeof(gen_spv), g_genPipe) || !makePipe(g_prepPL, prepadj_spv, sizeof(prepadj_spv), g_prepPipe) || !makePipe(g_argPL, argmin_spv, sizeof(argmin_spv), g_argPipe) || !makePipe(g_mutPL, mutate_spv, sizeof(mutate_spv), g_mutPipe)) { g_lastError = 1011; return false; }
    // coarse-to-fine filter: coarse_min (2 bindings), coarse_gather (3); pass 2 reuses eval/prepadj/argmin.
    if (!makeDSL(2, g_cminDSL) || !makeDSL(3, g_cgatDSL)) { g_lastError = 1009; return false; }
    if (!makePL(g_cminDSL, sizeof(CminPC), g_cminPL) || !makePL(g_cgatDSL, sizeof(CgatPC), g_cgatPL)) { g_lastError = 1010; return false; }
    if (!makePipe(g_cminPL, coarse_min_spv, sizeof(coarse_min_spv), g_cminPipe) || !makePipe(g_cgatPL, coarse_gather_spv, sizeof(coarse_gather_spv), g_cgatPipe)) { g_lastError = 1011; return false; }
    // moment search: momentseed (3 bindings), genmoment (6 bindings: +rampGlow); eval/prepadj/argmin reused.
    if (!makeDSL(3, g_msDSL) || !makeDSL(6, g_gmDSL)) { g_lastError = 1009; return false; }
    if (!makePL(g_msDSL, sizeof(MomSeedPC), g_msPL) || !makePL(g_gmDSL, sizeof(GenMomPC), g_gmPL)) { g_lastError = 1010; return false; }
    if (!makePipe(g_msPL, RSPV(momentseed), g_msPipe) || !makePipe(g_gmPL, genmoment_spv, sizeof(genmoment_spv), g_gmPipe)) { g_lastError = 1011; return false; }

    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 96}; // + the two mask-atlas bindings on eval (x3 sets) and apply
    VkDescriptorPoolCreateInfo dpci{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO};
    dpci.maxSets = 15; dpci.poolSizeCount = 1; dpci.pPoolSizes = &ps;
    if (vkCreateDescriptorPool(g_device, &dpci, nullptr, &g_descPool) != VK_SUCCESS) { g_lastError = 1012; return false; }
    VkDescriptorSetAllocateInfo e{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
    e.descriptorPool = g_descPool; e.descriptorSetCount = 1; e.pSetLayouts = &g_evalDSL;
    if (vkAllocateDescriptorSets(g_device, &e, &g_evalSet) != VK_SUCCESS) { g_lastError = 1013; return false; }
    VkDescriptorSetAllocateInfo a{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
    a.descriptorPool = g_descPool; a.descriptorSetCount = 1; a.pSetLayouts = &g_applyDSL;
    if (vkAllocateDescriptorSets(g_device, &a, &g_applySet) != VK_SUCCESS) { g_lastError = 1014; return false; }
    VkDescriptorSetAllocateInfo gr{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
    gr.descriptorPool = g_descPool; gr.descriptorSetCount = 1; gr.pSetLayouts = &g_gridDSL;
    if (vkAllocateDescriptorSets(g_device, &gr, &g_gridSet) != VK_SUCCESS) { g_lastError = 1014; return false; }
    // search sets: gen, search-eval (reuses the eval DSL), prepadj, argmin, momentseed, genmoment,
    // mutate, plus the coarse filter's five (cmin, cgather, and the pass-2 eval/prep/argmin views).
    VkDescriptorSetLayout sl[12] = { g_genDSL, g_evalDSL, g_prepDSL, g_argDSL, g_msDSL, g_gmDSL, g_mutDSL,
                                     g_cminDSL, g_cgatDSL, g_evalDSL, g_prepDSL, g_argDSL };
    VkDescriptorSet* sd[12] = { &g_genSet, &g_sevalSet, &g_prepSet, &g_argSet, &g_msSet, &g_gmSet, &g_mutSet,
                                &g_cminSet, &g_cgatSet, &g_seval2Set, &g_prep2Set, &g_arg2Set };
    for (int i = 0; i < 12; i++) {
        VkDescriptorSetAllocateInfo si{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
        si.descriptorPool = g_descPool; si.descriptorSetCount = 1; si.pSetLayouts = &sl[i];
        if (vkAllocateDescriptorSets(g_device, &si, sd[i]) != VK_SUCCESS) { g_lastError = 1014; return false; }
    }
    return true;
}

void writeDescriptors() {
    auto wr = [](VkDescriptorSet set, uint32_t binding, VkBuffer buf, VkDeviceSize size, VkWriteDescriptorSet& w, VkDescriptorBufferInfo& bi) {
        bi = {buf, 0, size};
        w = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w.dstSet = set; w.dstBinding = binding; w.descriptorCount = 1;
        w.descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w.pBufferInfo = &bi;
    };
    VkWriteDescriptorSet ws[16]; VkDescriptorBufferInfo bis[16];
    wr(g_evalSet, 0, g_cands.buf,  g_cands.size,  ws[0], bis[0]);
    wr(g_evalSet, 1, g_target.buf, g_target.size, ws[1], bis[1]);
    wr(g_evalSet, 2, g_canvas.buf, g_canvas.size, ws[2], bis[2]);
    wr(g_evalSet, 3, g_weight.buf, g_weight.size, ws[3], bis[3]);
    wr(g_evalSet, 4, g_out.buf,    g_out.size,    ws[4], bis[4]);
    wr(g_applySet, 0, g_canvas.buf, g_canvas.size, ws[5], bis[5]);
    wr(g_evalSet,  5, g_maskAtlas.buf, g_maskAtlas.size, ws[10], bis[10]);
    wr(g_evalSet,  6, g_maskMeta.buf,  g_maskMeta.size,  ws[11], bis[11]);
    wr(g_applySet, 1, g_maskAtlas.buf, g_maskAtlas.size, ws[12], bis[12]);
    wr(g_applySet, 2, g_maskMeta.buf,  g_maskMeta.size,  ws[13], bis[13]);
    wr(g_gridSet, 0, g_target.buf,  g_target.size,  ws[6], bis[6]);
    wr(g_gridSet, 1, g_canvas.buf,  g_canvas.size,  ws[7], bis[7]);
    wr(g_gridSet, 2, g_weight.buf,  g_weight.size,  ws[8], bis[8]);
    wr(g_gridSet, 3, g_gridBuf.buf, g_gridBuf.size, ws[9], bis[9]);
    vkUpdateDescriptorSets(g_device, 14, ws, 0, nullptr);
}

// submitWait records nothing itself — caller fills g_cmd; this submits + waits the fence.
void submitWait() {
    // A lost device (TDR/driver reset) never comes back: skip the driver entirely and keep
    // g_lastError armed so EVERY caller's post-call check reports the fault, not just the first.
    if (g_fatal) { if (!g_lastError) g_lastError = 1052; return; }
    if (g_profOn < 0) { const char* e = getenv("FH6VK_PROF"); g_profOn = (e && e[0] == '1') ? 1 : 0; }
    std::chrono::steady_clock::time_point t0;
    if (g_profOn) t0 = std::chrono::steady_clock::now();
    VkSubmitInfo si{VK_STRUCTURE_TYPE_SUBMIT_INFO}; si.commandBufferCount = 1; si.pCommandBuffers = &g_cmd;
    vkResetFences(g_device, 1, &g_fence);
    // Record submit/fence failure so the device-error check after Evaluate can
    // see a VK_ERROR_DEVICE_LOST — otherwise a stale g_out returns as valid.
    if (vkQueueSubmit(g_queue, 1, &si, g_fence) != VK_SUCCESS) { g_lastError = 1050; g_fatal = 1; return; }
    if (vkWaitForFences(g_device, 1, &g_fence, VK_TRUE, UINT64_MAX) != VK_SUCCESS) { g_lastError = 1051; g_fatal = 1; }
    if (g_profOn) {
        g_profSec[g_profScope] += std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        g_profCnt[g_profScope]++;
    }
}

// barrier making this dispatch's shader writes available to later shader reads, the
// host, AND a transfer (canvas readback after apply copies via the staging buffer).
void flushBarrier() {
    VkMemoryBarrier mb{VK_STRUCTURE_TYPE_MEMORY_BARRIER};
    mb.srcAccessMask = VK_ACCESS_SHADER_WRITE_BIT;
    mb.dstAccessMask = VK_ACCESS_SHADER_READ_BIT | VK_ACCESS_HOST_READ_BIT | VK_ACCESS_TRANSFER_READ_BIT;
    vkCmdPipelineBarrier(g_cmd, VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT,
        VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT | VK_PIPELINE_STAGE_HOST_BIT | VK_PIPELINE_STAGE_TRANSFER_BIT,
        0, 1, &mb, 0, nullptr, 0, nullptr);
}

// copyBuf transfers between buffers (staging <-> device-local) via a one-time submit.
// A trailing barrier makes the transfer write available to later shader reads + the host.
void copyBuf(VkBuffer src, VkBuffer dst, VkDeviceSize size) {
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    VkBufferCopy bc{0, 0, size};
    vkCmdCopyBuffer(g_cmd, src, dst, 1, &bc);
    VkMemoryBarrier mb{VK_STRUCTURE_TYPE_MEMORY_BARRIER};
    mb.srcAccessMask = VK_ACCESS_TRANSFER_WRITE_BIT;
    mb.dstAccessMask = VK_ACCESS_SHADER_READ_BIT | VK_ACCESS_HOST_READ_BIT;
    vkCmdPipelineBarrier(g_cmd, VK_PIPELINE_STAGE_TRANSFER_BIT,
        VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT | VK_PIPELINE_STAGE_HOST_BIT, 0, 1, &mb, 0, nullptr, 0, nullptr);
    vkEndCommandBuffer(g_cmd);
    submitWait();
}

// copyBufBatch transfers several staging regions in ONE submit. A submit costs a fence wait, and the
// upload paths here move five or six small, independent buffers in a row — the transfers themselves
// are microseconds and the round trips are not. Callers pack the payloads into the shared staging
// buffer at distinct offsets (stageAlign) and hand over the list. Same bytes, same destinations, so
// device state afterwards is identical to the one-at-a-time form.
struct StageCopy {
    VkDeviceSize srcOff;
    VkBuffer dst;
    VkDeviceSize size;
};

// stageAlign rounds a staging offset up. Copy offsets only need 4-byte alignment for these buffers,
// but keeping regions on 256-byte lines also stops two payloads sharing a cache line while the host
// writes them.
inline VkDeviceSize stageAlign(VkDeviceSize v) { return (v + 255) & ~(VkDeviceSize)255; }

void copyBufBatch(const StageCopy* c, int n) {
    if (n <= 0) return;
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    for (int i = 0; i < n; i++) {
        if (c[i].size == 0) continue;
        VkBufferCopy bc{c[i].srcOff, 0, c[i].size};
        vkCmdCopyBuffer(g_cmd, g_staging.buf, c[i].dst, 1, &bc);
    }
    VkMemoryBarrier mb{VK_STRUCTURE_TYPE_MEMORY_BARRIER};
    mb.srcAccessMask = VK_ACCESS_TRANSFER_WRITE_BIT;
    mb.dstAccessMask = VK_ACCESS_SHADER_READ_BIT | VK_ACCESS_HOST_READ_BIT;
    vkCmdPipelineBarrier(g_cmd, VK_PIPELINE_STAGE_TRANSFER_BIT,
        VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT | VK_PIPELINE_STAGE_HOST_BIT, 0, 1, &mb, 0, nullptr, 0, nullptr);
    vkEndCommandBuffer(g_cmd);
    submitWait();
}

// cmdStageCopies records staging->device copies plus the transfer->compute barrier into the OPEN
// command buffer, so an upload rides the same submit as the work that consumes it. copyBufBatch
// (above) already merged five fences into one; this removes that one too on the per-shape search
// path — on WDDM a submit+fence is 50-200us, and there were ~5 per placed shape.
void cmdStageCopies(const StageCopy* c, int n) {
    for (int i = 0; i < n; i++) {
        if (c[i].size == 0) continue;
        VkBufferCopy bc{c[i].srcOff, 0, c[i].size};
        vkCmdCopyBuffer(g_cmd, g_staging.buf, c[i].dst, 1, &bc);
    }
    VkMemoryBarrier mb{VK_STRUCTURE_TYPE_MEMORY_BARRIER};
    mb.srcAccessMask = VK_ACCESS_TRANSFER_WRITE_BIT;
    mb.dstAccessMask = VK_ACCESS_SHADER_READ_BIT;
    vkCmdPipelineBarrier(g_cmd, VK_PIPELINE_STAGE_TRANSFER_BIT,
        VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT, 0, 1, &mb, 0, nullptr, 0, nullptr);
}

// in-cmd barrier: this dispatch's shader writes available to the next dispatch's reads
// AND writes (RAW + WAR over render/dC during the sequential per-shape passes).
void cmdBarrierRW() {
    VkMemoryBarrier mb{VK_STRUCTURE_TYPE_MEMORY_BARRIER};
    mb.srcAccessMask = VK_ACCESS_SHADER_WRITE_BIT;
    mb.dstAccessMask = VK_ACCESS_SHADER_READ_BIT | VK_ACCESS_SHADER_WRITE_BIT;
    vkCmdPipelineBarrier(g_cmd, VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT, VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT,
        0, 1, &mb, 0, nullptr, 0, nullptr);
}

void beginCmd() {
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
}

void endSubmit() {
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
}

// ---- TDR chunking for the eval dispatch ----
// Windows resets the GPU when ONE submit runs past ~2s. eval runs one workgroup per candidate —
// up to 65535 of them, each scoring up to sampleBudget pixels — so a weak card crosses the
// watchdog inside a single search and the run dies mid-way (the "freezes then quits" report).
// The workgroups are independent and each writes only outb[gid*5..+4], so splitting the range
// across submits changes the fence count and NOTHING else: same commands, same reads, same
// writes, byte-identical result. g_evalCandCost is a seconds-per-candidate EMA; when it says the
// whole pool fits in one 250ms submit the callers record the search exactly as they always did,
// in ONE submit, so a fast card pays nothing.
double g_evalCandCost = 0.0;
int g_evalChunkForce = -1; // FH6VK_EVALCHUNK=<k>: pin the chunk size. A fast card never splits on
                           // its own, so this is how the split path gets exercised and proved
                           // byte-identical against the unsplit one.

int chunkForce() {
    if (g_evalChunkForce < 0) {
        const char* e = getenv("FH6VK_EVALCHUNK");
        g_evalChunkForce = e ? atoi(e) : 0;
        if (g_evalChunkForce < 0) g_evalChunkForce = 0;
    }
    return g_evalChunkForce;
}

int evalChunk(int n) {
    if (chunkForce() > 0) return g_evalChunkForce < n ? g_evalChunkForce : n;
    if (g_evalCandCost > 0.0) {
        double c = 0.25 / g_evalCandCost;
        if (c >= (double)n) return n;
        return c < 1.0 ? 1 : (int)c;
    }
    return n > 8192 ? 8192 : n; // first call of the process: probe before the cost is known
}

void evalCost(double dt, int n) {
    if (dt <= 0.0 || n < 1 || g_fatal) return;
    double per = dt / (double)n;
    g_evalCandCost = g_evalCandCost > 0.0 ? 0.7 * g_evalCandCost + 0.3 * per : per;
}

// cmdEvalRange records eval over candidates [first, first+count) against the given set.
void cmdEvalRange(VkDescriptorSet set, EvalPC pc, int first, int count) {
    pc.first = first;
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_evalPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_evalPL, 0, 1, &set, 0, nullptr);
    vkCmdPushConstants(g_cmd, g_evalPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
    vkCmdDispatch(g_cmd, (uint32_t)count, 1, 1);
}

// evalChunked scores the whole pool in submits of its own. Only for the split path — the
// unsplit path records cmdEvalRange straight into the caller's open buffer.
void evalChunked(VkDescriptorSet set, EvalPC pc, int n, int chunk) {
    for (int f = 0; f < n && !g_fatal; f += chunk) {
        int cnt = n - f < chunk ? n - f : chunk;
        beginCmd();
        cmdEvalRange(set, pc, f, cnt);
        endSubmit();
    }
}

// ---- the same chunking for the polish passes ----
// forward / hard / dcwalk are one INDEPENDENT thread per pixel and the backward reduce is a grid
// of independent (shape, slice) workgroups, so a range split is byte-identical here too. It also
// puts a ceiling under the 65535 workgroup limit, which a 4096x4096 fit crosses on its own:
// (4096*4096 + 255) / 256 == 65536.
enum { PCH_FWD = 0, PCH_HARD, PCH_WALK, PCH_REDUCE, PCH_N };
double g_polishCost[PCH_N] = {0, 0, 0, 0}; // seconds per workgroup, per pass

int polishChunk(int which, int wg) {
    int cap = wg < 65535 ? wg : 65535;
    if (chunkForce() > 0) return g_evalChunkForce < cap ? g_evalChunkForce : cap;
    double per = g_polishCost[which];
    if (per > 0.0) {
        double k = 0.25 / per;
        if (k < (double)cap) return k < 1.0 ? 1 : (int)k;
        return cap;
    }
    return cap > 4096 ? 4096 : cap; // first call of the process: probe before the cost is known
}

void polishCost(int which, double dt, int wg) {
    if (dt <= 0.0 || wg < 1 || g_fatal) return;
    double per = dt / (double)wg;
    g_polishCost[which] = g_polishCost[which] > 0.0 ? 0.7 * g_polishCost[which] + 0.3 * per : per;
}

bool makePolishPipe(const unsigned int* spv, size_t bytes, VkPipeline& pipe) {
    VkShaderModule sm = loadShader(spv, bytes);
    if (!sm) return false;
    VkComputePipelineCreateInfo ci{VK_STRUCTURE_TYPE_COMPUTE_PIPELINE_CREATE_INFO};
    ci.stage.sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO;
    ci.stage.stage = VK_SHADER_STAGE_COMPUTE_BIT; ci.stage.module = sm; ci.stage.pName = "main";
    ci.layout = g_pPL;
    VkResult r = vkCreateComputePipelines(g_device, VK_NULL_HANDLE, 1, &ci, nullptr, &pipe);
    vkDestroyShaderModule(g_device, sm, nullptr);
    return r == VK_SUCCESS;
}

// buildPolishPipelines: one DSL (10 storage buffers) + one pipeline layout (PolishPC push)
// for the dcinit + loss pipelines (full-image, shared-DSL). The forward/hard/backward passes
// are tiled and own their layouts (buildTiledForward / buildBackwardTiled).
bool buildPolishPipelines() {
    VkDescriptorSetLayoutBinding bs[13];
    for (uint32_t i = 0; i < 13; i++) {
        bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
        bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
    }
    VkDescriptorSetLayoutCreateInfo dci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
    dci.bindingCount = 13; dci.pBindings = bs;
    if (vkCreateDescriptorSetLayout(g_device, &dci, nullptr, &g_pDSL) != VK_SUCCESS) return false;
    VkPushConstantRange pr{VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(PolishPC)};
    VkPipelineLayoutCreateInfo plci{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO};
    plci.setLayoutCount = 1; plci.pSetLayouts = &g_pDSL; plci.pushConstantRangeCount = 1; plci.pPushConstantRanges = &pr;
    if (vkCreatePipelineLayout(g_device, &plci, nullptr, &g_pPL) != VK_SUCCESS) return false;
    if (!makePolishPipe(RSPV(p_dcinit), g_pDcinit)) return false;
    if (!makePolishPipe(RSPV(p_loss), g_pLoss)) return false;
    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 13};
    VkDescriptorPoolCreateInfo dpci{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO};
    dpci.maxSets = 1; dpci.poolSizeCount = 1; dpci.pPoolSizes = &ps;
    if (vkCreateDescriptorPool(g_device, &dpci, nullptr, &g_pPool) != VK_SUCCESS) return false;
    VkDescriptorSetAllocateInfo a{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
    a.descriptorPool = g_pPool; a.descriptorSetCount = 1; a.pSetLayouts = &g_pDSL;
    return vkAllocateDescriptorSets(g_device, &a, &g_pSet) == VK_SUCCESS;
}

void writePolishDescriptors() {
    VkBuffer bufs[13] = { g_pP.buf, g_pcol.buf, g_pkinds.buf, g_prender.buf, g_pbelow.buf,
                          g_pdC.buf, g_ppgrad.buf, g_ppartials.buf, g_target.buf, g_weight.buf,
                          g_feAdj.buf, g_ssAdj.buf, g_egAdj.buf };
    VkDeviceSize sizes[13] = { g_pP.size, g_pcol.size, g_pkinds.size, g_prender.size, g_pbelow.size,
                               g_pdC.size, g_ppgrad.size, g_ppartials.size, g_target.size, g_weight.size,
                               g_feAdj.size, g_ssAdj.size, g_egAdj.size };
    VkWriteDescriptorSet w[13]; VkDescriptorBufferInfo bi[13];
    for (uint32_t i = 0; i < 13; i++) {
        bi[i] = {bufs[i], 0, sizes[i]};
        w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w[i].dstSet = g_pSet; w[i].dstBinding = i; w[i].descriptorCount = 1;
        w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
    }
    vkUpdateDescriptorSets(g_device, 13, w, 0, nullptr);
}

// buildFE: the false-edge DSL/pipelines/sets (see the FE globals comment). Built lazily on the
// first non-zero set-lambda, after fp_polish_setup (g_prender must exist for the descriptor write).
bool buildFE() {
    VkDescriptorSetLayoutBinding bs[7];
    for (uint32_t i = 0; i < 7; i++) {
        bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
        bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
    }
    VkDescriptorSetLayoutCreateInfo dci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
    dci.bindingCount = 7; dci.pBindings = bs;
    if (vkCreateDescriptorSetLayout(g_device, &dci, nullptr, &g_feDSL) != VK_SUCCESS) return false;
    VkPushConstantRange pr{VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(FePC)};
    VkPipelineLayoutCreateInfo plci{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO};
    plci.setLayoutCount = 1; plci.pSetLayouts = &g_feDSL; plci.pushConstantRangeCount = 1; plci.pPushConstantRanges = &pr;
    if (vkCreatePipelineLayout(g_device, &plci, nullptr, &g_fePL) != VK_SUCCESS) return false;
    auto mk = [](const unsigned int* spv, size_t bytes, VkPipeline& pipe) -> bool {
        VkShaderModule sm = loadShader(spv, bytes);
        if (!sm) return false;
        VkComputePipelineCreateInfo ci{VK_STRUCTURE_TYPE_COMPUTE_PIPELINE_CREATE_INFO};
        ci.stage.sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO;
        ci.stage.stage = VK_SHADER_STAGE_COMPUTE_BIT; ci.stage.module = sm; ci.stage.pName = "main";
        ci.layout = g_fePL;
        VkResult r = vkCreateComputePipelines(g_device, VK_NULL_HANDLE, 1, &ci, nullptr, &pipe);
        vkDestroyShaderModule(g_device, sm, nullptr);
        return r == VK_SUCCESS;
    };
    if (!mk(fe_luma_spv, sizeof(fe_luma_spv), g_feLumaP)) return false;
    if (!mk(RSPV(fe_dir), g_feDirP)) return false;
    if (!mk(RSPV(fe_adj), g_feAdjP)) return false;
    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 14};
    VkDescriptorPoolCreateInfo dpci{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO};
    dpci.maxSets = 2; dpci.poolSizeCount = 1; dpci.pPoolSizes = &ps;
    if (vkCreateDescriptorPool(g_device, &dpci, nullptr, &g_fePool) != VK_SUCCESS) return false;
    VkDescriptorSetLayout layouts[2] = { g_feDSL, g_feDSL };
    VkDescriptorSetAllocateInfo a{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
    a.descriptorPool = g_fePool; a.descriptorSetCount = 2; a.pSetLayouts = layouts;
    VkDescriptorSet sets[2];
    if (vkAllocateDescriptorSets(g_device, &a, sets) != VK_SUCCESS) return false;
    g_feSetR = sets[0]; g_feSetT = sets[1];
    return true;
}

void writeFEDescriptors() {
    auto wr = [](VkDescriptorSet set, VkBuffer src, VkDeviceSize srcSz) {
        VkBuffer bufs[7] = { src, g_feTL.buf, g_feRL.buf, g_feDir.buf, g_feAdj.buf, g_feParts.buf, g_termW.buf };
        VkDeviceSize sizes[7] = { srcSz, g_feTL.size, g_feRL.size, g_feDir.size, g_feAdj.size, g_feParts.size, g_termW.size };
        VkWriteDescriptorSet w[7]; VkDescriptorBufferInfo bi[7];
        for (uint32_t i = 0; i < 7; i++) {
            bi[i] = {bufs[i], 0, sizes[i]};
            w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
            w[i].dstSet = set; w[i].dstBinding = i; w[i].descriptorCount = 1;
            w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
        }
        vkUpdateDescriptorSets(g_device, 7, w, 0, nullptr);
    };
    wr(g_feSetR, g_prender.buf, g_prender.size);
    // setT routes the TARGET through the luma pipe to fill g_feTL once; binding 2 (the luma
    // output) points at g_feTL here instead of the recon scratch.
    VkBuffer bufs[7] = { g_target.buf, g_feTL.buf, g_feTL.buf, g_feDir.buf, g_feAdj.buf, g_feParts.buf, g_termW.buf };
    VkDeviceSize sizes[7] = { g_target.size, g_feTL.size, g_feTL.size, g_feDir.size, g_feAdj.size, g_feParts.size, g_termW.size };
    VkWriteDescriptorSet w[7]; VkDescriptorBufferInfo bi[7];
    for (uint32_t i = 0; i < 7; i++) {
        bi[i] = {bufs[i], 0, sizes[i]};
        w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w[i].dstSet = g_feSetT; w[i].dstBinding = i; w[i].descriptorCount = 1;
        w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
    }
    vkUpdateDescriptorSets(g_device, 7, w, 0, nullptr);
}

// cmdFEPasses records luma(render)+dir(+adj when forBackward) into the OPEN command buffer.
void cmdFEPasses(bool forBackward) {
    FePC fpc{ g_w, g_h, (float)g_pfelambda, g_hasTermW, (float)g_pldlambda };
    uint32_t pixGroups = (uint32_t)(((size_t)g_w * g_h + 255) / 256);
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_feLumaP);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_fePL, 0, 1, &g_feSetR, 0, nullptr);
    vkCmdPushConstants(g_cmd, g_fePL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(fpc), &fpc);
    vkCmdDispatch(g_cmd, pixGroups, 1, 1);
    cmdBarrierRW();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_feDirP);
    vkCmdPushConstants(g_cmd, g_fePL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(fpc), &fpc);
    vkCmdDispatch(g_cmd, FE_GROUPS, 1, 1);
    cmdBarrierRW();
    if (forBackward) {
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_feAdjP);
        vkCmdPushConstants(g_cmd, g_fePL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(fpc), &fpc);
        vkCmdDispatch(g_cmd, pixGroups, 1, 1);
        cmdBarrierRW();
    }
}

// buildSSIM: the SSIM DSL/pipelines/sets (see the SSIM globals comment). Built lazily on the
// first non-zero set-lambda, after fp_polish_setup (g_prender must exist for the descriptor write).
bool buildSSIM() {
    VkDescriptorSetLayoutBinding bs[9];
    for (uint32_t i = 0; i < 9; i++) {
        bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
        bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
    }
    VkDescriptorSetLayoutCreateInfo dci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
    dci.bindingCount = 9; dci.pBindings = bs;
    if (vkCreateDescriptorSetLayout(g_device, &dci, nullptr, &g_ssDSL) != VK_SUCCESS) return false;
    VkPushConstantRange pr{VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(SsimPC)};
    VkPipelineLayoutCreateInfo plci{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO};
    plci.setLayoutCount = 1; plci.pSetLayouts = &g_ssDSL; plci.pushConstantRangeCount = 1; plci.pPushConstantRanges = &pr;
    if (vkCreatePipelineLayout(g_device, &plci, nullptr, &g_ssPL) != VK_SUCCESS) return false;
    auto mk = [](const unsigned int* spv, size_t bytes, VkPipeline& pipe) -> bool {
        VkShaderModule sm = loadShader(spv, bytes);
        if (!sm) return false;
        VkComputePipelineCreateInfo ci{VK_STRUCTURE_TYPE_COMPUTE_PIPELINE_CREATE_INFO};
        ci.stage.sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO;
        ci.stage.stage = VK_SHADER_STAGE_COMPUTE_BIT; ci.stage.module = sm; ci.stage.pName = "main";
        ci.layout = g_ssPL;
        VkResult r = vkCreateComputePipelines(g_device, VK_NULL_HANDLE, 1, &ci, nullptr, &pipe);
        vkDestroyShaderModule(g_device, sm, nullptr);
        return r == VK_SUCCESS;
    };
    if (!mk(fe_luma_spv, sizeof(fe_luma_spv), g_ssLumaP)) return false; // bindings 0/2 line up
    if (!mk(RSPV(ssim_h), g_ssHP)) return false;
    if (!mk(RSPV(ssim_myinit), g_ssMyP)) return false;
    if (!mk(RSPV(ssim_map), g_ssMapP)) return false;
    if (!mk(RSPV(ssim_gh), g_ssGHP)) return false;
    if (!mk(RSPV(ssim_adj), g_ssAdjP)) return false;
    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 18};
    VkDescriptorPoolCreateInfo dpci{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO};
    dpci.maxSets = 2; dpci.poolSizeCount = 1; dpci.pPoolSizes = &ps;
    if (vkCreateDescriptorPool(g_device, &dpci, nullptr, &g_ssPool) != VK_SUCCESS) return false;
    VkDescriptorSetLayout layouts[2] = { g_ssDSL, g_ssDSL };
    VkDescriptorSetAllocateInfo a{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
    a.descriptorPool = g_ssPool; a.descriptorSetCount = 2; a.pSetLayouts = layouts;
    VkDescriptorSet sets[2];
    if (vkAllocateDescriptorSets(g_device, &a, sets) != VK_SUCCESS) return false;
    g_ssSetR = sets[0]; g_ssSetT = sets[1];
    return true;
}

void writeSSIMDescriptors() {
    auto wr = [](VkDescriptorSet set, VkBuffer src, VkDeviceSize srcSz, VkBuffer luma, VkDeviceSize lumaSz) {
        VkBuffer bufs[9] = { src, g_ssTL.buf, luma, g_ssH.buf, g_ssMY.buf, g_ssG.buf, g_ssHG.buf, g_ssAdj.buf, g_ssParts.buf };
        VkDeviceSize sizes[9] = { srcSz, g_ssTL.size, lumaSz, g_ssH.size, g_ssMY.size, g_ssG.size, g_ssHG.size, g_ssAdj.size, g_ssParts.size };
        VkWriteDescriptorSet w[9]; VkDescriptorBufferInfo bi[9];
        for (uint32_t i = 0; i < 9; i++) {
            bi[i] = {bufs[i], 0, sizes[i]};
            w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
            w[i].dstSet = set; w[i].dstBinding = i; w[i].descriptorCount = 1;
            w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
        }
        vkUpdateDescriptorSets(g_device, 9, w, 0, nullptr);
    };
    wr(g_ssSetR, g_prender.buf, g_prender.size, g_ssRL.buf, g_ssRL.size);
    // setT routes the TARGET through luma + h-pass: binding 2 (the luma out/in) is the
    // target-luma plane, so the h-pass x·y sum degrades to y² there.
    wr(g_ssSetT, g_target.buf, g_target.size, g_ssTL.buf, g_ssTL.size);
}

// cmdSSIMPasses records luma(render)+h-pass+map(+gh+adj when forBackward) into the OPEN
// command buffer. Σ(1−S) lands in g_ssParts (λ·term added host-side by computeLoss).
void cmdSSIMPasses(bool forBackward) {
    int mw = g_w - SSWIN + 1, mh = g_h - SSWIN + 1;
    SsimPC spc{ g_w, g_h, mw, mh, forBackward ? 1 : 0, (float)g_psslambda };
    uint32_t pixGroups = (uint32_t)(((size_t)g_w * g_h + 255) / 256);
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ssLumaP);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ssPL, 0, 1, &g_ssSetR, 0, nullptr);
    vkCmdPushConstants(g_cmd, g_ssPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(spc), &spc);
    vkCmdDispatch(g_cmd, pixGroups, 1, 1);
    cmdBarrierRW();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ssHP);
    vkCmdPushConstants(g_cmd, g_ssPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(spc), &spc);
    vkCmdDispatch(g_cmd, (uint32_t)(((size_t)mw * g_h + 255) / 256), 1, 1);
    cmdBarrierRW();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ssMapP);
    vkCmdPushConstants(g_cmd, g_ssPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(spc), &spc);
    vkCmdDispatch(g_cmd, SS_GROUPS, 1, 1);
    cmdBarrierRW();
    if (forBackward) {
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ssGHP);
        vkCmdPushConstants(g_cmd, g_ssPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(spc), &spc);
        vkCmdDispatch(g_cmd, (uint32_t)(((size_t)g_w * mh + 255) / 256), 1, 1);
        cmdBarrierRW();
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ssAdjP);
        vkCmdPushConstants(g_cmd, g_ssPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(spc), &spc);
        vkCmdDispatch(g_cmd, pixGroups, 1, 1);
        cmdBarrierRW();
    }
}


// buildEagle: the EAGLE DSL/pipelines/sets (see the EAGLE globals comment). Built lazily on the
// first non-zero set-lambda, after fp_polish_setup.
bool buildEagle() {
    VkDescriptorSetLayoutBinding bs[19];
    for (uint32_t i = 0; i < 19; i++) {
        bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
        bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
    }
    VkDescriptorSetLayoutCreateInfo dci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
    dci.bindingCount = 19; dci.pBindings = bs;
    if (vkCreateDescriptorSetLayout(g_device, &dci, nullptr, &g_egDSL) != VK_SUCCESS) return false;
    VkPushConstantRange pr{VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(EagPC)};
    VkPipelineLayoutCreateInfo plci{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO};
    plci.setLayoutCount = 1; plci.pSetLayouts = &g_egDSL; plci.pushConstantRangeCount = 1; plci.pPushConstantRanges = &pr;
    if (vkCreatePipelineLayout(g_device, &plci, nullptr, &g_egPL) != VK_SUCCESS) return false;
    auto mk = [](const unsigned int* spv, size_t bytes, VkPipeline& pipe) -> bool {
        VkShaderModule sm = loadShader(spv, bytes);
        if (!sm) return false;
        VkComputePipelineCreateInfo ci{VK_STRUCTURE_TYPE_COMPUTE_PIPELINE_CREATE_INFO};
        ci.stage.sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO;
        ci.stage.stage = VK_SHADER_STAGE_COMPUTE_BIT; ci.stage.module = sm; ci.stage.pName = "main";
        ci.layout = g_egPL;
        VkResult r = vkCreateComputePipelines(g_device, VK_NULL_HANDLE, 1, &ci, nullptr, &pipe);
        vkDestroyShaderModule(g_device, sm, nullptr);
        return r == VK_SUCCESS;
    };
    if (!mk(fe_luma_spv, sizeof(fe_luma_spv), g_egLumaP)) return false; // bindings 0/2 line up
    if (!mk(RSPV(eagle_scharr), g_egScharrP)) return false;
    if (!mk(RSPV(eagle_var), g_egVarP)) return false;
    if (!mk(RSPV(eagle_boxx), g_egBoxXP)) return false;
    if (!mk(RSPV(eagle_boxy), g_egBoxYP)) return false;
    if (!mk(RSPV(eagle_hpfin), g_egHpP)) return false;
    if (!mk(RSPV(eagle_loss), g_egLossP)) return false;
    if (!mk(RSPV(eagle_sign), g_egSignP)) return false;
    if (!mk(RSPV(eagle_um), g_egUmP)) return false;
    if (!mk(RSPV(eagle_varadj), g_egVarAdjP)) return false;
    if (!mk(RSPV(eagle_scharradj), g_egScharrAdjP)) return false;
    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 40};
    VkDescriptorPoolCreateInfo dpci{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO};
    dpci.maxSets = 2; dpci.poolSizeCount = 1; dpci.pPoolSizes = &ps;
    if (vkCreateDescriptorPool(g_device, &dpci, nullptr, &g_egPool) != VK_SUCCESS) return false;
    VkDescriptorSetLayout layouts[2] = { g_egDSL, g_egDSL };
    VkDescriptorSetAllocateInfo a{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
    a.descriptorPool = g_egPool; a.descriptorSetCount = 2; a.pSetLayouts = layouts;
    VkDescriptorSet sets[2];
    if (vkAllocateDescriptorSets(g_device, &a, sets) != VK_SUCCESS) return false;
    g_egSetR = sets[0]; g_egSetT = sets[1];
    return true;
}

void writeEagleDescriptors() {
    // setR: recon maps land in hx/hy; setT: the SAME shaders write the fixed target maps
    // (bindings 9/10 point at tHx/tHy there -- the FE/SSIM setT trick).
    auto wr = [](VkDescriptorSet set, VkBuffer src_, VkDeviceSize srcSz, VkBuffer luma, VkDeviceSize lumaSz,
                 VkBuffer hx, VkDeviceSize hxSz, VkBuffer hy, VkDeviceSize hySz) {
        VkBuffer bufs[19] = { src_, g_egTHx.buf, luma, g_egGx.buf, g_egGy.buf, g_egVx.buf, g_egVy.buf,
                              g_egMx.buf, g_egMy.buf, hx, hy, g_egTHy.buf, g_egT1.buf, g_egT2.buf,
                              g_egSx.buf, g_egSy.buf, g_egAdj.buf, g_egParts.buf, g_termW.buf };
        VkDeviceSize sizes[19] = { srcSz, g_egTHx.size, lumaSz, g_egGx.size, g_egGy.size, g_egVx.size,
                                   g_egVy.size, g_egMx.size, g_egMy.size, hxSz, hySz, g_egTHy.size,
                                   g_egT1.size, g_egT2.size, g_egSx.size, g_egSy.size, g_egAdj.size, g_egParts.size, g_termW.size };
        VkWriteDescriptorSet w[19]; VkDescriptorBufferInfo bi[19];
        for (uint32_t i = 0; i < 19; i++) {
            bi[i] = {bufs[i], 0, sizes[i]};
            w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
            w[i].dstSet = set; w[i].dstBinding = i; w[i].descriptorCount = 1;
            w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
        }
        vkUpdateDescriptorSets(g_device, 19, w, 0, nullptr);
    };
    wr(g_egSetR, g_prender.buf, g_prender.size, g_egRL.buf, g_egRL.size, g_egHx.buf, g_egHx.size, g_egHy.buf, g_egHy.size);
    wr(g_egSetT, g_target.buf, g_target.size, g_egTL.buf, g_egTL.size, g_egTHx.buf, g_egTHx.size, g_egTHy.buf, g_egTHy.size);
}

// cmdEagleMaps records luma+scharr+var+highpass into hx/hy of the BOUND set (recon or target).
void cmdEagleMaps(VkDescriptorSet set) {
    uint32_t pixGroups = (uint32_t)(((size_t)g_w * g_h + 255) / 256);
    EagPC pc{ g_w, g_h, 0, 0, (float)g_peglambda, g_hasTermW };
    auto disp = [&](VkPipeline p, int dir, int mode) {
        pc.dir = dir; pc.mode = mode;
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, p);
        vkCmdPushConstants(g_cmd, g_egPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
        vkCmdDispatch(g_cmd, pixGroups, 1, 1);
        cmdBarrierRW();
    };
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_egPL, 0, 1, &set, 0, nullptr);
    disp(g_egLumaP, 0, 0);
    disp(g_egScharrP, 0, 0);
    disp(g_egVarP, 0, 0);
    for (int d = 0; d < 2; d++) { // gauss(v[d]) -> t1, then h[d] = v[d] - t1
        disp(g_egBoxXP, d, d);
        disp(g_egBoxYP, d, 0);
        disp(g_egBoxXP, d, 2);
        disp(g_egBoxYP, d, 0);
        disp(g_egHpP, d, d);
    }
}

// cmdEaglePasses records the recon forward (+ adjoint when forBackward); loss partials via the
// reduce pipe (the host adds lambda*sum, like FE/SSIM).
void cmdEaglePasses(bool forBackward) {
    uint32_t pixGroups = (uint32_t)(((size_t)g_w * g_h + 255) / 256);
    EagPC pc{ g_w, g_h, 0, 0, (float)g_peglambda, g_hasTermW };
    auto disp = [&](VkPipeline p, int dir, int mode, uint32_t groups) {
        pc.dir = dir; pc.mode = mode;
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, p);
        vkCmdPushConstants(g_cmd, g_egPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
        vkCmdDispatch(g_cmd, groups, 1, 1);
        cmdBarrierRW();
    };
    cmdEagleMaps(g_egSetR);
    disp(g_egLossP, 0, 0, EG_GROUPS);
    if (!forBackward) return;
    for (int d = 0; d < 2; d++) {
        disp(g_egSignP, d, d, pixGroups);          // s[d] = sign(h[d]-tH[d])
        disp(g_egBoxXP, d, 3 + d, pixGroups);      // gauss(s[d]) -> t1
        disp(g_egBoxYP, d, 0, pixGroups);
        disp(g_egBoxXP, d, 2, pixGroups);
        disp(g_egBoxYP, d, 0, pixGroups);
        disp(g_egHpP, d, 2 + d, pixGroups);        // t2 = s[d] - t1 (= u)
        disp(g_egUmP, d, d, pixGroups);            // v[d] = u*m[d]
        disp(g_egVarAdjP, d, d, pixGroups);        // s[d] = (2/9)(g[d]*Box9(u) - Box9(u*m))
    }
    disp(g_egScharrAdjP, 0, 0, pixGroups);         // adj from sx,sy
}

// fp_set_polish_eagle sets the EAGLE lambda (pointer ABI like the FE setter) and prepares the
// fixed target-side maps. lambda<=0 disables; below the CPU reference 8px floor degrades to 0.
// Call AFTER fp_polish_setup.
API void fp_set_polish_eagle(const double* lambdaPtr) {
    g_profScope = PROF_TERMSET;
    g_peglambda = lambdaPtr[0];
    if (g_peglambda <= 0.0 || !g_device || g_pn < 1) return;
    if (g_w < 8 || g_h < 8) { g_peglambda = 0.0; return; }
    size_t npix = (size_t)g_w * g_h;
    if (g_egDSL == VK_NULL_HANDLE && !buildEagle()) { g_lastError = 2010; g_peglambda = 0.0; return; }
    // The lambda must die with the allocation, exactly as the two branches around it do: a live
    // lambda over a NULL g_termW dispatches against descriptor sets nothing ever wrote, which on a
    // low-VRAM card turns a survivable OOM into a device fault.
    if (!ensureTermW()) { g_lastError = 2012; g_peglambda = 0.0; return; }
    if (g_egTL.buf == VK_NULL_HANDLE) {
        const VkBufferUsageFlags S = VK_BUFFER_USAGE_STORAGE_BUFFER_BIT;
        const VkMemoryPropertyFlags dl = VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT;
        bool ok = createBufEx(npix * 4, S, dl, false, g_egTL) && createBufEx(npix * 4, S, dl, false, g_egRL)
               && createBufEx(npix * g_rs, S, dl, false, g_egTHx) && createBufEx(npix * g_rs, S, dl, false, g_egTHy)
               && createBufEx(npix * g_rs, S, dl, false, g_egGx) && createBufEx(npix * g_rs, S, dl, false, g_egGy)
               && createBufEx(npix * g_rs, S, dl, false, g_egVx) && createBufEx(npix * g_rs, S, dl, false, g_egVy)
               && createBufEx(npix * g_rs, S, dl, false, g_egMx) && createBufEx(npix * g_rs, S, dl, false, g_egMy)
               && createBufEx(npix * g_rs, S, dl, false, g_egHx) && createBufEx(npix * g_rs, S, dl, false, g_egHy)
               && createBufEx(npix * g_rs, S, dl, false, g_egT1) && createBufEx(npix * g_rs, S, dl, false, g_egT2)
               && createBufEx(npix * g_rs, S, dl, false, g_egSx) && createBufEx(npix * g_rs, S, dl, false, g_egSy)
               && createHost(EG_GROUPS * 8, g_egParts);
        if (!ok) { g_lastError = 2011; g_peglambda = 0.0; return; }
    }
    writeEagleDescriptors();
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    cmdEagleMaps(g_egSetT); // target maps land in tHx/tHy via the setT bindings
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
}

// buildTileBinner: 4-binding DSL (bbx, counts, offs, list) + the three binning pipelines.
bool buildTileBinner() {
    VkDescriptorSetLayoutBinding bs[4];
    for (uint32_t i = 0; i < 4; i++) {
        bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
        bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
    }
    VkDescriptorSetLayoutCreateInfo dci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
    dci.bindingCount = 4; dci.pBindings = bs;
    if (vkCreateDescriptorSetLayout(g_device, &dci, nullptr, &g_tbDSL) != VK_SUCCESS) return false;
    VkPushConstantRange pr{VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(TilePC)};
    VkPipelineLayoutCreateInfo plci{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO};
    plci.setLayoutCount = 1; plci.pSetLayouts = &g_tbDSL; plci.pushConstantRangeCount = 1; plci.pPushConstantRanges = &pr;
    if (vkCreatePipelineLayout(g_device, &plci, nullptr, &g_tbPL) != VK_SUCCESS) return false;
    auto mk = [](const unsigned int* spv, size_t bytes, VkPipeline& pipe) -> bool {
        VkShaderModule sm = loadShader(spv, bytes);
        if (!sm) return false;
        VkComputePipelineCreateInfo ci{VK_STRUCTURE_TYPE_COMPUTE_PIPELINE_CREATE_INFO};
        ci.stage.sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO;
        ci.stage.stage = VK_SHADER_STAGE_COMPUTE_BIT; ci.stage.module = sm; ci.stage.pName = "main";
        ci.layout = g_tbPL;
        VkResult r = vkCreateComputePipelines(g_device, VK_NULL_HANDLE, 1, &ci, nullptr, &pipe);
        vkDestroyShaderModule(g_device, sm, nullptr);
        return r == VK_SUCCESS;
    };
    if (!mk(tb_count_spv, sizeof(tb_count_spv), g_tbCount)) return false;
    if (!mk(tb_scan_spv, sizeof(tb_scan_spv), g_tbScan)) return false;
    if (!mk(tb_fill_spv, sizeof(tb_fill_spv), g_tbFill)) return false;
    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 4};
    VkDescriptorPoolCreateInfo dpci{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO};
    dpci.maxSets = 1; dpci.poolSizeCount = 1; dpci.pPoolSizes = &ps;
    if (vkCreateDescriptorPool(g_device, &dpci, nullptr, &g_tbPool) != VK_SUCCESS) return false;
    VkDescriptorSetAllocateInfo a{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
    a.descriptorPool = g_tbPool; a.descriptorSetCount = 1; a.pSetLayouts = &g_tbDSL;
    return vkAllocateDescriptorSets(g_device, &a, &g_tbSet) == VK_SUCCESS;
}

void writeTileBinnerDescriptors() {
    if (g_tbSet == VK_NULL_HANDLE) return;
    VkBuffer bufs[4] = { g_pbbxBuf.buf, g_tileCount.buf, g_tileOff.buf, g_tileList.buf };
    VkDeviceSize sizes[4] = { g_pbbxBuf.size, g_tileCount.size, g_tileOff.size, g_tileList.size };
    VkWriteDescriptorSet w[4]; VkDescriptorBufferInfo bi[4];
    for (uint32_t i = 0; i < 4; i++) {
        bi[i] = {bufs[i], 0, sizes[i]};
        w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w[i].dstSet = g_tbSet; w[i].dstBinding = i; w[i].descriptorCount = 1;
        w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
    }
    vkUpdateDescriptorSets(g_device, 4, w, 0, nullptr);
}

// buildTiledForward: dedicated 8-binding DSL + pipeline layout (TiledPC push) + pool + set
// for the one-dispatch tiled forward/hard. Fully separate from the per-shape polish DSL so
// the two paths never share descriptor/push state (the trap the first tiling attempt hit).
bool buildTiledForward() {
    VkDescriptorSetLayoutBinding bs[12];
    for (uint32_t i = 0; i < 12; i++) {
        bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
        bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
    }
    VkDescriptorSetLayoutCreateInfo dci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
    dci.bindingCount = 12; dci.pBindings = bs;
    if (vkCreateDescriptorSetLayout(g_device, &dci, nullptr, &g_ptDSL) != VK_SUCCESS) return false;
    VkPushConstantRange pr{VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(TiledPC)};
    VkPipelineLayoutCreateInfo plci{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO};
    plci.setLayoutCount = 1; plci.pSetLayouts = &g_ptDSL; plci.pushConstantRangeCount = 1; plci.pPushConstantRanges = &pr;
    if (vkCreatePipelineLayout(g_device, &plci, nullptr, &g_ptPL) != VK_SUCCESS) return false;
    auto mk = [](const unsigned int* spv, size_t bytes, VkPipeline& pipe) -> bool {
        VkShaderModule sm = loadShader(spv, bytes);
        if (!sm) return false;
        VkComputePipelineCreateInfo ci{VK_STRUCTURE_TYPE_COMPUTE_PIPELINE_CREATE_INFO};
        ci.stage.sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO;
        ci.stage.stage = VK_SHADER_STAGE_COMPUTE_BIT; ci.stage.module = sm; ci.stage.pName = "main";
        ci.layout = g_ptPL;
        VkResult r = vkCreateComputePipelines(g_device, VK_NULL_HANDLE, 1, &ci, nullptr, &pipe);
        vkDestroyShaderModule(g_device, sm, nullptr);
        return r == VK_SUCCESS;
    };
    if (!mk(RSPV(pt_forward), g_ptFwd)) return false;
    if (!mk(RSPV(pt_hard), g_ptHard)) return false;
    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 12};
    VkDescriptorPoolCreateInfo dpci{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO};
    dpci.maxSets = 1; dpci.poolSizeCount = 1; dpci.pPoolSizes = &ps;
    if (vkCreateDescriptorPool(g_device, &dpci, nullptr, &g_ptPool) != VK_SUCCESS) return false;
    VkDescriptorSetAllocateInfo a{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
    a.descriptorPool = g_ptPool; a.descriptorSetCount = 1; a.pSetLayouts = &g_ptDSL;
    return vkAllocateDescriptorSets(g_device, &a, &g_ptSet) == VK_SUCCESS;
}

// writeTiledForwardDescriptors binds the 8 tiled-pass buffers. Re-called whenever g_pbelow
// is (re)allocated, since its handle changes.
void writeTiledForwardDescriptors() {
    if (g_ptSet == VK_NULL_HANDLE) return;
    VkBuffer bufs[12] = { g_pP.buf, g_pcol.buf, g_pkinds.buf, g_prender.buf, g_pbelow.buf,
                         g_pbbxBuf.buf, g_pboffBuf.buf, g_pbase.buf, g_tileOff.buf, g_tileList.buf,
                         g_maskAtlas.buf, g_maskMeta.buf };
    VkDeviceSize sizes[12] = { g_pP.size, g_pcol.size, g_pkinds.size, g_prender.size, g_pbelow.size,
                              g_pbbxBuf.size, g_pboffBuf.size, g_pbase.size, g_tileOff.size, g_tileList.size,
                              g_maskAtlas.size, g_maskMeta.size };
    VkWriteDescriptorSet w[12]; VkDescriptorBufferInfo bi[12];
    for (uint32_t i = 0; i < 12; i++) {
        bi[i] = {bufs[i], 0, sizes[i]};
        w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w[i].dstSet = g_ptSet; w[i].dstBinding = i; w[i].descriptorCount = 1;
        w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
    }
    vkUpdateDescriptorSets(g_device, 12, w, 0, nullptr);
}

// buildBackwardTiled: dedicated DSL + pipeline layout (TiledPC push) + pool + set for the
// three-pass barrier-free backward (Pass A dC walk + Pass B sliced reduce + Pass C combine).
bool buildBackwardTiled() {
    VkDescriptorSetLayoutBinding bs[14];
    for (uint32_t i = 0; i < 14; i++) {
        bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
        bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
    }
    VkDescriptorSetLayoutCreateInfo dci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
    dci.bindingCount = 14; dci.pBindings = bs;
    if (vkCreateDescriptorSetLayout(g_device, &dci, nullptr, &g_pbDSL) != VK_SUCCESS) return false;
    VkPushConstantRange pr{VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(TiledPC)};
    VkPipelineLayoutCreateInfo plci{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO};
    plci.setLayoutCount = 1; plci.pSetLayouts = &g_pbDSL; plci.pushConstantRangeCount = 1; plci.pPushConstantRanges = &pr;
    if (vkCreatePipelineLayout(g_device, &plci, nullptr, &g_pbPL) != VK_SUCCESS) return false;
    auto mk = [](const unsigned int* spv, size_t bytes, VkPipeline& pipe) -> bool {
        VkShaderModule sm = loadShader(spv, bytes);
        if (!sm) return false;
        VkComputePipelineCreateInfo ci{VK_STRUCTURE_TYPE_COMPUTE_PIPELINE_CREATE_INFO};
        ci.stage.sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO;
        ci.stage.stage = VK_SHADER_STAGE_COMPUTE_BIT; ci.stage.module = sm; ci.stage.pName = "main";
        ci.layout = g_pbPL;
        VkResult r = vkCreateComputePipelines(g_device, VK_NULL_HANDLE, 1, &ci, nullptr, &pipe);
        vkDestroyShaderModule(g_device, sm, nullptr);
        return r == VK_SUCCESS;
    };
    if (!mk(RSPV(pt_dcwalk), g_pbWalk)) return false;
    if (!mk(RSPV(pt_breduce), g_pbReduce)) return false;
    if (!mk(RSPV(pt_bcombine), g_pbCombine)) return false;
    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 14};
    VkDescriptorPoolCreateInfo dpci{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO};
    dpci.maxSets = 1; dpci.poolSizeCount = 1; dpci.pPoolSizes = &ps;
    if (vkCreateDescriptorPool(g_device, &dpci, nullptr, &g_pbPool) != VK_SUCCESS) return false;
    VkDescriptorSetAllocateInfo a{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
    a.descriptorPool = g_pbPool; a.descriptorSetCount = 1; a.pSetLayouts = &g_pbDSL;
    return vkAllocateDescriptorSets(g_device, &a, &g_pbSet) == VK_SUCCESS;
}

// writeBackwardDescriptors binds the backward buffers (the shared nine, the two binning lists,
// the mask atlas pair, and the slice-partial buffer). Re-called when g_pbelow/g_pdcsnap/
// g_tileList/g_pbwPart are (re)allocated (their handles change).
void writeBackwardDescriptors() {
    if (g_pbSet == VK_NULL_HANDLE) return;
    VkBuffer bufs[14] = { g_pP.buf, g_pcol.buf, g_pkinds.buf, g_pdC.buf, g_pbelow.buf,
                         g_pdcsnap.buf, g_ppgrad.buf, g_pbbxBuf.buf, g_pboffBuf.buf,
                         g_tileOff.buf, g_tileList.buf, g_maskAtlas.buf, g_maskMeta.buf,
                         g_pbwPart.buf };
    VkDeviceSize sizes[14] = { g_pP.size, g_pcol.size, g_pkinds.size, g_pdC.size, g_pbelow.size,
                              g_pdcsnap.size, g_ppgrad.size, g_pbbxBuf.size, g_pboffBuf.size,
                              g_tileOff.size, g_tileList.size, g_maskAtlas.size, g_maskMeta.size,
                              g_pbwPart.size };
    VkWriteDescriptorSet w[14]; VkDescriptorBufferInfo bi[14];
    for (uint32_t i = 0; i < 14; i++) {
        bi[i] = {bufs[i], 0, sizes[i]};
        w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w[i].dstSet = g_pbSet; w[i].dstBinding = i; w[i].descriptorCount = 1;
        w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
    }
    vkUpdateDescriptorSets(g_device, 14, w, 0, nullptr);
}

// ensureStaging grows the shared staging buffer if a transfer needs more than its size.
void ensureStaging(VkDeviceSize need) {
    if (g_staging.buf != VK_NULL_HANDLE && need <= g_staging.size) return;
    destroyBuf(g_staging);
    createHost(need, g_staging);
}

// stagingReady is the checked front door: a failed staging grow used to leave callers memcpy-ing
// into a null map — a hard host crash on the exact machines (low RAM/VRAM) that hit it.
bool stagingReady(VkDeviceSize need) {
    ensureStaging(need);
    if (!g_staging.map) { g_lastError = 1060; return false; }
    return true;
}

// computeLoss runs the loss reduction over the CURRENT g_prender vs target and sums the
// host-visible partials. Shared by fp_polish_loss (soft) and fp_polish_hard_loss (hard).
double computeLoss() {
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pLoss);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pPL, 0, 1, &g_pSet, 0, nullptr);
    PolishPC pc{0, g_w, g_h, 0, 0, 0, 0, 0, g_pste, g_w * g_h, 0.0f, g_poklab, (float)g_pfelambda, (float)g_psslambda, (float)g_peglambda, (float)g_pldlambda};
    vkCmdPushConstants(g_cmd, g_pPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
    vkCmdDispatch(g_cmd, PLOSS_GROUPS, 1, 1);
    if (g_pfelambda > 0.0 || g_pldlambda > 0.0) {
        cmdBarrierRW();
        cmdFEPasses(false); // luma(render) + dir -> g_feParts (λ·FE added host-side below)
    }
    if (g_psslambda > 0.0) {
        cmdBarrierRW();
        cmdSSIMPasses(false); // luma(render) + h-pass + map -> g_ssParts (λ·term added below)
    }
    if (g_peglambda > 0.0) {
        cmdBarrierRW();
        cmdEaglePasses(false); // maps + |hp diff| -> g_egParts (lambda*term added below)
    }
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
    // The partials buffers hold REAL — double on the fp64 pipelines, float on the fp32 fallback —
    // so the read has to follow g_fp64. The host-side sum stays double either way.
    auto sumParts = [](const void* map, int n) {
        double s = 0.0;
        if (g_fp64) {
            const double* p = (const double*)map;
            for (int i = 0; i < n; i++) s += p[i];
        } else {
            const float* p = (const float*)map;
            for (int i = 0; i < n; i++) s += (double)p[i];
        }
        return s;
    };
    double s = sumParts(g_ppartials.map, PLOSS_GROUPS);
    if (g_pfelambda > 0.0 || g_pldlambda > 0.0) {
        // fe_dir emits the partials ALREADY lambda-weighted (it carries two different lambdas),
        // so this must not scale again.
        s += sumParts(g_feParts.map, FE_GROUPS);
    }
    if (g_psslambda > 0.0) {
        s += g_psslambda * sumParts(g_ssParts.map, SS_GROUPS);
    }
    if (g_peglambda > 0.0) {
        s += g_peglambda * sumParts(g_egParts.map, EG_GROUPS);
    }
    return s;
}

void writeSearchDescriptors() {
    auto wr = [](VkDescriptorSet set, uint32_t b, VkBuffer buf, VkDeviceSize sz, VkWriteDescriptorSet& w, VkDescriptorBufferInfo& bi) {
        bi = {buf, 0, sz};
        w = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w.dstSet = set; w.dstBinding = b; w.descriptorCount = 1; w.descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w.pBufferInfo = &bi;
    };
    VkWriteDescriptorSet w[48]; VkDescriptorBufferInfo bi[48]; int k = 0;
    wr(g_genSet, 0, g_scand.buf, g_scand.size, w[k], bi[k]); k++;
    wr(g_genSet, 1, g_kindsB.buf, g_kindsB.size, w[k], bi[k]); k++;
    wr(g_genSet, 2, g_kindcdf.buf, g_kindcdf.size, w[k], bi[k]); k++;
    wr(g_genSet, 3, g_gridcdf.buf, g_gridcdf.size, w[k], bi[k]); k++;
    wr(g_genSet, 4, g_orient.buf, g_orient.size, w[k], bi[k]); k++;
    wr(g_genSet, 5, g_bound.buf, g_bound.size, w[k], bi[k]); k++;
    wr(g_genSet, 6, g_kgate.buf, g_kgate.size, w[k], bi[k]); k++;
    wr(g_genSet, 7, g_rampglow.buf, g_rampglow.size, w[k], bi[k]); k++;
    // Vulkan requires every binding to be written even when the feature is off, so the proposal
    // bindings fall back to an existing buffer; the shader never reads them unless hasProp is set.
    wr(g_genSet, 8, g_hasProposer ? g_propMap.buf : g_kindsB.buf,
       g_hasProposer ? g_propMap.size : g_kindsB.size, w[k], bi[k]); k++;
    wr(g_genSet, 9, g_hasProposer ? g_propKinds.buf : g_kindsB.buf,
       g_hasProposer ? g_propKinds.size : g_kindsB.size, w[k], bi[k]); k++;
    wr(g_genSet, 10, g_coh.buf, g_coh.size, w[k], bi[k]); k++;
    wr(g_genSet, 11, g_genCfg.buf, g_genCfg.size, w[k], bi[k]); k++;
    wr(g_sevalSet, 0, g_scand.buf, g_scand.size, w[k], bi[k]); k++;
    wr(g_sevalSet, 1, g_target.buf, g_target.size, w[k], bi[k]); k++;
    wr(g_sevalSet, 2, g_canvas.buf, g_canvas.size, w[k], bi[k]); k++;
    wr(g_sevalSet, 3, g_weight.buf, g_weight.size, w[k], bi[k]); k++;
    wr(g_sevalSet, 4, g_sout.buf, g_sout.size, w[k], bi[k]); k++;
    wr(g_sevalSet, 5, g_maskAtlas.buf, g_maskAtlas.size, w[k], bi[k]); k++; // search-eval shares the eval DSL
    wr(g_sevalSet, 6, g_maskMeta.buf, g_maskMeta.size, w[k], bi[k]); k++;
    wr(g_prepSet, 0, g_sout.buf, g_sout.size, w[k], bi[k]); k++;
    wr(g_prepSet, 1, g_scand.buf, g_scand.size, w[k], bi[k]); k++;
    wr(g_prepSet, 2, g_adj.buf, g_adj.size, w[k], bi[k]); k++;
    wr(g_argSet, 0, g_adj.buf, g_adj.size, w[k], bi[k]); k++;
    wr(g_argSet, 1, g_scand.buf, g_scand.size, w[k], bi[k]); k++;
    wr(g_argSet, 2, g_sout.buf, g_sout.size, w[k], bi[k]); k++;
    wr(g_argSet, 3, g_best.buf, g_best.size, w[k], bi[k]); k++;
    wr(g_mutSet, 0, g_scand.buf, g_scand.size, w[k], bi[k]); k++;
    wr(g_mutSet, 1, g_best.buf, g_best.size, w[k], bi[k]); k++;
    // Coarse-filter sets. Vulkan requires every binding written even while the filter is off, so
    // until fp_set_coarse has created the pass-2 buffers they fall back to existing ones — the
    // shaders behind these sets never dispatch in that state.
    {
        const Buf& c2 = g_scand2.buf ? g_scand2 : g_scand;
        const Buf& o2 = g_sout2.buf ? g_sout2 : g_sout;
        const Buf& a2 = g_adj2.buf ? g_adj2 : g_adj;
        const Buf& se = g_sel.buf ? g_sel : g_adj;
        wr(g_cminSet, 0, g_adj.buf, g_adj.size, w[k], bi[k]); k++;
        wr(g_cminSet, 1, se.buf, se.size, w[k], bi[k]); k++;
        wr(g_cgatSet, 0, g_scand.buf, g_scand.size, w[k], bi[k]); k++;
        wr(g_cgatSet, 1, se.buf, se.size, w[k], bi[k]); k++;
        wr(g_cgatSet, 2, c2.buf, c2.size, w[k], bi[k]); k++;
        wr(g_seval2Set, 0, c2.buf, c2.size, w[k], bi[k]); k++;
        wr(g_seval2Set, 1, g_target.buf, g_target.size, w[k], bi[k]); k++;
        wr(g_seval2Set, 2, g_canvas.buf, g_canvas.size, w[k], bi[k]); k++;
        wr(g_seval2Set, 3, g_weight.buf, g_weight.size, w[k], bi[k]); k++;
        wr(g_seval2Set, 4, o2.buf, o2.size, w[k], bi[k]); k++;
        wr(g_seval2Set, 5, g_maskAtlas.buf, g_maskAtlas.size, w[k], bi[k]); k++;
        wr(g_seval2Set, 6, g_maskMeta.buf, g_maskMeta.size, w[k], bi[k]); k++;
        wr(g_prep2Set, 0, o2.buf, o2.size, w[k], bi[k]); k++;
        wr(g_prep2Set, 1, c2.buf, c2.size, w[k], bi[k]); k++;
        wr(g_prep2Set, 2, a2.buf, a2.size, w[k], bi[k]); k++;
        wr(g_arg2Set, 0, a2.buf, a2.size, w[k], bi[k]); k++;
        wr(g_arg2Set, 1, c2.buf, c2.size, w[k], bi[k]); k++;
        wr(g_arg2Set, 2, o2.buf, o2.size, w[k], bi[k]); k++;
        wr(g_arg2Set, 3, g_best.buf, g_best.size, w[k], bi[k]); k++;
    }
    vkUpdateDescriptorSets(g_device, (uint32_t)k, w, 0, nullptr);
}

// ensureSearch grows the per-shape candidate scratch (scand/sout/adj) to hold n candidates
// and (re)writes the search descriptor sets when a buffer was recreated.
bool ensureSearch(int n) {
    if (n > g_searchCap) {
        destroyBuf(g_scand); destroyBuf(g_sout); destroyBuf(g_adj);
        const VkMemoryPropertyFlags dl = VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT;
        const VkBufferUsageFlags S = VK_BUFFER_USAGE_STORAGE_BUFFER_BIT;
        if (!createBufEx((size_t)n * 11 * sizeof(float), S, dl, false, g_scand) ||
            !createBufEx((size_t)n * 5 * sizeof(float), S, dl, false, g_sout) ||
            !createBufEx((size_t)n * sizeof(float), S, dl, false, g_adj)) {
            // The old buffers are already destroyed: reset the cap or a later smaller call would
            // skip this block and dispatch against freed buffers.
            destroyBuf(g_scand); destroyBuf(g_sout); destroyBuf(g_adj);
            g_searchCap = 0; g_searchSetsDirty = true;
            return false;
        }
        g_searchCap = n; g_searchSetsDirty = true;
    }
    if (g_searchSetsDirty) { writeSearchDescriptors(); g_searchSetsDirty = false; }
    return true;
}

// ensureCoarse grows the pass-2 scratch to k survivors and marks the search sets for rebinding.
// The buffers are tiny (k <= 8192 -> under half a megabyte total), so growth is rare and cheap.
bool ensureCoarse(int k) {
    if (k <= g_coarseCap) return true;
    destroyBuf(g_scand2); destroyBuf(g_sout2); destroyBuf(g_adj2); destroyBuf(g_sel);
    const VkMemoryPropertyFlags dl = VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT;
    // TRANSFER_SRC so fp_search_survivors can copy the refined pool out (batch placement).
    const VkBufferUsageFlags S = VK_BUFFER_USAGE_STORAGE_BUFFER_BIT | VK_BUFFER_USAGE_TRANSFER_SRC_BIT;
    if (!createBufEx((size_t)k * 11 * sizeof(float), S, dl, false, g_scand2) ||
        !createBufEx((size_t)k * 5 * sizeof(float), S, dl, false, g_sout2) ||
        !createBufEx((size_t)k * sizeof(float), S, dl, false, g_adj2) ||
        !createBufEx((size_t)k * sizeof(int32_t), S, dl, false, g_sel)) {
        // Partial success is worse than none: a surviving g_scand2 makes useCoarse fire while the
        // descriptor fallback silently aliases the missing buffers onto pass 1's — pass 2 would
        // then overwrite the full-budget scores. Drop everything and reset the cap.
        destroyBuf(g_scand2); destroyBuf(g_sout2); destroyBuf(g_adj2); destroyBuf(g_sel);
        g_coarseCap = 0; g_searchSetsDirty = true;
        return false;
    }
    g_coarseCap = k; g_searchSetsDirty = true;
    return true;
}

// ---- batch placement: export the refined survivor pool ----
// Under alpha-over in linear light two candidates with disjoint bboxes have EXACTLY additive SSE
// deltas — each touches only its own pixels and each colour solves from the canvas under its own
// bbox. So the host can take a maximal independent set over the pool the coarse filter already
// re-scored at the FULL budget and place all of them in one round, with no approximation. The
// export is the last thing recorded into the search submit, so it costs no extra round trip.
bool ensureSurv(int k) {
    if (k <= g_survCap) return true;
    destroyBuf(g_survBuf);
    if (!createBufEx((size_t)k * 17 * sizeof(float), VK_BUFFER_USAGE_TRANSFER_DST_BIT, HOSTVIS, true, g_survBuf)) {
        g_survCap = 0;
        return false;
    }
    g_survCap = k;
    return true;
}

// cmdExportSurvivors records the survivor copy-out at the tail of the search submit. Caller has
// just recorded the final argmin, so a shader-write -> transfer-read barrier comes first.
void cmdExportSurvivors(int k) {
    flushBarrier(); // shader writes -> transfer reads
    VkBufferCopy c[3];
    c[0] = {0, 0, (VkDeviceSize)k * sizeof(float)};
    c[1] = {0, (VkDeviceSize)k * sizeof(float), (VkDeviceSize)k * 11 * sizeof(float)};
    c[2] = {0, (VkDeviceSize)k * 12 * sizeof(float), (VkDeviceSize)k * 5 * sizeof(float)};
    vkCmdCopyBuffer(g_cmd, g_adj2.buf, g_survBuf.buf, 1, &c[0]);
    vkCmdCopyBuffer(g_cmd, g_scand2.buf, g_survBuf.buf, 1, &c[1]);
    vkCmdCopyBuffer(g_cmd, g_sout2.buf, g_survBuf.buf, 1, &c[2]);
    VkMemoryBarrier mb{VK_STRUCTURE_TYPE_MEMORY_BARRIER};
    mb.srcAccessMask = VK_ACCESS_TRANSFER_WRITE_BIT;
    mb.dstAccessMask = VK_ACCESS_HOST_READ_BIT;
    vkCmdPipelineBarrier(g_cmd, VK_PIPELINE_STAGE_TRANSFER_BIT, VK_PIPELINE_STAGE_HOST_BIT, 0, 1, &mb, 0, nullptr, 0, nullptr);
    g_survN = k;
}

// searchTail records everything after the candidate pool has been scored: the selection-adjusted
// score, then either a plain argmin or the coarse-to-fine refine (partition argmin over the cheap
// pass, gather the kpart survivors, re-score them at the FULL sample budget, re-adjust, argmin).
// Caller stands after a cmdBarrierRW() — or after a fence, which is a superset.
//
// `split` = the TDR path: the refine's full-budget eval gets its own chunked submits (kpart at the
// full budget is a big dispatch in its own right) and the buffer is left OPEN for the caller to
// close, exactly as in the unsplit case. Same commands in the same order either way.
void searchTail(int n, int compact, int shapeCount, bool useCoarse, bool split) {
    if (split) beginCmd();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_prepPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_prepPL, 0, 1, &g_prepSet, 0, nullptr);
    PrepPC ppc{n, compact, shapeCount, g_w, g_h};
    vkCmdPushConstants(g_cmd, g_prepPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(ppc), &ppc);
    vkCmdDispatch(g_cmd, (uint32_t)((n + 255) / 256), 1, 1);
    cmdBarrierRW();
    if (!useCoarse) {
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_argPipe);
        vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_argPL, 0, 1, &g_argSet, 0, nullptr);
        ArgPC apc{n, 0, 0, 0, 0, 0, 0};
        vkCmdPushConstants(g_cmd, g_argPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(apc), &apc);
        vkCmdDispatch(g_cmd, 1, 1, 1);
        return;
    }
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_cminPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_cminPL, 0, 1, &g_cminSet, 0, nullptr);
    CminPC cpc{n, g_kpart};
    vkCmdPushConstants(g_cmd, g_cminPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(cpc), &cpc);
    vkCmdDispatch(g_cmd, (uint32_t)g_kpart, 1, 1);
    cmdBarrierRW();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_cgatPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_cgatPL, 0, 1, &g_cgatSet, 0, nullptr);
    CgatPC gpc2{g_kpart};
    vkCmdPushConstants(g_cmd, g_cgatPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(gpc2), &gpc2);
    vkCmdDispatch(g_cmd, (uint32_t)((g_kpart + 255) / 256), 1, 1);
    EvalPC e2{g_kpart, g_w, g_h, g_sampleBudget, g_alphaGridN, {g_alphaGrid[0], g_alphaGrid[1], g_alphaGrid[2], g_alphaGrid[3], g_alphaGrid[4], g_alphaGrid[5]}, g_gradOn, 0};
    if (split) {
        endSubmit();
        evalChunked(g_seval2Set, e2, g_kpart, evalChunk(g_kpart));
        if (g_fatal) { beginCmd(); return; } // caller still closes a buffer; submitWait no-ops
        beginCmd();
    } else {
        cmdBarrierRW();
        cmdEvalRange(g_seval2Set, e2, 0, g_kpart);
        cmdBarrierRW();
    }
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_prepPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_prepPL, 0, 1, &g_prep2Set, 0, nullptr);
    PrepPC p2{g_kpart, compact, shapeCount, g_w, g_h};
    vkCmdPushConstants(g_cmd, g_prepPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(p2), &p2);
    vkCmdDispatch(g_cmd, (uint32_t)((g_kpart + 255) / 256), 1, 1);
    cmdBarrierRW();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_argPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_argPL, 0, 1, &g_arg2Set, 0, nullptr);
    ArgPC a2{g_kpart, 0, 0, 0, 0, 0, 0};
    vkCmdPushConstants(g_cmd, g_argPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(a2), &a2);
    vkCmdDispatch(g_cmd, 1, 1, 1);
    if (g_batchOn && ensureSurv(g_kpart)) cmdExportSurvivors(g_kpart);
}

void writeMomentDescriptors() {
    auto wr = [](VkDescriptorSet set, uint32_t b, VkBuffer buf, VkDeviceSize sz, VkWriteDescriptorSet& w, VkDescriptorBufferInfo& bi) {
        bi = {buf, 0, sz};
        w = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w.dstSet = set; w.dstBinding = b; w.descriptorCount = 1; w.descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w.pBufferInfo = &bi;
    };
    VkWriteDescriptorSet w[10]; VkDescriptorBufferInfo bi[10]; int k = 0;
    wr(g_msSet, 0, g_seeds.buf, g_seeds.size, w[k], bi[k]); k++;
    wr(g_msSet, 1, g_gridcdf.buf, g_gridcdf.size, w[k], bi[k]); k++;
    wr(g_msSet, 2, g_bound.buf, g_bound.size, w[k], bi[k]); k++;
    wr(g_gmSet, 0, g_scand.buf, g_scand.size, w[k], bi[k]); k++;
    wr(g_gmSet, 1, g_seeds.buf, g_seeds.size, w[k], bi[k]); k++;
    wr(g_gmSet, 2, g_kindsB.buf, g_kindsB.size, w[k], bi[k]); k++;
    wr(g_gmSet, 3, g_kindcdf.buf, g_kindcdf.size, w[k], bi[k]); k++;
    wr(g_gmSet, 4, g_kgate.buf, g_kgate.size, w[k], bi[k]); k++;
    wr(g_gmSet, 5, g_rampglow.buf, g_rampglow.size, w[k], bi[k]); k++;
    vkUpdateDescriptorSets(g_device, (uint32_t)k, w, 0, nullptr);
}

// ensureMoment grows the seed scratch to K seeds and rewrites the moment descriptor sets
// (cheap; called per shape — g_scand may have been recreated by ensureSearch).
bool ensureMoment(int K) {
    if (K > g_momentCap) {
        destroyBuf(g_seeds);
        if (!createBufEx((size_t)K * 6 * sizeof(float), VK_BUFFER_USAGE_STORAGE_BUFFER_BIT, VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT, false, g_seeds)) {
            g_momentCap = 0; // the old buffer is gone; a later smaller K must re-allocate
            return false;
        }
        g_momentCap = K;
    }
    writeMomentDescriptors();
    return true;
}

// ensureTermW lazily creates the per-pixel term-weight buffer (bound by the FE and EAGLE
// descriptor sets even when unweighted; kernels only read it when hasTW is set).
bool ensureTermW() {
    if (g_termW.buf != VK_NULL_HANDLE) return true;
    return createBufEx((size_t)g_w * g_h * sizeof(float),
        VK_BUFFER_USAGE_STORAGE_BUFFER_BIT | VK_BUFFER_USAGE_TRANSFER_DST_BIT,
        VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT, false, g_termW);
}

} // namespace

API int fp_init(const float* target, const float* weight, int w, int h, int maxCands, int gridSize) {
    g_profScope = PROF_INIT;
    memset(g_profSec, 0, sizeof(g_profSec));
    memset(g_profCnt, 0, sizeof(g_profCnt));
    teardown();
    g_dirtyFull = true;
    g_dirtyX0 = g_dirtyY0 = 0; g_dirtyX1 = g_dirtyY1 = -1;
    g_lastError = 0;
    g_fatal = 0; // buildContext creates a FRESH device below; the previous loss does not carry over
    // The chunking EMA is per-workload: a run that jumps from 1100px to native 4096px would size
    // its first submit off the SMALL canvas's round cost and can blow the TDR window it guards.
    g_mutRoundCost = 0.0;
    for (int i = 0; i < PCH_N; i++) g_polishCost[i] = 0.0;
    g_evalCandCost = 0.0;
    g_w = w; g_h = h; g_maxCands = maxCands; g_grid = gridSize;
    // fp_error_grid dispatches one workgroup per CELL, so the grid resolution has its own dispatch
    // ceiling: 256 cells a side is already 65536 workgroups, one past the guaranteed limit. The
    // shipped resolutions are 48-160; -grid is an expert knob and this keeps it honest.
    if (g_grid > 255) g_grid = 255;
    if (!buildContext()) { teardown(); return g_lastError ? g_lastError : 1; }
    // Several per-pixel shaders are thread-per-pixel with 256-wide groups; a canvas whose group
    // count exceeds the device's 1-D dispatch ceiling (guaranteed floor 65535, i.e. >16.7Mpx on
    // Intel-class parts) would be undefined behaviour. Refuse honestly until those shaders are
    // grid-strided; NVIDIA/AMD report ~2^31 so real canvases never trip this there.
    if (((size_t)w * h + 255) / 256 > g_maxGroupsX) { teardown(); return 1061; }

    size_t npix = (size_t)w * h;
    VkDeviceSize tSize = npix * 4 * sizeof(float), wSize = npix * sizeof(float);
    const VkBufferUsageFlags dstStore = VK_BUFFER_USAGE_STORAGE_BUFFER_BIT | VK_BUFFER_USAGE_TRANSFER_DST_BIT;
    const VkMemoryPropertyFlags devLocal = VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT;
    // target/weight/canvas live in DEVICE_LOCAL memory (VRAM on a discrete GPU) so the
    // per-pixel eval loop reads them at full bandwidth, not over PCIe. canvas also needs
    // TRANSFER_SRC for readback. cands/out/staging stay host-visible (CPU-accessed).
    if (!createBufEx(tSize, dstStore, devLocal, false, g_target) ||
        !createBufEx(wSize, dstStore, devLocal, false, g_weight) ||
        !createBufEx(tSize, dstStore | VK_BUFFER_USAGE_TRANSFER_SRC_BIT, devLocal, false, g_canvas) ||
        !createHost((size_t)maxCands * 11 * sizeof(float), g_cands) ||
        !createHost((size_t)maxCands * 5 * sizeof(float), g_out) ||
        !createHost(tSize, g_staging) ||
        !createHost((size_t)gridSize * gridSize * sizeof(float), g_gridBuf) ||
        !createHost(12 * sizeof(float), g_best) ||
        !createHost(sizeof(GenCfg), g_genCfg) ||
        !createBufEx(8 * sizeof(float), dstStore, devLocal, false, g_kindsB) ||
        !createBufEx(8 * sizeof(float), dstStore, devLocal, false, g_kindcdf) ||
        !createBufEx((size_t)gridSize * gridSize * sizeof(float), dstStore, devLocal, false, g_gridcdf) ||
        !createBufEx(npix * sizeof(float), dstStore, devLocal, false, g_orient) ||
        !createBufEx(npix * sizeof(float), dstStore, devLocal, false, g_bound) ||
        !createBufEx(npix * sizeof(float), dstStore, devLocal, false, g_kgate) ||
        !createBufEx(npix * sizeof(float), dstStore, devLocal, false, g_rampglow) ||
        !createBufEx(npix * sizeof(float), dstStore, devLocal, false, g_coh) ||
        // Placeholders so every set can be written before a bank arrives; meta[0]=0 = no words.
        !createBufEx(16, dstStore, devLocal, false, g_maskAtlas) ||
        !createBufEx(16, dstStore, devLocal, false, g_maskMeta)) {
        g_lastError = 1020; teardown(); return g_lastError;
    }
    {
        int32_t zero[4] = {0, 0, 0, 0};
        memcpy(g_staging.map, zero, sizeof(zero)); copyBuf(g_staging.buf, g_maskMeta.buf, sizeof(zero));
    }
    g_masksOn = 0;
    g_searchCap = 0; g_hasOrient = 0; g_hasBound = 0; g_hasGate = 0; g_hasRampGlow = 0; g_hasCoh = 0; g_aspectCap = 0.f; g_searchSetsDirty = true;
    // upload target + weight; zero the canvas — all via the staging buffer.
    memcpy(g_staging.map, target, tSize); copyBuf(g_staging.buf, g_target.buf, tSize);
    memcpy(g_staging.map, weight, wSize); copyBuf(g_staging.buf, g_weight.buf, wSize);
    memset(g_staging.map, 0, tSize);      copyBuf(g_staging.buf, g_canvas.buf, tSize);
    writeDescriptors();
    return 0;
}

// fp_set_masks uploads the dictionary-word coverage atlas and its meta table, so the eval, apply
// and polish shaders can score and composite bank words. Layout mirrors the CUDA shim: the atlas is
// every word's coverage concatenated; meta is {count, (offset,w,h) x count}. Returns 0 on success.
API int fp_set_masks(const float* atlas, long long totalFloats, const int* meta, int count) {
    if (!g_device || !atlas || !meta || count < 1 || totalFloats < 1) return 1;
    destroyBuf(g_maskAtlas); destroyBuf(g_maskMeta);
    const VkBufferUsageFlags use = VK_BUFFER_USAGE_STORAGE_BUFFER_BIT | VK_BUFFER_USAGE_TRANSFER_DST_BIT;
    const VkMemoryPropertyFlags dl = VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT;
    size_t aSize = (size_t)totalFloats * sizeof(float);
    size_t mSize = (size_t)(1 + count * 3) * sizeof(int32_t);
    if (!createBufEx(aSize, use, dl, false, g_maskAtlas) || !createBufEx(mSize, use, dl, false, g_maskMeta)) {
        g_lastError = 1021; return 2;
    }
    if (!stagingReady(aSize > mSize ? aSize : mSize)) { g_lastError = 1060; return 3; }
    memcpy(g_staging.map, atlas, aSize); copyBuf(g_staging.buf, g_maskAtlas.buf, aSize);
    std::vector<int32_t> m(1 + (size_t)count * 3);
    m[0] = count;
    memcpy(m.data() + 1, meta, (size_t)count * 3 * sizeof(int32_t));
    memcpy(g_staging.map, m.data(), mSize); copyBuf(g_staging.buf, g_maskMeta.buf, mSize);
    g_masksOn = 1;
    writeDescriptors();      // the atlas handles changed: rebind eval/apply
    g_searchSetsDirty = true; // and the search-eval set
    writeTiledForwardDescriptors();
    writeBackwardDescriptors();
    return 0;
}

API int fp_masks_on() { return g_masksOn; }

API void fp_eval(const float* cands, int n, float* out) {
    g_profScope = PROF_EVAL;
    if (n <= 0 || !g_device) return;
    if (n > g_maxCands) n = g_maxCands;
    if (n > 65535) n = 65535; // one eval workgroup per candidate; clamp to the guaranteed dispatch limit (Intel iGPUs enforce exactly 65535)
    memcpy(g_cands.map, cands, (size_t)n * 11 * sizeof(float));

    EvalPC pc{n, g_w, g_h, g_sampleBudget, g_alphaGridN, {g_alphaGrid[0], g_alphaGrid[1], g_alphaGrid[2], g_alphaGrid[3], g_alphaGrid[4], g_alphaGrid[5]}, g_gradOn, 0};
    // One workgroup per candidate, chunked across submits when the measured cost says a single one
    // would approach the TDR watchdog. See evalChunk — chunk == n on a healthy card, so this is the
    // same single submit it has always been.
    int chunk = evalChunk(n);
    auto t0 = std::chrono::steady_clock::now();
    evalChunked(g_evalSet, pc, n, chunk);
    evalCost(std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count(), n);

    memcpy(out, g_out.map, (size_t)n * 5 * sizeof(float));
}

API void fp_apply(const float* cand) {
    g_profScope = PROF_APPLY;
    if (!g_device) return;
    int kind = (int)(cand[0] + 0.5f);
    applyDirty(kind, cand + 1);
    ApplyPC pc{kind, cand[1], cand[2], cand[3], cand[4], cand[5], cand[6],
               cand[7], cand[8], cand[9], cand[10], g_w, g_h};
    // The shader grid-strides the shape's OWN bbox pixels, so any dispatch size covers them all —
    // size it from the conservative host rect instead of the whole frame (a 30px shape on a 2000²
    // canvas used to launch 4M threads to write 900 pixels), and clamp to the guaranteed 65535
    // workgroup ceiling that a 4096² canvas would otherwise cross on Intel.
    uint32_t groups = (uint32_t)(((size_t)g_w * g_h + 255) / 256);
    int rx0, ry0, rx1, ry1;
    if (shapeRect(kind, cand + 1, rx0, ry0, rx1, ry1) && rx1 >= rx0 && ry1 >= ry0) {
        size_t area = (size_t)(rx1 - rx0 + 1) * (size_t)(ry1 - ry0 + 1);
        uint32_t g2 = (uint32_t)((area + 255) / 256);
        if (g2 < groups) groups = g2;
    }
    if (groups < 1) groups = 1;
    if (groups > 65535) groups = 65535;

    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_applyPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_applyPL, 0, 1, &g_applySet, 0, nullptr);
    vkCmdPushConstants(g_cmd, g_applyPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
    vkCmdDispatch(g_cmd, groups, 1, 1);
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
}

// fp_apply_batch composites n candidates (11-float wire format each) in ORDER with ONE fence per
// chunk instead of one per shape. A full-stack rerender (LOO rounds, pass gates, best-of restore)
// used to be ~1000 fenced submits; the profiler counted 12.9k apply fences in one default run.
// Dispatches, push constants, barriers and order are identical to n fp_apply calls — the compute
// →compute barrier between shapes carries the same RAW dependency the old per-call fence did.
API void fp_apply_batch(const float* cands, int n) {
    g_profScope = PROF_APPLY;
    if (!g_device || g_fatal || n < 1) return;
    const int CHUNK = 512; // TDR guard: bbox-sized dispatches are tiny, but never risk one giant submit
    for (int done = 0; done < n && !g_fatal; done += CHUNK) {
        int m = n - done; if (m > CHUNK) m = CHUNK;
        vkResetCommandBuffer(g_cmd, 0);
        VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
        bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
        vkBeginCommandBuffer(g_cmd, &bi);
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_applyPipe);
        vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_applyPL, 0, 1, &g_applySet, 0, nullptr);
        for (int i = done; i < done + m; i++) {
            const float* cand = cands + (size_t)i * 11;
            int kind = (int)(cand[0] + 0.5f);
            applyDirty(kind, cand + 1);
            ApplyPC pc{kind, cand[1], cand[2], cand[3], cand[4], cand[5], cand[6],
                       cand[7], cand[8], cand[9], cand[10], g_w, g_h};
            uint32_t groups = (uint32_t)(((size_t)g_w * g_h + 255) / 256);
            int rx0, ry0, rx1, ry1;
            if (shapeRect(kind, cand + 1, rx0, ry0, rx1, ry1) && rx1 >= rx0 && ry1 >= ry0) {
                size_t area = (size_t)(rx1 - rx0 + 1) * (size_t)(ry1 - ry0 + 1);
                uint32_t g2 = (uint32_t)((area + 255) / 256);
                if (g2 < groups) groups = g2;
            }
            if (groups < 1) groups = 1;
            if (groups > 65535) groups = 65535;
            vkCmdPushConstants(g_cmd, g_applyPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
            vkCmdDispatch(g_cmd, groups, 1, 1);
            // Each shape composites over the previous one's writes — the same dependency the old
            // per-call fence enforced, now an in-buffer barrier. Last shape: flushBarrier below.
            if (i != done + m - 1) cmdBarrierRW();
        }
        flushBarrier();
        vkEndCommandBuffer(g_cmd);
        submitWait();
    }
}

API void fp_read_canvas(float* dst) {
    g_profScope = PROF_READCANVAS;
    if (!g_device) return;
    VkDeviceSize sz = (size_t)g_w * g_h * 4 * sizeof(float);
    copyBuf(g_canvas.buf, g_staging.buf, sz); // device-local -> staging
    memcpy(dst, g_staging.map, sz);
}

API void fp_error_grid(float* out) {
    g_profScope = PROF_GRID;
    if (!g_device) return;
    int gw = g_grid, gh = g_grid;
    // Cell range from the dirty pixel rect, widened one cell each side (the pixel->cell floor
    // mapping straddles boundaries). A clean grid skips the dispatch and hands back the cache.
    int cx0 = 0, cy0 = 0, cx1 = gw - 1, cy1 = gh - 1;
    if (!g_dirtyFull) {
        if (g_dirtyX1 < g_dirtyX0) {
            memcpy(out, g_gridBuf.map, (size_t)gw * gh * sizeof(float));
            return;
        }
        cx0 = g_dirtyX0 * gw / g_w - 1; if (cx0 < 0) cx0 = 0;
        cy0 = g_dirtyY0 * gh / g_h - 1; if (cy0 < 0) cy0 = 0;
        cx1 = g_dirtyX1 * gw / g_w + 1; if (cx1 > gw - 1) cx1 = gw - 1;
        cy1 = g_dirtyY1 * gh / g_h + 1; if (cy1 > gh - 1) cy1 = gh - 1;
    }
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_gridPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_gridPL, 0, 1, &g_gridSet, 0, nullptr);
    GridPC pc{g_w, g_h, gw, gh, cx0, cy0, cx1, cy1};
    vkCmdPushConstants(g_cmd, g_gridPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
    vkCmdDispatch(g_cmd, (uint32_t)(gw * gh), 1, 1); // one workgroup per cell; clean cells return at once
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
    // The dirty rect is consumed only AFTER a successful submit: clearing it up front meant a
    // failed dispatch (device loss) silently froze those cells stale forever.
    if (g_fatal) return; // the dispatch died: g_gridBuf holds the PREVIOUS grid, and handing that
                         // back reads to the caller as a fresh measurement of a canvas it never saw
    g_dirtyFull = false;
    g_dirtyX0 = g_dirtyY0 = 0; g_dirtyX1 = g_dirtyY1 = -1;
    memcpy(out, g_gridBuf.map, (size_t)gw * gh * sizeof(float));
}

API void fp_set_orient(const float* orient) {
    if (!g_device) return;
    size_t sz = (size_t)g_w * g_h * sizeof(float);
    if (!stagingReady(sz)) return;
    memcpy(g_staging.map, orient, sz);
    copyBuf(g_staging.buf, g_orient.buf, sz);
    g_hasOrient = 1;
}

API void fp_set_boundary_dist(const float* dist) {
    if (!g_device) return;
    if (!dist) { g_hasBound = 0; return; }
    size_t sz = (size_t)g_w * g_h * sizeof(float);
    if (!stagingReady(sz)) return;
    memcpy(g_staging.map, dist, sz);
    copyBuf(g_staging.buf, g_bound.buf, sz);
    g_hasBound = 1;
}

// fp_set_kind_gate uploads the per-pixel region-kinds gate (metric.HardEdgeMap): the on-device
// generators draw the full kind pool with probability gate[centre] and force ellipse otherwise.
// NULL clears it (gate off). Mirrors shim.cu.
// fp_set_glow_swap sets the deep-smooth glow-swap pair (tau, prob): gate cells with hard<tau swap
// their forced ellipse for a RIMLESS glow with probability prob (the generation-side patchwork
// kill — an ellipse rim in a structureless zone is a luminance step; a glow's rim does not exist).
API void fp_set_glow_swap(const float* tauProb) {
    if (!tauProb) { g_glowTau = 0.f; g_glowProb = 0.f; return; }
    g_glowTau = tauProb[0];
    g_glowProb = tauProb[1];
}

// fp_set_alpha_grid installs (or clears: vals NULL / n<=0) the analytic-alpha grid carried to
// eval.comp via push constants: the epilogue re-solves the optimal colour per grid alpha and the
// ΔSSE-min (alpha, colour) pair wins. Mirrors shim.cu (grid capped at 6 here — the PC block size).
API void fp_set_alpha_grid(const float* vals, int n) {
    if (!vals || n < 0) n = 0;
    if (n > 6) n = 6;
    for (int i = 0; i < 6; i++) g_alphaGrid[i] = (i < n) ? vals[i] : 0.f;
    g_alphaGridN = n;
}

// ---------------------------------------------------------------------------------------------
// Neural candidate proposer
// ---------------------------------------------------------------------------------------------

// buildProposerPipelines creates the four pipelines the network needs, lazily on the first upload
// so a run that never enables the proposer pays nothing.
static bool buildProposerPipelines() {
    if (g_pcPipe) return true;
    auto dsl = [](uint32_t count, VkDescriptorSetLayout& out) -> bool {
        std::vector<VkDescriptorSetLayoutBinding> bs(count);
        for (uint32_t i = 0; i < count; i++) {
            bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
            bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
        }
        VkDescriptorSetLayoutCreateInfo ci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
        ci.bindingCount = count; ci.pBindings = bs.data();
        return vkCreateDescriptorSetLayout(g_device, &ci, nullptr, &out) == VK_SUCCESS;
    };
    auto pl = [](VkDescriptorSetLayout d, uint32_t pcSize, VkPipelineLayout& out) -> bool {
        VkPushConstantRange pr{VK_SHADER_STAGE_COMPUTE_BIT, 0, pcSize};
        VkPipelineLayoutCreateInfo ci{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO};
        ci.setLayoutCount = 1; ci.pSetLayouts = &d; ci.pushConstantRangeCount = 1; ci.pPushConstantRanges = &pr;
        return vkCreatePipelineLayout(g_device, &ci, nullptr, &out) == VK_SUCCESS;
    };
    auto pipe = [](VkPipelineLayout layout, const uint32_t* spv, size_t bytes, VkPipeline& out) -> bool {
        VkShaderModule sm = loadShader(spv, bytes);
        if (!sm) return false;
        VkComputePipelineCreateInfo ci{VK_STRUCTURE_TYPE_COMPUTE_PIPELINE_CREATE_INFO};
        ci.stage.sType = VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO;
        ci.stage.stage = VK_SHADER_STAGE_COMPUTE_BIT; ci.stage.module = sm; ci.stage.pName = "main";
        ci.layout = layout;
        VkResult r = vkCreateComputePipelines(g_device, VK_NULL_HANDLE, 1, &ci, nullptr, &out);
        vkDestroyShaderModule(g_device, sm, nullptr);
        return r == VK_SUCCESS;
    };
    if (!dsl(4, g_pcDSL) || !dsl(3, g_piDSL) || !dsl(10, g_phDSL) || !dsl(1, g_poDSL)) return false;
    if (!pl(g_pcDSL, sizeof(ConvPC), g_pcPL) || !pl(g_piDSL, sizeof(PropInPC), g_piPL) ||
        !pl(g_phDSL, sizeof(PropHeadPC), g_phPL) || !pl(g_poDSL, sizeof(PropOrPC), g_poPL)) return false;
    if (!pipe(g_pcPL, conv3x3_spv, sizeof(conv3x3_spv), g_pcPipe) ||
        !pipe(g_piPL, prop_input_spv, sizeof(prop_input_spv), g_piPipe) ||
        !pipe(g_poPL, prop_orient_spv, sizeof(prop_orient_spv), g_poPipe) ||
        !pipe(g_phPL, prop_head_spv, sizeof(prop_head_spv), g_phPipe)) return false;

    return true;
}

// ensureProposerSets allocates ONE descriptor set per trunk layer plus the three fixed ones. Called
// from fp_set_proposer, where the layer count is finally known; the pool is rebuilt whenever that
// count changes and is destroyed with the device.
static bool ensureProposerSets(size_t layers) {
    if (g_propDPool != VK_NULL_HANDLE && g_pcSets.size() == layers) return true;
    if (g_propDPool != VK_NULL_HANDLE) {
        vkDestroyDescriptorPool(g_device, g_propDPool, nullptr); // frees every set allocated from it
        g_propDPool = VK_NULL_HANDLE;
    }
    g_pcSets.clear();
    g_piSet = g_phSet = g_poSet = VK_NULL_HANDLE;
    uint32_t nSets = (uint32_t)layers + 3;
    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, (uint32_t)(layers * 4 + 3 + 10 + 1)};
    VkDescriptorPoolCreateInfo dpci{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO};
    dpci.maxSets = nSets; dpci.poolSizeCount = 1; dpci.pPoolSizes = &ps;
    if (vkCreateDescriptorPool(g_device, &dpci, nullptr, &g_propDPool) != VK_SUCCESS) { g_propDPool = VK_NULL_HANDLE; return false; }
    g_pcSets.resize(layers, VK_NULL_HANDLE);
    for (size_t i = 0; i < layers; i++) {
        VkDescriptorSetAllocateInfo ai{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
        ai.descriptorPool = g_propDPool; ai.descriptorSetCount = 1; ai.pSetLayouts = &g_pcDSL;
        if (vkAllocateDescriptorSets(g_device, &ai, &g_pcSets[i]) != VK_SUCCESS) return false;
    }
    VkDescriptorSetLayout ls[3] = { g_piDSL, g_phDSL, g_poDSL };
    VkDescriptorSet* ds[3] = { &g_piSet, &g_phSet, &g_poSet };
    for (int i = 0; i < 3; i++) {
        VkDescriptorSetAllocateInfo ai{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
        ai.descriptorPool = g_propDPool; ai.descriptorSetCount = 1; ai.pSetLayouts = &ls[i];
        if (vkAllocateDescriptorSets(g_device, &ai, ds[i]) != VK_SUCCESS) return false;
    }
    return true;
}

// proposerTeardown destroys everything buildProposerPipelines and ensureProposerSets created.
// It used to destroy NOTHING — the descriptor pool was a never-stored local and the pipelines,
// layouts and DSLs outlived vkDestroyDevice, so a second run in one process (the studio, engined)
// bound handles from a dead device.
void proposerTeardown() {
    if (!g_device) { g_pcSets.clear(); g_propDPool = VK_NULL_HANDLE; return; }
    if (g_propDPool) { vkDestroyDescriptorPool(g_device, g_propDPool, nullptr); g_propDPool = VK_NULL_HANDLE; }
    g_pcSets.clear();
    g_piSet = g_phSet = g_poSet = VK_NULL_HANDLE;
    if (g_pcPipe) { vkDestroyPipeline(g_device, g_pcPipe, nullptr); g_pcPipe = VK_NULL_HANDLE; }
    if (g_piPipe) { vkDestroyPipeline(g_device, g_piPipe, nullptr); g_piPipe = VK_NULL_HANDLE; }
    if (g_phPipe) { vkDestroyPipeline(g_device, g_phPipe, nullptr); g_phPipe = VK_NULL_HANDLE; }
    if (g_poPipe) { vkDestroyPipeline(g_device, g_poPipe, nullptr); g_poPipe = VK_NULL_HANDLE; }
    if (g_pcPL) { vkDestroyPipelineLayout(g_device, g_pcPL, nullptr); g_pcPL = VK_NULL_HANDLE; }
    if (g_piPL) { vkDestroyPipelineLayout(g_device, g_piPL, nullptr); g_piPL = VK_NULL_HANDLE; }
    if (g_phPL) { vkDestroyPipelineLayout(g_device, g_phPL, nullptr); g_phPL = VK_NULL_HANDLE; }
    if (g_poPL) { vkDestroyPipelineLayout(g_device, g_poPL, nullptr); g_poPL = VK_NULL_HANDLE; }
    if (g_pcDSL) { vkDestroyDescriptorSetLayout(g_device, g_pcDSL, nullptr); g_pcDSL = VK_NULL_HANDLE; }
    if (g_piDSL) { vkDestroyDescriptorSetLayout(g_device, g_piDSL, nullptr); g_piDSL = VK_NULL_HANDLE; }
    if (g_phDSL) { vkDestroyDescriptorSetLayout(g_device, g_phDSL, nullptr); g_phDSL = VK_NULL_HANDLE; }
    if (g_poDSL) { vkDestroyDescriptorSetLayout(g_device, g_poDSL, nullptr); g_poDSL = VK_NULL_HANDLE; }
}

void freeProposer() {
    for (auto& l : g_propLayers) { destroyBuf(l.w); destroyBuf(l.b); }
    g_propLayers.clear();
    destroyBuf(g_propIn); destroyBuf(g_propA); destroyBuf(g_propB);
    destroyBuf(g_propMap); destroyBuf(g_propKinds);
    destroyBuf(g_pMixW); destroyBuf(g_pMixB); destroyBuf(g_pGeoW); destroyBuf(g_pGeoB);
    destroyBuf(g_pAlpW); destroyBuf(g_pAlpB); destroyBuf(g_pCnfW); destroyBuf(g_pCnfB);
    g_hasProposer = 0; g_propOn = 0; g_propChan = 0; g_propInC = 6; g_propHasConf = 0; g_propW = g_propH = 0;
}

// uploadTo allocates a device buffer of n floats and fills it from src.
static bool uploadTo(const float* src, size_t n, Buf& dst) {
    size_t bytes = n * sizeof(float);
    if (!createHost(bytes, dst)) return false;
    memcpy(dst.map, src, bytes);
    return true;
}

// fp_set_proposer installs the trained network from the flat blob written by export_weights.py.
// blob == NULL clears it. The layout is deliberately dumb (a header then tensors back to back) so
// there is no parser here to disagree with the exporter; debug/cmd/propcheck reads the same file
// with an independent implementation and matches torch to 1e-6, which is what pins the contract.
API int fp_set_proposer(const void* blob, int bytes) {
    if (!g_device) return 0;
    freeProposer();
    if (!blob || bytes <= 24) return 1;
    const unsigned char* p = (const unsigned char*)blob;
    if (memcmp(p, "FH6P", 4) != 0) return 0;
    size_t off = 4;
    auto i32 = [&]() -> int32_t { int32_t v; memcpy(&v, p + off, 4); off += 4; return v; };
    int ver = i32();
    if (ver < 1 || ver > 3) return 0;
    int width = i32();
    g_propHeads = i32();
    int layers = i32();
    g_propPatchSrc = i32();
    g_propPool = i32();
    if (ver >= 2) { g_propCtxSrc = i32(); g_propInDim = i32(); }
    (void)width;
    std::vector<float> kinds(g_propHeads);
    for (int i = 0; i < g_propHeads; i++) kinds[i] = (float)i32();
    if (!uploadTo(kinds.data(), kinds.size(), g_propKinds)) return 0;

    for (int l = 0; l < layers; l++) {
        PropLayer pl{};
        pl.inC = i32(); pl.outC = i32(); pl.stride = i32();
        if (l == 0) g_propInC = pl.inC;
        size_t wn = (size_t)pl.outC * pl.inC * 9;
        if (!uploadTo((const float*)(p + off), wn, pl.w)) return 0;
        off += wn * 4;
        if (!uploadTo((const float*)(p + off), (size_t)pl.outC, pl.b)) return 0;
        off += (size_t)pl.outC * 4;
        g_propLayers.push_back(pl);
        g_propChan = pl.outC;
    }
    // prop_head.comp holds the pooled and mixed vectors in fixed float[192] arrays and silently
    // min()s the channel count against 192 — a wider trunk would have been truncated there while
    // the head's weight strides still assumed the full width, so every value after the first row
    // would be read from the wrong offset. Refuse the blob instead.
    if (g_propChan > 192) { freeProposer(); return 0; }
    // Four linear heads since the confidence head was added; a blob that predates it (three) is
    // rejected by the trailing-bytes check below rather than read as garbage.
    struct { Buf* w; Buf* b; } lin[4] = { {&g_pMixW, &g_pMixB}, {&g_pGeoW, &g_pGeoB},
                                          {&g_pAlpW, &g_pAlpB}, {&g_pCnfW, &g_pCnfB} };
    // v3 added the confidence head. Older exports stay loadable and simply have no opinion about
    // where they are useful: the buffers are zero-filled, so the head emits 0 and the learned gate
    // refuses to turn on rather than gating on uninitialised memory.
    g_propHasConf = (ver >= 3) ? 1 : 0;
    int nlin = g_propHasConf ? 4 : 3;
    for (int i = 0; i < nlin; i++) {
        int in = i32(), out = i32();
        if (!uploadTo((const float*)(p + off), (size_t)in * out, *lin[i].w)) return 0;
        off += (size_t)in * out * 4;
        if (!uploadTo((const float*)(p + off), (size_t)out, *lin[i].b)) return 0;
        off += (size_t)out * 4;
    }
    if (!g_propHasConf) {
        std::vector<float> z((size_t)g_propHeads * 4096, 0.f);
        if (!uploadTo(z.data(), z.size(), g_pCnfW)) return 0;
        if (!uploadTo(z.data(), (size_t)g_propHeads, g_pCnfB)) return 0;
    }
    if ((int)off != bytes) { freeProposer(); return 0; }
    if (!buildProposerPipelines()) { freeProposer(); return 0; }

    // Feature buffers are sized for the worst case: the first layer's output at half resolution with
    // the widest channel count of any layer.
    // The network sees the canvas reduced by the same factor its training windows were: a context
    // window of g_propCtxSrc source pixels became g_propInDim network pixels, on canvases whose short
    // side was g_propTrainDim. Anything else is a scale mismatch -- measured as both wrong extents and
    // a sweep costing the square of the error in time.
    float ctxCanvas = (float)g_propCtxSrc * (float)std::min(g_w, g_h) / (float)g_propTrainDim;
    g_propScale = std::max(1.0f, ctxCanvas / (float)g_propInDim);
    g_propNW = std::max(16, (int)((float)g_w / g_propScale));
    g_propNH = std::max(16, (int)((float)g_h / g_propScale));
    size_t npix = (size_t)g_propNW * g_propNH;
    size_t widest = 0;
    for (auto& l : g_propLayers) widest = std::max(widest, (size_t)l.outC);
    if (g_propInC != 6 && g_propInC != 8) { freeProposer(); return 0; }
    if (!createHost(npix * (size_t)g_propInC * sizeof(float), g_propIn)) return 0;
    size_t featMax = (npix + 64) * widest * sizeof(float);
    if (!createHost(featMax, g_propA) || !createHost(featMax, g_propB)) return 0;
    int fw = g_propNW, fh = g_propNH;
    for (auto& l : g_propLayers) { fw = (fw + l.stride - 1) / l.stride; fh = (fh + l.stride - 1) / l.stride; }
    g_propW = std::max(1, fw - g_propPool + 1);
    g_propH = std::max(1, fh - g_propPool + 1);
    if (!createHost((size_t)g_propW * g_propH * g_propHeads * 8 * sizeof(float), g_propMap)) return 0;
    // One descriptor set per trunk layer — only knowable here, where the blob has been parsed.
    if (!ensureProposerSets(g_propLayers.size())) { freeProposer(); return 0; }
    g_hasProposer = 1;
    g_searchSetsDirty = true;   // the generator's proposal bindings now point at real buffers
    return 1;
}

// writeSet points a descriptor set's bindings at the given buffers.
static void writeSet(VkDescriptorSet set, std::initializer_list<Buf*> bufs) {
    std::vector<VkWriteDescriptorSet> w;
    std::vector<VkDescriptorBufferInfo> bi(bufs.size());
    uint32_t i = 0;
    for (Buf* b : bufs) {
        bi[i] = {b->buf, 0, b->size};
        VkWriteDescriptorSet ws{VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        ws.dstSet = set; ws.dstBinding = i; ws.descriptorCount = 1;
        ws.descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; ws.pBufferInfo = &bi[i];
        w.push_back(ws);
        i++;
    }
    vkUpdateDescriptorSets(g_device, (uint32_t)w.size(), w.data(), 0, nullptr);
}

// fp_run_proposer refreshes the proposal map from the CURRENT canvas. It is a whole-canvas sweep of
// the trunk plus the head, and the caller decides how often to pay for it: the canvas barely changes
// between adjacent steps, so refreshing every N shapes amortises the cost to nothing next to the
// candidate scoring it replaces (measured at ~5 ms per sweep against ~36 s of scoring per run).
API int fp_run_proposer(float progress) {
    if (!g_device || !g_hasProposer) return 0;
    g_propProgress = progress;
    if (g_pcSets.size() != g_propLayers.size()) return 0; // sets and layers must be 1:1
    // EVERY descriptor write happens BEFORE recording starts. Updating a set that a recorded (let
    // alone submitted) command buffer binds is undefined, and doing it mid-recording is what made
    // the trunk run six layers off two alternating sets.
    writeSet(g_piSet, {&g_target, &g_canvas, &g_propIn});
    if (g_propInC > 6) writeSet(g_poSet, {&g_propIn});
    {
        Buf* s = &g_propIn;
        Buf* d = &g_propA;
        for (size_t l = 0; l < g_propLayers.size(); l++) {
            writeSet(g_pcSets[l], {s, d, &g_propLayers[l].w, &g_propLayers[l].b});
            s = d;
            d = (d == &g_propA) ? &g_propB : &g_propA;
        }
        writeSet(g_phSet, {s, &g_pMixW, &g_pMixB, &g_pGeoW, &g_pGeoB, &g_pAlpW, &g_pAlpB, &g_propMap,
                           &g_pCnfW, &g_pCnfB});
    }

    beginCmd();

    PropInPC ipc{ g_w, g_h, g_propNW, g_propNH, g_propScale };
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_piPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_piPL, 0, 1, &g_piSet, 0, nullptr);
    vkCmdPushConstants(g_cmd, g_piPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(ipc), &ipc);
    vkCmdDispatch(g_cmd, (uint32_t)((g_propNW + 15) / 16), (uint32_t)((g_propNH + 15) / 16), 1);
    cmdBarrierRW();

    // Orientation planes, when the trunk was trained with them. Same buffer in and out: the pass
    // reads the target planes and writes past the six prop_input filled, so there is no aliasing
    // beyond the barrier already placed above.
    if (g_propInC > 6) {
        PropOrPC opc{ g_propNW, g_propNH };
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_poPipe);
        vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_poPL, 0, 1, &g_poSet, 0, nullptr);
        vkCmdPushConstants(g_cmd, g_poPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(opc), &opc);
        vkCmdDispatch(g_cmd, (uint32_t)((g_propNW + 15) / 16), (uint32_t)((g_propNH + 15) / 16), 1);
        cmdBarrierRW();
    }

    int inW = g_propNW, inH = g_propNH;
    Buf* src = &g_propIn;
    Buf* dst = &g_propA;
    for (size_t l = 0; l < g_propLayers.size(); l++) {
        PropLayer& pl = g_propLayers[l];
        int outW = (inW + pl.stride - 1) / pl.stride;
        int outH = (inH + pl.stride - 1) / pl.stride;
        ConvPC cpc{ pl.inC, pl.outC, inW, inH, outW, outH, pl.stride, 1 };
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pcPipe);
        vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pcPL, 0, 1, &g_pcSets[l], 0, nullptr);
        vkCmdPushConstants(g_cmd, g_pcPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(cpc), &cpc);
        vkCmdDispatch(g_cmd, (uint32_t)((outW + 15) / 16), (uint32_t)((outH + 15) / 16), (uint32_t)pl.outC);
        cmdBarrierRW();
        inW = outW; inH = outH;
        src = dst;
        dst = (dst == &g_propA) ? &g_propB : &g_propA;
    }

    PropHeadPC hpc{ g_propChan, inW, inH, g_propW, g_propH, g_propHeads, g_propPool, g_propProgress };
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_phPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_phPL, 0, 1, &g_phSet, 0, nullptr);
    vkCmdPushConstants(g_cmd, g_phPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(hpc), &hpc);
    vkCmdDispatch(g_cmd, (uint32_t)((g_propW + 7) / 8), (uint32_t)((g_propH + 7) / 8), 1);
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
    return 1;
}

// fp_proposer_map copies the proposal map out for the golden diff. Test-only: the generator reads
// the device buffer directly.
API int fp_proposer_map(float* out, int maxFloats) {
    if (!g_hasProposer || !out) return 0;
    int n = g_propW * g_propH * g_propHeads * 8;
    if (n > maxFloats) return 0;
    memcpy(out, g_propMap.map, (size_t)n * sizeof(float));
    return n;
}

API void fp_proposer_dims(int* out4) {
    if (!out4) return;
    out4[0] = g_propW; out4[1] = g_propH; out4[2] = g_propHeads; out4[3] = g_propPatchSrc;
}

// fp_set_proposer_enabled turns the installed network on or off for the run, and sets the progress
// value the head layer folds in (the candidate distribution moves through a run, so the network is
// told where in it we are).
API void fp_set_proposer_enabled(int on, float progress, float frac, float jitter) {
    g_propOn = (on && g_hasProposer) ? 1 : 0;
    g_propProgress = progress;
    if (frac >= 0.f && frac <= 1.f) g_propFrac = frac;
    if (jitter >= 0.f) g_propJitter = jitter;
}

// fp_set_proposer_gate selects WHICH gate decides where proposals are used: 0 keeps the hand-made
// region gate, 1 hands the decision to the network's own confidence head. Ignored when the installed
// blob predates that head, so an old model cannot be gated on zeros.
API void fp_set_proposer_gate(int mode, float tau) { g_propConfGate = mode ? 1 : 0; g_propConfTau = tau; }

// fp_set_coherence uploads the structure tensor's per-pixel COHERENCE (len w*h) plus the maximum
// aspect ratio at full coherence. The generator already seeds each candidate's ANGLE from the
// orientation map; this supplies the missing half — how confident that angle is, and therefore how
// elongated the candidate should be. NULL clears it (the preset's single global aspect applies
// everywhere again). Deliberately its own buffer rather than values packed into the orientation
// map: one buffer owning two unrelated meanings is what hid the gradient-scoring bug for months.
// params[0] = the maximum aspect ratio at full coherence. Passed by POINTER, not by value: this
// entry point is called through Go's syscall bridge, which places every argument in an integer
// register, while the Windows x64 ABI passes a float parameter in XMM. A by-value float would
// therefore arrive as garbage.
API void fp_set_coherence(const float* coh, const float* params) {
    if (!g_device) return;
    float aspectCap = params ? params[0] : 0.f;
    if (!coh || aspectCap <= 1.f) { g_hasCoh = 0; g_aspectCap = 0.f; return; }
    size_t sz = (size_t)g_w * g_h * sizeof(float);
    if (!stagingReady(sz)) return;
    memcpy(g_staging.map, coh, sz);
    copyBuf(g_staging.buf, g_coh.buf, sz);
    g_hasCoh = 1;
    g_aspectCap = aspectCap;
}

API void fp_set_kind_gate(const float* hard) {
    if (!g_device) return;
    if (!hard) { g_hasGate = 0; return; }
    size_t sz = (size_t)g_w * g_h * sizeof(float);
    if (!stagingReady(sz)) return;
    memcpy(g_staging.map, hard, sz);
    copyBuf(g_staging.buf, g_kgate.buf, sz);
    g_hasGate = 1;
}

// fp_set_ramp_glow uploads the per-pixel smooth-gradient map (metric.RampMap, len w*h) and its
// hot-glow params (params = {thresh, tau, prob}): where rampGlow[centre] > thresh the deep-smooth
// glow swap runs at the hotter (tau, prob) instead of the global pair. NULL ramp clears it (falls
// back to the global glow swap everywhere). Mirrors shim.cu fp_set_ramp_glow; rides fp_set_kind_gate.
API void fp_set_ramp_glow(const float* ramp, const float* params) {
    if (!g_device) return;
    if (!ramp) { g_hasRampGlow = 0; g_rampGlowThresh = g_rampGlowTau = g_rampGlowProb = 0.f; return; }
    size_t sz = (size_t)g_w * g_h * sizeof(float);
    if (!stagingReady(sz)) return;
    memcpy(g_staging.map, ramp, sz);
    copyBuf(g_staging.buf, g_rampglow.buf, sz);
    g_hasRampGlow = 1;
    g_rampGlowThresh = params[0];
    g_rampGlowTau = params[1];
    g_rampGlowProb = params[2];
}

// fp_set_big_glow sets the SIZE-conditioned glow swap (params = {tau, prob, kinds}): a candidate
// whose size exceeds tau*min(W,H) becomes a rimless glow with probability prob. Independent of the
// hardness gate — a large shape is approximating broad shading whatever the surrounding structure,
// and its rim is the long low-contrast contour the eye reads as a standout oval. kinds 0 = ellipses
// only (their wire params already ARE a glow's), 1 = rects and triangles too (a rect is a free
// rewrite; a triangle is re-emitted as the glow inscribed in its vertex box). prob 0 disables.
API void fp_set_big_glow(const float* params) {
    if (!params) { g_bigGlowTau = 0.f; g_bigGlowProb = 0.f; g_bigGlowKinds = 0; g_bigGlowKind = 4; return; }
    g_bigGlowTau = params[0];
    g_bigGlowProb = params[1];
    g_bigGlowKinds = (int)params[2];
    g_bigGlowKind = (int)params[3];
    if (g_bigGlowKind != 4 && g_bigGlowKind != 5) g_bigGlowKind = 4;
}

// fp_search_random: generate n candidates on-device (seeded RNG + error-grid CDF + kind
// weighting), score them, apply the compactness penalty, and argmin — all in one submit,
// returning the single best candidate in out_best[12]. ip/fp carry the scalars (the syscall
// ABI can't pass float registers). Mirrors shim.cu fp_search_random (the simple path; the
// coarse-to-fine filter is a later optimisation).
API void fp_search_random(unsigned long long seed, const int* ip, const float* fp,
                          const float* kinds, const float* kindCDF, const float* gridCDF, float* out_best) {
    g_profScope = PROF_SEARCH;
    int n = ip[0], nKinds = ip[1], gw = ip[2], gh = ip[3];
    int compact = ip[4], shapeCount = ip[5], allowAlpha = ip[6];
    // g_fatal: after a device loss g_best still holds the LAST GOOD argmin result — returning it
    // would hand the greedy a plausible stale winner it then places over and over. FLT_MAX = fail.
    if (!g_device || g_fatal || n < 1 || nKinds < 1) { out_best[0] = 3.4028235e38f; return; }
    if (n > 65535) n = 65535; // one eval workgroup per candidate; clamp to the dispatch limit
    g_survN = 0; // a failed or non-coarse search must not leave the previous pool readable
    if (!ensureSearch(n)) { out_best[0] = 3.4028235e38f; return; }
    // upload kinds / kindCDF / gridCDF — recorded at the head of the SEARCH submit rather than
    // fenced on their own. This runs once per PLACED SHAPE, and the kind table and its CDF are
    // the same handful of floats every time — but the grid CDF is not, so the batch still has to go.
    size_t szKi = (size_t)nKinds * sizeof(float), szG = (size_t)gw * gh * sizeof(float);
    VkDeviceSize o1 = 0, o2 = stageAlign(szKi), o3 = stageAlign(o2 + szKi);
    ensureStaging(o3 + szG);
    {
        char* base = (char*)g_staging.map;
        memcpy(base + o1, kinds, szKi);
        memcpy(base + o2, kindCDF, szKi);
        memcpy(base + o3, gridCDF, szG);
    }
    const StageCopy ups[3] = {{o1, g_kindsB.buf, szKi}, {o2, g_kindcdf.buf, szKi}, {o3, g_gridcdf.buf, szG}};

    // TDR guard, byte-identical: see evalChunk. A fast card measures chunk >= n and records the
    // whole search in ONE submit exactly as before; a slow one pays extra fences instead of a
    // device reset.
    int chunk = evalChunk(n);
    bool split = chunk < n;
    auto tSearch = std::chrono::steady_clock::now();

    beginCmd();
    cmdStageCopies(ups, 3);
    // 1. generate
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_genPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_genPL, 0, 1, &g_genSet, 0, nullptr);
    // The proposal map is a stride-8 grid over the canvas, and its geometry units are relative to a
    // patch of propPatchSrc pixels AT THE RESOLUTION THE NETWORK WAS TRAINED ON. The studio fits at
    // whatever the source happens to be, so the patch is rescaled with the canvas rather than pinned
    // to a pixel count that would mean a different fraction of the frame on every image.
    // The patch the network's geometry refers to, in canvas pixels. It must keep the same FRACTION
    // of the frame it had in training, not a fixed pixel count: training ran patch_src pixels on
    // canvases whose short side was g_propTrainDim, so anything else hands the network a window of a
    // different relative size and every extent it predicts comes out at the wrong scale -- which also
    // costs time, since scoring a candidate is per covered pixel.
    float propPatch = (float)g_propPatchSrc * (float)std::min(g_w, g_h) / (float)g_propTrainDim;
    // The proposer scalars travel via the GenCfg SSBO (see GenPC's size cap). Refreshed on every
    // search call — 44 host bytes, and the submit below is what makes them device-visible.
    GenCfg gcfg{g_propOn, g_propW, g_propH, g_propHeads, (g_propPool - 1) / 2,
                (g_propConfGate && g_propHasConf) ? 1 : 0,
                g_propFrac, propPatch, g_propJitter, 8.0f * g_propScale, g_propConfTau};
    memcpy(g_genCfg.map, &gcfg, sizeof(gcfg));
    GenPC gpc{(uint32_t)seed, (uint32_t)(seed >> 32), n, nKinds, gw, gh, g_w, g_h, allowAlpha, g_hasOrient, g_hasBound, g_hasGate, g_hasRampGlow, g_bigGlowKinds, g_bigGlowKind, g_hasCoh,
              fp[0], fp[1], fp[2], fp[3], fp[4], fp[5], g_glowTau, g_glowProb, g_rampGlowThresh, g_rampGlowTau, g_rampGlowProb, g_bigGlowTau, g_bigGlowProb, g_aspectCap};
    vkCmdPushConstants(g_cmd, g_genPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(gpc), &gpc);
    vkCmdDispatch(g_cmd, (uint32_t)((n + 255) / 256), 1, 1);
    cmdBarrierRW();
    // 2. score (reuse eval pipeline on the search buffers). With the coarse filter on, this first
    // pass runs at the CHEAP pixel cap; the partition argmin keeps kpart survivors and only those
    // pay the full budget below — the winner is always full-budget scored.
    bool useCoarse = g_coarseOn && g_scand2.buf != VK_NULL_HANDLE && n > 4 * g_kpart;
    int budget1 = useCoarse ? (g_coarseBudget < g_sampleBudget ? g_coarseBudget : g_sampleBudget) : g_sampleBudget;
    // g_gradOn belongs here too. Leaving it off aggregate-initialised it to 0, so fp_set_gradients
    // reached the host Evaluate path and NOT the on-device search — the two halves of one run scored
    // gradients differently, and every A/B ever run with the honest-gradient flag moved only half the
    // system. The shipped default is 0, so this is inert until the flag is set.
    EvalPC epc{n, g_w, g_h, budget1, g_alphaGridN, {g_alphaGrid[0], g_alphaGrid[1], g_alphaGrid[2], g_alphaGrid[3], g_alphaGrid[4], g_alphaGrid[5]}, g_gradOn, 0};
    if (split) {
        endSubmit();
        evalChunked(g_sevalSet, epc, n, chunk);
    } else {
        // 2. score (reuse eval pipeline on the search buffers). With the coarse filter on, this
        // first pass runs at the CHEAP pixel cap; the partition argmin keeps kpart survivors and
        // only those pay the full budget below — the winner is always full-budget scored.
        cmdEvalRange(g_sevalSet, epc, 0, n);
        cmdBarrierRW();
    }
    if (g_fatal) { out_best[0] = 3.4028235e38f; return; } // an eval chunk died: nothing left to reduce
    // 3. selection-adjusted score, then 4. argmin (or the coarse refine)
    searchTail(n, compact, shapeCount, useCoarse, split);
    endSubmit();
    evalCost(std::chrono::duration<double>(std::chrono::steady_clock::now() - tSearch).count(), n);
    if (g_fatal) { out_best[0] = 3.4028235e38f; return; } // the submit died: g_best is stale
    memcpy(out_best, g_best.map, 12 * sizeof(float));
}

// fp_search_moment: fit K covariance-ellipse seeds from the residual grid, generate a
// localised refine pool around them, score + argmin — one submit. ip[7] = K. Mirrors
// shim.cu fp_search_moment (simple path; coarse filter is a later optimisation).
API void fp_search_moment(unsigned long long seed, const int* ip, const float* fp,
                          const float* kinds, const float* kindCDF, const float* gridCDF, float* out_best) {
    g_profScope = PROF_SEARCH;
    int n = ip[0], nKinds = ip[1], gw = ip[2], gh = ip[3];
    int compact = ip[4], shapeCount = ip[5], allowAlpha = ip[6], K = ip[7];
    float maxR = fp[0], alphaMin = fp[1], boundPad = fp[3], boundMix = fp[4], canvasPad = fp[5];
    if (!g_device || g_fatal || n < 1 || nKinds < 1 || K < 1) { out_best[0] = 3.4028235e38f; return; }
    if (n > 65535) n = 65535;
    // K > n (reachable through the expert panel's near-unclamped seed counter) makes nGen == K
    // exceed n; the buffers must be sized for what the DISPATCHES write, not the requested n —
    // otherwise genmoment/eval write past scand/sout with robustBufferAccess off. And nGen has to
    // respect the same 65535 workgroup ceiling the n path does.
    if (K > 65535) K = 65535;
    int perSeed = n / K; if (perSeed < 1) perSeed = 1;
    while (perSeed > 1 && (long long)perSeed * K > 65535) perSeed--;
    int nGen = perSeed * K; // perSeed==1 leaves nGen == K <= 65535
    g_survN = 0; // a failed or non-coarse search must not leave the previous pool readable
    if (!ensureSearch(nGen > n ? nGen : n) || !ensureMoment(K)) { out_best[0] = 3.4028235e38f; return; }
    // Same one-submit upload as fp_search_random: recorded at the head of the search submit.
    size_t szKi = (size_t)nKinds * sizeof(float), szG = (size_t)gw * gh * sizeof(float);
    VkDeviceSize o1 = 0, o2 = stageAlign(szKi), o3 = stageAlign(o2 + szKi);
    if (!stagingReady(o3 + szG)) { out_best[0] = 3.4028235e38f; return; }
    {
        char* base = (char*)g_staging.map;
        memcpy(base + o1, kinds, szKi);
        memcpy(base + o2, kindCDF, szKi);
        memcpy(base + o3, gridCDF, szG);
    }
    const StageCopy ups[3] = {{o1, g_kindsB.buf, szKi}, {o2, g_kindcdf.buf, szKi}, {o3, g_gridcdf.buf, szG}};

    int chunk = evalChunk(nGen); // TDR guard, byte-identical — see evalChunk
    bool split = chunk < nGen;
    auto tSearch = std::chrono::steady_clock::now();

    beginCmd();
    cmdStageCopies(ups, 3);
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_msPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_msPL, 0, 1, &g_msSet, 0, nullptr);
    MomSeedPC mpc{(uint32_t)seed, (uint32_t)(seed >> 32), K, gw, gh, g_w, g_h, g_hasBound, maxR, boundPad, boundMix};
    vkCmdPushConstants(g_cmd, g_msPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(mpc), &mpc);
    vkCmdDispatch(g_cmd, (uint32_t)((K + 127) / 128), 1, 1);
    cmdBarrierRW();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_gmPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_gmPL, 0, 1, &g_gmSet, 0, nullptr);
    GenMomPC gpc{(uint32_t)seed, (uint32_t)(seed >> 32), nGen, perSeed, K, nKinds, allowAlpha, g_w, g_h, g_hasGate, g_hasRampGlow, g_bigGlowKinds, g_bigGlowKind, alphaMin, canvasPad, g_glowTau, g_glowProb, g_rampGlowThresh, g_rampGlowTau, g_rampGlowProb, g_bigGlowTau, g_bigGlowProb};
    vkCmdPushConstants(g_cmd, g_gmPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(gpc), &gpc);
    vkCmdDispatch(g_cmd, (uint32_t)((nGen + 255) / 256), 1, 1);
    cmdBarrierRW();
    bool useCoarse = g_coarseOn && g_scand2.buf != VK_NULL_HANDLE && nGen > 4 * g_kpart;
    int budget1 = useCoarse ? (g_coarseBudget < g_sampleBudget ? g_coarseBudget : g_sampleBudget) : g_sampleBudget;
    EvalPC epc{nGen, g_w, g_h, budget1, g_alphaGridN, {g_alphaGrid[0], g_alphaGrid[1], g_alphaGrid[2], g_alphaGrid[3], g_alphaGrid[4], g_alphaGrid[5]}, g_gradOn, 0};
    if (split) {
        endSubmit();
        evalChunked(g_sevalSet, epc, nGen, chunk);
    } else {
        cmdEvalRange(g_sevalSet, epc, 0, nGen);
        cmdBarrierRW();
    }
    if (g_fatal) { out_best[0] = 3.4028235e38f; return; } // an eval chunk died: nothing left to reduce
    searchTail(nGen, compact, shapeCount, useCoarse, split);
    endSubmit();
    evalCost(std::chrono::duration<double>(std::chrono::steady_clock::now() - tSearch).count(), nGen);
    if (g_fatal) { out_best[0] = 3.4028235e38f; return; } // the submit died: g_best is stale
    memcpy(out_best, g_best.map, 12 * sizeof(float));
}

// fp_search_mutate: the WHOLE hill climb for one shape in one submit. io_best carries the incumbent
// in the best[12] wire format ([score,kind,p0..p5,r,g,b,a]) and returns the final winner in place.
// Each round is mutate -> eval -> prepadj -> argmin(keep) entirely on-device; the host used to pay
// one upload + one readback PER ROUND (~39 per shape), which made the mutate phase round-trip-bound
// rather than compute-bound. The RNG is the device wanghash, not the host math/rand — same
// neighbourhood, different stream — so this path is validated by paired end-to-end quality.
// ip = [perRound, rounds, compact, shapeCount, allowAlpha]; fp = [moveStep, radiusStep, alphaMin, canvasPad].
API void fp_search_mutate(unsigned long long seed, const int* ip, const float* fp, float* io_best) {
    g_profScope = PROF_MUTATE;
    int m = ip[0], rounds = ip[1], compact = ip[2], shapeCount = ip[3], allowAlpha = ip[4];
    if (!g_device || g_fatal || m < 1 || rounds < 1) return;
    if (m > 65535) m = 65535; // one eval workgroup per candidate; clamp to the dispatch limit
    if (!ensureSearch(m)) return;
    memcpy(g_best.map, io_best, 12 * sizeof(float));

    EvalPC epc{m, g_w, g_h, g_sampleBudget, g_alphaGridN, {g_alphaGrid[0], g_alphaGrid[1], g_alphaGrid[2], g_alphaGrid[3], g_alphaGrid[4], g_alphaGrid[5]}, g_gradOn, 0};
    ArgPC apc{m, 1, 1, compact, shapeCount, g_w, g_h}; // inlineAdj: prepadj folded into the argmin
    // TDR guard: Windows resets the GPU when ONE submit runs past ~2s, and all the rounds in a
    // single submit can cross that on a weak or busy card (the exact "freeze then the run dies"
    // users report). Chunk the rounds so each submit targets ~250ms, sized by a measured
    // seconds-per-round EMA. Chunk boundaries are only extra fence waits — the recorded commands,
    // seeds (absolute round index) and device state are IDENTICAL, so the output is byte-equal to
    // the single-submit version; on a fast card the second chunk covers every remaining round.
    for (int done = 0; done < rounds && !g_fatal; ) {
        // The expert panel ships a near-unclamped per-round counter, so ONE round can be
        // watchdog-sized on its own — the round chunk below cannot go below 1. When the shared
        // eval-cost EMA says m candidates do not fit in a 250ms submit, split the round itself
        // into mutate | eval chunks | argmin. Fences replace the barriers; nothing else changes.
        int ec = evalChunk(m);
        if (ec < m) {
            unsigned long long rs = seed + (unsigned long long)(done + 1) * 0x9E3779B97F4A7C15ull;
            MutPC mpc{(uint32_t)rs, (uint32_t)(rs >> 32), m, g_w, g_h, allowAlpha, fp[0], fp[1], fp[2], fp[3]};
            beginCmd();
            vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_mutPipe);
            vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_mutPL, 0, 1, &g_mutSet, 0, nullptr);
            vkCmdPushConstants(g_cmd, g_mutPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(mpc), &mpc);
            vkCmdDispatch(g_cmd, (uint32_t)((m + 255) / 256), 1, 1);
            endSubmit();
            evalChunked(g_sevalSet, epc, m, ec);
            if (g_fatal) break;
            beginCmd();
            vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_argPipe);
            vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_argPL, 0, 1, &g_argSet, 0, nullptr);
            vkCmdPushConstants(g_cmd, g_argPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(apc), &apc);
            vkCmdDispatch(g_cmd, 1, 1, 1);
            endSubmit();
            done++;
            continue;
        }
        int chunk = rounds - done;
        if (g_mutRoundCost > 0.0) {
            int c = (int)(0.25 / g_mutRoundCost);
            if (c < 1) c = 1;
            if (c < chunk) chunk = c;
        } else if (chunk > 1) {
            chunk = 1; // first-ever call: single-round probe until a round cost is known
        }
        auto t0 = std::chrono::steady_clock::now();
        beginCmd();
        for (int rd = done; rd < done + chunk; rd++) {
            // A fresh seed per round on the host side keeps the shader's counter scheme untouched.
            unsigned long long rs = seed + (unsigned long long)(rd + 1) * 0x9E3779B97F4A7C15ull;
            MutPC mpc{(uint32_t)rs, (uint32_t)(rs >> 32), m, g_w, g_h, allowAlpha, fp[0], fp[1], fp[2], fp[3]};
            vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_mutPipe);
            vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_mutPL, 0, 1, &g_mutSet, 0, nullptr);
            vkCmdPushConstants(g_cmd, g_mutPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(mpc), &mpc);
            vkCmdDispatch(g_cmd, (uint32_t)((m + 255) / 256), 1, 1);
            cmdBarrierRW();
            vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_evalPipe);
            vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_evalPL, 0, 1, &g_sevalSet, 0, nullptr);
            vkCmdPushConstants(g_cmd, g_evalPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(epc), &epc);
            vkCmdDispatch(g_cmd, (uint32_t)m, 1, 1);
            cmdBarrierRW();
            // prepadj is folded into the argmin here (ArgPC.inlineAdj): same expressions, same
            // winner, one dispatch + one barrier fewer per round (~39 rounds per shape).
            vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_argPipe);
            vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_argPL, 0, 1, &g_argSet, 0, nullptr);
            vkCmdPushConstants(g_cmd, g_argPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(apc), &apc);
            vkCmdDispatch(g_cmd, 1, 1, 1);
            // The next round's mutate reads g_best that this argmin may have just written; the
            // barrier is queue-scoped, so it also orders the first round of the NEXT chunk. On
            // the chunk's LAST round flushBarrier below is a superset — skip the duplicate.
            if (rd != done + chunk - 1) cmdBarrierRW();
        }
        flushBarrier();
        vkEndCommandBuffer(g_cmd);
        submitWait();
        double dt = std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count();
        if (dt > 0 && !g_fatal) {
            double per = dt / chunk;
            g_mutRoundCost = g_mutRoundCost > 0.0 ? 0.7 * g_mutRoundCost + 0.3 * per : per;
        }
        done += chunk;
    }
    if (g_fatal) return; // leave the caller's incumbent untouched; fp_last_error reports the fault
    memcpy(io_best, g_best.map, 12 * sizeof(float));
}

API void fp_reset(const float* canvas) {
    g_profScope = PROF_RESET;
    if (!g_device) return;
    g_dirtyFull = true; // the whole canvas changed; the next grid read recomputes every cell
    VkDeviceSize sz = (size_t)g_w * g_h * 4 * sizeof(float);
    if (!stagingReady(sz)) return;
    memcpy(g_staging.map, canvas, sz);
    copyBuf(g_staging.buf, g_canvas.buf, sz); // staging -> device-local
}

API void fp_set_sample_budget(int n) { g_sampleBudget = (n < 1) ? 4000 : n; }

// fp_set_batch arms the survivor export (batch placement). Off = the search submit is unchanged.
API void fp_set_batch(int on) { g_batchOn = on ? 1 : 0; }

// fp_search_survivors hands the last search's refined pool to the host in the device's own block
// layout: [k adjusted scores][k*11 candidate rows][k*5 eval rows (raw score + solved rgba)].
// Returns the row count, or 0 when the last search had no refined pool (coarse filter off / too
// small a candidate batch / a dead device).
API int fp_search_survivors(float* out, int cap) {
    if (g_fatal || !g_survBuf.map || g_survN < 1 || !out) return 0;
    if (cap < g_survN * 17) return 0;
    memcpy(out, g_survBuf.map, (size_t)g_survN * 17 * sizeof(float));
    return g_survN;
}

// fp_set_coarse configures the coarse-to-fine filter for both on-device searches. kpart is the
// survivor count (one per partition); the searches gate themselves on n > 4*kpart, so small pools
// fall through to the single full-budget pass unchanged.
API void fp_set_coarse(int enable, int budget, int kpart) {
    if (!g_device) return;
    g_coarseOn = enable ? 1 : 0;
    g_coarseBudget = (budget < 1) ? 4000 : budget;
    if (kpart < 1) kpart = 2048;
    if (kpart > 8192) kpart = 8192; // coarse_min.comp's int32 index math relies on this clamp
    g_kpart = kpart;
    if (g_coarseOn && !ensureCoarse(kpart)) g_coarseOn = 0;
}

API int fp_last_error() { int e = g_lastError; g_lastError = 0; return e; }

// fp_prof_dump writes the FH6VK_PROF=1 profiler table into buf (truncated to cap): per-scope GPU
// seconds (fence-synchronous submits, so submitWait's wall IS the submit's GPU time) and submit
// COUNTS — the round-trip census that host-side profiling can only estimate. Returns the number
// of characters it wanted to write; 0 when the profiler never armed. Counters reset at fp_init.
API int fp_prof_dump(char* buf, int cap) {
    if (g_profOn != 1 || !buf || cap < 1) { if (buf && cap > 0) buf[0] = 0; return 0; }
    int off = 0;
    double tot = 0;
    for (int i = 0; i < PROF_N; i++) tot += g_profSec[i];
    off += snprintf(buf + off, (size_t)((cap - off) > 0 ? cap - off : 0),
                    "gpu-prof (fence-synchronous submit time, total %.2fs):", tot);
    for (int i = 0; i < PROF_N; i++) {
        if (g_profCnt[i] == 0) continue;
        off += snprintf(buf + off, (size_t)((cap - off) > 0 ? cap - off : 0),
                        " %s=%.2fs/%lld", g_profNames[i], g_profSec[i], g_profCnt[i]);
    }
    if (off >= cap) buf[cap - 1] = 0;
    return off;
}

// fp_device_lost reports the STICKY device-loss flag (unlike fp_last_error it is not consumed):
// 1 after any submit/fence failure — TDR, driver reset, or an OOM-killed context. The engine
// polls it to abort a run with an honest message instead of spinning on a dead device.
API int fp_device_lost() { return g_fatal; }

// fp_mem_info fills out[3] = {budgetBytes, usageBytes, heapSizeBytes} for the largest
// DEVICE_LOCAL heap. budget/usage are live driver numbers via VK_EXT_memory_budget and stay 0
// when the extension is absent — the caller then falls back to a fraction of heapSize.
API void fp_mem_info(long long* out) {
    out[0] = out[1] = out[2] = 0;
    if (!g_phys) return;
    VkPhysicalDeviceMemoryBudgetPropertiesEXT mb{VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_MEMORY_BUDGET_PROPERTIES_EXT};
    VkPhysicalDeviceMemoryProperties2 mp2{VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_MEMORY_PROPERTIES_2};
    if (g_memBudgetExt) mp2.pNext = &mb;
    vkGetPhysicalDeviceMemoryProperties2(g_phys, &mp2);
    int best = -1; VkDeviceSize bestSz = 0;
    for (uint32_t i = 0; i < mp2.memoryProperties.memoryHeapCount; i++) {
        const VkMemoryHeap& hp = mp2.memoryProperties.memoryHeaps[i];
        if ((hp.flags & VK_MEMORY_HEAP_DEVICE_LOCAL_BIT) && hp.size > bestSz) { best = (int)i; bestSz = hp.size; }
    }
    if (best < 0) return;
    out[2] = (long long)bestSz;
    if (g_memBudgetExt) { out[0] = (long long)mb.heapBudget[best]; out[1] = (long long)mb.heapUsage[best]; }
}

// fp_polish_mem_need estimates the DEVICE_LOCAL bytes the polish would allocate for n shapes:
// fp_polish_setup's buffers, the first upload's below/dcsnap snapshots (belowTotal = Σ expanded
// bbox pixels, the caller computes it at the widest tau), the tile-bin list, and the optional
// term planes (terms bit 0 = false-edge/lost-detail, 1 = SSIM, 2 = EAGLE). The formulas mirror
// the actual createBufEx calls — keep them in sync when the allocations change. Host-visible
// buffers (grad readback, loss partials, staging) are excluded: they live in system memory.
API void fp_polish_mem_need(int n, long long belowTotal, int terms, long long* out) {
    long long npix = (long long)g_w * g_h;
    long long rs = g_rs ? g_rs : 8;
    long long nTiles = (long long)((g_w + PTILE - 1) / PTILE) * ((g_h + PTILE - 1) / PTILE);
    long long need = npix * 16 * 3                    // base, render, dC
                   + npix * rs * 3                    // feAdj/ssAdj/egAdj (always bound by dcinit)
                   + (long long)n * 64                // P, col, kinds, bbx, boff
                   + (long long)n * PBSLICES * 10 * rs // backward reduce partials
                   + nTiles * 8 + belowTotal / 256    // tile counts+offsets, ~list
                   + belowTotal * 4 * 2;              // below + dcsnap composite snapshots
    if (terms & 1) need += npix * (8 + 2 * rs);       // FE/LD: TL, RL, Dir
    if (terms & 2) need += npix * (8 + 11 * rs);      // SSIM: TL, RL, H(3), MY(2), G(3), HG(3)
    if (terms & 4) need += npix * (8 + 14 * rs);      // EAGLE: TL, RL + 14 REAL planes
    if (terms) need += npix * 4;                      // term-weight map
    *out = need;
}

// fp_set_gradients is accepted for interface parity but does not gate anything here: the eval
// shader always scores the native gradient kinds (KindGlow/KindDisk) with their per-pixel alpha,
// exactly as CUDA's block eval kernel does. On CUDA the flag still picks the kernel — only the
// block one carries that branch — so setting it on both backends leaves them in agreement.
API void fp_set_gradients(int on) { g_gradOn = on ? 1 : 0; }

// ===================== joint-polish API (mirrors shim.cu fp_polish_*) =====================

API void fp_polish_setup(const float* base, int n) {
    g_profScope = PROF_PSETUP;
    polishTeardown();
    if (!g_device || n < 1) { g_lastError = 2001; return; }
    g_pn = n;
    g_pkindsUp = false;
    if (!buildPolishPipelines()) { g_lastError = 2002; polishTeardown(); return; }
    if (!buildTileBinner()) { g_lastError = 2013; polishTeardown(); return; }
    if (!buildTiledForward()) { g_lastError = 2004; polishTeardown(); return; }
    if (!buildBackwardTiled()) { g_lastError = 2005; polishTeardown(); return; }
    size_t npix = (size_t)g_w * g_h;
    const VkBufferUsageFlags S = VK_BUFFER_USAGE_STORAGE_BUFFER_BIT;
    const VkBufferUsageFlags SD = S | VK_BUFFER_USAGE_TRANSFER_DST_BIT;
    const VkBufferUsageFlags SDS = SD | VK_BUFFER_USAGE_TRANSFER_SRC_BIT;
    const VkMemoryPropertyFlags dl = VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT;
    bool ok = createBufEx(npix * 16, SDS, dl, false, g_pbase)
           && createBufEx(npix * 16, SDS, dl, false, g_prender)
           && createBufEx(npix * 16, S, dl, false, g_pdC)
           && createBufEx((size_t)n * 6 * 4, SD, dl, false, g_pP) // float32: see fp_polish_upload
           && createBufEx((size_t)n * 4 * 4, SD, dl, false, g_pcol)
           && createBufEx((size_t)n * 4, SD, dl, false, g_pkinds)
           && createBufEx((size_t)n * 16, SD, dl, false, g_pbbxBuf)
           && createBufEx((size_t)n * 4, SD, dl, false, g_pboffBuf)
           && createHost((size_t)n * 10 * g_rs, g_ppgrad)
           && createBufEx((size_t)n * PBSLICES * 10 * g_rs, S, dl, false, g_pbwPart)
           && createHost(PLOSS_GROUPS * 8, g_ppartials)
           && createBufEx(16, S, dl, false, g_pbelow)
           && createBufEx(16, S, dl, false, g_pdcsnap)
           && createBufEx(npix * g_rs, S, dl, false, g_feAdj)  // dcinit binding 10 must be valid even with feLambda=0
           && createBufEx(npix * g_rs, S, dl, false, g_ssAdj)  // dcinit binding 11, same contract
           && createBufEx(npix * g_rs, S, dl, false, g_egAdj); // dcinit binding 12 (EAGLE), same contract
    g_tilesX = (g_w + PTILE - 1) / PTILE;
    g_tilesY = (g_h + PTILE - 1) / PTILE;
    g_nTiles = g_tilesX * g_tilesY;
    g_binned = 0;
    g_tileListCap = 0;
    ok = ok && createBufEx((size_t)g_nTiles * 4, S, dl, false, g_tileCount)
            && createBufEx(((size_t)g_nTiles + 1) * 4, SDS, dl, false, g_tileOff)
            && createBufEx(16, S, dl, false, g_tileList); // grown on demand by buildTileBins
    if (!ok) { g_lastError = 2003; polishTeardown(); return; }
    g_belowCap = 16;
    if (!stagingReady(npix * 16)) { g_lastError = 2003; polishTeardown(); return; }
    memcpy(g_staging.map, base, npix * 16);
    copyBuf(g_staging.buf, g_pbase.buf, npix * 16);
    memset(g_ppgrad.map, 0, (size_t)n * 10 * g_rs);
    writePolishDescriptors();
    writeTileBinnerDescriptors();
    writeTiledForwardDescriptors();
    writeBackwardDescriptors();
}

API void fp_set_polish_ste(int on) { g_pste = on ? 1 : 0; }

// fp_set_polish_oklab toggles the perceptual OKLab colour metric in the polish loss and
// backward seed together (one flag keeps the optimisation self-consistent). Mirrors shim.cu.
API void fp_set_polish_oklab(int on) { g_poklab = on ? 1 : 0; }

// fp_set_term_weight uploads (or clears, hostW==null) the per-pixel FE/EAGLE term-weight map —
// region-weighted perceptual λ (1−HardEdgeMap): strong in smooth zones where the rim patchwork
// lives, ~zero on legitimate line-work. Kernels treat hasTW==0 as uniform 1.
API void fp_set_term_weight(const float* hostW) {
    if (!g_device) return;
    if (!hostW) { g_hasTermW = 0; return; }
    if (!ensureTermW()) { g_lastError = 2012; return; }
    size_t sz = (size_t)g_w * g_h * sizeof(float);
    if (!stagingReady(sz)) return;
    memcpy(g_staging.map, hostW, sz);
    copyBuf(g_staging.buf, g_termW.buf, sz);
    g_hasTermW = 1;
}

// fp_set_polish_false_edge sets the false-edge λ (pointer: the Go syscall path keeps doubles out
// of XMM) and prepares the FE planes + the fixed target-luma plane. λ<=0 disables the term.
// Call AFTER fp_polish_setup (the FE descriptors reference the polish render buffer).
// ensureFEPlanes builds the shared false-edge / lost-detail planes and (re)computes the fixed
// target-luma plane. Both terms ride the same passes (see fe_dir.comp), so either setter can be
// the one that brings the resources up — and the second must not rebuild them.
static void ensureFEPlanes() {
    g_profScope = PROF_TERMSET;
    if (!g_device || g_pn < 1) return;
    size_t npix = (size_t)g_w * g_h;
    if (g_feDSL == VK_NULL_HANDLE && !buildFE()) { g_lastError = 2006; g_pfelambda = 0.0; g_pldlambda = 0.0; return; } // lambda MUST die with the failed build: a live lambda with NULL pipelines is a device fault
    if (!ensureTermW()) { g_lastError = 2012; g_pfelambda = 0.0; g_pldlambda = 0.0; return; } // same rule for the weight buffer
    if (g_feTL.buf == VK_NULL_HANDLE) {
        const VkBufferUsageFlags S = VK_BUFFER_USAGE_STORAGE_BUFFER_BIT;
        const VkMemoryPropertyFlags dl = VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT;
        if (!createBufEx(npix * 4, S, dl, false, g_feTL) ||
            !createBufEx(npix * 4, S, dl, false, g_feRL) ||
            !createBufEx(npix * 2 * g_rs, S, dl, false, g_feDir) ||
            !createHost(FE_GROUPS * 8, g_feParts)) { g_lastError = 2007; g_pfelambda = 0.0; g_pldlambda = 0.0; return; }
    }
    writeFEDescriptors();
    // One-off: target luma plane via the luma pipe on setT (binding 0 = target, 2 = g_feTL).
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    FePC fpc{ g_w, g_h, (float)g_pfelambda, g_hasTermW, (float)g_pldlambda };
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_feLumaP);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_fePL, 0, 1, &g_feSetT, 0, nullptr);
    vkCmdPushConstants(g_cmd, g_fePL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(fpc), &fpc);
    vkCmdDispatch(g_cmd, (uint32_t)((npix + 255) / 256), 1, 1);
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
}

API void fp_set_polish_false_edge(const double* lambdaPtr) {
    g_pfelambda = lambdaPtr[0];
    if (g_pfelambda <= 0.0) return;
    ensureFEPlanes();
}

// fp_set_polish_lostdetail sets the lost-detail λ — the MIRROR of the false edge: it charges
// structure the recon ERASED (see lostdetail.go / fe_dir.comp). Shares the FE planes and passes;
// λ<=0 disables. Call AFTER fp_polish_setup, like the other term setters.
API void fp_set_polish_lostdetail(const double* lambdaPtr) {
    g_pldlambda = lambdaPtr[0];
    if (g_pldlambda <= 0.0) return;
    ensureFEPlanes();
}

// fp_set_polish_ssim sets the SSIM λ (pointer ABI like the FE setter) and prepares the
// target-side window moments. λ<=0 disables; a canvas smaller than one window degrades to
// λ=0 (the CPU reference's nil-state contract). Call AFTER fp_polish_setup.
API void fp_set_polish_ssim(const double* lambdaPtr) {
    g_profScope = PROF_TERMSET;
    g_psslambda = lambdaPtr[0];
    if (g_psslambda <= 0.0 || !g_device || g_pn < 1) return;
    int mw = g_w - SSWIN + 1, mh = g_h - SSWIN + 1;
    if (mw < 1 || mh < 1) { g_psslambda = 0.0; return; }
    size_t npix = (size_t)g_w * g_h;
    if (g_ssDSL == VK_NULL_HANDLE && !buildSSIM()) { g_lastError = 2008; g_psslambda = 0.0; return; }
    if (g_ssTL.buf == VK_NULL_HANDLE) {
        const VkBufferUsageFlags S = VK_BUFFER_USAGE_STORAGE_BUFFER_BIT;
        const VkMemoryPropertyFlags dl = VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT;
        if (!createBufEx(npix * 4, S, dl, false, g_ssTL) ||
            !createBufEx(npix * 4, S, dl, false, g_ssRL) ||
            !createBufEx((size_t)mw * g_h * 3 * g_rs, S, dl, false, g_ssH) ||
            !createBufEx((size_t)mw * mh * 2 * g_rs, S, dl, false, g_ssMY) ||
            !createBufEx((size_t)mw * mh * 3 * g_rs, S, dl, false, g_ssG) ||
            !createBufEx((size_t)g_w * mh * 3 * g_rs, S, dl, false, g_ssHG) ||
            !createHost(SS_GROUPS * 8, g_ssParts)) { g_lastError = 2009; g_psslambda = 0.0; return; }
    }
    writeSSIMDescriptors();
    // One-off: target luma + h-pass + window moments via setT (binding 0 = target, 2 = g_ssTL).
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    SsimPC spc{ g_w, g_h, mw, mh, 0, (float)g_psslambda };
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ssLumaP);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ssPL, 0, 1, &g_ssSetT, 0, nullptr);
    vkCmdPushConstants(g_cmd, g_ssPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(spc), &spc);
    vkCmdDispatch(g_cmd, (uint32_t)((npix + 255) / 256), 1, 1);
    cmdBarrierRW();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ssHP);
    vkCmdPushConstants(g_cmd, g_ssPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(spc), &spc);
    vkCmdDispatch(g_cmd, (uint32_t)(((size_t)mw * g_h + 255) / 256), 1, 1);
    cmdBarrierRW();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ssMyP);
    vkCmdPushConstants(g_cmd, g_ssPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(spc), &spc);
    vkCmdDispatch(g_cmd, (uint32_t)(((size_t)mw * mh + 255) / 256), 1, 1);
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
}

// buildTileBins re-bins the shapes after every upload (the expanded bboxes move with tau):
// count per tile -> prefix sum -> fill. The total comes back through the staging buffer so the
// list can be grown; if it would exceed the cap, binned stays 0 and the polish shaders fall back
// to scanning the whole stack, so correctness never depends on the bin.
// buildTileBins also carries the caller's staging copies in its FIRST submit (one fence instead
// of two per polish iteration). Returns false when it bailed before submitting anything — the
// caller must then flush the copies itself.
bool buildTileBins(const StageCopy* ups, int nups) {
    g_binned = 0;
    if (!g_device || g_pn < 1 || g_nTiles < 1) return false;
    static int noBin = -1; // cached: this runs once per polish iteration, getenv is not free
    if (noBin < 0) { const char* off = getenv("FH6_NO_TILEBIN"); noBin = (off && off[0] == '1') ? 1 : 0; }
    if (noBin) return false; // kill switch: full scan
    uint32_t groups = (uint32_t)((g_nTiles + 255) / 256);
    ensureStaging(((size_t)g_nTiles + 1) * 4);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;

    // Count, scan and fill in ONE submit, then read the total. The fill used to wait for that total
    // so the host could size the list first, which cost two extra fence waits on EVERY polish
    // iteration; the shader now bounds-checks its writes instead, so a fill against a list that turns
    // out to be too small is harmless and simply repeated after the grow. The common case — a list
    // already big enough, since the bboxes barely move between iterations — is one submit.
    auto record = [&](bool withFill) {
        TilePC pc{ g_pn, g_tilesX, g_nTiles, PTILE, (int32_t)(g_tileListCap / 4) };
        vkResetCommandBuffer(g_cmd, 0);
        vkBeginCommandBuffer(g_cmd, &bi);
        // The caller's param uploads ride this submit (one fence, not two, per polish iteration).
        // The trailing tileOff->staging readback writes over the same staging bytes, but only
        // AFTER the upload copies were consumed: copies -> barrier -> dispatches -> flushBarrier
        // -> readback is a transitive execution chain, which is all a write-after-read needs.
        if (withFill && ups) cmdStageCopies(ups, nups);
        vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_tbPL, 0, 1, &g_tbSet, 0, nullptr);
        vkCmdPushConstants(g_cmd, g_tbPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
        if (withFill) {
            vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_tbCount);
            vkCmdDispatch(g_cmd, groups, 1, 1);
            cmdBarrierRW();
            vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_tbScan);
            vkCmdDispatch(g_cmd, 1, 1, 1);
            cmdBarrierRW();
        }
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_tbFill);
        vkCmdDispatch(g_cmd, groups, 1, 1);
        flushBarrier();
        VkBufferCopy bc{0, 0, ((VkDeviceSize)g_nTiles + 1) * 4};
        vkCmdCopyBuffer(g_cmd, g_tileOff.buf, g_staging.buf, 1, &bc);
        VkMemoryBarrier mb{VK_STRUCTURE_TYPE_MEMORY_BARRIER};
        mb.srcAccessMask = VK_ACCESS_TRANSFER_WRITE_BIT;
        mb.dstAccessMask = VK_ACCESS_HOST_READ_BIT;
        vkCmdPipelineBarrier(g_cmd, VK_PIPELINE_STAGE_TRANSFER_BIT, VK_PIPELINE_STAGE_HOST_BIT, 0, 1, &mb, 0, nullptr, 0, nullptr);
        vkEndCommandBuffer(g_cmd);
        submitWait();
    };

    record(true); // from here on the caller's copies are submitted: every path returns true
    int total = ((const int*)g_staging.map)[g_nTiles];
    const long long cap = 48LL << 20; // 192 MB of indices, far past any real stack
    if (total <= 0 || (long long)total > cap) return true;
    if ((VkDeviceSize)total * 4 > g_tileListCap) {
        destroyBuf(g_tileList);
        if (!createBufEx((VkDeviceSize)total * 4, VK_BUFFER_USAGE_STORAGE_BUFFER_BIT,
                         VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT, false, g_tileList)) { g_tileListCap = 0; return true; }
        g_tileListCap = (VkDeviceSize)total * 4;
        writeTileBinnerDescriptors();
        writeTiledForwardDescriptors();
        writeBackwardDescriptors();
        record(false); // offsets are already correct; only the truncated fill has to be redone
    }
    g_binned = 1;
    return true;
}

API void fp_polish_upload(const double* P, const double* col, const int* kinds,
                          const int* bbx, const long long* boff, long long belowTotal) {
    g_profScope = PROF_PUPLOAD;
    if (!g_device || g_pn < 1 || g_fatal) return;
    VkDeviceSize need = (VkDeviceSize)belowTotal * 4;
    if (need < 4) need = 4;
    if (need > g_belowCap) {
        // This is the single largest allocation of the whole program (belowTotal*4, twice). It is
        // CHECKED: a silent failure here used to write null buffers with non-zero ranges into live
        // descriptor sets — a driver fault instead of the skipped-polish contract.
        destroyBuf(g_pbelow);
        destroyBuf(g_pdcsnap);
        bool ok = createBufEx(need, VK_BUFFER_USAGE_STORAGE_BUFFER_BIT, VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT, false, g_pbelow)
               && createBufEx(need, VK_BUFFER_USAGE_STORAGE_BUFFER_BIT, VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT, false, g_pdcsnap);
        if (!ok) {
            g_belowCap = 0;
            g_lastError = 2014;
            g_pn = 0; // polish state is unusable: degrade every later fp_polish_* to a no-op
            return;
        }
        g_belowCap = need;
        writePolishDescriptors();
        writeTiledForwardDescriptors(); // g_pbelow handle changed -> rebind binding 4
        writeBackwardDescriptors();     // g_pbelow + g_pdcsnap handles changed
    }
    // Params go to the device as FLOAT32. Every shader that reads them narrowed each value to float
    // the moment it loaded it, so the doubles bought nothing on device and cost twice the traffic —
    // and this loop is re-read per pixel per shape. Converting here is the same IEEE round-to-nearest
    // the shaders were applying, so the values they see are bit-for-bit what they saw before.
    size_t szP = (size_t)g_pn * 6 * 4, szC = (size_t)g_pn * 4 * 4, szK = (size_t)g_pn * 4;
    size_t szB = (size_t)g_pn * 16, szO = (size_t)g_pn * 4;
    // One staging layout, one submit. This runs once per polish ITERATION, so five fence waits here
    // were five per iteration for transfers measured in microseconds.
    VkDeviceSize oP = 0;
    VkDeviceSize oC = stageAlign(oP + szP);
    VkDeviceSize oK = stageAlign(oC + szC);
    VkDeviceSize oB = stageAlign(oK + szK);
    VkDeviceSize oO = stageAlign(oB + szB);
    // Sized to cover BOTH this upload's layout and buildTileBins' tileOff readback, because the
    // bins ride the same staging buffer in the same submit — growing it later would destroy the
    // params just written.
    VkDeviceSize stNeed = oO + szO;
    if (VkDeviceSize tb = ((VkDeviceSize)g_nTiles + 1) * 4; tb > stNeed) stNeed = tb;
    ensureStaging(stNeed);
    if (!g_staging.map) { g_lastError = 1060; g_pn = 0; return; } // host-visible OOM: degrade, don't null-deref
    char* base = (char*)g_staging.map;
    {
        float* dst = (float*)(base + oP);
        for (size_t i = 0; i < (size_t)g_pn * 6; i++) dst[i] = (float)P[i];
    }
    {
        // Colours go as FLOAT32 too, same argument as P above: every consumer narrowed on load, so
        // converting host-side is the identical rounding minus the per-pixel fp64 fetch+convert.
        float* dst = (float*)(base + oC);
        for (size_t i = 0; i < (size_t)g_pn * 4; i++) dst[i] = (float)col[i];
    }
    memcpy(base + oB, bbx, szB); // bbx + boff(int32) feed the tiled forward/hard passes
    // Widest slice count over the CURRENT boxes. Pass B is dispatched (n, PBSLICES) but each
    // workgroup returns at once when its slice index is past its own shape's sliceCount — at 3000
    // shapes that is 192k workgroups of which only a few thousand ever read a pixel. The host
    // already has the boxes right here, and sliceCount is a pure function of the box, so capping
    // the dispatch height at the widest one drops only workgroups that were guaranteed to return.
    // Bit-identical by construction. pc.slices stays PBSLICES so sliceCount itself is unchanged
    // (polish_backward_combine computes the same value from the same cap).
    {
        int mx = 1;
        for (int i = 0; i < g_pn; i++) {
            int bw = bbx[i * 4 + 2] - bbx[i * 4 + 0] + 1, bh = bbx[i * 4 + 3] - bbx[i * 4 + 1] + 1;
            long long total = (bw < 1 || bh < 1) ? 0 : (long long)bw * bh;
            int s = (int)((total + PBSLICE_PX - 1) / PBSLICE_PX);
            if (s < 1) s = 1;
            if (s > PBSLICES) s = PBSLICES;
            if (s > mx) mx = s;
        }
        g_pMaxSlices = mx;
    }
    {
        int32_t* dst = (int32_t*)(base + oO);
        for (int i = 0; i < g_pn; i++) dst[i] = (int32_t)boff[i];
    }
    // kinds never change between setup and free — upload them once, not once per iteration.
    StageCopy ups[5] = {
        {oP, g_pP.buf, szP}, {oC, g_pcol.buf, szC},
        {oB, g_pbbxBuf.buf, szB}, {oO, g_pboffBuf.buf, szO}, {0, VK_NULL_HANDLE, 0},
    };
    int nups = 4;
    if (!g_pkindsUp) {
        memcpy(base + oK, kinds, szK);
        ups[nups++] = {oK, g_pkinds.buf, szK};
        g_pkindsUp = true;
    }
    if (!buildTileBins(ups, nups)) copyBufBatch(ups, nups);
}

// fp_polish_forward — ONE tiled dispatch (thread-per-pixel walks all shapes in order). No
// base->render copy (the shader inits render=base per pixel) and no per-shape barriers.
API void fp_polish_forward(const int* bbxHost, const double* tauPtr) {
    g_profScope = PROF_PFWD;
    if (!g_device || g_pn < 1) return;
    (void)bbxHost; // bbx now lives on-device (g_pbbxBuf)
    int wg = (int)(((size_t)g_w * g_h + 255) / 256);
    int chunk = polishChunk(PCH_FWD, wg);
    auto t0 = std::chrono::steady_clock::now();
    for (int f = 0; f < wg && !g_fatal; f += chunk) {
        int cnt = wg - f < chunk ? wg - f : chunk;
        beginCmd();
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ptFwd);
        vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ptPL, 0, 1, &g_ptSet, 0, nullptr);
        TiledPC pc{ g_pn, g_w, g_h, g_pste, (float)*tauPtr, g_tilesX, PTILE, g_binned, 0, f * 256 };
        vkCmdPushConstants(g_cmd, g_ptPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
        vkCmdDispatch(g_cmd, (uint32_t)cnt, 1, 1);
        endSubmit();
    }
    polishCost(PCH_FWD, std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count(), wg);
}

API void fp_polish_loss(double* out) {
    g_profScope = PROF_PLOSS;
    if (!g_device || g_pn < 1) { *out = 0; return; }
    *out = computeLoss();
}

// fp_polish_hard_loss — ONE tiled hard dispatch (render=base, all shapes binary-inside in
// order) then the loss reduction. No base->render copy, no per-shape barriers.
API void fp_polish_hard_loss(const int* bbxHost, double* out) {
    g_profScope = PROF_PHARD;
    if (!g_device || g_pn < 1) { *out = 0; return; }
    (void)bbxHost; // bbx lives on-device (g_pbbxBuf)
    int wg = (int)(((size_t)g_w * g_h + 255) / 256);
    int chunk = polishChunk(PCH_HARD, wg);
    auto t0 = std::chrono::steady_clock::now();
    for (int f = 0; f < wg && !g_fatal; f += chunk) {
        int cnt = wg - f < chunk ? wg - f : chunk;
        beginCmd();
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ptHard);
        vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ptPL, 0, 1, &g_ptSet, 0, nullptr);
        TiledPC pc{ g_pn, g_w, g_h, g_pste, 0.0f, g_tilesX, PTILE, g_binned, 0, f * 256 };
        vkCmdPushConstants(g_cmd, g_ptPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
        vkCmdDispatch(g_cmd, (uint32_t)cnt, 1, 1);
        endSubmit();
    }
    polishCost(PCH_HARD, std::chrono::duration<double>(std::chrono::steady_clock::now() - t0).count(), wg);
    *out = computeLoss();
}

// fp_polish_backward — dcinit (full-image dC seed) + Pass A (per-pixel reverse dC walk ->
// dcsnap) + Pass B (per-shape gradient reduce, N workgroups one dispatch). 3 dispatches, 2
// barriers total — replacing the per-shape 1 + N-dispatch / N-barrier path. Bit-identical
// gradient (same fixed-order tree reduction); the barrier count drops from ~N to 2.
API void fp_polish_backward(const int* bbxHost, const double* tauPtr) {
    g_profScope = PROF_PBWD;
    if (!g_device || g_pn < 1) return;
    (void)bbxHost; // bbx lives on-device (g_pbbxBuf)
    double tau = *tauPtr;
    // FH6VK_BWPROBE=1: split the one-submit backward into three fenced submits and report where
    // the time goes (terms+dcinit / walk / reduce). Diagnostic only — the extra fences cost real
    // wall, so never leave it on for a measured run.
    static int bwProbe = -1;
    if (bwProbe < 0) { const char* e = getenv("FH6VK_BWPROBE"); bwProbe = (e && e[0] == '1') ? 1 : 0; }
    PolishPC pcd{0, g_w, g_h, 0, 0, 0, 0, 0, g_pste, g_w * g_h, (float)tau, g_poklab, (float)g_pfelambda, (float)g_psslambda, (float)g_peglambda, (float)g_pldlambda};
    TiledPC tpc{ g_pn, g_w, g_h, g_pste, (float)tau, g_tilesX, PTILE, g_binned, PBSLICES, 0 };
    auto begin = [&]() { beginCmd(); };
    auto recTerms = [&]() {
        // False-edge + SSIM adjoint planes first, feeding the dcinit dC seed below.
        if (g_pfelambda > 0.0 || g_pldlambda > 0.0) cmdFEPasses(true);
        if (g_psslambda > 0.0) cmdSSIMPasses(true);
        if (g_peglambda > 0.0) cmdEaglePasses(true);
        // dC = 2*weight*(render-target) — full image, shared polish DSL
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pDcinit);
        vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pPL, 0, 1, &g_pSet, 0, nullptr);
        vkCmdPushConstants(g_cmd, g_pPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pcd), &pcd);
        vkCmdDispatch(g_cmd, (uint32_t)(((size_t)g_w * g_h + 255) / 256), 1, 1);
        cmdBarrierRW();
    };
    auto recWalk = [&]() {
        // Pass A: per-pixel reverse dC walk -> dcsnap (one dispatch, no per-shape barriers)
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbWalk);
        vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbPL, 0, 1, &g_pbSet, 0, nullptr);
        vkCmdPushConstants(g_cmd, g_pbPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(tpc), &tpc);
        vkCmdDispatch(g_cmd, (uint32_t)(((size_t)g_w * g_h + 255) / 256), 1, 1);
        cmdBarrierRW();
    };
    auto recReduce = [&]() {
        // Pass B: sliced per-shape gradient reduce -> slice partials. The (n, PBSLICES) dispatch
        // spreads a canvas-sized bbox over up to PBSLICES workgroups instead of serialising it in
        // one — the single-workgroup layout made the whole dispatch wait on its largest shape
        // (measured 26× on img_26 @3000).
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbReduce);
        vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbPL, 0, 1, &g_pbSet, 0, nullptr);
        vkCmdPushConstants(g_cmd, g_pbPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(tpc), &tpc);
        vkCmdDispatch(g_cmd, (uint32_t)g_pn, (uint32_t)g_pMaxSlices, 1);
        cmdBarrierRW();
        // Pass C: sum each shape's slice partials in ascending slice order -> pgrad
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbCombine);
        vkCmdPushConstants(g_cmd, g_pbPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(tpc), &tpc);
        vkCmdDispatch(g_cmd, (uint32_t)g_pn, 1, 1);
        flushBarrier(); // pgrad shader write -> host read
    };
    // TDR split. Pass A is thread-per-pixel and Pass B a grid of independent (shape, slice)
    // workgroups, so slicing them by range is byte-identical — the fence count changes, nothing
    // else. Costs are measured on the first backward of the process (its probe chunks), and after
    // that a healthy card measures chunk == whole and records the fused single submit as before.
    int pwg = (int)(((size_t)g_w * g_h + 255) / 256);
    int wchunk = polishChunk(PCH_WALK, pwg);
    int rchunk = polishChunk(PCH_REDUCE, g_pn);
    if (!bwProbe && wchunk >= pwg && rchunk >= g_pn) {
        begin(); recTerms(); recWalk(); recReduce();
        vkEndCommandBuffer(g_cmd);
        submitWait();
        return;
    }
    if (!bwProbe) {
        begin(); recTerms(); endSubmit();
        auto tw = std::chrono::steady_clock::now();
        for (int f = 0; f < pwg && !g_fatal; f += wchunk) {
            int cnt = pwg - f < wchunk ? pwg - f : wchunk;
            begin();
            tpc.first = f * 256;
            vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbWalk);
            vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbPL, 0, 1, &g_pbSet, 0, nullptr);
            vkCmdPushConstants(g_cmd, g_pbPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(tpc), &tpc);
            vkCmdDispatch(g_cmd, (uint32_t)cnt, 1, 1);
            endSubmit();
        }
        polishCost(PCH_WALK, std::chrono::duration<double>(std::chrono::steady_clock::now() - tw).count(), pwg);
        auto tr = std::chrono::steady_clock::now();
        for (int f = 0; f < g_pn && !g_fatal; f += rchunk) {
            int cnt = g_pn - f < rchunk ? g_pn - f : rchunk;
            begin();
            tpc.first = f;
            vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbReduce);
            vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbPL, 0, 1, &g_pbSet, 0, nullptr);
            vkCmdPushConstants(g_cmd, g_pbPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(tpc), &tpc);
            vkCmdDispatch(g_cmd, (uint32_t)cnt, (uint32_t)g_pMaxSlices, 1);
            endSubmit();
        }
        polishCost(PCH_REDUCE, std::chrono::duration<double>(std::chrono::steady_clock::now() - tr).count(), g_pn);
        if (g_fatal) return;
        begin();
        tpc.first = 0;
        vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbCombine);
        vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbPL, 0, 1, &g_pbSet, 0, nullptr);
        vkCmdPushConstants(g_cmd, g_pbPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(tpc), &tpc);
        vkCmdDispatch(g_cmd, (uint32_t)g_pn, 1, 1);
        endSubmit();
        return;
    }
    static double tTerms = 0, tWalk = 0, tReduce = 0;
    static int bwCalls = 0;
    using clk = std::chrono::steady_clock;
    auto part = [&](double& acc, int which) {
        auto t0 = clk::now();
        begin();
        if (which == 0) recTerms(); else if (which == 1) recWalk(); else recReduce();
        vkEndCommandBuffer(g_cmd);
        submitWait();
        acc += std::chrono::duration<double>(clk::now() - t0).count();
    };
    part(tTerms, 0);
    part(tWalk, 1);
    part(tReduce, 2);
    if (++bwCalls % 100 == 0)
        fprintf(stderr, "bwprobe %d iters: terms+dcinit %.2fs walk %.2fs reduce %.2fs\n",
                bwCalls, tTerms, tWalk, tReduce);
}

API void fp_polish_read_grad_impl_fp64(double* dst);
API void fp_polish_read_grad(double* dst) {
    if (!g_fp64) {
        // The fp32 pipelines wrote float grads; the Go side speaks float64, so widen on the way out.
        if (!g_device || g_pn < 1 || g_fatal) return;
        const float* src = (const float*)g_ppgrad.map;
        for (size_t i = 0; i < (size_t)g_pn * 10; i++) dst[i] = (double)src[i];
        return;
    }
    fp_polish_read_grad_impl_fp64(dst);
}
API void fp_polish_read_grad_impl_fp64(double* dst) {
    // g_fatal: a dead device leaves g_ppgrad holding the PREVIOUS iteration's gradients — Adam
    // would keep stepping on them until the engine's DeviceLost poll fires.
    if (!g_device || g_pn < 1 || g_fatal) return;
    memcpy(dst, g_ppgrad.map, (size_t)g_pn * 10 * 8);
}

API void fp_polish_read_render(float* dst) {
    g_profScope = PROF_PREADRENDER;
    // g_pn guard: after a FAILED setup (OOM teardown) g_prender does not exist — a copy from the
    // null buffer would fault the driver instead of no-opping like the other polish entries.
    if (!g_device || g_pn < 1 || g_fatal) return;
    size_t sz = (size_t)g_w * g_h * 16;
    ensureStaging(sz);
    copyBuf(g_prender.buf, g_staging.buf, sz);
    memcpy(dst, g_staging.map, sz);
}

API void fp_polish_sync() { if (g_device) vkDeviceWaitIdle(g_device); }

API void fp_polish_free() { polishTeardown(); }

API void fp_free() { teardown(); }
