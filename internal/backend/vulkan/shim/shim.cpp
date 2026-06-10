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
#include <cstdint>
#include <cstring>
#include <cstdlib>
#include <vector>

#include "eval.spv.h"   // unsigned int eval_spv[]  (glslangValidator --vn)
#include "apply.spv.h"  // unsigned int apply_spv[]
#include "grid.spv.h"   // unsigned int grid_spv[]
#include "gen.spv.h"     // gen_spv (on-device random candidate generator)
#include "prepadj.spv.h" // prepadj_spv
#include "argmin.spv.h"  // argmin_spv
#include "momentseed.spv.h" // momentseed_spv (covariance-ellipse seeds)
#include "genmoment.spv.h"  // genmoment_spv (localised pool around seeds)
#include "polish_forward_tiled.spv.h" // pt_forward_spv (one-dispatch tiled forward)
#include "polish_hard_tiled.spv.h"    // pt_hard_spv    (one-dispatch tiled hard)
#include "polish_dcwalk_tiled.spv.h"    // pt_dcwalk_spv  (tiled backward Pass A: dC reverse walk)
#include "polish_backward_reduce.spv.h" // pt_breduce_spv (tiled backward Pass B: per-shape reduce)
#include "polish_dcinit.spv.h"   // p_dcinit_spv
#include "polish_loss.spv.h"     // p_loss_spv
#include "fe_luma.spv.h"         // fe_luma_spv
#include "fe_dir.spv.h"          // fe_dir_spv
#include "fe_adj.spv.h"          // fe_adj_spv

#ifdef _WIN32
#define API extern "C" __declspec(dllexport)
#else
#define API extern "C"
#endif

