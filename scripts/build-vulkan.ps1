# build-vulkan.ps1 -- compile the Vulkan shim into fh6vk.dll (+ the CLI, binh6paint.exe).
# Portable toolchain: glslangValidator + headers + volk live in third_party\vulkan (no SDK install).
# Needs: that toolchain + MSVC Build Tools (cl.exe) + Go. Run from the repo root.
# Usage: powershell -ExecutionPolicy Bypass -File .\scripts\build-vulkan.ps1 [-Version <x.y.z>]
param([string]$Version = "0.0.0")
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not $root) { $root = (Get-Location).Path }

$vk    = Join-Path $root "third_party\vulkan"
$glsl  = Join-Path $vk "bin\glslangValidator.exe"
$inc   = Join-Path $vk "include"
$volkC = Join-Path $vk "src\volk.c"
$shim  = Join-Path $root "internal\backend\vulkan\shim"
if (-not (Test-Path $glsl)) { throw "portable Vulkan toolchain missing -- expected $glsl" }

# --- locate Go ---
$goExe = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $goExe) { $goExe = "$env:USERPROFILE\go-dist\go\bin\go.exe" }
if (-not (Test-Path $goExe)) { throw "go not found" }

# --- import MSVC x64 environment so cl.exe is on PATH ---
if (-not (Get-Command cl -ErrorAction SilentlyContinue)) {
    $vcvars = "C:\Program Files (x86)\Microsoft Visual Studio\2022\BuildTools\VC\Auxiliary\Build\vcvars64.bat"
    if (-not (Test-Path $vcvars)) {
        $vc = Get-ChildItem "C:\Program Files (x86)\Microsoft Visual Studio\2022" -Recurse -Filter vcvars64.bat -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($vc) { $vcvars = $vc.FullName }
    }
    if (-not (Test-Path $vcvars)) { throw "vcvars64.bat not found -- install MSVC C++ Build Tools" }
    Write-Host "vcvars: $vcvars" -ForegroundColor Cyan
    cmd /c "`"$vcvars`" >nul 2>&1 && set" | ForEach-Object {
        if ($_ -match '^([^=]+)=(.*)$') { Set-Item "env:$($matches[1])" $matches[2] }
    }
}

# --- compile shaders -> embeddable SPIR-V C headers (glslangValidator --vn) ---
# Map each .comp to its C array variable name (the shim #includes <name>.spv.h).
Write-Host "Compiling shaders -> SPIR-V ..." -ForegroundColor Cyan
$shaders = [ordered]@{
    'refine'          = 'refine_spv'
    'eval'            = 'eval_spv'
    'apply'          = 'apply_spv'
    'grid'            = 'grid_spv'
    'gen'             = 'gen_spv'
    'prepadj'         = 'prepadj_spv'
    'argmin'          = 'argmin_spv'
    'mutate'          = 'mutate_spv'
    'momentseed'      = 'momentseed_spv'
    'genmoment'       = 'genmoment_spv'
    'polish_forward_tiled' = 'pt_forward_spv'
    'polish_hard_tiled'    = 'pt_hard_spv'
    'polish_dcinit'   = 'p_dcinit_spv'
    'polish_loss'     = 'p_loss_spv'
    'polish_dcwalk_tiled'    = 'pt_dcwalk_spv'
    'polish_backward_reduce' = 'pt_breduce_spv'
    'polish_backward_combine' = 'pt_bcombine_spv'
    'fe_luma'         = 'fe_luma_spv'
    'fe_dir'          = 'fe_dir_spv'
    'fe_adj'          = 'fe_adj_spv'
    'ssim_h'          = 'ssim_h_spv'
    'ssim_myinit'     = 'ssim_myinit_spv'
    'ssim_map'        = 'ssim_map_spv'
    'ssim_gh'         = 'ssim_gh_spv'
    'ssim_adj'        = 'ssim_adj_spv'
    'eagle_scharr'    = 'eagle_scharr_spv'
    'eagle_var'       = 'eagle_var_spv'
    'eagle_boxx'      = 'eagle_boxx_spv'
    'eagle_boxy'      = 'eagle_boxy_spv'
    'eagle_hpfin'     = 'eagle_hpfin_spv'
    'eagle_loss'      = 'eagle_loss_spv'
    'eagle_sign'      = 'eagle_sign_spv'
    'eagle_um'        = 'eagle_um_spv'
    'eagle_varadj'    = 'eagle_varadj_spv'
    'eagle_scharradj' = 'eagle_scharradj_spv'
    'coarse_min'      = 'coarse_min_spv'
    'coarse_gather'   = 'coarse_gather_spv'
    'conv3x3'         = 'conv3x3_spv'
    'prop_input'      = 'prop_input_spv'
    'prop_orient'     = 'prop_orient_spv'
    'prop_head'       = 'prop_head_spv'
    'tile_count'      = 'tb_count_spv'
    'tile_scan'       = 'tb_scan_spv'
    'tile_fill'       = 'tb_fill_spv'
}
foreach ($s in $shaders.Keys) {
    & $glsl -V --target-env vulkan1.2 (Join-Path $shim "$s.comp") --vn $shaders[$s] -o (Join-Path $shim "$s.spv.h")
    if ($LASTEXITCODE -ne 0) { throw "glslangValidator failed on $s.comp" }
}

# fp32 variants of every REAL-typed shader (see the FH6_FP32 guard in the sources): devices
# without shaderFloat64 get these pipelines instead of a startup failure. Variable naming: the
# fp64 array `foo_spv` pairs with `foo_f32_spv`, which is what shim.cpp's RSPV macro pastes.
$fp64Shaders = @('eval','grid','momentseed','polish_forward_tiled','polish_hard_tiled',
    'polish_dcinit','polish_loss','polish_dcwalk_tiled','polish_backward_reduce',
    'polish_backward_combine','fe_dir','fe_adj','ssim_h','ssim_myinit','ssim_map','ssim_gh',
    'ssim_adj','eagle_scharr','eagle_var','eagle_boxx','eagle_boxy','eagle_hpfin','eagle_loss',
    'eagle_sign','eagle_um','eagle_varadj','eagle_scharradj')
foreach ($s in $fp64Shaders) {
    $v32 = ($shaders[$s] -replace '_spv$','') + '_f32_spv'
    & $glsl -V --target-env vulkan1.2 -DFH6_FP32=1 (Join-Path $shim "$s.comp") --vn $v32 -o (Join-Path $shim "${s}_f32.spv.h")
    if ($LASTEXITCODE -ne 0) { throw "glslangValidator failed on $s.comp (fp32 variant)" }
}

# --- compile shim.cpp + volk.c -> bin\fh6vk.dll (volk loads vulkan-1.dll at runtime; no import lib) ---
$bin = Join-Path $root "bin"
New-Item -ItemType Directory -Force -Path $bin | Out-Null
$dll = Join-Path $bin "fh6vk.dll"
Write-Host "Building $dll ..." -ForegroundColor Cyan
# The version resource is compiled first and linked in: the shim used to ship with no company, no
# description and no version at all, which is exactly the shape a scanner treats as anonymous.
$verNum = (($Version -replace '^v', '') -split '-')[0]
$vp = $verNum -split '\.'
$vMaj = if ($vp.Count -gt 0 -and $vp[0] -match '^\d+$') { [int]$vp[0] } else { 0 }
$vMin = if ($vp.Count -gt 1 -and $vp[1] -match '^\d+$') { [int]$vp[1] } else { 0 }
$vPat = if ($vp.Count -gt 2 -and $vp[2] -match '^\d+$') { [int]$vp[2] } else { 0 }

Push-Location $shim
# Generated rather than passed with /d: rc.exe has no way to take a quoted string on the command
# line, and FH6VK_VER_STRING has to reach the resource already spelled as a string literal.
@(
    "// Generated by scripts\build-vulkan.ps1. Do not edit; do not commit."
    "#define FH6VK_VER_MAJOR $vMaj"
    "#define FH6VK_VER_MINOR $vMin"
    "#define FH6VK_VER_PATCH $vPat"
    "#define FH6VK_VER_STRING `"$verNum`""
) | Set-Content (Join-Path $shim "fh6vk_version.h") -Encoding ascii

