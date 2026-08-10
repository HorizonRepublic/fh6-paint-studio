<h1 align="center">FH6 Paint Studio</h1>

<p align="center">Turn any image into a Forza Horizon vinyl livery, automatically.</p>

<p align="center">
  <a href="https://github.com/HorizonRepublic/fh6-paint-studio/releases/latest"><img src="https://img.shields.io/github/v/release/HorizonRepublic/fh6-paint-studio" alt="Latest release"></a>
  <a href="https://github.com/HorizonRepublic/fh6-paint-studio/releases"><img src="https://img.shields.io/github/downloads/HorizonRepublic/fh6-paint-studio/total" alt="Downloads"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/HorizonRepublic/fh6-paint-studio" alt="License"></a>
  <a href="https://github.com/HorizonRepublic/fh6-paint-studio/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/HorizonRepublic/fh6-paint-studio/ci.yml" alt="Build"></a>
  <a href="https://www.nexusmods.com/forzahorizon6/mods/314"><img src="https://img.shields.io/badge/Nexus%20Mods-FH6%20Paint%20Studio-da8e35" alt="Nexus Mods"></a>
</p>

<p align="center"><img src="assets/screens/main-screen.png" width="760" alt="A finished reconstruction, ready to inject"></p>

FH6 Paint Studio rebuilds a photo or artwork from a few thousand coloured
shapes, the same layers the in-game Vinyl Group Editor stacks, then writes the
result straight into the game. You place nothing by hand unless you want to.

## Features

- **Almost any image works**: photos, logos, anime, memes, your cat.
- **A preset per art style**: photo, drawing, flat logos, line art, soft glow,
  pixel art.
- **A built-in editor**: drag, scale, rotate and recolour shapes, group and move
  them, pick any colour, place shapes from the in-game bank, all with full undo.
- **Crop to what matters**: box a face and the whole shape budget goes to the
  detail you care about.
- **Runs stay put**: every generation is ready to re-inject or re-export.

## Download

Windows 10 or 11, with a GPU that supports Vulkan (recent NVIDIA, AMD or Intel).

**[Download the latest release](https://github.com/HorizonRepublic/fh6-paint-studio/releases/latest)**,
extract the `.7z` anywhere, and run **`FH6 Paint Studio.exe`**. Windows 11 opens
`.7z` natively; Windows 10 needs [7-Zip](https://www.7-zip.org/). It is also on
[Nexus Mods](https://www.nexusmods.com/forzahorizon6/mods/314).

## Usage

Open an image, pick a style, click **Generate**. Touch up the result in the
editor if you like, then put it on your car:

1. In Forza's **Vinyl Group Editor**, make a group with at least as many shapes
   as your generation has. They are placeholders the app overwrites.
2. Set the layer count in the app and click **Inject**.
3. **Save the vinyl in-game, then reopen it.** A fresh injection looks rough
   until the game reloads it, then it renders as previewed.

> Injection writes into a running game process, which may violate the game's
> terms of service or trip anti-cheat. It exists for personal, offline use, so
> use it at your own risk. Generating and exporting never touch the game.

<details>
<summary>More screenshots</summary>

<table>
  <tr>
    <td align="center"><img src="assets/screens/style-picker.png" width="380" alt="Style picker"><br><sub>Styles</sub></td>
    <td align="center"><img src="assets/screens/editor.png" width="380" alt="Editor"><br><sub>Editor</sub></td>
  </tr>
  <tr>
    <td align="center"><img src="assets/screens/crop.png" width="380" alt="Crop"><br><sub>Crop</sub></td>
    <td align="center"><img src="assets/screens/advanced-settings.png" width="380" alt="Advanced settings"><br><sub>Advanced</sub></td>
  </tr>
</table>

</details>

## Building from source

A Flutter client over a Go engine service. With [Go 1.26+](https://go.dev/dl/),
[Flutter](https://flutter.dev) and MSVC installed:

```powershell
powershell -File scripts\build-client-release.ps1 -Version dev -Out release
```

Tests: `go test ./...`, `go test -tags vulkan ./internal/backend/vulkan` on a
GPU, and `flutter test` in `client\`.

## License

[MIT](LICENSE). © 2026 Horizon Republic.

Every release carries a Sigstore build-provenance attestation. Verify a download
with `gh attestation verify <file> --repo HorizonRepublic/fh6-paint-studio`.

"Forza Horizon" is a trademark of Microsoft; this is an independent,
unaffiliated tool.