namespace {

struct Buf { VkBuffer buf = VK_NULL_HANDLE; VkDeviceMemory mem = VK_NULL_HANDLE; void* map = nullptr; VkDeviceSize size = 0; };

// ---- global single device context (engine drives one backend serially) ----
VkInstance       g_instance = VK_NULL_HANDLE;
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

struct EvalPC  { int32_t n, W, H, sampleBudget; };
struct ApplyPC { int32_t kind; float p0, p1, p2, p3, p4, p5; float cr, cg, cb, ca; int32_t W, H; };
struct GridPC  { int32_t W, H, gw, gh; };

// ---- on-device random search (fp_search_random) ----
struct GenPC  { uint32_t seedLo, seedHi; int32_t n, nKinds, gw, gh, W, H, allowAlpha, hasOrient, hasBound;
                float maxR, alphaMin, aspectMax, boundPad, boundMix, canvasPad; };
struct PrepPC { int32_t n, compact, shapeCount, W, H; };
struct ArgPC  { int32_t n; };

VkDescriptorSetLayout g_genDSL = VK_NULL_HANDLE, g_prepDSL = VK_NULL_HANDLE, g_argDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_genPL = VK_NULL_HANDLE, g_prepPL = VK_NULL_HANDLE, g_argPL = VK_NULL_HANDLE;
VkPipeline            g_genPipe = VK_NULL_HANDLE, g_prepPipe = VK_NULL_HANDLE, g_argPipe = VK_NULL_HANDLE;
VkDescriptorSet       g_genSet = VK_NULL_HANDLE, g_sevalSet = VK_NULL_HANDLE, g_prepSet = VK_NULL_HANDLE, g_argSet = VK_NULL_HANDLE;
Buf g_scand, g_sout, g_adj, g_best, g_kindsB, g_kindcdf, g_gridcdf, g_orient, g_bound;
int g_searchCap = 0, g_hasOrient = 0, g_hasBound = 0;
bool g_searchSetsDirty = true;

// ---- on-device moment-seeded search (fp_search_moment) ----
struct MomSeedPC { uint32_t seedLo, seedHi; int32_t K, gw, gh, W, H, hasBound; float maxR, boundPad, boundMix; };
struct GenMomPC  { uint32_t seedLo, seedHi; int32_t n, perSeed, K, nKinds, allowAlpha, W, H; float alphaMin, canvasPad; };
VkDescriptorSetLayout g_msDSL = VK_NULL_HANDLE, g_gmDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_msPL = VK_NULL_HANDLE, g_gmPL = VK_NULL_HANDLE;
VkPipeline            g_msPipe = VK_NULL_HANDLE, g_gmPipe = VK_NULL_HANDLE;
VkDescriptorSet       g_msSet = VK_NULL_HANDLE, g_gmSet = VK_NULL_HANDLE;
Buf g_seeds;
int g_momentCap = 0;

// ---- joint-polish state (built lazily by fp_polish_setup, freed by fp_polish_free) ----
struct PolishPC { int32_t shapeIdx, w, h, xMin, yMin, xMax, yMax, boff, ste, npix; float tau; int32_t oklab; float feLambda; };
const int PLOSS_GROUPS = 64; // loss reduction workgroups (host sums the partials)

VkDescriptorSetLayout g_pDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_pPL  = VK_NULL_HANDLE;
VkPipeline g_pDcinit = VK_NULL_HANDLE, g_pLoss = VK_NULL_HANDLE; // shared-DSL pipelines (forward/hard/backward are tiled, below)
VkDescriptorPool g_pPool = VK_NULL_HANDLE;
VkDescriptorSet  g_pSet  = VK_NULL_HANDLE;
Buf g_pbase, g_prender, g_pbelow, g_pdC, g_pP, g_pcol, g_pkinds, g_ppgrad, g_ppartials;
int g_pn = 0, g_pste = 0, g_poklab = 0;
VkDeviceSize g_belowCap = 0;

// ---- false-edge additive polish term (mirrors engine/falseedge.go + shim.cu): its own small
// DSL (0=src4 1=targetLuma 2=reconLuma 3=dir 4=adj 5=partials) with two sets — setT computes the
// fixed target-luma plane once at set-lambda, setR runs per evaluation on the current render. ----
struct FePC { int32_t w, h; float feLambda; };
double g_pfelambda = 0.0;
Buf g_feTL, g_feRL, g_feDir, g_feAdj, g_feParts;
VkDescriptorSetLayout g_feDSL = VK_NULL_HANDLE;
VkPipelineLayout      g_fePL  = VK_NULL_HANDLE;
VkPipeline g_feLumaP = VK_NULL_HANDLE, g_feDirP = VK_NULL_HANDLE, g_feAdjP = VK_NULL_HANDLE;
VkDescriptorPool g_fePool = VK_NULL_HANDLE;
VkDescriptorSet  g_feSetR = VK_NULL_HANDLE, g_feSetT = VK_NULL_HANDLE;
const int FE_GROUPS = 64;

// ---- tiled polish forward/hard: ONE dispatch, no barriers (its own DSL/PL/pool/set so it
// never touches the shared 10-binding per-shape polish set). 8 bindings:
// 0=P 1=col 2=kinds 3=render 4=below 5=bbx 6=boff 7=base. bbx/boff live on-device. ----
struct TiledPC { int32_t n, w, h, ste; float tau; };
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
VkPipeline g_pbWalk = VK_NULL_HANDLE, g_pbReduce = VK_NULL_HANDLE;
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

bool createBufEx(VkDeviceSize size, VkBufferUsageFlags usage, VkMemoryPropertyFlags props, bool doMap, Buf& b) {
    b.size = size;
    VkBufferCreateInfo bci{VK_STRUCTURE_TYPE_BUFFER_CREATE_INFO};
    bci.size = size; bci.usage = usage; bci.sharingMode = VK_SHARING_MODE_EXCLUSIVE;
    if (vkCreateBuffer(g_device, &bci, nullptr, &b.buf) != VK_SUCCESS) return false;
    VkMemoryRequirements mr; vkGetBufferMemoryRequirements(g_device, b.buf, &mr);
    uint32_t mt = findMemType(mr.memoryTypeBits, props);
    if (mt == UINT32_MAX) return false;
    VkMemoryAllocateInfo mai{VK_STRUCTURE_TYPE_MEMORY_ALLOCATE_INFO};
    mai.allocationSize = mr.size; mai.memoryTypeIndex = mt;
    if (vkAllocateMemory(g_device, &mai, nullptr, &b.mem) != VK_SUCCESS) return false;
    if (vkBindBufferMemory(g_device, b.buf, b.mem, 0) != VK_SUCCESS) return false;
    if (doMap && vkMapMemory(g_device, b.mem, 0, size, 0, &b.map) != VK_SUCCESS) return false;
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
    destroyBuf(g_pP); destroyBuf(g_pcol); destroyBuf(g_pkinds); destroyBuf(g_ppgrad); destroyBuf(g_ppartials);
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
    g_pn = 0; g_belowCap = 0;
}

void teardown() {
    if (g_device) vkDeviceWaitIdle(g_device);
    polishTeardown();
    destroyBuf(g_target); destroyBuf(g_weight); destroyBuf(g_canvas); destroyBuf(g_cands); destroyBuf(g_out); destroyBuf(g_staging); destroyBuf(g_gridBuf);
    if (g_evalPipe)  { vkDestroyPipeline(g_device, g_evalPipe, nullptr);  g_evalPipe = VK_NULL_HANDLE; }
    if (g_applyPipe) { vkDestroyPipeline(g_device, g_applyPipe, nullptr); g_applyPipe = VK_NULL_HANDLE; }
    if (g_gridPipe)  { vkDestroyPipeline(g_device, g_gridPipe, nullptr);  g_gridPipe = VK_NULL_HANDLE; }
    if (g_gridPL)    { vkDestroyPipelineLayout(g_device, g_gridPL, nullptr); g_gridPL = VK_NULL_HANDLE; }
    if (g_gridDSL)   { vkDestroyDescriptorSetLayout(g_device, g_gridDSL, nullptr); g_gridDSL = VK_NULL_HANDLE; }
    destroyBuf(g_scand); destroyBuf(g_sout); destroyBuf(g_adj); destroyBuf(g_best);
    destroyBuf(g_kindsB); destroyBuf(g_kindcdf); destroyBuf(g_gridcdf); destroyBuf(g_orient); destroyBuf(g_bound);
    if (g_genPipe)  { vkDestroyPipeline(g_device, g_genPipe, nullptr);  g_genPipe = VK_NULL_HANDLE; }
    if (g_prepPipe) { vkDestroyPipeline(g_device, g_prepPipe, nullptr); g_prepPipe = VK_NULL_HANDLE; }
    if (g_argPipe)  { vkDestroyPipeline(g_device, g_argPipe, nullptr);  g_argPipe = VK_NULL_HANDLE; }
    if (g_genPL)  { vkDestroyPipelineLayout(g_device, g_genPL, nullptr);  g_genPL = VK_NULL_HANDLE; }
    if (g_prepPL) { vkDestroyPipelineLayout(g_device, g_prepPL, nullptr); g_prepPL = VK_NULL_HANDLE; }
    if (g_argPL)  { vkDestroyPipelineLayout(g_device, g_argPL, nullptr);  g_argPL = VK_NULL_HANDLE; }
    if (g_genDSL)  { vkDestroyDescriptorSetLayout(g_device, g_genDSL, nullptr);  g_genDSL = VK_NULL_HANDLE; }
    if (g_prepDSL) { vkDestroyDescriptorSetLayout(g_device, g_prepDSL, nullptr); g_prepDSL = VK_NULL_HANDLE; }
    if (g_argDSL)  { vkDestroyDescriptorSetLayout(g_device, g_argDSL, nullptr);  g_argDSL = VK_NULL_HANDLE; }
    destroyBuf(g_seeds);
    if (g_msPipe) { vkDestroyPipeline(g_device, g_msPipe, nullptr); g_msPipe = VK_NULL_HANDLE; }
    if (g_gmPipe) { vkDestroyPipeline(g_device, g_gmPipe, nullptr); g_gmPipe = VK_NULL_HANDLE; }
    if (g_msPL) { vkDestroyPipelineLayout(g_device, g_msPL, nullptr); g_msPL = VK_NULL_HANDLE; }
    if (g_gmPL) { vkDestroyPipelineLayout(g_device, g_gmPL, nullptr); g_gmPL = VK_NULL_HANDLE; }
    if (g_msDSL) { vkDestroyDescriptorSetLayout(g_device, g_msDSL, nullptr); g_msDSL = VK_NULL_HANDLE; }
    if (g_gmDSL) { vkDestroyDescriptorSetLayout(g_device, g_gmDSL, nullptr); g_gmDSL = VK_NULL_HANDLE; }
    g_searchCap = 0; g_hasOrient = 0; g_hasBound = 0; g_searchSetsDirty = true; g_momentCap = 0;
    if (g_evalPL)    { vkDestroyPipelineLayout(g_device, g_evalPL, nullptr);  g_evalPL = VK_NULL_HANDLE; }
    if (g_applyPL)   { vkDestroyPipelineLayout(g_device, g_applyPL, nullptr); g_applyPL = VK_NULL_HANDLE; }
    if (g_descPool)  { vkDestroyDescriptorPool(g_device, g_descPool, nullptr); g_descPool = VK_NULL_HANDLE; g_evalSet = g_applySet = VK_NULL_HANDLE; }
    if (g_evalDSL)   { vkDestroyDescriptorSetLayout(g_device, g_evalDSL, nullptr);  g_evalDSL = VK_NULL_HANDLE; }
    if (g_applyDSL)  { vkDestroyDescriptorSetLayout(g_device, g_applyDSL, nullptr); g_applyDSL = VK_NULL_HANDLE; }
    if (g_fence)     { vkDestroyFence(g_device, g_fence, nullptr); g_fence = VK_NULL_HANDLE; }
    if (g_cmdPool)   { vkDestroyCommandPool(g_device, g_cmdPool, nullptr); g_cmdPool = VK_NULL_HANDLE; g_cmd = VK_NULL_HANDLE; }
    if (g_device)    { vkDestroyDevice(g_device, nullptr); g_device = VK_NULL_HANDLE; }
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
    if (vkCreateInstance(&ici, nullptr, &g_instance) != VK_SUCCESS) { g_lastError = 1002; return false; }
    volkLoadInstance(g_instance);

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
    VkPhysicalDeviceFeatures feats{};
    feats.shaderFloat64 = VK_TRUE; // eval.comp does the final ΔSSE math in double
    VkDeviceCreateInfo dci{VK_STRUCTURE_TYPE_DEVICE_CREATE_INFO};
    dci.queueCreateInfoCount = 1; dci.pQueueCreateInfos = &qci; dci.pEnabledFeatures = &feats;
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
    if (!makeDSL(5, g_evalDSL) || !makeDSL(1, g_applyDSL)) { g_lastError = 1009; return false; }

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
    if (!makePipe(g_evalPL, eval_spv, sizeof(eval_spv), g_evalPipe) ||
        !makePipe(g_applyPL, apply_spv, sizeof(apply_spv), g_applyPipe)) { g_lastError = 1011; return false; }
    // grid: error-grid reduction (4 storage buffers: target, canvas, weight, grid)
    if (!makeDSL(4, g_gridDSL)) { g_lastError = 1009; return false; }
    if (!makePL(g_gridDSL, sizeof(GridPC), g_gridPL)) { g_lastError = 1010; return false; }
    if (!makePipe(g_gridPL, grid_spv, sizeof(grid_spv), g_gridPipe)) { g_lastError = 1011; return false; }
    // on-device search: gen (6 bindings), prepadj (3), argmin (4); eval is reused for scoring.
    if (!makeDSL(6, g_genDSL) || !makeDSL(3, g_prepDSL) || !makeDSL(4, g_argDSL)) { g_lastError = 1009; return false; }
    if (!makePL(g_genDSL, sizeof(GenPC), g_genPL) || !makePL(g_prepDSL, sizeof(PrepPC), g_prepPL) || !makePL(g_argDSL, sizeof(ArgPC), g_argPL)) { g_lastError = 1010; return false; }
    if (!makePipe(g_genPL, gen_spv, sizeof(gen_spv), g_genPipe) || !makePipe(g_prepPL, prepadj_spv, sizeof(prepadj_spv), g_prepPipe) || !makePipe(g_argPL, argmin_spv, sizeof(argmin_spv), g_argPipe)) { g_lastError = 1011; return false; }
    // moment search: momentseed (3 bindings), genmoment (4); eval/prepadj/argmin reused.
    if (!makeDSL(3, g_msDSL) || !makeDSL(4, g_gmDSL)) { g_lastError = 1009; return false; }
    if (!makePL(g_msDSL, sizeof(MomSeedPC), g_msPL) || !makePL(g_gmDSL, sizeof(GenMomPC), g_gmPL)) { g_lastError = 1010; return false; }
    if (!makePipe(g_msPL, momentseed_spv, sizeof(momentseed_spv), g_msPipe) || !makePipe(g_gmPL, genmoment_spv, sizeof(genmoment_spv), g_gmPipe)) { g_lastError = 1011; return false; }

    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 35}; // +3 momentseed +4 genmoment
    VkDescriptorPoolCreateInfo dpci{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO};
    dpci.maxSets = 9; dpci.poolSizeCount = 1; dpci.pPoolSizes = &ps;
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
    // search sets: gen, search-eval (reuses the eval DSL), prepadj, argmin, momentseed, genmoment
    VkDescriptorSetLayout sl[6] = { g_genDSL, g_evalDSL, g_prepDSL, g_argDSL, g_msDSL, g_gmDSL };
    VkDescriptorSet* sd[6] = { &g_genSet, &g_sevalSet, &g_prepSet, &g_argSet, &g_msSet, &g_gmSet };
    for (int i = 0; i < 6; i++) {
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
    VkWriteDescriptorSet ws[10]; VkDescriptorBufferInfo bis[10];
    wr(g_evalSet, 0, g_cands.buf,  g_cands.size,  ws[0], bis[0]);
    wr(g_evalSet, 1, g_target.buf, g_target.size, ws[1], bis[1]);
    wr(g_evalSet, 2, g_canvas.buf, g_canvas.size, ws[2], bis[2]);
    wr(g_evalSet, 3, g_weight.buf, g_weight.size, ws[3], bis[3]);
    wr(g_evalSet, 4, g_out.buf,    g_out.size,    ws[4], bis[4]);
    wr(g_applySet, 0, g_canvas.buf, g_canvas.size, ws[5], bis[5]);
    wr(g_gridSet, 0, g_target.buf,  g_target.size,  ws[6], bis[6]);
    wr(g_gridSet, 1, g_canvas.buf,  g_canvas.size,  ws[7], bis[7]);
    wr(g_gridSet, 2, g_weight.buf,  g_weight.size,  ws[8], bis[8]);
    wr(g_gridSet, 3, g_gridBuf.buf, g_gridBuf.size, ws[9], bis[9]);
    vkUpdateDescriptorSets(g_device, 10, ws, 0, nullptr);
}

// submitWait records nothing itself — caller fills g_cmd; this submits + waits the fence.
void submitWait() {
    VkSubmitInfo si{VK_STRUCTURE_TYPE_SUBMIT_INFO}; si.commandBufferCount = 1; si.pCommandBuffers = &g_cmd;
    vkResetFences(g_device, 1, &g_fence);
    vkQueueSubmit(g_queue, 1, &si, g_fence);
    vkWaitForFences(g_device, 1, &g_fence, VK_TRUE, UINT64_MAX);
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

// in-cmd barrier: this dispatch's shader writes available to the next dispatch's reads
// AND writes (RAW + WAR over render/dC during the sequential per-shape passes).
void cmdBarrierRW() {
    VkMemoryBarrier mb{VK_STRUCTURE_TYPE_MEMORY_BARRIER};
    mb.srcAccessMask = VK_ACCESS_SHADER_WRITE_BIT;
    mb.dstAccessMask = VK_ACCESS_SHADER_READ_BIT | VK_ACCESS_SHADER_WRITE_BIT;
    vkCmdPipelineBarrier(g_cmd, VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT, VK_PIPELINE_STAGE_COMPUTE_SHADER_BIT,
        0, 1, &mb, 0, nullptr, 0, nullptr);
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
    VkDescriptorSetLayoutBinding bs[11];
    for (uint32_t i = 0; i < 11; i++) {
        bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
        bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
    }
    VkDescriptorSetLayoutCreateInfo dci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
    dci.bindingCount = 11; dci.pBindings = bs;
    if (vkCreateDescriptorSetLayout(g_device, &dci, nullptr, &g_pDSL) != VK_SUCCESS) return false;
    VkPushConstantRange pr{VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(PolishPC)};
    VkPipelineLayoutCreateInfo plci{VK_STRUCTURE_TYPE_PIPELINE_LAYOUT_CREATE_INFO};
    plci.setLayoutCount = 1; plci.pSetLayouts = &g_pDSL; plci.pushConstantRangeCount = 1; plci.pPushConstantRanges = &pr;
    if (vkCreatePipelineLayout(g_device, &plci, nullptr, &g_pPL) != VK_SUCCESS) return false;
    if (!makePolishPipe(p_dcinit_spv, sizeof(p_dcinit_spv), g_pDcinit)) return false;
    if (!makePolishPipe(p_loss_spv, sizeof(p_loss_spv), g_pLoss)) return false;
    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 11};
    VkDescriptorPoolCreateInfo dpci{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO};
    dpci.maxSets = 1; dpci.poolSizeCount = 1; dpci.pPoolSizes = &ps;
    if (vkCreateDescriptorPool(g_device, &dpci, nullptr, &g_pPool) != VK_SUCCESS) return false;
    VkDescriptorSetAllocateInfo a{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
    a.descriptorPool = g_pPool; a.descriptorSetCount = 1; a.pSetLayouts = &g_pDSL;
    return vkAllocateDescriptorSets(g_device, &a, &g_pSet) == VK_SUCCESS;
}

void writePolishDescriptors() {
    VkBuffer bufs[11] = { g_pP.buf, g_pcol.buf, g_pkinds.buf, g_prender.buf, g_pbelow.buf,
                          g_pdC.buf, g_ppgrad.buf, g_ppartials.buf, g_target.buf, g_weight.buf,
                          g_feAdj.buf };
    VkDeviceSize sizes[11] = { g_pP.size, g_pcol.size, g_pkinds.size, g_prender.size, g_pbelow.size,
                               g_pdC.size, g_ppgrad.size, g_ppartials.size, g_target.size, g_weight.size,
                               g_feAdj.size };
    VkWriteDescriptorSet w[11]; VkDescriptorBufferInfo bi[11];
    for (uint32_t i = 0; i < 11; i++) {
        bi[i] = {bufs[i], 0, sizes[i]};
        w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w[i].dstSet = g_pSet; w[i].dstBinding = i; w[i].descriptorCount = 1;
        w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
    }
    vkUpdateDescriptorSets(g_device, 11, w, 0, nullptr);
}

// buildFE: the false-edge DSL/pipelines/sets (see the FE globals comment). Built lazily on the
// first non-zero set-lambda, after fp_polish_setup (g_prender must exist for the descriptor write).
bool buildFE() {
    VkDescriptorSetLayoutBinding bs[6];
    for (uint32_t i = 0; i < 6; i++) {
        bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
        bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
    }
    VkDescriptorSetLayoutCreateInfo dci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
    dci.bindingCount = 6; dci.pBindings = bs;
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
    if (!mk(fe_dir_spv, sizeof(fe_dir_spv), g_feDirP)) return false;
    if (!mk(fe_adj_spv, sizeof(fe_adj_spv), g_feAdjP)) return false;
    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 12};
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
        VkBuffer bufs[6] = { src, g_feTL.buf, g_feRL.buf, g_feDir.buf, g_feAdj.buf, g_feParts.buf };
        VkDeviceSize sizes[6] = { srcSz, g_feTL.size, g_feRL.size, g_feDir.size, g_feAdj.size, g_feParts.size };
        VkWriteDescriptorSet w[6]; VkDescriptorBufferInfo bi[6];
        for (uint32_t i = 0; i < 6; i++) {
            bi[i] = {bufs[i], 0, sizes[i]};
            w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
            w[i].dstSet = set; w[i].dstBinding = i; w[i].descriptorCount = 1;
            w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
        }
        vkUpdateDescriptorSets(g_device, 6, w, 0, nullptr);
    };
    wr(g_feSetR, g_prender.buf, g_prender.size);
    // setT routes the TARGET through the luma pipe to fill g_feTL once; binding 2 (the luma
    // output) points at g_feTL here instead of the recon scratch.
    VkBuffer bufs[6] = { g_target.buf, g_feTL.buf, g_feTL.buf, g_feDir.buf, g_feAdj.buf, g_feParts.buf };
    VkDeviceSize sizes[6] = { g_target.size, g_feTL.size, g_feTL.size, g_feDir.size, g_feAdj.size, g_feParts.size };
    VkWriteDescriptorSet w[6]; VkDescriptorBufferInfo bi[6];
    for (uint32_t i = 0; i < 6; i++) {
        bi[i] = {bufs[i], 0, sizes[i]};
        w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w[i].dstSet = g_feSetT; w[i].dstBinding = i; w[i].descriptorCount = 1;
        w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
    }
    vkUpdateDescriptorSets(g_device, 6, w, 0, nullptr);
}

