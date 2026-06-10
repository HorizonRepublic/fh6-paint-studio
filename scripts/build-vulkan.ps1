# build-vulkan.ps1 -- compile the Vulkan shim into fh6vk.dll (+ optional fh6paint-vulkan.exe).
# Portable toolchain: glslangValidator + headers + volk live in third_party\vulkan (no SDK install).
# Needs: that toolchain + MSVC Build Tools (cl.exe) + Go. Run from the repo root.
# Usage: powershell -ExecutionPolicy Bypass -File .\scripts\build-vulkan.ps1
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
    'eval'            = 'eval_spv'
    'apply'          = 'apply_spv'
    'grid'            = 'grid_spv'
    'gen'             = 'gen_spv'
    'prepadj'         = 'prepadj_spv'
    'argmin'          = 'argmin_spv'
    'momentseed'      = 'momentseed_spv'
    'genmoment'       = 'genmoment_spv'
    'polish_forward_tiled' = 'pt_forward_spv'
    'polish_hard_tiled'    = 'pt_hard_spv'
    'polish_dcinit'   = 'p_dcinit_spv'
    'polish_loss'     = 'p_loss_spv'
    'polish_dcwalk_tiled'    = 'pt_dcwalk_spv'
    'polish_backward_reduce' = 'pt_breduce_spv'
    'fe_luma'         = 'fe_luma_spv'
    'fe_dir'          = 'fe_dir_spv'
    'fe_adj'          = 'fe_adj_spv'
    'ssim_h'          = 'ssim_h_spv'
    'ssim_myinit'     = 'ssim_myinit_spv'
    'ssim_map'        = 'ssim_map_spv'
    'ssim_gh'         = 'ssim_gh_spv'
    'ssim_adj'        = 'ssim_adj_spv'
}
foreach ($s in $shaders.Keys) {
    & $glsl -V --target-env vulkan1.2 (Join-Path $shim "$s.comp") --vn $shaders[$s] -o (Join-Path $shim "$s.spv.h")
    if ($LASTEXITCODE -ne 0) { throw "glslangValidator failed on $s.comp" }
}

# --- compile shim.cpp + volk.c -> bin\fh6vk.dll (volk loads vulkan-1.dll at runtime; no import lib) ---
$bin = Join-Path $root "bin"
New-Item -ItemType Directory -Force -Path $bin | Out-Null
$dll = Join-Path $bin "fh6vk.dll"
Write-Host "Building $dll ..." -ForegroundColor Cyan
Push-Location $shim
& cl /nologo /O2 /EHsc /std:c++17 /LD "/I$inc" shim.cpp "$volkC" /Fe:"$dll" /Fo:"$bin\"
$ok = $LASTEXITCODE -eq 0
Pop-Location
if (-not $ok) { throw "cl failed building fh6vk.dll" }
Write-Host "Built bin\fh6vk.dll" -ForegroundColor Green

# --- copy DLL next to the Go package so `go test -tags vulkan` (CWD=pkg dir) finds it ---
Copy-Item $dll (Join-Path $root "internal\backend\vulkan\fh6vk.dll") -Force
Write-Host "Copied fh6vk.dll into internal\backend\vulkan\ (for tests)" -ForegroundColor Green

# --- optional: build the CLI with the vulkan backend ---
$env:CGO_ENABLED = "0"
Push-Location $root
& $goExe build -tags vulkan -o bin\fh6paint-vulkan.exe .\cmd\fh6paint
$ok = $LASTEXITCODE -eq 0
Pop-Location
if ($ok) {
    Write-Host "Built bin\fh6paint-vulkan.exe" -ForegroundColor Green
} else {
    Write-Host "(go build -tags vulkan skipped/failed -- backend wiring may be pending)" -ForegroundColor Yellow
}
