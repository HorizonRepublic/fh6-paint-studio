# build-cuda-fat.ps1 -- SUPERSEDED (2026-08-03). CUDA is no longer shipped: the release is Vulkan
# only, so nothing calls this any more. `build-cuda.ps1 -Release` produces the same fat DLL against
# the INSTALLED toolkit and is the one to maintain; this script exists solely because it targets a
# PORTABLE toolkit dir (-Toolkit), which the old CI needed. Delete it once that is definitely moot.
#
# Fat build: compile fh6cuda.dll as a MULTI-ARCH binary so it runs on a broad range of NVIDIA GPUs
# (not just the build box's RTX 5080).
#
# Uses a PORTABLE CUDA 12.8 toolkit (no admin install). CUDA 13.x dropped Pascal/Maxwell, but 12.8
# spans sm_61 (Pascal GTX 10xx) .. sm_120 (Blackwell RTX 50xx). Build the portable toolkit from the
# redist component archives (cuda_nvcc + cuda_cudart + cuda_cccl, windows-x86_64, 12.8.x) merged into
# one bin/include/lib/nvvm dir. The dev build (build-cuda.ps1) stays on the system CUDA 13.2
# -arch=native for fast iteration; THIS script only cuts a release DLL.
#
# Coverage: explicit SASS for Pascal/Turing/Ampere/Ada/Blackwell + a compute_61 PTX fallback
# (forward-compatible JIT for any other >=6.1 GPU incl Volta/Hopper/future). Maxwell (sm_52, <5.3)
# is NOT supported: the FP16/half2 coarse filter needs compute capability >= 5.3.
#
# Usage: powershell -ExecutionPolicy Bypass -File .\scripts\build-cuda-fat.ps1 [-Toolkit <dir>]
# Point -Toolkit (or set the CUDA_TOOLKIT env var) at your portable CUDA 12.8 toolkit dir.
param(
    [string]$Toolkit = $(if ($env:CUDA_TOOLKIT) { $env:CUDA_TOOLKIT } else { "C:\cuda12.8-portable\toolkit" })
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not $root) { $root = (Get-Location).Path }

$nvcc = Join-Path $Toolkit "bin\nvcc.exe"
if (-not (Test-Path $nvcc)) { throw "portable nvcc not found at $nvcc -- fetch the CUDA 12.8 redist components" }
Write-Host "nvcc (portable): $nvcc" -ForegroundColor Cyan
& $nvcc --version | Select-String release

# --- import MSVC x64 environment so nvcc finds cl.exe ---
if (-not (Get-Command cl -ErrorAction SilentlyContinue)) {
    $vcvars = "C:\Program Files (x86)\Microsoft Visual Studio\2022\BuildTools\VC\Auxiliary\Build\vcvars64.bat"
    if (-not (Test-Path $vcvars)) {
        $vc = Get-ChildItem "C:\Program Files (x86)\Microsoft Visual Studio\2022" -Recurse -Filter "vcvars64.bat" -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($vc) { $vcvars = $vc.FullName }
    }
    if (-not (Test-Path $vcvars)) { throw "vcvars64.bat not found -- install MSVC C++ Build Tools" }
    Write-Host "vcvars: $vcvars" -ForegroundColor Cyan
    cmd /c "`"$vcvars`" >nul 2>&1 && set" | ForEach-Object {
        if ($_ -match '^([^=]+)=(.*)$') { Set-Item "env:$($matches[1])" $matches[2] }
    }
}

# Multi-arch gaming coverage (SASS) + PTX fallback. Min sm_61 (FP16/half2 needs CC>=5.3).
$gencodes = @(
    "-gencode=arch=compute_61,code=sm_61",     # Pascal    (GTX 10xx)
    "-gencode=arch=compute_75,code=sm_75",     # Turing    (GTX 16xx / RTX 20xx)
    "-gencode=arch=compute_86,code=sm_86",     # Ampere    (RTX 30xx)
    "-gencode=arch=compute_89,code=sm_89",     # Ada       (RTX 40xx)
    "-gencode=arch=compute_120,code=sm_120",   # Blackwell (RTX 50xx)
    "-gencode=arch=compute_61,code=compute_61" # PTX JIT for any other >=6.1 (Volta/Hopper/future)
)

$out = Join-Path $root "release"
New-Item -ItemType Directory -Force -Path $out | Out-Null
$dll = Join-Path $out "fh6cuda.dll"
$cu  = Join-Path $root "internal\backend\cuda\shim.cu"
Write-Host "Building FAT $dll (sm_61/75/86/89/120 + PTX) ..." -ForegroundColor Cyan
# -allow-unsupported-compiler: the box MSVC may be newer than 12.8 officially lists (the dev 13.2
# build uses the same cl.exe fine; the version gate is conservative).
& $nvcc -O3 -shared --cudart static -allow-unsupported-compiler @gencodes -o $dll $cu
if ($LASTEXITCODE -ne 0) { throw "nvcc fat build failed ($LASTEXITCODE)" }
$mb = [math]::Round((Get-Item $dll).Length / 1MB, 1)
Write-Host "Built release\fh6cuda.dll ($mb MB, multi-arch)" -ForegroundColor Green