// cmdFEPasses records luma(render)+dir(+adj when forBackward) into the OPEN command buffer.
void cmdFEPasses(bool forBackward) {
    FePC fpc{ g_w, g_h, (float)g_pfelambda };
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

// buildTiledForward: dedicated 8-binding DSL + pipeline layout (TiledPC push) + pool + set
// for the one-dispatch tiled forward/hard. Fully separate from the per-shape polish DSL so
// the two paths never share descriptor/push state (the trap the first tiling attempt hit).
bool buildTiledForward() {
    VkDescriptorSetLayoutBinding bs[8];
    for (uint32_t i = 0; i < 8; i++) {
        bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
        bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
    }
    VkDescriptorSetLayoutCreateInfo dci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
    dci.bindingCount = 8; dci.pBindings = bs;
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
    if (!mk(pt_forward_spv, sizeof(pt_forward_spv), g_ptFwd)) return false;
    if (!mk(pt_hard_spv, sizeof(pt_hard_spv), g_ptHard)) return false;
    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 8};
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
    VkBuffer bufs[8] = { g_pP.buf, g_pcol.buf, g_pkinds.buf, g_prender.buf, g_pbelow.buf,
                         g_pbbxBuf.buf, g_pboffBuf.buf, g_pbase.buf };
    VkDeviceSize sizes[8] = { g_pP.size, g_pcol.size, g_pkinds.size, g_prender.size, g_pbelow.size,
                              g_pbbxBuf.size, g_pboffBuf.size, g_pbase.size };
    VkWriteDescriptorSet w[8]; VkDescriptorBufferInfo bi[8];
    for (uint32_t i = 0; i < 8; i++) {
        bi[i] = {bufs[i], 0, sizes[i]};
        w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w[i].dstSet = g_ptSet; w[i].dstBinding = i; w[i].descriptorCount = 1;
        w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
    }
    vkUpdateDescriptorSets(g_device, 8, w, 0, nullptr);
}

