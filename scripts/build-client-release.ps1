# build-client-release.ps1 -- cut the distributable release: the Flutter client + the engine service
# + the Vulkan shim, staged clean and zipped.
#
#   powershell -ExecutionPolicy Bypass -File .\scripts\build-client-release.ps1 [-Version <x.y.z>] [-Out <dir>] [-SkipDLL]
#
# The product is the Flutter client talking to engined over a loopback socket, so the release is that
# pair plus the shim the engine loads. The Gio studio has its own script (build-release.ps1) and is
# not part of this one.
#
# The staging directory is built from scratch every time rather than cleaned in place: a release
# folder that accumulates logs, preference files and yesterday's binaries is how a stray file ends up
# shipped, and "clean" has to mean "nothing here that the build did not just put here".
param(
    [string]$Version = "0.0.0",
    [string]$Out = "release",
    [switch]$SkipDLL
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot; if (-not $root) { $root = (Get-Location).Path }
if (-not [System.IO.Path]::IsPathRooted($Out)) { $Out = Join-Path $root $Out }

Push-Location $root
try {
    # 1) The Vulkan shim, unless we are told the existing one is current.
    $vkdll = Join-Path $root "bin\fh6vk.dll"
    if ($SkipDLL -and (Test-Path $vkdll)) {
        Write-Host "Reusing the existing shim: $vkdll" -ForegroundColor Yellow
    } else {
        & (Join-Path $PSScriptRoot 'build-vulkan.ps1')
        if ($LASTEXITCODE -ne 0) { throw "Vulkan shim build failed" }
    }

    # 2) Version resources for both Go binaries, so neither ships nameless.
    & (Join-Path $PSScriptRoot 'gen-winres.ps1') -Version $Version
    if ($LASTEXITCODE -ne 0) { throw "version resources failed" }

    # 3) The engine service. No console window: it is a child process and a flashing console on
    # every launch is not something a user should have to look at.
    $env:CGO_ENABLED = "0"
    $env:GOAMD64 = "v1"
    & go build -tags vulkan -trimpath -ldflags "-s -w -H windowsgui" -o (Join-Path $root "bin\engined.exe") ./cmd/engined
    if ($LASTEXITCODE -ne 0) { throw "engined build failed" }

    # 4) The client.
    Push-Location (Join-Path $root 'client')
    try {
        & flutter build windows --release
        if ($LASTEXITCODE -ne 0) { throw "flutter build failed" }
    } finally { Pop-Location }

    # 5) Stage. Copy what the build produced, and nothing else.
    $stage = Join-Path $Out "fh6-paint-studio"
    if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
    New-Item -ItemType Directory -Force $stage | Out-Null

    $built = Join-Path $root 'client\build\windows\x64\runner\Release'
    Copy-Item (Join-Path $built '*.exe') $stage
    Copy-Item (Join-Path $built '*.dll') $stage
    Copy-Item (Join-Path $built 'data') $stage -Recurse
    Copy-Item (Join-Path $root 'bin\engined.exe') $stage
    Copy-Item $vkdll $stage

    # Anything the app WROTE beside itself while it was being tested is not part of the release.
    Get-ChildItem $stage -Recurse -Include *.log, *.log.1, client.json, native_assets.json |
        Remove-Item -Force -ErrorAction SilentlyContinue

    # 6) Zip it, replacing any previous one.
    $zip = Join-Path $Out "fh6-paint-studio-$Version.zip"
    if (Test-Path $zip) { Remove-Item -Force $zip }
    Compress-Archive -Path $stage -DestinationPath $zip -CompressionLevel Optimal

    # Checksums beside the zip. One antivirus engine out of seventy calls this
    # unsigned binary a generic malware hash, and it always will until the build
    # is signed -- so the least we can do is let a suspicious user verify that
    # the file they downloaded is the file we built.
    $sums = Join-Path $Out "fh6-paint-studio-$Version.sha256"
    Get-FileHash $zip -Algorithm SHA256 |
        ForEach-Object { "{0}  {1}" -f $_.Hash.ToLower(), (Split-Path $_.Path -Leaf) } |
        Set-Content $sums -Encoding utf8
    Get-ChildItem $stage -Recurse -File |
        Get-FileHash -Algorithm SHA256 |
        ForEach-Object { "{0}  {1}" -f $_.Hash.ToLower(), $_.Path.Substring($stage.Length + 1) } |
        Add-Content $sums -Encoding utf8

    $size = [math]::Round((Get-Item $zip).Length / 1MB, 1)
    Write-Host ""
    Write-Host "Release $Version -> $zip ($size MB)" -ForegroundColor Green
    Get-ChildItem $stage | Select-Object Name, @{n = 'MB'; e = { [math]::Round($_.Length / 1MB, 2) } } | Format-Table
} finally { Pop-Location }
