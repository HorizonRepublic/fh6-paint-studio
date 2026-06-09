package inject

import (
	"fmt"
	"runtime"

	"fh6-paint-studio/internal/model"
)

// FH6 is the Forza Horizon 6 memory injector. It writes the reconstructed shapes into the live
// vinyl-group editor's layer table. It requires the game running with the (ungrouped) template
// group open in the Vinyl Group Editor, and the exact template layer count. Windows-only; on
// other platforms Inject returns ErrNotImplemented.
type FH6 struct {
	Profile     GameProfile
	Layers      int        // exact template layer count of the open FH6 group (required)
	Canvas      *CanvasMap // nil -> DefaultCanvasMap(w,h)
	ClearUnused bool       // blank the template's leftover layers after the art
	Log         func(string)
}

// NewFH6 returns an FH6 injector with the default profile and clear-unused enabled.
func NewFH6() *FH6 { return &FH6{Profile: FH6Profile(), ClearUnused: true} }

func (f *FH6) Name() string    { return "Forza Horizon 6" }
func (f *FH6) Available() bool { return runtime.GOOS == "windows" }

// Inject writes the shapes into the running FH6 editor. w,h are the source image dimensions
// (used for the default canvas map). shapes[0] (the background) is included as the bottom layer.
func (f *FH6) Inject(shapes []model.Shape, w, h int) error {
	if !f.Available() {
		return ErrNotImplemented
	}
	if f.Layers <= 0 {
		return fmt.Errorf("enter the exact FH6 template layer count before injecting")
	}
	cm := DefaultCanvasMap(w, h)
	if f.Canvas != nil {
		cm = *f.Canvas
	}
	return f.run(shapes, cm)
}

// Locate finds the FH6 process and the live layer table for the configured layer count WITHOUT
// writing anything — a safe diagnostic to validate the locator against the live game before any
// import. Returns a human-readable description on success.
func (f *FH6) Locate() (string, error) {
	if !f.Available() {
		return "", ErrNotImplemented
	}
	if f.Layers <= 0 {
		return "", fmt.Errorf("enter the exact FH6 template layer count first")
	}
	return f.locate()
}

func (f *FH6) logf(format string, a ...any) {
	if f.Log != nil {
		f.Log(fmt.Sprintf(format, a...))
	}
}

// LayerInfo is a decoded read-only snapshot of one live layer — a calibration diagnostic.
type LayerInfo struct {
	Index int
	Ptr   uintptr
	Pos   [2]float32 // 0x18 (x, y)
	Scale [2]float32 // 0x28 (sx, sy)
	Rot   float32    // 0x50 degrees
	Skew  float32    // 0x70
	Color [4]byte    // 0x74 RGBA
	Mask  byte       // 0x78
	Word  uint16     // 0x7A shape word
	Res   uintptr    // 0xA8 geometry resource pointer — READ-ONLY diagnostic (FH6 selects the mesh from Word, not this)
}

// Dump reads and decodes the live template's layers at the given 0-based slot indices WITHOUT
// writing anything. It is the calibration tool: read a real template's positions/scales to learn
// the game's coordinate space before trusting placement. Windows-only.
func (f *FH6) Dump(indices []int) ([]LayerInfo, error) {
	if !f.Available() {
		return nil, ErrNotImplemented
	}
	if f.Layers <= 0 {
		return nil, fmt.Errorf("enter the exact FH6 template layer count first")
	}
	return f.dump(indices)
}

// GroupInfo is a read-only RTTI snapshot of one live CLiveryGroup instance.
type GroupInfo struct {
	Addr  uintptr // address of the group object (its first qword is the CLiveryGroup vtable)
	Count int     // layer count read from LiveryCountOffset
	Table uintptr // layer pointer-table read from LayerTableOffset (0 if unreadable/invalid)
	Valid bool    // the table scored + coverage-validated for Count layers (safe to write)
}

// DumpGroups locates every live CLiveryGroup via RTTI and reports each instance's layer count and
// table validity WITHOUT writing anything and WITHOUT needing a preset layer count. Use it to see
// what groups are open in the editor and to verify the RTTI locator against the live game.
func (f *FH6) DumpGroups() ([]GroupInfo, error) {
	if !f.Available() {
		return nil, ErrNotImplemented
	}
	return f.dumpGroups()
}

// CalibLayer is one controlled placement for gradient-falloff calibration. CalibWrite sets ONLY the
// slot's transform + colour and preserves the existing shape-word and geometry resource, so a
// gradient already placed via the FH6 UI keeps its mesh and repaints live (no save+reload needed).
type CalibLayer struct {
	Slot     int        `json:"slot"`
	WantWord uint16     `json:"word"`              // expected current word at the slot (safety gate; 0 = skip the check)
	Pos      [2]float32 `json:"pos"`               // editor units (origin = canvas centre, +X right, +Y up)
	Scale    [2]float32 `json:"scale"`             // scale multipliers (circle word 0x66 at 1.0 = radius 64 = ScaleBase; measured 2026-06-04)
	Rot      float32    `json:"rot"`               // degrees
	Skew     float32    `json:"skew,omitempty"`    // shear, offset 0x70 (degrees); the 6th transform DOF
	Color    [4]byte    `json:"color"`             // base colour RGBA (the falloff lives in the mesh, not here)
	SetWord  uint16     `json:"setword,omitempty"` // if non-zero, ALSO write this shape-word (mesh re-derives only on save+reload). NEVER the resource (0xA8).
}

// CalibWrite sets the transform + colour of the given EXISTING template slots WITHOUT changing their
// shape-word or geometry resource — the gradient-calibration helper. Each slot must already carry the
// expected gradient word (WantWord); the write is refused otherwise (which also guards against the
// locator anchoring on the wrong group). Windows-only.
func (f *FH6) CalibWrite(layers []CalibLayer) error {
	if !f.Available() {
		return ErrNotImplemented
	}
	if f.Layers <= 0 {
		return fmt.Errorf("enter the exact FH6 template layer count first")
	}
	return f.calibWrite(layers)
}

// ProbeHit is one read-only match of a byte pattern in the target's memory (a diagnostic).
type ProbeHit struct {
	Addr    uintptr
	Type    uint32 // MEM_IMAGE (0x1000000) / MEM_MAPPED (0x40000) / MEM_PRIVATE (0x20000)
	Context string // printable ASCII around the hit
}

// Probe is a read-only memory diagnostic: it searches the live game's image/mapped regions for the
// given ASCII needle (returning up to max hits with context) and logs per-type region stats. If
// where is non-empty, only hits whose surrounding context contains it are returned (e.g. needle
// ".?AV", where "Livery"). Use it to discover the real RTTI class name when the locator can't anchor.
func (f *FH6) Probe(needle, where string, max int) ([]ProbeHit, error) {
	if !f.Available() {
		return nil, ErrNotImplemented
	}
	return f.probe(needle, where, max)
}