// buildBackwardTiled: dedicated 9-binding DSL + pipeline layout (TiledPC push) + pool + set
// for the two-pass barrier-free backward (Pass A dC walk + Pass B per-shape reduce).
bool buildBackwardTiled() {
    VkDescriptorSetLayoutBinding bs[9];
    for (uint32_t i = 0; i < 9; i++) {
        bs[i] = {}; bs[i].binding = i; bs[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER;
        bs[i].descriptorCount = 1; bs[i].stageFlags = VK_SHADER_STAGE_COMPUTE_BIT;
    }
    VkDescriptorSetLayoutCreateInfo dci{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_LAYOUT_CREATE_INFO};
    dci.bindingCount = 9; dci.pBindings = bs;
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
    if (!mk(pt_dcwalk_spv, sizeof(pt_dcwalk_spv), g_pbWalk)) return false;
    if (!mk(pt_breduce_spv, sizeof(pt_breduce_spv), g_pbReduce)) return false;
    VkDescriptorPoolSize ps{VK_DESCRIPTOR_TYPE_STORAGE_BUFFER, 9};
    VkDescriptorPoolCreateInfo dpci{VK_STRUCTURE_TYPE_DESCRIPTOR_POOL_CREATE_INFO};
    dpci.maxSets = 1; dpci.poolSizeCount = 1; dpci.pPoolSizes = &ps;
    if (vkCreateDescriptorPool(g_device, &dpci, nullptr, &g_pbPool) != VK_SUCCESS) return false;
    VkDescriptorSetAllocateInfo a{VK_STRUCTURE_TYPE_DESCRIPTOR_SET_ALLOCATE_INFO};
    a.descriptorPool = g_pbPool; a.descriptorSetCount = 1; a.pSetLayouts = &g_pbDSL;
    return vkAllocateDescriptorSets(g_device, &a, &g_pbSet) == VK_SUCCESS;
}

// writeBackwardDescriptors binds the 9 backward buffers. Re-called when g_pbelow/g_pdcsnap
// are (re)allocated (their handles change).
void writeBackwardDescriptors() {
    if (g_pbSet == VK_NULL_HANDLE) return;
    VkBuffer bufs[9] = { g_pP.buf, g_pcol.buf, g_pkinds.buf, g_pdC.buf, g_pbelow.buf,
                         g_pdcsnap.buf, g_ppgrad.buf, g_pbbxBuf.buf, g_pboffBuf.buf };
    VkDeviceSize sizes[9] = { g_pP.size, g_pcol.size, g_pkinds.size, g_pdC.size, g_pbelow.size,
                              g_pdcsnap.size, g_ppgrad.size, g_pbbxBuf.size, g_pboffBuf.size };
    VkWriteDescriptorSet w[9]; VkDescriptorBufferInfo bi[9];
    for (uint32_t i = 0; i < 9; i++) {
        bi[i] = {bufs[i], 0, sizes[i]};
        w[i] = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w[i].dstSet = g_pbSet; w[i].dstBinding = i; w[i].descriptorCount = 1;
        w[i].descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w[i].pBufferInfo = &bi[i];
    }
    vkUpdateDescriptorSets(g_device, 9, w, 0, nullptr);
}

// ensureStaging grows the shared staging buffer if a transfer needs more than its size.
void ensureStaging(VkDeviceSize need) {
    if (g_staging.buf != VK_NULL_HANDLE && need <= g_staging.size) return;
    destroyBuf(g_staging);
    createHost(need, g_staging);
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
    PolishPC pc{0, g_w, g_h, 0, 0, 0, 0, 0, g_pste, g_w * g_h, 0.0f, g_poklab, (float)g_pfelambda};
    vkCmdPushConstants(g_cmd, g_pPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
    vkCmdDispatch(g_cmd, PLOSS_GROUPS, 1, 1);
    if (g_pfelambda > 0.0) {
        cmdBarrierRW();
        cmdFEPasses(false); // luma(render) + dir -> g_feParts (λ·FE added host-side below)
    }
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
    double s = 0.0;
    const double* pp = (const double*)g_ppartials.map;
    for (int i = 0; i < PLOSS_GROUPS; i++) s += pp[i];
    if (g_pfelambda > 0.0) {
        double f = 0.0;
        const double* fp = (const double*)g_feParts.map;
        for (int i = 0; i < FE_GROUPS; i++) f += fp[i];
        s += g_pfelambda * f;
    }
    return s;
}

void writeSearchDescriptors() {
    auto wr = [](VkDescriptorSet set, uint32_t b, VkBuffer buf, VkDeviceSize sz, VkWriteDescriptorSet& w, VkDescriptorBufferInfo& bi) {
        bi = {buf, 0, sz};
        w = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w.dstSet = set; w.dstBinding = b; w.descriptorCount = 1; w.descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w.pBufferInfo = &bi;
    };
    VkWriteDescriptorSet w[18]; VkDescriptorBufferInfo bi[18]; int k = 0;
    wr(g_genSet, 0, g_scand.buf, g_scand.size, w[k], bi[k]); k++;
    wr(g_genSet, 1, g_kindsB.buf, g_kindsB.size, w[k], bi[k]); k++;
    wr(g_genSet, 2, g_kindcdf.buf, g_kindcdf.size, w[k], bi[k]); k++;
    wr(g_genSet, 3, g_gridcdf.buf, g_gridcdf.size, w[k], bi[k]); k++;
    wr(g_genSet, 4, g_orient.buf, g_orient.size, w[k], bi[k]); k++;
    wr(g_genSet, 5, g_bound.buf, g_bound.size, w[k], bi[k]); k++;
    wr(g_sevalSet, 0, g_scand.buf, g_scand.size, w[k], bi[k]); k++;
    wr(g_sevalSet, 1, g_target.buf, g_target.size, w[k], bi[k]); k++;
    wr(g_sevalSet, 2, g_canvas.buf, g_canvas.size, w[k], bi[k]); k++;
    wr(g_sevalSet, 3, g_weight.buf, g_weight.size, w[k], bi[k]); k++;
    wr(g_sevalSet, 4, g_sout.buf, g_sout.size, w[k], bi[k]); k++;
    wr(g_prepSet, 0, g_sout.buf, g_sout.size, w[k], bi[k]); k++;
    wr(g_prepSet, 1, g_scand.buf, g_scand.size, w[k], bi[k]); k++;
    wr(g_prepSet, 2, g_adj.buf, g_adj.size, w[k], bi[k]); k++;
    wr(g_argSet, 0, g_adj.buf, g_adj.size, w[k], bi[k]); k++;
    wr(g_argSet, 1, g_scand.buf, g_scand.size, w[k], bi[k]); k++;
    wr(g_argSet, 2, g_sout.buf, g_sout.size, w[k], bi[k]); k++;
    wr(g_argSet, 3, g_best.buf, g_best.size, w[k], bi[k]); k++;
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
            !createBufEx((size_t)n * sizeof(float), S, dl, false, g_adj)) return false;
        g_searchCap = n; g_searchSetsDirty = true;
    }
    if (g_searchSetsDirty) { writeSearchDescriptors(); g_searchSetsDirty = false; }
    return true;
}

