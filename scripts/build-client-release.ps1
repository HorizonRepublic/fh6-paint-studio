# build-client-release.ps1 -- cut the distributable release: the Flutter client + the engine service
# + the Vulkan shim, staged clean and packed.
#
#   powershell -ExecutionPolicy Bypass -File .\scripts\build-client-release.ps1 [-Version <x.y.z>] [-Out <dir>] [-SkipDLL]
#
# The product is the Flutter client talking to engined over the pipes between them, so the release is
# that pair plus the shim the engine loads. The Gio studio has its own script (build-release.ps1) and
# is not part of this one.
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
    # A clean build (step 7) leaves the shim only inside the prod layout, so restore the loose copy the
    # staging step reads from before deciding whether -SkipDLL can reuse it.
    if ($SkipDLL -and -not (Test-Path $vkdll)) {
        $prodDll = Join-Path $root "bin\bin\engine\fh6vk.dll"
        if (Test-Path $prodDll) { Copy-Item $prodDll $vkdll }
    }
    if ($SkipDLL -and (Test-Path $vkdll)) {
        Write-Host "Reusing the existing shim: $vkdll" -ForegroundColor Yellow
    } else {
        & (Join-Path $PSScriptRoot 'build-vulkan.ps1') -Version $Version
        if ($LASTEXITCODE -ne 0) { throw "Vulkan shim build failed" }
    }

    # 2) Version resources for the Go binaries, so none of them ships nameless.
    & (Join-Path $PSScriptRoot 'gen-winres.ps1') -Version $Version
    if ($LASTEXITCODE -ne 0) { throw "version resources failed" }

    # 3) The engine service. No console window: it is a child process and a flashing console on
    # every launch is not something a user should have to look at.
    #
    # Stripped (-s -w), the same as the studio and as every release up to 1.4.0. Shipping the symbol
    # table and full DWARF was tried instead, on the theory that a scanner should be able to read
    # every function name rather than guess: measured 2026-08-06, it COST a detection. The same
    # engined built both ways, packed alone and scanned the same hour, went 2/75 with symbols and
    # 1/75 without -- MaxSecure's generic heuristic fires on the symbol-carrying build. The binary
    # repeats every injector API name three times there and carries 129 strings naming the injector
    # package against 3 when stripped, so a keyword-density heuristic sees forty times the evidence.
    #
    # Do NOT drop -w while keeping -s. Go's default DWARF is zlib'd into eight sections named
    # /4 /19 /32 /46 /65 /78 /95 /112 -- numeric COFF string-table references, unreadable without
    # parsing the format -- five at entropy 7.99, together 23% of the file. That is the packed-binary
    # signature. If debug info is ever wanted again it has to be -compressdwarf=false.
    # Pinned to the toolchain go.mod asks for, which is what CI installs (setup-go with
    # go-version-file). Without this Go silently prefers a NEWER toolchain if one is installed
    # locally, so a local release build and the CI release build are different binaries -- we shipped
    # go1.26.3 locally while CI shipped go1.26.1 and never noticed.
    $goDirective = (Select-String -Path (Join-Path $root 'go.mod') -Pattern '^go\s+(\S+)').Matches[0].Groups[1].Value
    $env:GOTOOLCHAIN = "go$goDirective"
    $env:CGO_ENABLED = "0"
    $env:GOAMD64 = "v1"
    & go build -tags vulkan -trimpath -ldflags "-s -w -H windowsgui" -o (Join-Path $root "bin\engined.exe") ./cmd/engined
    if ($LASTEXITCODE -ne 0) { throw "engined build failed" }

    # 4) The client. The previous build's assets are cleared first: `flutter build` leaves whatever it
    # wrote last time in place and the staging step below copies the directory wholesale, so an asset
    # a former build needed would ship in a release that does not need it.
    $built = Join-Path $root 'client\build\windows\x64\runner\Release'
    if (Test-Path (Join-Path $built 'data')) { Remove-Item -Recurse -Force (Join-Path $built 'data') }
    Push-Location (Join-Path $root 'client')
    try {
        # --build-name carries the release version into the runner's version resource; without it
        # the shipped exe claims pubspec's placeholder 1.0.0 forever.
        & flutter build windows --release --build-name=$Version --build-number=0
        if ($LASTEXITCODE -ne 0) { throw "flutter build failed" }
    } finally { Pop-Location }

    # 5) Stage. Copy what the build produced, and nothing else.
    $stage = Join-Path $Out "fh6-paint-studio"
    if (Test-Path $stage) {
        # Probe for a running copy BEFORE deleting anything. A Remove-Item that dies halfway on a
        # locked engined.exe leaves a gutted folder whose exe starts nothing — silently.
        $locked = Get-ChildItem $stage -Recurse -File | Where-Object {
            try {
                $h = [IO.File]::Open($_.FullName, 'Open', 'ReadWrite', 'None'); $h.Close(); $false
            } catch { $true }
        } | Select-Object -First 1
        if ($locked) { throw "close the running app first: '$($locked.FullName)' is in use" }
        Remove-Item -Recurse -Force $stage
    }
    New-Item -ItemType Directory -Force $stage | Out-Null

    # Named, not wildcarded. `flutter build` does not clean its output directory, so the Release
    # folder still holds whatever earlier runs left there -- at the time of writing, an engined.exe
    # from a build two versions old. A `*.exe` copy picks that up, and the only thing that kept it
    # out of the release was the explicit copy two lines later happening to overwrite it. Name what
    # ships instead of naming what to sweep.
    # The folder a user opens holds the exe, the README, and ONE directory. Everything else —
    # Flutter's runtime, data\, the engine service, the licences — lives inside bin\: the runner
    # delay-loads its DLLs and points the loader there before the first call needs one
    # (client/windows/runner), the client looks for engined in bin\engine first, and engined finds
    # fh6vk.dll beside itself because a process's own directory heads the DLL search order.
    Copy-Item (Join-Path $built 'fh6_paint_studio.exe') (Join-Path $stage 'FH6 Paint Studio.exe')
    $binDir = New-Item -ItemType Directory -Force (Join-Path $stage 'bin')
    Copy-Item (Join-Path $built 'flutter_windows.dll') $binDir
    Get-ChildItem (Join-Path $built '*_plugin.dll') | ForEach-Object { Copy-Item $_.FullName $binDir }
    Copy-Item (Join-Path $built 'data') $binDir -Recurse
    $engineDir = New-Item -ItemType Directory -Force (Join-Path $binDir 'engine')
    Copy-Item (Join-Path $root 'bin\engined.exe') $engineDir
    Copy-Item $vkdll $engineDir

    # Our own licence. Every binary's version resource claims MIT; the text has to travel with them
    # for that claim to mean anything, and mod hosts ask for an explicit permissions statement.
    $docsDir = New-Item -ItemType Directory -Force (Join-Path $binDir 'docs')
    Copy-Item (Join-Path $root 'LICENSE') (Join-Path $docsDir 'LICENSE.txt')

    # No README in the drop. The folder a user opens is the exe and bin\, nothing else; the
    # instructions and the VC++ redist note live in the repo README and on the mod page, which is
    # where someone whose app will not start actually goes. NB the client and both Flutter plugins
    # import msvcp140 / vcruntime140 / vcruntime140_1 and we ship none of them -- the download page
    # must keep saying so, or a machine without the runtime reads the missing-DLL box as a broken
    # download.

    # Directories Flutter leaves behind with nothing in them (a package whose assets were all
    # dropped still gets its folder). An empty directory in the archive is a folder entry that
    # serves nothing and reads as leftovers.
    do {
        $empty = Get-ChildItem $stage -Recurse -Directory |
            Where-Object { -not (Get-ChildItem $_.FullName -Force) }
        $empty | Remove-Item -Force
    } while ($empty)


    $7z = @('7z.exe', (Join-Path $env:ProgramFiles '7-Zip\7z.exe')) |
        Where-Object { Get-Command $_ -ErrorAction SilentlyContinue } | Select-Object -First 1
    if (-not $7z) { throw "7-Zip not found; install it or add 7z.exe to PATH" }

    # The third-party licence texts. Flutter buries them in a gzip blob inside the asset tree, which
    # is both invisible to anyone who wants to read them and a compressed archive nested inside the
    # release -- something mod hosts reject on sight, because a nested archive cannot be scanned.
    # They are a CONDITION of using the libraries in here (MIT and BSD both require the notice to
    # travel with every copy), so they are unpacked to plain text in docs\, not dropped.
    $noticesZ = Join-Path $binDir 'data\flutter_assets\NOTICES.Z'
    if (Test-Path $noticesZ) {
        # 7-Zip does the gunzip. It is already a dependency of this script, and leaning on it beats
        # hand-driving .NET streams from Windows PowerShell, which will not bind GZipStream's
        # methods here at all.
        & $7z e -tgzip -so $noticesZ 2>$null |
            Set-Content (Join-Path $docsDir 'THIRD-PARTY-NOTICES.txt') -Encoding utf8
        if ($LASTEXITCODE -ne 0) { throw "could not unpack $noticesZ" }
        Remove-Item -Force $noticesZ
    }

    # Flutter's blob covers the Flutter half only. The engine links Go modules whose licences say
    # the same thing about travelling with the binary, and none of them appear in it -- so they are
    # appended here, read out of the module cache for whatever the engine actually links rather than
    # from a hand-kept list that would rot.
    $notices = Join-Path $docsDir 'THIRD-PARTY-NOTICES.txt'
    $goMods = & go list -deps -tags vulkan -f '{{if .Module}}{{.Module.Path}}{{end}}' ./cmd/engined |
        Sort-Object -Unique | Where-Object { $_ -and $_ -ne 'fh6-paint-studio' }
    foreach ($m in $goMods) {
        $dir = & go list -m -f '{{.Dir}}' $m
        if (-not $dir) { throw "no module cache entry for $m; cannot ship its licence" }
        $lic = Get-ChildItem $dir -File | Where-Object { $_.Name -match '^(LICENSE|LICENCE|COPYING)' } |
            Select-Object -First 1
        if (-not $lic) { throw "no licence file in $dir for $m" }
        Add-Content $notices "`n$('=' * 78)`n$m`n$('=' * 78)`n" -Encoding utf8
        Add-Content $notices (Get-Content $lic.FullName -Raw) -Encoding utf8
    }

    # The Material icon font, which we ship none of: not one Icons.* reference exists in the client,
    # every glyph in the interface is drawn. Flutter emits the font anyway because the material
    # package declares it in FontManifest, and the icon tree-shaker does not subset a font nothing
    # references. Removing the declaration with it keeps the manifest honest.
    Remove-Item -Recurse -Force (Join-Path $stage 'data\flutter_assets\fonts') -ErrorAction SilentlyContinue
    $fm = Join-Path $stage 'data\flutter_assets\FontManifest.json'
    # WriteAllText with a BOM-less encoding, not Set-Content -Encoding utf8: Windows PowerShell
    # writes a BOM, and Dart's jsonDecode throws on one. The app would fail to start.
    if (Test-Path $fm) { [IO.File]::WriteAllText($fm, '[]', (New-Object Text.UTF8Encoding $false)) }

    # Anything the app WROTE beside itself while it was being tested is not part of the release.
    Get-ChildItem $stage -Recurse -Include *.log, *.log.1, client.json, native_assets.json |
        Remove-Item -Force -ErrorAction SilentlyContinue

    # 6) Pack it, replacing any previous one. 7z, matching what CI attaches to the release: scanners
    # look inside a zip far more readily than a 7z, and a local archive that differs from the shipped
    # one is a local archive nobody can compare against.
    $archive = Join-Path $Out "fh6-paint-studio-$Version.7z"
    if (Test-Path $archive) { Remove-Item -Force $archive }
    # -mx=7, not -mx=9. The staged files are byte-identical either way -- this only changes how the
    # container is encoded -- but the two archives scan differently: measured 2026-08-06 on the same
    # stage, -mx=9 came back 2/75 and -mx=7 came back 1/75, the difference being Bkav. That engine's
    # verdict is not stable on unchanged bytes (see the handoff memory), so treat this as the setting
    # that happened to land clean and re-scan the artefact before every release rather than trusting
    # the flag. The 200 KB it costs is not worth arguing about.
    & $7z a -t7z -mx=7 $archive $stage | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "7z failed ($LASTEXITCODE)" }

    # Checksums beside the archive. One antivirus engine out of seventy calls this
    # unsigned binary a generic malware hash, and it always will until the build
    # is signed -- so the least we can do is let a suspicious user verify that
    # the file they downloaded is the file we built.
    # Written with WriteAllText and LF endings, not Set-Content: Windows PowerShell puts a BOM on a
    # utf8 file, and `sha256sum -c` treats the BOM as part of the first hash and rejects the line.
    # A checksum file nobody can check is worse than none.
    $sums = Join-Path $Out "fh6-paint-studio-$Version.sha256"
    $lines = @(Get-FileHash $archive -Algorithm SHA256 |
        ForEach-Object { "{0}  {1}" -f $_.Hash.ToLower(), (Split-Path $_.Path -Leaf) })
    $lines += @(Get-ChildItem $stage -Recurse -File |
        Get-FileHash -Algorithm SHA256 |
        ForEach-Object { "{0}  {1}" -f $_.Hash.ToLower(), $_.Path.Substring($stage.Length + 1) })
    [IO.File]::WriteAllText($sums, (($lines -join "`n") + "`n"), (New-Object Text.UTF8Encoding $false))

    # 7) Reduce bin\ to EXACTLY the runnable prod layout, so the app runs and injects from the familiar
    # place without unpacking the archive. The shim and engined builds drop intermediates here
    # (.obj/.exp/.lib/.res, loose exes, the CLI, test logs) that have no business in a prod folder — the
    # user opens bin\ expecting what a release drop looks like: the launcher and its bin\ runtime, and
    # nothing else. Everything still needed to run, and to reuse the shim on the next -SkipDLL build,
    # lives inside bin\bin\. Staging has already consumed bin\engined.exe and bin\fh6vk.dll by now.
    # Wrapped so a locked bin\ (the app left running) warns instead of failing the finished release.
    $binRoot = Join-Path $root 'bin'
    try {
        Get-ChildItem $binRoot -Force | Remove-Item -Recurse -Force
        Copy-Item (Join-Path $stage 'FH6 Paint Studio.exe') $binRoot
        Copy-Item (Join-Path $stage 'bin') $binRoot -Recurse
        Write-Host "bin\ is now the runnable prod layout" -ForegroundColor Green
    } catch {
        Write-Host "Could not refresh bin\ (is the app running?): $_" -ForegroundColor Yellow
    }

    $size = [math]::Round((Get-Item $archive).Length / 1MB, 1)
    Write-Host ""
    Write-Host "Release $Version -> $archive ($size MB)" -ForegroundColor Green
    Get-ChildItem $stage | Select-Object Name, @{n = 'MB'; e = { [math]::Round($_.Length / 1MB, 2) } } | Format-Table
} finally { Pop-Location }
