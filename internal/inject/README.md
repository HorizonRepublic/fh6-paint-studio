# internal/inject — Forza Horizon 6 vinyl injector

Writes the reconstructed shapes directly into the live **Forza Horizon 6 Vinyl Group
Editor** layer table, in memory. **Windows-only**; built with no cgo via
`golang.org/x/sys/windows`.

## What it does (and how)
1. **Find the process** — `ForzaHorizon6.exe` (or the FH5 fallback) via a toolhelp snapshot.
2. **Locate the live layer table** — a four-tier chain (each step logged): a cached group
   pointer, a cached `CLiveryGroup` vtable, an RTTI type-descriptor scan, and finally a
   layout-count fallback that scans committed, writable, `MEM_PRIVATE` regions for the `uint16`
   template **layer count**, derives the group (`count_addr - 0x5A`), reads the table pointer at
   `group + 0x78`, and **validates** that its entries look like real layers
   (`scoreLayerPointer` / `strictLayerPointer` / `validateCoverage`). A learned group/vtable is
   cached to a temp file so subsequent locates skip the scan; the cache is always re-validated
   before any write.
3. **Write each shape** into a consecutive template slot (compacting over unsupported shapes),
   writing seven layer fields at their offsets — and **only** these seven:

   | offset | field | encoding |
   | --- | --- | --- |
   | `0x18` | position x, y | 2× f32 |
   | `0x28` | scale sx, sy | 2× f32 |
   | `0x50` | rotation | f32 (deg) |
   | `0x70` | skew | f32 |
   | `0x74` | color RGBA | 4 bytes |
   | `0x78` | mask | 1 byte |
   | `0x7A` | shape word | u16 LE |

4. **Clear leftover** template layers (zero position, tiny scale, zero color).

Shape-word mapping (page-1 primitives, low 16 bits of the in-game code): rect→Square `0x65`,
ellipse→Circle `0x66` with a non-uniform scale (the dedicated Ellipse word `0x88` renders as a
crescent, so it is not used), triangle→Triangle `0x68`. Lines have no in-game primitive and are
skipped.

> **Never write the per-layer geometry resource pointer (layer offset `0xA8`).** The
> editor selects each layer's mesh from the shape word alone; aliasing one resource pointer across
> layers corrupts its per-layer ownership and crashes the game on free. Writing the shape word is
> sufficient for every primitive.

## Usage notes
- **Run as administrator** — reading and writing another process's memory requires it. The studio
  shows a clickable *Run as admin* badge when it is not elevated.
- **Set up the template first** — in the Vinyl Group Editor, load a placeholder template (e.g. a
  few thousand circles), **ungroup it**, and stay in the editor. Inject with the exact template
  layer count.
- **Save and reload the vinyl after injecting.** The editor re-derives each layer's mesh from its
  shape word only on (re)load; until you save and reload, freshly written non-ellipse shapes render
  with the slot's stale cached mesh.

## Files
- `profile.go` — `GameProfile` offsets + shape-word constants. (pure, tested)
- `layer.go` — `LayerWrite` field encoding + `CanvasMap` (pixel → editor space). (pure, tested)
- `triangle.go` — `TriangleFit`: solves the editor transform that maps a free-vertex triangle onto
  the in-game Triangle primitive. (pure, tested)
- `winmem_windows.go` — process find / open / read / write / `VirtualQueryEx` region walk.
- `locate_windows.go` — layout-count table locator + scoring/validation.
- `locate_rtti_windows.go` — RTTI / cached-vtable locators.
- `fh6.go` / `fh6_windows.go` / `fh6_other.go` — the `FH6` injector; non-Windows stub.
- `inject.go` — the `Injector` interface + `Stub`.

## Maintaining across game updates
The build-specific values live in `profile.go` (field offsets + shape words) and
`locate_rtti_windows.go` (`cliveryGroupRTTINames`). If a game update moves a field or renames the
class, update them there; the count-scan locator keeps working as a fallback and re-learns the
vtable. The `CanvasMap` scale (`ScaleBase`) is calibrated against the live editor and may need
re-checking if the editor's coordinate system changes.