void writeMomentDescriptors() {
    auto wr = [](VkDescriptorSet set, uint32_t b, VkBuffer buf, VkDeviceSize sz, VkWriteDescriptorSet& w, VkDescriptorBufferInfo& bi) {
        bi = {buf, 0, sz};
        w = {VK_STRUCTURE_TYPE_WRITE_DESCRIPTOR_SET};
        w.dstSet = set; w.dstBinding = b; w.descriptorCount = 1; w.descriptorType = VK_DESCRIPTOR_TYPE_STORAGE_BUFFER; w.pBufferInfo = &bi;
    };
    VkWriteDescriptorSet w[7]; VkDescriptorBufferInfo bi[7]; int k = 0;
    wr(g_msSet, 0, g_seeds.buf, g_seeds.size, w[k], bi[k]); k++;
    wr(g_msSet, 1, g_gridcdf.buf, g_gridcdf.size, w[k], bi[k]); k++;
    wr(g_msSet, 2, g_bound.buf, g_bound.size, w[k], bi[k]); k++;
    wr(g_gmSet, 0, g_scand.buf, g_scand.size, w[k], bi[k]); k++;
    wr(g_gmSet, 1, g_seeds.buf, g_seeds.size, w[k], bi[k]); k++;
    wr(g_gmSet, 2, g_kindsB.buf, g_kindsB.size, w[k], bi[k]); k++;
    wr(g_gmSet, 3, g_kindcdf.buf, g_kindcdf.size, w[k], bi[k]); k++;
    vkUpdateDescriptorSets(g_device, (uint32_t)k, w, 0, nullptr);
}

// ensureMoment grows the seed scratch to K seeds and rewrites the moment descriptor sets
// (cheap; called per shape — g_scand may have been recreated by ensureSearch).
bool ensureMoment(int K) {
    if (K > g_momentCap) {
        destroyBuf(g_seeds);
        if (!createBufEx((size_t)K * 6 * sizeof(float), VK_BUFFER_USAGE_STORAGE_BUFFER_BIT, VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT, false, g_seeds)) return false;
        g_momentCap = K;
    }
    writeMomentDescriptors();
    return true;
}

} // namespace

API int fp_init(const float* target, const float* weight, int w, int h, int maxCands, int gridSize) {
    teardown();
    g_lastError = 0;
    g_w = w; g_h = h; g_maxCands = maxCands; g_grid = gridSize;
    if (!buildContext()) { teardown(); return g_lastError ? g_lastError : 1; }

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
        !createBufEx(8 * sizeof(float), dstStore, devLocal, false, g_kindsB) ||
        !createBufEx(8 * sizeof(float), dstStore, devLocal, false, g_kindcdf) ||
        !createBufEx((size_t)gridSize * gridSize * sizeof(float), dstStore, devLocal, false, g_gridcdf) ||
        !createBufEx(npix * sizeof(float), dstStore, devLocal, false, g_orient) ||
        !createBufEx(npix * sizeof(float), dstStore, devLocal, false, g_bound)) {
        g_lastError = 1020; teardown(); return g_lastError;
    }
    g_searchCap = 0; g_hasOrient = 0; g_hasBound = 0; g_searchSetsDirty = true;
    // upload target + weight; zero the canvas — all via the staging buffer.
    memcpy(g_staging.map, target, tSize); copyBuf(g_staging.buf, g_target.buf, tSize);
    memcpy(g_staging.map, weight, wSize); copyBuf(g_staging.buf, g_weight.buf, wSize);
    memset(g_staging.map, 0, tSize);      copyBuf(g_staging.buf, g_canvas.buf, tSize);
    writeDescriptors();
    return 0;
}