$res = Join-Path $bin "fh6vk.res"
& rc /nologo /fo "$res" fh6vk.rc
if ($LASTEXITCODE -ne 0) { Pop-Location; throw "rc failed on fh6vk.rc" }
& cl /nologo /O2 /EHsc /std:c++17 /LD "/I$inc" shim.cpp "$volkC" "$res" /Fe:"$dll" /Fo:"$bin\"
$ok = $LASTEXITCODE -eq 0
Pop-Location
if (-not $ok) { throw "cl failed building fh6vk.dll" }
Write-Host "Built bin\fh6vk.dll" -ForegroundColor Green

# --- copy the DLL next to every package whose tests load it ---
# `go test` runs with CWD set to the package directory, so each of these needs its own copy. They are
# listed here rather than refreshed by hand because a STALE copy does not look like a stale copy: the
# test fails with "procedure not found" for whichever export was added last, which reads as a broken
# feature rather than an out-of-date file. Add a directory here the moment its tests touch Vulkan.
$testCopies = @(
    "internal\backend\vulkan",
    "internal\engine",
    "internal\runner",
    "internal\ipc"
)
foreach ($d in $testCopies) {
    $dest = Join-Path $root $d
    if (Test-Path $dest) { Copy-Item $dll (Join-Path $dest "fh6vk.dll") -Force }
}
Write-Host "Copied fh6vk.dll into $($testCopies.Count) package dirs (for tests)" -ForegroundColor Green

# --- optional: build the CLI ---
# ONE name: bin\fh6paint.exe — the same file vkregress/abbench run. The old fh6paint-vulkan.exe
# split let a Go change be "validated" by a harness that never loaded it (bit twice in one night).
$env:CGO_ENABLED = "0"
Push-Location $root
& $goExe build -o bin\fh6paint.exe .\cmd\fh6paint
$ok = $LASTEXITCODE -eq 0
Pop-Location
if ($ok) {
    Write-Host "Built bin\fh6paint.exe" -ForegroundColor Green
} else {
    Write-Host "(go build skipped/failed -- backend wiring may be pending)" -ForegroundColor Yellow
}
