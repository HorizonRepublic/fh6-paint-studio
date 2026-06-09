# setup-vulkan-ci.ps1 -- assemble third_party\vulkan on a clean CI runner so build-vulkan.ps1 can
# compile fh6vk.dll. The toolchain folder is gitignored (a local, portable thing), so on CI we fetch a
# pinned, compatible set from upstream: glslang (the GLSL->SPIR-V compiler), the Vulkan headers, and
# volk. shim.cpp pulls in volk + the Vulkan headers; VMA is not used, so it is not fetched.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue' # IWR's progress bar makes large downloads crawl on PS 5.1
$root = Split-Path -Parent $PSScriptRoot; if (-not $root) { $root = (Get-Location).Path }
$vk = Join-Path $root 'third_party\vulkan'
$tmp = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { $env:TEMP }
New-Item -ItemType Directory -Force -Path (Join-Path $vk 'bin'), (Join-Path $vk 'include'), (Join-Path $vk 'src') | Out-Null

# Vulkan-Headers and volk MUST come from the same SDK tag, so volk never references a symbol the
# headers lack. Recent enough for the shim's Vulkan 1.2 use.
$tag = 'vulkan-sdk-1.4.350.0'

function Get-Zip($url, $dest) {
    $zip = Join-Path $tmp ([IO.Path]::GetRandomFileName() + '.zip')
    Write-Host "fetch $url"
    Invoke-WebRequest -Uri $url -OutFile $zip
    if (Test-Path $dest) { Remove-Item $dest -Recurse -Force }
    Expand-Archive -Path $zip -DestinationPath $dest -Force
}

# 1) glslang -> bin\glslangValidator.exe. Newer builds name the binary glslang.exe; either accepts the
#    -V/--target-env/--vn flags build-vulkan.ps1 uses, so copy whichever is present under that name.
$gdir = Join-Path $tmp 'glslang'
Get-Zip 'https://github.com/KhronosGroup/glslang/releases/download/main-tot/glslang-master-windows-Release.zip' $gdir
$gexe = Get-ChildItem $gdir -Recurse -Include 'glslangValidator.exe', 'glslang.exe' | Select-Object -First 1
if (-not $gexe) { throw 'glslang executable not found in the release zip' }
Copy-Item $gexe.FullName (Join-Path $vk 'bin\glslangValidator.exe') -Force

# 2) Vulkan-Headers -> include\vulkan + include\vk_video.
$hdir = Join-Path $tmp 'vk-headers'
Get-Zip "https://github.com/KhronosGroup/Vulkan-Headers/archive/refs/tags/$tag.zip" $hdir
$hroot = Get-ChildItem $hdir -Directory | Select-Object -First 1
Copy-Item (Join-Path $hroot.FullName 'include\vulkan') (Join-Path $vk 'include\vulkan') -Recurse -Force
Copy-Item (Join-Path $hroot.FullName 'include\vk_video') (Join-Path $vk 'include\vk_video') -Recurse -Force

# 3) volk -> include\volk.h + src\volk.c (same tag as the headers).
$vdir = Join-Path $tmp 'volk'
Get-Zip "https://github.com/zeux/volk/archive/refs/tags/$tag.zip" $vdir
$vroot = Get-ChildItem $vdir -Directory | Select-Object -First 1
Copy-Item (Join-Path $vroot.FullName 'volk.h') (Join-Path $vk 'include\volk.h') -Force
Copy-Item (Join-Path $vroot.FullName 'volk.c') (Join-Path $vk 'src\volk.c') -Force

Write-Host "Vulkan toolchain assembled in $vk" -ForegroundColor Green
& (Join-Path $vk 'bin\glslangValidator.exe') --version | Select-Object -First 1