API void fp_eval(const float* cands, int n, float* out) {
    if (n <= 0 || !g_device) return;
    if (n > g_maxCands) n = g_maxCands;
    memcpy(g_cands.map, cands, (size_t)n * 11 * sizeof(float));

    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_evalPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_evalPL, 0, 1, &g_evalSet, 0, nullptr);
    EvalPC pc{n, g_w, g_h, g_sampleBudget};
    vkCmdPushConstants(g_cmd, g_evalPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
    vkCmdDispatch(g_cmd, (uint32_t)n, 1, 1); // one workgroup per candidate
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();

    memcpy(out, g_out.map, (size_t)n * 5 * sizeof(float));
}

API void fp_apply(const float* cand) {
    if (!g_device) return;
    int kind = (int)(cand[0] + 0.5f);
    ApplyPC pc{kind, cand[1], cand[2], cand[3], cand[4], cand[5], cand[6],
               cand[7], cand[8], cand[9], cand[10], g_w, g_h};
    uint32_t groups = (uint32_t)(((size_t)g_w * g_h + 255) / 256);
    if (groups < 1) groups = 1;

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

API void fp_read_canvas(float* dst) {
    if (!g_device) return;
    VkDeviceSize sz = (size_t)g_w * g_h * 4 * sizeof(float);
    copyBuf(g_canvas.buf, g_staging.buf, sz); // device-local -> staging
    memcpy(dst, g_staging.map, sz);
}

API void fp_error_grid(float* out) {
    if (!g_device) return;
    int gw = g_grid, gh = g_grid;
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_gridPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_gridPL, 0, 1, &g_gridSet, 0, nullptr);
    GridPC pc{g_w, g_h, gw, gh};
    vkCmdPushConstants(g_cmd, g_gridPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
    vkCmdDispatch(g_cmd, (uint32_t)(gw * gh), 1, 1); // one workgroup per cell
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
    memcpy(out, g_gridBuf.map, (size_t)gw * gh * sizeof(float));
}

API void fp_set_orient(const float* orient) {
    if (!g_device) return;
    size_t sz = (size_t)g_w * g_h * sizeof(float);
    ensureStaging(sz);
    memcpy(g_staging.map, orient, sz);
    copyBuf(g_staging.buf, g_orient.buf, sz);
    g_hasOrient = 1;
}

API void fp_set_boundary_dist(const float* dist) {
    if (!g_device) return;
    if (!dist) { g_hasBound = 0; return; }
    size_t sz = (size_t)g_w * g_h * sizeof(float);
    ensureStaging(sz);
    memcpy(g_staging.map, dist, sz);
    copyBuf(g_staging.buf, g_bound.buf, sz);
    g_hasBound = 1;
}

// fp_search_random: generate n candidates on-device (seeded RNG + error-grid CDF + kind
// weighting), score them, apply the compactness penalty, and argmin — all in one submit,
// returning the single best candidate in out_best[12]. ip/fp carry the scalars (the syscall
// ABI can't pass float registers). Mirrors shim.cu fp_search_random (the simple path; the
// coarse-to-fine filter is a later optimisation).
API void fp_search_random(unsigned long long seed, const int* ip, const float* fp,
                          const float* kinds, const float* kindCDF, const float* gridCDF, float* out_best) {
    int n = ip[0], nKinds = ip[1], gw = ip[2], gh = ip[3];
    int compact = ip[4], shapeCount = ip[5], allowAlpha = ip[6];
    if (!g_device || n < 1 || nKinds < 1) { out_best[0] = 3.4028235e38f; return; }
    if (n > 65535) n = 65535; // one eval workgroup per candidate; clamp to the dispatch limit
    if (!ensureSearch(n)) { out_best[0] = 3.4028235e38f; return; }
    // upload kinds / kindCDF / gridCDF
    ensureStaging((size_t)gw * gh * sizeof(float));
    memcpy(g_staging.map, kinds, (size_t)nKinds * sizeof(float));   copyBuf(g_staging.buf, g_kindsB.buf, (size_t)nKinds * sizeof(float));
    memcpy(g_staging.map, kindCDF, (size_t)nKinds * sizeof(float)); copyBuf(g_staging.buf, g_kindcdf.buf, (size_t)nKinds * sizeof(float));
    memcpy(g_staging.map, gridCDF, (size_t)gw * gh * sizeof(float)); copyBuf(g_staging.buf, g_gridcdf.buf, (size_t)gw * gh * sizeof(float));

    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    // 1. generate
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_genPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_genPL, 0, 1, &g_genSet, 0, nullptr);
    GenPC gpc{(uint32_t)seed, (uint32_t)(seed >> 32), n, nKinds, gw, gh, g_w, g_h, allowAlpha, g_hasOrient, g_hasBound,
              fp[0], fp[1], fp[2], fp[3], fp[4], fp[5]};
    vkCmdPushConstants(g_cmd, g_genPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(gpc), &gpc);
    vkCmdDispatch(g_cmd, (uint32_t)((n + 255) / 256), 1, 1);
    cmdBarrierRW();
    // 2. score (reuse eval pipeline on the search buffers)
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_evalPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_evalPL, 0, 1, &g_sevalSet, 0, nullptr);
    EvalPC epc{n, g_w, g_h, g_sampleBudget};
    vkCmdPushConstants(g_cmd, g_evalPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(epc), &epc);
    vkCmdDispatch(g_cmd, (uint32_t)n, 1, 1);
    cmdBarrierRW();
    // 3. selection-adjusted score
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_prepPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_prepPL, 0, 1, &g_prepSet, 0, nullptr);
    PrepPC ppc{n, compact, shapeCount, g_w, g_h};
    vkCmdPushConstants(g_cmd, g_prepPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(ppc), &ppc);
    vkCmdDispatch(g_cmd, (uint32_t)((n + 255) / 256), 1, 1);
    cmdBarrierRW();
    // 4. argmin + gather
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_argPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_argPL, 0, 1, &g_argSet, 0, nullptr);
    ArgPC apc{n};
    vkCmdPushConstants(g_cmd, g_argPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(apc), &apc);
    vkCmdDispatch(g_cmd, 1, 1, 1);
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
    memcpy(out_best, g_best.map, 12 * sizeof(float));
}

