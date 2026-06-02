# build-cuda.ps1 -- compile the CUDA shim into fh6cuda.dll and build fh6paint-cuda.exe.
# Run from the repo root. Needs: CUDA Toolkit (nvcc) + MSVC Build Tools (cl.exe) + Go.
# Usage: powershell -ExecutionPolicy Bypass -File .\build-cuda.ps1
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
if (-not $root) { $root = (Get-Location).Path }

function Find-One($base, $filter) {
    if (-not (Test-Path $base)) { return $null }
    Get-ChildItem $base -Recurse -Filter $filter -ErrorAction SilentlyContinue | Select-Object -First 1
}

# --- locate Go ---
$goExe = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $goExe) { $goExe = "$env:USERPROFILE\go-dist\go\bin\go.exe" }
if (-not (Test-Path $goExe)) { throw "go not found" }

# --- locate nvcc (CUDA may not yet be on this shell's PATH) ---
$nvcc = (Get-Command nvcc -ErrorAction SilentlyContinue).Source
if (-not $nvcc) {
    $c = Find-One "C:\Program Files\NVIDIA GPU Computing Toolkit\CUDA" "nvcc.exe"
    if ($c) { $nvcc = $c.FullName }
}
if (-not $nvcc) { throw "nvcc not found -- install CUDA Toolkit" }
Write-Host "nvcc: $nvcc" -ForegroundColor Cyan

# --- import MSVC x64 environment so nvcc finds cl.exe ---
if (-not (Get-Command cl -ErrorAction SilentlyContinue)) {
    $vcvars = "C:\Program Files (x86)\Microsoft Visual Studio\2022\BuildTools\VC\Auxiliary\Build\vcvars64.bat"
    if (-not (Test-Path $vcvars)) {
        $vc = Find-One "C:\Program Files (x86)\Microsoft Visual Studio\2022" "vcvars64.bat"
        if ($vc) { $vcvars = $vc.FullName }
    }
    if (-not (Test-Path $vcvars)) { throw "vcvars64.bat not found -- install MSVC C++ Build Tools" }
    Write-Host "vcvars: $vcvars" -ForegroundColor Cyan
    cmd /c "`"$vcvars`" >nul 2>&1 && set" | ForEach-Object {
        if ($_ -match '^([^=]+)=(.*)$') { Set-Item "env:$($matches[1])" $matches[2] }
    }
}

# All build artifacts go to bin\ (gitignored). The DLL sits next to the exe so the
# OS loader finds it (LoadLibrary searches the executable's directory first).
$bin = Join-Path $root "bin"
New-Item -ItemType Directory -Force -Path $bin | Out-Null

# --- compile shim.cu -> bin\fh6cuda.dll (static cudart = self-contained) ---
$dll = Join-Path $bin "fh6cuda.dll"
$cu  = Join-Path $root "internal\backend\cuda\shim.cu"
Write-Host "Building $dll ..." -ForegroundColor Cyan
& $nvcc -O3 -shared --cudart static -arch=native -o $dll $cu
if ($LASTEXITCODE -ne 0) { throw "nvcc failed ($LASTEXITCODE)" }
Write-Host "Built bin\fh6cuda.dll" -ForegroundColor Green

# --- ensure x/sys dep, then build bin\fh6paint-cuda.exe ---
$env:CGO_ENABLED = "0"
Push-Location $root
& $goExe get golang.org/x/sys/windows
& $goExe build -tags cuda -o bin\fh6paint-cuda.exe .\cmd\fh6paint
$ok = $LASTEXITCODE -eq 0
Pop-Location
if (-not $ok) { throw "go build -tags cuda failed" }
Write-Host "Built bin\fh6paint-cuda.exe" -ForegroundColor Green
Write-Host "`nRun: .\bin\fh6paint-cuda.exe -input testdata/super-image.jpg -shapes 1000 -max-res 1200 -preview out/cuda.png -output out/cuda.json"
