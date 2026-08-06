# gen-winres.ps1 -- regenerate each Go binary's Windows resource (icon + manifest + version info)
# into cmd\<name>\rsrc_windows_amd64.syso via goversioninfo. `go build` then links any *.syso in the
# package automatically.
#
#   powershell -File scripts\gen-winres.ps1 [-Version <x.y.z>]
#
# -Version stamps the file/product version: the release tag in CI, 0.0.0 for a local dev build.
# verNum drops a leading "v" and any pre-release suffix (Windows version resources are numeric).
#
# Both shipped binaries are stamped, not just the one with a window. An unsigned Windows binary with
# no company, no description and no version is the shape heuristic scanners are most suspicious of,
# and the engine legitimately writes to a live game process -- so it starts from a bad position and
# every bit of provenance it can carry is worth carrying.
param([string]$Version = "0.0.0")
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
if (-not $repo) { $repo = (Get-Location).Path }

$verNum = (($Version -replace '^v', '') -split '-')[0]
$vp = $verNum -split '\.'
$vMaj = if ($vp.Count -gt 0 -and $vp[0] -match '^\d+$') { [int]$vp[0] } else { 0 }
$vMin = if ($vp.Count -gt 1 -and $vp[1] -match '^\d+$') { [int]$vp[1] } else { 0 }
$vPat = if ($vp.Count -gt 2 -and $vp[2] -match '^\d+$') { [int]$vp[2] } else { 0 }

$targets = @('studio', 'engined')
foreach ($t in $targets) {
    Push-Location (Join-Path $repo "cmd\$t")
    try {
        & go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 -64 `
            -ver-major $vMaj -ver-minor $vMin -ver-patch $vPat `
            -product-ver-major $vMaj -product-ver-minor $vMin -product-ver-patch $vPat `
            -file-version $verNum -product-version $verNum `
            -o rsrc_windows_amd64.syso versioninfo.json
        if ($LASTEXITCODE -ne 0) { throw "goversioninfo failed for $t" }
    }
    finally { Pop-Location }
}
Write-Host "Stamped $($targets -join ', ') resources (version $verNum)" -ForegroundColor Green