// fp_search_moment: fit K covariance-ellipse seeds from the residual grid, generate a
// localised refine pool around them, score + argmin — one submit. ip[7] = K. Mirrors
// shim.cu fp_search_moment (simple path; coarse filter is a later optimisation).
API void fp_search_moment(unsigned long long seed, const int* ip, const float* fp,
                          const float* kinds, const float* kindCDF, const float* gridCDF, float* out_best) {
    int n = ip[0], nKinds = ip[1], gw = ip[2], gh = ip[3];
    int compact = ip[4], shapeCount = ip[5], allowAlpha = ip[6], K = ip[7];
    float maxR = fp[0], alphaMin = fp[1], boundPad = fp[3], boundMix = fp[4], canvasPad = fp[5];
    if (!g_device || n < 1 || nKinds < 1 || K < 1) { out_best[0] = 3.4028235e38f; return; }
    if (n > 65535) n = 65535;
    if (!ensureSearch(n) || !ensureMoment(K)) { out_best[0] = 3.4028235e38f; return; }
    int perSeed = n / K; if (perSeed < 1) perSeed = 1;
    int nGen = perSeed * K;
    ensureStaging((size_t)gw * gh * sizeof(float));
    memcpy(g_staging.map, kinds, (size_t)nKinds * sizeof(float));   copyBuf(g_staging.buf, g_kindsB.buf, (size_t)nKinds * sizeof(float));
    memcpy(g_staging.map, kindCDF, (size_t)nKinds * sizeof(float)); copyBuf(g_staging.buf, g_kindcdf.buf, (size_t)nKinds * sizeof(float));
    memcpy(g_staging.map, gridCDF, (size_t)gw * gh * sizeof(float)); copyBuf(g_staging.buf, g_gridcdf.buf, (size_t)gw * gh * sizeof(float));

    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_msPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_msPL, 0, 1, &g_msSet, 0, nullptr);
    MomSeedPC mpc{(uint32_t)seed, (uint32_t)(seed >> 32), K, gw, gh, g_w, g_h, g_hasBound, maxR, boundPad, boundMix};
    vkCmdPushConstants(g_cmd, g_msPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(mpc), &mpc);
    vkCmdDispatch(g_cmd, (uint32_t)((K + 127) / 128), 1, 1);
    cmdBarrierRW();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_gmPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_gmPL, 0, 1, &g_gmSet, 0, nullptr);
    GenMomPC gpc{(uint32_t)seed, (uint32_t)(seed >> 32), nGen, perSeed, K, nKinds, allowAlpha, g_w, g_h, alphaMin, canvasPad};
    vkCmdPushConstants(g_cmd, g_gmPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(gpc), &gpc);
    vkCmdDispatch(g_cmd, (uint32_t)((nGen + 255) / 256), 1, 1);
    cmdBarrierRW();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_evalPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_evalPL, 0, 1, &g_sevalSet, 0, nullptr);
    EvalPC epc{nGen, g_w, g_h, g_sampleBudget};
    vkCmdPushConstants(g_cmd, g_evalPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(epc), &epc);
    vkCmdDispatch(g_cmd, (uint32_t)nGen, 1, 1);
    cmdBarrierRW();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_prepPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_prepPL, 0, 1, &g_prepSet, 0, nullptr);
    PrepPC ppc{nGen, compact, shapeCount, g_w, g_h};
    vkCmdPushConstants(g_cmd, g_prepPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(ppc), &ppc);
    vkCmdDispatch(g_cmd, (uint32_t)((nGen + 255) / 256), 1, 1);
    cmdBarrierRW();
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_argPipe);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_argPL, 0, 1, &g_argSet, 0, nullptr);
    ArgPC apc{nGen};
    vkCmdPushConstants(g_cmd, g_argPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(apc), &apc);
    vkCmdDispatch(g_cmd, 1, 1, 1);
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
    memcpy(out_best, g_best.map, 12 * sizeof(float));
}

API void fp_reset(const float* canvas) {
    if (!g_device) return;
    VkDeviceSize sz = (size_t)g_w * g_h * 4 * sizeof(float);
    memcpy(g_staging.map, canvas, sz);
    copyBuf(g_staging.buf, g_canvas.buf, sz); // staging -> device-local
}

API void fp_set_sample_budget(int n) { g_sampleBudget = (n < 1) ? 4000 : n; }

API int fp_last_error() { int e = g_lastError; g_lastError = 0; return e; }

// ===================== joint-polish API (mirrors shim.cu fp_polish_*) =====================

API void fp_polish_setup(const float* base, int n) {
    polishTeardown();
    if (!g_device || n < 1) { g_lastError = 2001; return; }
    g_pn = n;
    if (!buildPolishPipelines()) { g_lastError = 2002; polishTeardown(); return; }
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
           && createBufEx((size_t)n * 6 * 8, SD, dl, false, g_pP)
           && createBufEx((size_t)n * 4 * 8, SD, dl, false, g_pcol)
           && createBufEx((size_t)n * 4, SD, dl, false, g_pkinds)
           && createBufEx((size_t)n * 16, SD, dl, false, g_pbbxBuf)
           && createBufEx((size_t)n * 4, SD, dl, false, g_pboffBuf)
           && createHost((size_t)n * 10 * 8, g_ppgrad)
           && createHost(PLOSS_GROUPS * 8, g_ppartials)
           && createBufEx(16, S, dl, false, g_pbelow)
           && createBufEx(16, S, dl, false, g_pdcsnap)
           && createBufEx(npix * 8, S, dl, false, g_feAdj); // dcinit binding 10 must be valid even with feLambda=0
    if (!ok) { g_lastError = 2003; polishTeardown(); return; }
    g_belowCap = 16;
    ensureStaging(npix * 16);
    memcpy(g_staging.map, base, npix * 16);
    copyBuf(g_staging.buf, g_pbase.buf, npix * 16);
    memset(g_ppgrad.map, 0, (size_t)n * 10 * 8);
    writePolishDescriptors();
    writeTiledForwardDescriptors();
    writeBackwardDescriptors();
}

API void fp_set_polish_ste(int on) { g_pste = on ? 1 : 0; }

// fp_set_polish_oklab toggles the perceptual OKLab colour metric in the polish loss and
// backward seed together (one flag keeps the optimisation self-consistent). Mirrors shim.cu.
API void fp_set_polish_oklab(int on) { g_poklab = on ? 1 : 0; }

// fp_set_polish_false_edge sets the false-edge λ (pointer: the Go syscall path keeps doubles out
// of XMM) and prepares the FE planes + the fixed target-luma plane. λ<=0 disables the term.
// Call AFTER fp_polish_setup (the FE descriptors reference the polish render buffer).
API void fp_set_polish_false_edge(const double* lambdaPtr) {
    g_pfelambda = lambdaPtr[0];
    if (g_pfelambda <= 0.0 || !g_device || g_pn < 1) return;
    size_t npix = (size_t)g_w * g_h;
    if (g_feDSL == VK_NULL_HANDLE && !buildFE()) { g_lastError = 2006; return; }
    if (g_feTL.buf == VK_NULL_HANDLE) {
        const VkBufferUsageFlags S = VK_BUFFER_USAGE_STORAGE_BUFFER_BIT;
        const VkMemoryPropertyFlags dl = VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT;
        if (!createBufEx(npix * 4, S, dl, false, g_feTL) ||
            !createBufEx(npix * 4, S, dl, false, g_feRL) ||
            !createBufEx(npix * 16, S, dl, false, g_feDir) ||
            !createHost(FE_GROUPS * 8, g_feParts)) { g_lastError = 2007; return; }
    }
    writeFEDescriptors();
    // One-off: target luma plane via the luma pipe on setT (binding 0 = target, 2 = g_feTL).
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    FePC fpc{ g_w, g_h, (float)g_pfelambda };
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_feLumaP);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_fePL, 0, 1, &g_feSetT, 0, nullptr);
    vkCmdPushConstants(g_cmd, g_fePL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(fpc), &fpc);
    vkCmdDispatch(g_cmd, (uint32_t)((npix + 255) / 256), 1, 1);
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
}

