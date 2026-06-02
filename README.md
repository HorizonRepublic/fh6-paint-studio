# FH6 Paint Studio

Turn any image into a **Forza Horizon 6 vinyl livery**. FH6 Paint Studio reconstructs a photo or
piece of art as a few thousand simple coloured shapes (ellipses, triangles, rectangles) — exactly
the kind of "layers" the in-game Vinyl Group Editor stacks — so you can recreate artwork the editor
could never let you draw by hand.

It greedily places the shapes that best match your image, then runs a GPU **joint polish** that
nudges every shape's position, size, rotation and colour together to sharpen the result. The output
is a livery you can export or inject straight into a running game.

---

## Quick start

You need **[Go 1.26+](https://go.dev/dl/)**. The default build is pure Go — nothing else required.

```powershell
# Windows
.\build.ps1            # builds bin\fh6-paint-studio.exe (GUI) + bin\fh6paint.exe (CLI)
```
```sh
# Linux / macOS
./build.sh             # builds bin/fh6-paint-studio (GUI) + bin/fh6paint (CLI)
```

Then launch the studio:

```
bin/fh6-paint-studio
```

> Want maximum speed on an NVIDIA GPU? See **[Building with CUDA](#building-with-cuda)**. The pure-Go
> build works everywhere and needs no GPU; CUDA just makes generation much faster.

---

## Using the Studio

1. **Open an image** — click the big drop area (or *Open image…*). Optionally drag a **crop box** to
   reconstruct just one region (e.g. a face) at full detail.
2. **Pick a preset and budget** — *anime*, *photo* or *flat*, and how many shapes to spend
   (up to 3000, the editor's per-group cap). Higher budget = more detail.
3. **Generate** — watch the reconstruction build live, with a before/after wipe over the source.
   Click **Zoom** to inspect the result full-screen.
4. **Keep it** — every finished run is auto-saved to your **Library**
   (`~/FH6PaintStudio/library`), where you can re-open, rename, re-export or re-inject it later
   without regenerating.
5. **Export or inject** — save the geometry as JSON, or inject it directly into a running game
   (see below).

## Using the CLI

For batch or scripted use:

```sh
fh6paint -input photo.jpg -mode anime -shapes 3000 \
         -output out/photo.forza.json -preview out/photo.png
```

`-mode` picks a content preset (`anime` | `photo` | `flat`) and `-shapes` is the budget (≤3000).
Run `fh6paint -h` for the full flag set.

## Injecting into Forza Horizon 6

From the Library, set your in-game template's **layer count**, then **Inject into FH6**.

> **How it works:** injection writes shape data into a running Forza Horizon 6 process
> (**Windows-only**, usually needs **administrator**). First load a placeholder template in the
> Vinyl Group Editor and ungroup it; inject with the exact template layer count; then **save the
> vinyl and reload it in-game** so the editor re-derives every layer's mesh.
>
> Writing to another process's memory may violate the game's terms of service and could be flagged
> by anti-cheat. It is provided for personal, offline use — **use it at your own risk.** Generation,
> preview and JSON export never touch the game.

---

## Building with CUDA

The GPU backend is a self-contained `fh6cuda.dll` (compiled from
`internal/backend/cuda/shim.cu` with `nvcc` + MSVC) that is loaded at runtime — no cgo.

```powershell
.\build.ps1 -Cuda          # compiles fh6cuda.dll, then builds both apps with -tags cuda
```

For a redistributable build, `build-cuda-fat.ps1` compiles a **multi-arch fat** `fh6cuda.dll` with a
**portable CUDA 12.8 toolkit** (no admin install — merge the `cuda_nvcc` + `cuda_cudart` + `cuda_cccl`
redist archives into one directory and pass it via `-Toolkit <dir>` or the `CUDA_TOOLKIT` env var).
CUDA 12.8 spans `sm_61` (Pascal, GTX 10xx) through `sm_120` (Blackwell, RTX 50xx). For first-time
Windows toolchain setup (Go + CUDA + MSVC), see `setup-windows.ps1`.

You can also build a single target by hand:

```sh
go build ./cmd/fh6paint     # CLI
go build ./cmd/studio       # desktop GUI (CGO-free; Gio renders on the GPU)
```

## How it works

A **from-scratch Go + CUDA engine** (not a fork of existing tools). The CPU backend is a pure-Go
reference implementation; the CUDA backend mirrors it kernel-for-kernel and is verified against it.

```
cmd/fh6paint      CLI
cmd/studio        desktop GUI (Gio)
internal/engine   greedy placement loop + differentiable polish
internal/backend  Backend interface — cpu (reference) + cuda (build-tagged)
internal/model    shape model + importer-compatible geometry JSON
internal/raster   per-primitive rasterisation / inside-tests
internal/metric   edge-weight / saliency maps, content classification
internal/preset   content presets — the single source of truth for tuned defaults
internal/imageio  load / downscale / render (incl. the in-game-faithful RenderFH6)
internal/inject   the Forza Horizon 6 livery-editor injector (Windows)
internal/library  on-disk library of saved generations
internal/ui       Studio widgets / theme / panels
internal/runner   drives the engine off the UI goroutine
```

Run the tests with `go test ./...`.

## License

MIT — see [LICENSE](LICENSE). © 2026 Horizon Republic.

"Forza Horizon" is a trademark of Microsoft; this is an independent, unaffiliated tool.