API void fp_polish_upload(const double* P, const double* col, const int* kinds,
                          const int* bbx, const long long* boff, long long belowTotal) {
    if (!g_device || g_pn < 1) return;
    VkDeviceSize need = (VkDeviceSize)belowTotal * 4;
    if (need < 4) need = 4;
    if (need > g_belowCap) {
        destroyBuf(g_pbelow);
        createBufEx(need, VK_BUFFER_USAGE_STORAGE_BUFFER_BIT, VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT, false, g_pbelow);
        destroyBuf(g_pdcsnap);
        createBufEx(need, VK_BUFFER_USAGE_STORAGE_BUFFER_BIT, VK_MEMORY_PROPERTY_DEVICE_LOCAL_BIT, false, g_pdcsnap);
        g_belowCap = need;
        writePolishDescriptors();
        writeTiledForwardDescriptors(); // g_pbelow handle changed -> rebind binding 4
        writeBackwardDescriptors();     // g_pbelow + g_pdcsnap handles changed
    }
    size_t szP = (size_t)g_pn * 6 * 8, szC = (size_t)g_pn * 4 * 8, szK = (size_t)g_pn * 4;
    ensureStaging(szP); // szP >= szC >= szK >= n*16 (bbx) >= n*4 (boff)
    memcpy(g_staging.map, P, szP);     copyBuf(g_staging.buf, g_pP.buf, szP);
    memcpy(g_staging.map, col, szC);   copyBuf(g_staging.buf, g_pcol.buf, szC);
    memcpy(g_staging.map, kinds, szK); copyBuf(g_staging.buf, g_pkinds.buf, szK);
    // bbx + boff(int32) -> device for the tiled forward/hard passes
    size_t szB = (size_t)g_pn * 16;
    memcpy(g_staging.map, bbx, szB); copyBuf(g_staging.buf, g_pbbxBuf.buf, szB);
    std::vector<int32_t> boff32((size_t)g_pn);
    for (int i = 0; i < g_pn; i++) boff32[i] = (int32_t)boff[i];
    size_t szO = (size_t)g_pn * 4;
    memcpy(g_staging.map, boff32.data(), szO); copyBuf(g_staging.buf, g_pboffBuf.buf, szO);
}

// fp_polish_forward — ONE tiled dispatch (thread-per-pixel walks all shapes in order). No
// base->render copy (the shader inits render=base per pixel) and no per-shape barriers.
API void fp_polish_forward(const int* bbxHost, const double* tauPtr) {
    if (!g_device || g_pn < 1) return;
    (void)bbxHost; // bbx now lives on-device (g_pbbxBuf)
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ptFwd);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ptPL, 0, 1, &g_ptSet, 0, nullptr);
    TiledPC pc{ g_pn, g_w, g_h, g_pste, (float)*tauPtr };
    vkCmdPushConstants(g_cmd, g_ptPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
    vkCmdDispatch(g_cmd, (uint32_t)(((size_t)g_w * g_h + 255) / 256), 1, 1);
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
}

API void fp_polish_loss(double* out) {
    if (!g_device || g_pn < 1) { *out = 0; return; }
    *out = computeLoss();
}

// fp_polish_hard_loss — ONE tiled hard dispatch (render=base, all shapes binary-inside in
// order) then the loss reduction. No base->render copy, no per-shape barriers.
API void fp_polish_hard_loss(const int* bbxHost, double* out) {
    if (!g_device || g_pn < 1) { *out = 0; return; }
    (void)bbxHost; // bbx lives on-device (g_pbbxBuf)
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ptHard);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_ptPL, 0, 1, &g_ptSet, 0, nullptr);
    TiledPC pc{ g_pn, g_w, g_h, g_pste, 0.0f };
    vkCmdPushConstants(g_cmd, g_ptPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pc), &pc);
    vkCmdDispatch(g_cmd, (uint32_t)(((size_t)g_w * g_h + 255) / 256), 1, 1);
    flushBarrier();
    vkEndCommandBuffer(g_cmd);
    submitWait();
    *out = computeLoss();
}

// fp_polish_backward — dcinit (full-image dC seed) + Pass A (per-pixel reverse dC walk ->
// dcsnap) + Pass B (per-shape gradient reduce, N workgroups one dispatch). 3 dispatches, 2
// barriers total — replacing the per-shape 1 + N-dispatch / N-barrier path. Bit-identical
// gradient (same fixed-order tree reduction); the barrier count drops from ~N to 2.
API void fp_polish_backward(const int* bbxHost, const double* tauPtr) {
    if (!g_device || g_pn < 1) return;
    (void)bbxHost; // bbx lives on-device (g_pbbxBuf)
    double tau = *tauPtr;
    vkResetCommandBuffer(g_cmd, 0);
    VkCommandBufferBeginInfo bi{VK_STRUCTURE_TYPE_COMMAND_BUFFER_BEGIN_INFO};
    bi.flags = VK_COMMAND_BUFFER_USAGE_ONE_TIME_SUBMIT_BIT;
    vkBeginCommandBuffer(g_cmd, &bi);
    // False-edge adjoint plane first (luma -> dir -> adj), feeding the dcinit dC seed below.
    if (g_pfelambda > 0.0) cmdFEPasses(true);
    // dC = 2*weight*(render-target) — full image, shared polish DSL
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pDcinit);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pPL, 0, 1, &g_pSet, 0, nullptr);
    PolishPC pcd{0, g_w, g_h, 0, 0, 0, 0, 0, g_pste, g_w * g_h, (float)tau, g_poklab, (float)g_pfelambda};
    vkCmdPushConstants(g_cmd, g_pPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(pcd), &pcd);
    vkCmdDispatch(g_cmd, (uint32_t)(((size_t)g_w * g_h + 255) / 256), 1, 1);
    cmdBarrierRW();
    // Pass A: per-pixel reverse dC walk -> dcsnap (one dispatch, no per-shape barriers)
    TiledPC tpc{ g_pn, g_w, g_h, g_pste, (float)tau };
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbWalk);
    vkCmdBindDescriptorSets(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbPL, 0, 1, &g_pbSet, 0, nullptr);
    vkCmdPushConstants(g_cmd, g_pbPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(tpc), &tpc);
    vkCmdDispatch(g_cmd, (uint32_t)(((size_t)g_w * g_h + 255) / 256), 1, 1);
    cmdBarrierRW();
    // Pass B: per-shape gradient reduce -> pgrad (N independent workgroups, one dispatch)
    vkCmdBindPipeline(g_cmd, VK_PIPELINE_BIND_POINT_COMPUTE, g_pbReduce);
    vkCmdPushConstants(g_cmd, g_pbPL, VK_SHADER_STAGE_COMPUTE_BIT, 0, sizeof(tpc), &tpc);
    vkCmdDispatch(g_cmd, (uint32_t)g_pn, 1, 1);
    flushBarrier(); // pgrad shader write -> host read
    vkEndCommandBuffer(g_cmd);
    submitWait();
}

API void fp_polish_read_grad(double* dst) {
    if (!g_device || g_pn < 1) return;
    memcpy(dst, g_ppgrad.map, (size_t)g_pn * 10 * 8);
}

API void fp_polish_read_render(float* dst) {
    if (!g_device) return;
    size_t sz = (size_t)g_w * g_h * 16;
    ensureStaging(sz);
    copyBuf(g_prender.buf, g_staging.buf, sz);
    memcpy(dst, g_staging.map, sz);
}

API void fp_polish_sync() { if (g_device) vkDeviceWaitIdle(g_device); }

API void fp_polish_free() { polishTeardown(); }

API void fp_free() { teardown(); }
