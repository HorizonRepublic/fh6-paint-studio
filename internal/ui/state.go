package ui

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/gesture"
	"gioui.org/layout"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"fh6-paint-studio/internal/i18n"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/preset"
	"fh6-paint-studio/internal/userpreset"
)

// Phase is the app's coarse state machine.
type Phase int

const (
	PhaseIdle Phase = iota
	PhaseRunning
	PhaseDone
	PhaseError
)

// View is the top-level screen: the Studio generate workspace vs the saved-generation Library.
type View int

const (
	ViewStudio View = iota
	ViewLibrary
	ViewEditor
)

// RunStats is the live telemetry shown in the run panel.
type RunStats struct {
	Shapes, Total int
	Cap           int // the shape budget the user REQUESTED (the slider). The auto-knee may finish below it; Shapes < Cap then means the app auto-picked the optimal count.
	Err, Err0     float64
	Elapsed, ETA  time.Duration
	History       []float64 // error per progress tick, for the sparkline
	Stage         string    // current post-greedy phase (polish/standout); "" during the greedy build — drives the indeterminate "still working" indicator instead of a bar stuck at 100%

	// ETA state: an EMA of seconds/shape over RECENT shapes, not the cumulative average. The
	// per-shape cost is heavily front-loaded (big early shapes), so a linear elapsed/frac ETA
	// grossly overestimates early; the recent-rate EMA tracks the speed-up onto cheap late shapes.
	etaPerShape    float64
	etaLastShapes  int
	etaLastElapsed time.Duration
	etaPrevTick    time.Duration // elapsed at the previous UpdateETA call (the display countdown step)
}

// UpdateETA refreshes ETA once per progress tick. The RATE is a recent EMA of seconds/shape; the
// DISPLAYED value is a smooth countdown: it ticks down with the wall clock and converges toward the
// raw rate×remaining estimate at 1/6 of the gap per tick — the raw estimate wobbles ±5-10s tick to
// tick (per-shape cost is bursty), and showing it directly made the countdown jump back and forth.
// A genuine slowdown still pulls the display up, just over ~10 ticks instead of instantly.
func (s *RunStats) UpdateETA(shapes, total int, elapsed time.Duration) {
	if shapes > s.etaLastShapes {
		dt := (elapsed - s.etaLastElapsed).Seconds()
		dn := float64(shapes - s.etaLastShapes)
		if dt >= 0 && dn > 0 {
			inst := dt / dn
			if s.etaPerShape == 0 {
				s.etaPerShape = inst
			} else {
				s.etaPerShape = 0.85*s.etaPerShape + 0.15*inst
			}
		}
		s.etaLastShapes = shapes
		s.etaLastElapsed = elapsed
	}
	wallDelta := elapsed - s.etaPrevTick
	s.etaPrevTick = elapsed
	remaining := total - shapes
	if remaining <= 0 || s.etaPerShape <= 0 {
		s.ETA = 0
		return
	}
	raw := time.Duration(s.etaPerShape * float64(remaining) * float64(time.Second))
	if s.ETA <= 0 {
		s.ETA = raw
		return
	}
	eta := s.ETA - wallDelta
	if eta < time.Second {
		eta = time.Second // floor while shapes are still pending (never shows 00:00 mid-run)
	}
	s.ETA = eta + (raw-eta)/6
}

type UpdateInfo struct {
	Version string // display tag, e.g. "v0.3.0"
	Notes   string
	URL     string
}

// AppState holds every widget state plus the loaded image and run telemetry. The panel Layout methods
// read and mutate it; the main loop feeds runner events into it.
type AppState struct {
	Th *Theme

	// app identity (set by main)
	Version      string
	BackendLabel string
	Backend      *Dropdown // engine picker (CUDA/Vulkan); nil when only one backend works (no choice to make)
	Lang         *Dropdown // UI language picker (endonym options); drives i18n.SetLocale

	// loaded image
	ImgPath   string
	Source    *image.NRGBA
	SourceOp  paint.ImageOp
	Preview   *image.NRGBA
	PreviewOp paint.ImageOp

	// run
	Phase Phase
	Stats RunStats
	Log   []LogEntry
	Toast string

	// activity console (shared bottom drawer): a one-line status strip, expandable to the full feed.
	ConsoleOpen     bool
	ConsoleToggle   widget.Clickable
	OpenLogBtn      widget.Clickable // "Open log" affordance shown in the Activity error state
	LogFilterErrors bool             // console feed shows only warnings + errors
	LogClearBtn     widget.Clickable
	LogCopyBtn      widget.Clickable
	LogFilterBtn    widget.Clickable

	// Quality is the perceptual score of the last finished generation (shown as a friendly badge).
	Quality QualityInfo

	// settings widgets
	Budget     widget.Float
	BudgetEd   widget.Editor // manual shape-count entry, two-way synced with Budget
	lastBudget int
	Mode       *Dropdown
	InkRatio   widget.Float // hybrid Artist knob: fraction of the budget for FDoG ink lines (slider 0..1 -> ratio 0..MaxInkRatio); the rest is the geometrize fill

	// step-1 preset picker: the built-in modes and the saved presets are rendered as selectable cards
	// (recognition over a text dropdown). Click targets parallel builtinCards / Presets respectively.
	BuiltinCards []widget.Clickable
	PresetCards  []widget.Clickable
	Alpha        widget.Bool
	Backfit      widget.Bool
	Boundary     widget.Bool // boundary-aware radius — smoother gradients on character/photo liveries (opt-in)
	KeepInside   widget.Bool // generate against a transparent surround so the spill-penalty keeps every shape INSIDE the image (no edge bleed); the result is mapped back to the original size (no frame artefact)
	SourceRes    widget.Bool // fit the ENGINE at the image's original resolution instead of the working cap — maximum detail on large sources, much slower (display stays at the working size)
	Mono         widget.Bool // MONO single-colour logo/decal: force every shape to one solid colour (auto-detected) on a clean cutout — no grey antialiased-edge shapes
	Economy      widget.Bool // OPT-IN economy/co-adaptation schedule at low budgets — better quality, much slower (off by default)
	Seed         widget.Editor

	AlphaHint      Hint
	BackfitHint    Hint
	BoundaryHint   Hint
	KeepInsideHint Hint
	SourceResHint  Hint
	MonoHint       Hint
	EconomyHint    Hint
	BudgetHint     Hint
	ModeHint       Hint
	InkHint        Hint
	SeedHint       Hint

	// advanced
	AdvClick  widget.Clickable
	AdvOpen   bool
	RandomEd  widget.Editor
	MutatedEd widget.Editor
	SampleEd  widget.Editor

	// expert mode: a master toggle inside the Custom section that unlocks every generator knob.
	// The knobs always show CONCRETE values (filled from the mode's tuned defaults), so they read
	// like a preset and can be cloned/saved. Hidden by default to keep the common path clean.
	Expert     widget.Bool
	ExpertHint Hint

	QualityDD   *Dropdown
	QualityHint Hint

	PolishOn        widget.Bool
	PolishHint      Hint
	PolishItersEd   widget.Editor
	PolishItersHint Hint
	TauSlider       widget.Float // edge softness (polish tau1); lower = crisper
	TauHint         Hint

	AlphaMinSlider widget.Float // semi-transparent alpha floor; higher = more opaque
	AlphaMinHint   Hint

	WeightedOn      widget.Bool
	WeightedHint    Hint
	WeightStrSlider widget.Float
	WeightStrHint   Hint

	CompactOn   widget.Bool
	CompactHint Hint

	AspectEd        widget.Editor
	AspectHint      Hint
	KindsSel        *MultiSelect
	KindsHint       Hint
	KindWeightEds   []widget.Editor // one per kind (parallel to preset.KindNames); only selected ones render
	KindWeightsHint Hint
	ShapeChips      []widget.Clickable // "Used shapes" icon toggles in Adjust — a second view on KindsSel

	StandoutSlider widget.Float // post-polish standout suppression tolerance; 0 = off
	StandoutHint   Hint

	MaxNIEd      widget.Editor
	MaxNIHint    Hint
	GridEd       widget.Editor
	GridHint     Hint
	OverdrawEd   widget.Editor
	OverdrawHint Hint
	RandomHint   Hint
	MutatedHint  Hint
	SampleHint   Hint

	// content descriptor for the concrete mode defaults (set by main when an image loads)
	ContentColors int
	ContentCutout bool

	// custom presets: a saved config snapshot reloadable from the Preset dropdown (loaded by main).
	// baseMode is the content mode sent to the engine; the dropdown may instead show a preset name.
	Presets         []userpreset.Preset
	baseMode        string
	PresetNameEd    widget.Editor
	SavePresetBtn   widget.Clickable
	DeletePresetBtn widget.Clickable
	PresetNameHint  Hint
	ExpGroups       [4]expertGroup // collapse state of the expert sub-sections

	// actions
	OpenBtn          widget.Clickable
	PreviewOpen      widget.Clickable // the empty-state preview area doubles as an Open button
	escTag           int              // key-focus tag for Esc-dismisses-overlay (focus is only grabbed while a modal is up)
	lightboxTag      int              // pointer tag for the lightbox scrim — captures clicks so they dismiss it (and don't fall through to the gallery thumbs behind)
	GenBtn           widget.Clickable
	CancelBtn        widget.Clickable
	InjectLayers     widget.Editor // exact FH6 template layer count for injection (library inject controls)
	InjectLayersErr  bool          // the FH6-layers field was empty/invalid on an Inject click — draw it red until a valid count is entered
	InjectScale      widget.Editor // uniform scale of the injected art on the decal (1.0 = fit the canvas; <1 shrinks toward centre so it fits a zoomed-in editor view)
	InjectHint       Hint          // explains the FH6-template-layers field (the inject footgun)
	InjectGuideClick widget.Clickable
	InjectGuideOpen  bool // the "How injecting works" steps are expanded
	ElevateBtn       widget.Clickable
	Elevated         bool        // process is running as administrator (set by main at startup)
	Shield           image.Image // system UAC shield icon (nil if unavailable)
	ShieldOp         paint.ImageOp

	// preview interaction
	Wipe        widget.Float
	WipeDrag    gesture.Drag     // drag the before/after divider directly on the image (aspect-independent, unlike the slider)
	PreviewZoom widget.Clickable // corner "Zoom" button on the reconstruction — opens the full-size lightbox

	// crop tool (source-swap model): draw + 8-handle-resize a selection on the loaded source; Apply
	// replaces the working source with that crop (re-decoded at full resolution by the studio), so the
	// preview/generate/wipe all operate on the crop as a normal image. cropSel is the PENDING selection
	// as image fractions {fx,fy,fw,fh}, valid only while CropMode.
	CropMode      bool
	Cropped       bool             // the working source is a crop of the original (show "Reset to original")
	CropBtn       widget.Clickable // enter crop mode
	CropApplyBtn  widget.Clickable
	CropCancelBtn widget.Clickable
	ResetBtn      widget.Clickable
	CropDrag      gesture.Drag
	cropSel       [4]float64 // pending selection, image fractions; cropSel[2] (w) <= cropMinFrac = none
	cropDragKind  int        // active drag: cropNone / cropNew / cropMove / cropHandle0+i (i=0..7)
	cropStartSel  [4]float64 // selection at drag start (base for move/resize)
	cropAnchor    f32.Point  // drag start point in image-fraction space

	// top-level view + tabs
	View       View
	StudioTab  widget.Clickable
	LibraryTab widget.Clickable

	// update check + About dialog
	UpdateCheckEnabled bool        // compiled in via -tags updatecheck; gates the check controls in About
	Update             *UpdateInfo // non-nil when a newer release exists
	LastSeen           string      // release tag acknowledged in About; hides the dot
	UpdateStatus       string      // transient text for a manual check
	AboutOn            bool
	AboutBtn           widget.Clickable
	AboutClose         widget.Clickable
	AboutCardSink      widget.Clickable // absorbs clicks over the card so they miss the scrim
	DownloadBtn        widget.Clickable
	GitHubBtn          widget.Clickable
	NexusBtn           widget.Clickable
	CheckNowBtn        widget.Clickable
	AutoUpdate         widget.Bool
	AboutList          widget.List

	// preferences (persisted in studio.json, surfaced in the status bar — not the generator settings)
	SoundOn widget.Bool // play a chime when a generation finishes

	// inject status (library): the in-flight inject shows a spinner on its row button + disables the
	// others; the result lingers as a green tick / red cross pill until InjectResultUntil, then reverts.
	InjectingID       string
	InjectResultID    string
	InjectOK          bool
	InjectResultUntil time.Time

	// library (saved generations)
	LibRows       []LibraryRow
	LibScroll     widget.List
	OpenFolderBtn widget.Clickable

	// lightbox: click a library thumb to view its full preview as a dismissable overlay.
	LightboxOn    bool
	LightboxOp    paint.ImageOp
	LightboxClose widget.Clickable

	// scrolling
	LeftScroll widget.List // left column (source + settings) — scrolls when toggles overflow the height
	LogList    widget.List

	// Shape editor (opt-in mode; see editor.go). Working on a deep COPY so Cancel discards cleanly.
	EditorMode     bool
	EditShapes     []model.Shape // working copy being edited
	EditW, EditH   int           // canvas size of the working doc
	EditSel        int           // selected shape index, -1 = none
	editDrag       editorDrag    // active drag (kind + start snapshot)
	editDragMoved  bool          // the active drag actually moved/scaled/rotated (else a click-to-select pushes no undo)
	editSelExtra   map[int]bool  // multi-select: selected shapes OTHER than the primary EditSel (nil/empty = single-select)
	editDragSkip   map[int]bool  // shapes excluded from editDragBase, re-composited live each drag frame (1 for single, N for a group move)
	editMarqueeOn  bool          // a rubber-band (marquee) selection is in progress
	editMarqueeAdd bool          // marquee unions into the current selection instead of replacing it (Ctrl held)
	editMarqueeA   f32.Point     // marquee start corner (image fraction)
	editMarqueeB   f32.Point     // marquee current corner (image fraction)
	editPanning    bool          // middle/secondary-button canvas pan in progress
	editShift      bool          // Shift held during the active drag (rotate snaps to 15°)
	panLast        f32.Point     // last pan pointer position
	editWantFocus  bool          // request canvas key focus next frame (Ctrl+Z / Delete)
	editUndo       [][]model.Shape
	editOp         paint.ImageOp    // cached render of EditShapes; rebuilt only when editDirty
	editDirty      bool             // EditShapes changed → re-render on the next editorArea pass
	editDragBase   *image.NRGBA     // pre-rendered shapes EXCEPT the dragged one, for live composite during a drag
	editDragBaseOp paint.ImageOp    // editDragBase uploaded once at drag start (fast-drag: base stays a cached GPU texture)
	fastDrag       bool             // fast-drag path: cache the static base, re-raster only the dragged shape's region
	EditBtn        widget.Clickable // enter the editor from a generated result
	NewBlankBtn    widget.Clickable // enter the editor on a blank canvas
	EditorTab      widget.Clickable // top-bar Editor tab
	EditSaveBtn    widget.Clickable // save the design to the library
	editKeyTag     int              // key-focus tag for Ctrl+Z / Ctrl+Shift+Z / Delete

	// editor save flow: a name field + an override-on-name confirmation + transient "saved" feedback.
	EditName          widget.Editor
	EditOverrideBtn   widget.Clickable
	EditSaveCancelBtn widget.Clickable
	editSavePending   bool   // a name-collision override confirmation is showing
	editPendingName   string // the name awaiting override confirmation
	editSavedMsg      string // transient post-save feedback
	editSavedUntil    time.Time

	// editor canvas zoom + pan
	editZoom                             float64 // 1 = fit
	editPan                              f32.Point
	editZoomIn, editZoomOut, editZoomFit widget.Clickable

	// editor inspector: numeric controls for the selected shape (two-way synced) + colour picker.
	inspFor                             int // shape index the fields currently reflect (-1 = none)
	inspX, inspY, inspW, inspH, inspRot widget.Editor
	editForward, editBack               widget.Clickable
	editDup, editDelete                 widget.Clickable
	editMirror                          widget.Clickable    // mirror the selection left↔right (across the vertical centre)
	editMirrorV                         widget.Clickable    // mirror the selection up↕down (across the horizontal centre)
	alignBtns                           [8]widget.Clickable // L,Cx,R,T,My,B, distribute-H, distribute-V
	colorSwatchBtn                      widget.Clickable    // opens the colour picker
	colorPickerOpen                     bool
	pickR, pickG, pickB, pickA          widget.Float     // R/G/B/Alpha sliders (0..1)
	eyedropBtn                          widget.Clickable // eyedropper toggle
	eyedropMode                         bool             // next canvas click samples a colour
	editImg                             *image.NRGBA     // last full render, sampled by the eyedropper
	recentColors                        []color.NRGBA    // recently applied colours
	recentBtns                          []widget.Clickable

	// editor array/radial duplicate: repeat the selection in a row or evenly around the canvas centre.
	arrayCount   widget.Editor // how many copies in total (2..24)
	arrayRowBtn  widget.Clickable
	arrayRingBtn widget.Clickable

	// editor palette + layers + redo (the undo stack is editUndo above).
	palCircle, palSquare, palTriangle widget.Clickable
	palGlow, palDisk                  widget.Clickable
	editUndoBtn, editRedoBtn          widget.Clickable
	editRedo                          [][]model.Shape
	editLayerList                     widget.List
	editLayerBtns                     []widget.Clickable
	editLockBtns                      []widget.Clickable // per-row lock toggle
	layerDrags                        []gesture.Drag     // per-row drag-to-reorder grips
	layerDragFrom                     int                // shape index being dragged (-1 = none)
	layerDragAccum                    float64            // accumulated drag px toward the next row swap
	layerDragLastY                    float32            // last drag pointer Y
	layerDragMoved                    bool               // a swap happened this drag (one undo per drag)

	// editor bank palette (curves/decorative/glyphs), always shown in the left column.
	bankList        widget.List
	bankBtns        []widget.Clickable
	bankThumbs      []paint.ImageOp
	bankRows        []bankRow
	bankThumbsBuilt bool

	// editor double-click-to-spawn: a single stray click on a palette item no longer spawns a shape; a
	// double-click drops it on the canvas.
	dblTag int       // which palette fired the previous click (1=bank thumbnail, 2=primitive chip)
	dblIdx int       // index within that palette
	dblAt  time.Time // timestamp of that click

	// editor destructive-action confirmation (red two-step buttons)
	deleteArmed   bool
	deleteArmedAt time.Time
	clearArmed    bool
	clearArmedAt  time.Time
	EditNewBtn    widget.Clickable // reset the editor to a blank canvas (armed confirm)

	// editor HSV colour wheel (continuous picker for any RGBA)
	pickH, pickS, pickV float64      // authoritative HSV of the current colour (hue+sat = disc, value = slider)
	pickVf              widget.Float // brightness/value slider
	colorWheelTag       int          // pointer focus tag for the disc
	colorWheelOp        paint.ImageOp
	colorWheelSize      int // px side the cached disc was built at
	colorWheelV         int // value bucket (0..20) the cached disc was built for
	colorWheelBuilt     bool

	// editor layer icons: cached glyph thumbnails for mask shapes, keyed by word id
	layerThumbs map[uint16]paint.ImageOp

	// editor canvas backdrop / measurement guide (checkerboard / grid / ruler), cycled from the toolbar
	canvasGuide int
	GuideBtn    widget.Clickable

	// editor smart snapping (toolbar toggle, default OFF). While moving a shape/group its bbox edges and
	// centres snap to other shapes, the canvas edges/centre and (when the grid guide is on) grid lines;
	// Alt suspends it for the duration of the press.
	snapOn        bool             // toggle state
	SnapBtn       widget.Clickable // toolbar toggle button
	editAlt       bool             // Alt held during the active drag (suspends snapping)
	snapThreshImg float64          // snap distance for this drag, in image px (screen threshold ÷ zoom)
	snapGridStep  float64          // grid spacing in image px to snap to (0 = grid not active)
	snapGuideX    float64          // active vertical snap line (image px)
	snapGuideY    float64          // active horizontal snap line (image px)
	snapShowX     bool             // a vertical guide is active this frame
	snapShowY     bool             // a horizontal guide is active this frame

	// editor symmetry stamping: a toolbar cycle (off / mirror-across-vertical / mirror-across-horizontal).
	// While on, every added shape also drops a mirrored copy; "Mirror all" reflects the whole design.
	symMode      int // symOff / symVert / symHorz
	SymBtn       widget.Clickable
	MirrorAllBtn widget.Clickable

	// editor drag-and-drop: drag a shape from the bank/primitive palette onto the canvas. A full-window
	// pass-through pointer layer tracks the cursor in window coords; the window→canvas offset is observed
	// while hovering the canvas so the drop maps to the right image position.
	bankCandKind  int       // 0 none, 1 mask, 2 primitive — a press that may become a drag
	bankCandWord  int       // mask word (bankCandKind 1)
	bankCandPrim  int       // primitive kind (bankCandKind 2)
	bankDragging  bool      // a drag past the threshold is underway (ghost shown)
	dragTag       int       // top-level pass-through pointer tag
	dragStartWin  f32.Point // press position (overlay/window coords)
	dragWin       f32.Point // current cursor (overlay/window coords)
	canvasHover   bool      // cursor currently over the canvas (offset capture)
	canvasLocal   f32.Point // cursor in canvas-local coords (last hover)
	canvasOff     f32.Point // window minus canvas-local (constant while the layout holds)
	canvasOffOK   bool
	canvasImgRect image.Rectangle // the zoomed image rect in canvas-local coords
}

// expertGroup is the collapse state of one expert sub-section.
type expertGroup struct {
	click widget.Clickable
	open  bool
}

// NewAppState builds the initial app state with sensible defaults.
func NewAppState(th *Theme) *AppState {
	s := &AppState{
		Th: th,
		// 3 manual content presets (chosen explicitly; there is no auto content-classifier) + the
		// niche "gaussian" mode (soft-glow reconstruction for SMOOTH / gradient / painterly content —
		// 8x better than greedy on a gradient, loses on fine detail; no greedy, trains on the GPU).
		// Default = anime, the best general-purpose preset.
		Mode: NewDropdown([]string{"anime", "photo", "flat", "lineart", "anime-ink", "gaussian"}, 0),
	}
	s.Budget.Value = shapesToFrac(1000)
	s.BudgetEd.SingleLine = true
	s.BudgetEd.SetText("1000")
	s.lastBudget = 1000
	s.KeepInside.Value = true // bound shapes inside the image by default (no in-game edge bleed)
	s.SoundOn.Value = true    // chime on finish by default (overridden by the saved preference)
	s.Seed.SingleLine = true
	s.Seed.SetText("1")
	s.InkRatio.Value = ratioToSlider(preset.DefaultInkRatio("anime-ink")) // sane start; reseeded when a hybrid preset is picked
	s.BuiltinCards = make([]widget.Clickable, len(builtinCards))
	s.RandomEd.SingleLine = true
	s.MutatedEd.SingleLine = true
	s.SampleEd.SingleLine = true
	s.PolishItersEd.SingleLine = true
	s.AspectEd.SingleLine = true
	allKinds := make([]bool, len(preset.KindNames))
	for i := range allKinds {
		allKinds[i] = true
	}
	s.KindsSel = NewMultiSelect(preset.KindNames, allKinds)
	s.ShapeChips = make([]widget.Clickable, len(shapeChipKinds))
	s.KindWeightEds = make([]widget.Editor, len(preset.KindNames))
	for i := range s.KindWeightEds {
		s.KindWeightEds[i].SingleLine = true
	}
	s.MaxNIEd.SingleLine = true
	s.GridEd.SingleLine = true
	s.OverdrawEd.SingleLine = true
	s.QualityDD = NewDropdown([]string{"fast", "balanced", "max", "quality", "ultra"}, 3)
	{
		av := i18n.Available()
		endonyms := make([]string, len(av))
		sel := 0
		for i, l := range av {
			endonyms[i] = l.Endonym
			if l.Tag == i18n.Current() {
				sel = i
			}
		}
		s.Lang = NewDropdown(endonyms, sel)
	}
	s.InjectLayers.SingleLine = true
	s.InjectScale.SingleLine = true
	s.InjectScale.SetText("1.0")
	s.Wipe.Value = 0.5 // before/after wipe centred by default
	s.LeftScroll.Axis = layout.Vertical
	s.LibScroll.Axis = layout.Vertical
	s.LogList.Axis = layout.Vertical
	s.LogList.ScrollToEnd = true
	s.AutoUpdate.Value = true // overridden by the saved preference
	s.AboutList.Axis = layout.Vertical
	if img := loadShield(); img != nil {
		s.Shield = img
		s.ShieldOp = paint.NewImageOp(img)
	}
	s.PresetNameEd.SingleLine = true
	s.ExpGroups[0].open = true // the smoothness/crispness group is open by default
	s.baseMode = s.Mode.Value()
	s.applyModeKnobs() // fill the expert knobs with the default mode's concrete values
	return s
}

// SetBackends configures the engine label and, when more than one backend works on this machine, a
// picker dropdown (CUDA/Vulkan). opts[0] is the default/preferred. With a single backend there is no
// choice to make, so no dropdown is created and the label shows it statically.
func (s *AppState) SetBackends(opts []string) {
	if len(opts) == 0 {
		opts = []string{"CPU"}
	}
	s.BackendLabel = "shape engine · " + opts[0]
	if len(opts) > 1 {
		s.Backend = NewDropdown(opts, 0)
	}
}

// crop drag kinds (cropDragKind): cropHandle0+i selects handle i (0..7 = NW,N,NE,E,SE,S,SW,W).
const (
	cropNone    = 0
	cropNew     = 1
	cropMove    = 2
	cropHandle0 = 10
	cropMinFrac = 0.02 // a selection smaller than this in either axis is treated as "none"
)

// SetSource replaces the loaded source image (and its paint op). A new image resets the crop tool.
func (s *AppState) SetSource(img *image.NRGBA, path string) {
	s.Source = img
	s.SourceOp = paint.NewImageOp(img)
	s.ImgPath = path
	s.Preview = nil
	s.PreviewOp = paint.ImageOp{}
	s.Phase = PhaseIdle
	s.Stats = RunStats{}
	s.CropMode = false
	s.Cropped = false
	s.cropSel = [4]float64{}
	s.cropDragKind = cropNone
}

// EnterCropMode starts the crop tool with a centred default selection the user can resize or redraw.
func (s *AppState) EnterCropMode() {
	s.CropMode = true
	s.cropSel = [4]float64{0.2, 0.2, 0.6, 0.6}
	s.cropDragKind = cropNone
}

// ExitCropMode leaves the crop tool, discarding the pending selection.
func (s *AppState) ExitCropMode() {
	s.CropMode = false
	s.cropDragKind = cropNone
}

// cropSelValid reports whether the pending selection is large enough to use.
func (s *AppState) cropSelValid() bool {
	return s.cropSel[2] > cropMinFrac && s.cropSel[3] > cropMinFrac
}

// CropSelection returns the pending selection {fx,fy,fw,fh} (image fractions); ok is false when there
// is no usable selection. The studio composes it with the current view's source rect on Apply.
func (s *AppState) CropSelection() (fx, fy, fw, fh float64, ok bool) {
	if !s.cropSelValid() {
		return 0, 0, 0, 0, false
	}
	return s.cropSel[0], s.cropSel[1], s.cropSel[2], s.cropSel[3], true
}

// BeginInject marks a library entry's injection as in flight (spinner button, others blocked).
func (s *AppState) BeginInject(id string) {
	s.InjectingID = id
	s.InjectResultID = ""
}

// FinishInject records an inject outcome as a transient tick/cross pill that reverts at `until`.
func (s *AppState) FinishInject(id string, ok bool, until time.Time) {
	if s.InjectingID == id {
		s.InjectingID = ""
	}
	s.InjectResultID = id
	s.InjectOK = ok
	s.InjectResultUntil = until
}

// InjectBusy reports whether any injection is currently in flight (used to block re-entry).
func (s *AppState) InjectBusy() bool { return s.InjectingID != "" }

// InjectLayersValue is the FH6 template layer count entered in the Library header (0 = unset/invalid).
func (s *AppState) InjectLayersValue() int {
	if v, err := strconv.Atoi(strings.TrimSpace(s.InjectLayers.Text())); err == nil && v > 0 {
		return v
	}
	return 0
}

// MaybeClearInjectResult drops the lingering tick/cross pill once its display window has elapsed.
func (s *AppState) MaybeClearInjectResult(now time.Time) {
	if s.InjectResultID != "" && !s.InjectResultUntil.IsZero() && now.After(s.InjectResultUntil) {
		s.InjectResultID = ""
	}
}

// ShowLightbox opens the full-image overlay for a decoded library preview.
func (s *AppState) ShowLightbox(img image.Image) {
	if img == nil {
		return
	}
	s.LightboxOp = paint.NewImageOp(img)
	s.LightboxOn = true
}

// HideLightbox dismisses the preview overlay.
func (s *AppState) HideLightbox() { s.LightboxOn = false }

// OpenAbout acknowledges any pending update and returns the tag to persist as seen ("" if none).
func (s *AppState) OpenAbout() string {
	s.AboutOn = true
	if s.Update != nil {
		s.LastSeen = s.Update.Version
		return s.Update.Version
	}
	return ""
}

func (s *AppState) CloseAbout() { s.AboutOn = false }

func (s *AppState) HasUpdateBadge() bool {
	return s.Update != nil && s.Update.Version != s.LastSeen
}

// SetPreview replaces the current reconstruction frame (and its paint op).
func (s *AppState) SetPreview(img *image.NRGBA) {
	if img == nil {
		return
	}
	s.Preview = img
	s.PreviewOp = paint.NewImageOp(img)
}

// AppendLog adds a line to the activity feed, inferring its severity from the text (capped to 600).
func (s *AppState) AppendLog(line string) { s.AppendLogLvl(classifyLog(line), line) }

// AppendLogLvl adds a line with an explicit severity (used for the messages that matter most — phase
// transitions, inject — where content-based classification would be ambiguous).
func (s *AppState) AppendLogLvl(level LogLevel, line string) {
	s.Log = append(s.Log, LogEntry{Time: time.Now(), Level: level, Text: line})
	if len(s.Log) > 600 {
		s.Log = s.Log[len(s.Log)-600:]
	}
}

// BudgetShapes maps the budget slider (0..1) to a shape count in [1, MaxShapes].
func (s *AppState) BudgetShapes() int {
	return fracToShapes(s.Budget.Value)
}

// syncBudget keeps the slider and the manual entry in agreement — whichever the user last
// changed drives the other, clamped to [1, MaxShapes].
func (s *AppState) syncBudget() {
	if cur := s.BudgetShapes(); cur != s.lastBudget { // slider moved -> editor
		s.BudgetEd.SetText(strconv.Itoa(cur))
		s.lastBudget = cur
		return
	}
	if v, err := strconv.Atoi(strings.TrimSpace(s.BudgetEd.Text())); err == nil && v != s.lastBudget {
		cl := v
		if cl < 1 {
			cl = 1
		}
		if cl > preset.MaxShapes {
			cl = preset.MaxShapes
		}
		s.Budget.Value = shapesToFrac(cl)
		s.lastBudget = cl
		if cl != v {
			s.BudgetEd.SetText(strconv.Itoa(cl))
		}
	}
}

// SetBudgetShapes positions the budget slider at a given shape count.
func (s *AppState) SetBudgetShapes(n int) { s.Budget.Value = shapesToFrac(n) }

// InkRatioValue is the current Artist Lines<->Fill split (fraction of budget for ink, 0..MaxInkRatio).
func (s *AppState) InkRatioValue() float64 { return sliderToRatio(s.InkRatio.Value) }

// SetInkRatio positions the Artist slider at a given ink fraction (clamped to [0, MaxInkRatio]).
func (s *AppState) SetInkRatio(r float64) { s.InkRatio.Value = ratioToSlider(r) }

// sliderToRatio / ratioToSlider map the Artist slider (0..1) to the ink fraction (0..MaxInkRatio).
func sliderToRatio(f float32) float64 { return float64(f) * preset.MaxInkRatio }

func ratioToSlider(r float64) float32 {
	s := float32(r / preset.MaxInkRatio)
	if s < 0 {
		s = 0
	}
	if s > 1 {
		s = 1
	}
	return s
}

// RestoreBudget sets the budget from a saved/clamped count, keeping the slider, the manual entry, and
// the internal sync baseline in agreement so syncBudget does not fight the restore on the first frame.
func (s *AppState) RestoreBudget(n int) {
	if n < 1 {
		n = 1
	}
	if n > preset.MaxShapes {
		n = preset.MaxShapes
	}
	s.Budget.Value = shapesToFrac(n)
	s.BudgetEd.SetText(strconv.Itoa(n))
	s.lastBudget = n
}

// Choices builds preset.Choices from the current widget state. alpha/backfit are only
// passed as overrides once the user has actually toggled them, so the content mode's
// auto-defaults apply until then. polish is always explicit (its default is on anyway).
func (s *AppState) Choices() preset.Choices {
	c := preset.DefaultChoices()
	c.Shapes = s.BudgetShapes()
	c.InkRatio = s.InkRatioValue() // the Artist Lines<->Fill split (used only for hybrid modes); non-expert, always set
	c.Mode = s.baseMode            // the engine mode; the dropdown may show a custom preset name instead
	c.Quality = "quality"          // the high-quality knee; expert overrides this and every knob below
	if v, err := strconv.ParseInt(strings.TrimSpace(s.Seed.Text()), 10, 64); err == nil {
		c.Seed = v
	}
	// MONO single-colour logo/decal — a curated toggle (works in any mode; preset.Resolve forces the
	// flat single-colour cutout). "auto" picks the logo's dominant ink colour.
	if s.Mono.Value {
		c.MonoColor = "auto"
	}
	c.Economy = s.Economy.Value // opt-in low-budget co-adaptation (slow); curated toggle, applied in both modes
	// Used-shapes picker (top of Advanced): a curated control like Mono/Economy — a restricted set keeps
	// applying even when Advanced is collapsed again. All-on stays "" so the mode keeps its default mix.
	if s.KindsSel.OnCount() < len(preset.KindNames) {
		c.Kinds = s.KindsSel.ValueCSV()
	}
	if !s.Expert.Value {
		return c
	}

	// Expert: every knob holds a concrete value, so each one is an explicit override.
	c.Quality = s.QualityDD.Value()

	polish := s.PolishOn.Value
	c.Polish = &polish
	if v := editorPosInt(&s.PolishItersEd); v > 0 {
		c.PolishIters = v
	}
	c.PolishTau1 = float64(sliderToVal(s.TauSlider.Value, tauLo, tauHi))

	alpha := s.Alpha.Value
	c.Alpha = &alpha
	c.AlphaMin = float64(sliderToVal(s.AlphaMinSlider.Value, alphaFloorLo, alphaFloorHi))

	weighted := s.WeightedOn.Value
	c.Weighted = &weighted
	c.WeightStrength = float64(sliderToVal(s.WeightStrSlider.Value, weightStrLo, weightStrHi))

	boundary := s.Boundary.Value
	c.Boundary = &boundary
	backfit := s.Backfit.Value
	c.Backfit = &backfit
	compact := s.CompactOn.Value
	c.Compact = &compact

	if v, ok := editorFloat(&s.AspectEd); ok && v >= 0 {
		c.Aspect = v
	}
	c.Kinds = s.KindsSel.ValueCSV()
	c.KindWeights = s.kindWeightsCSV()
	c.StandoutTol = float64(sliderToVal(s.StandoutSlider.Value, standoutLo, standoutHi))

	if v := editorPosInt(&s.RandomEd); v > 0 {
		c.Random = v
	}
	if v := editorPosInt(&s.MutatedEd); v > 0 {
		c.Mutated = v
	}
	if v := editorPosInt(&s.SampleEd); v > 0 {
		c.SampleBudget = v
	}
	if v := editorPosInt(&s.MaxNIEd); v > 0 {
		c.MaxNoImprove = v
	}
	if v := editorPosInt(&s.GridEd); v > 0 {
		c.Grid = v
	}
	if v, ok := editorFloat(&s.OverdrawEd); ok && v >= 1 {
		c.Overdraw = v
	}
	return c
}

// ApplyChoices loads a full configuration into the widgets (the inverse of Choices): the base mode,
// budget, seed, and every expert knob, with Expert mode turned on so the loaded knobs are visible.
// KeepInside is not part of Choices, so the caller restores it separately. Mode.Set does not raise the
// dropdown's changed flag, so the next frame will not overwrite these knobs with the mode defaults.
func (s *AppState) ApplyChoices(c preset.Choices) {
	s.baseMode = c.Mode
	s.RestoreBudget(c.Shapes)
	s.SetInkRatio(c.InkRatio)
	s.Mono.Value = c.MonoColor != ""
	s.Economy.Value = c.Economy
	s.Seed.SetText(strconv.FormatInt(c.Seed, 10))
	s.Expert.Value = true
	s.AdvOpen = true // a saved preset is a full snapshot — open Advanced so its loaded knobs are visible
	s.applyKnobs(c)
}

// syncSettings reloads the knobs when the Preset dropdown changes (a built-in mode loads its concrete
// defaults; a custom preset loads its full snapshot), then keeps the budget slider in agreement.
func (s *AppState) syncSettings() {
	if s.Mode.Changed() {
		s.applySelectedMode()
	}
	s.syncBudget()
}

// applySelectedMode applies the dropdown's current selection: a built-in mode fills the expert knobs
// with that mode's concrete defaults; a custom preset name loads its saved snapshot.
func (s *AppState) applySelectedMode() {
	v := s.Mode.Value()
	if !IsBuiltinMode(v) {
		for i := range s.Presets {
			if s.Presets[i].Name == v {
				s.ApplyChoices(s.Presets[i].Choices)
				s.KeepInside.Value = s.Presets[i].KeepInside
				return
			}
		}
	}
	s.baseMode = v
	s.applyModeKnobs()
	if preset.IsHybridMode(v) { // seed the Artist Lines<->Fill slider from the preset's default split
		s.SetInkRatio(preset.DefaultInkRatio(v))
	}
}

func IsBuiltinMode(m string) bool {
	switch m {
	case "anime", "photo", "flat", "lineart", "anime-ink", "gaussian":
		return true
	}
	return false
}

// SelectPreset sets the Preset dropdown to value and applies it (a built-in mode loads its concrete
// defaults; a custom preset loads its snapshot). Used by main after a save/delete to refresh the view.
func (s *AppState) SelectPreset(value string) {
	s.Mode.Set(value)
	s.applySelectedMode()
}

// SetPresets stores the loaded custom presets and rebuilds the Preset dropdown options (built-in modes
// followed by the preset names), preserving the current selection where possible.
func (s *AppState) SetPresets(ps []userpreset.Preset) {
	s.Presets = ps
	s.PresetCards = make([]widget.Clickable, len(ps))
	builtin := []string{"anime", "photo", "flat", "lineart", "anime-ink", "gaussian"}
	opts := append([]string{}, builtin...)
	for i := range ps {
		opts = append(opts, ps[i].Name)
	}
	s.Mode.SetOptions(opts, len(builtin))
}

// SelectedPreset returns the custom preset matching the dropdown's current value, or nil for a built-in.
func (s *AppState) SelectedPreset() *userpreset.Preset {
	v := s.Mode.Value()
	if IsBuiltinMode(v) {
		return nil
	}
	for i := range s.Presets {
		if s.Presets[i].Name == v {
			return &s.Presets[i]
		}
	}
	return nil
}

func shapesToFrac(n int) float32 {
	if n <= 1 {
		return 0
	}
	if n >= preset.MaxShapes {
		return 1
	}
	return float32(n-1) / float32(preset.MaxShapes-1)
}

func fracToShapes(f float32) int {
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	n := 1 + int(f*float32(preset.MaxShapes-1)+0.5)
	if n > preset.MaxShapes {
		n = preset.MaxShapes
	}
	return n
}

// Expert slider ranges: a widget.Float value in 0..1 maps linearly into [lo, hi].
const (
	tauLo, tauHi               float32 = 0.02, 0.20
	alphaFloorLo, alphaFloorHi float32 = 0.0, 1.0
	weightStrLo, weightStrHi   float32 = 0.0, 1.0
	standoutLo, standoutHi     float32 = 0.0, 0.02
)

func sliderToVal(f, lo, hi float32) float32 { return lo + f*(hi-lo) }

func valToSlider(v, lo, hi float32) float32 {
	if hi <= lo {
		return 0
	}
	s := (v - lo) / (hi - lo)
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}

func derefBool(p *bool, def bool) bool {
	if p != nil {
		return *p
	}
	return def
}

func orFloat(v, def float64) float64 {
	if v > 0 {
		return v
	}
	return def
}

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func setEditorInt(ed *widget.Editor, v int) {
	if v > 0 {
		ed.SetText(strconv.Itoa(v))
	} else {
		ed.SetText("")
	}
}

func editorPosInt(ed *widget.Editor) int {
	if v, err := strconv.Atoi(strings.TrimSpace(ed.Text())); err == nil && v > 0 {
		return v
	}
	return 0
}

func editorFloat(ed *widget.Editor) (float64, bool) {
	if v, err := strconv.ParseFloat(strings.TrimSpace(ed.Text()), 64); err == nil {
		return v, true
	}
	return 0, false
}

// applyKnobs sets every expert control to the values in c. A mode/preset snapshot carries concrete
// values; sentinels (-1, nil) fall back to a sane display default. Budget/mode/seed are caller-owned.
func (s *AppState) applyKnobs(c preset.Choices) {
	if c.Quality != "" {
		s.QualityDD.Set(c.Quality)
	}
	s.PolishOn.Value = derefBool(c.Polish, true)
	setEditorInt(&s.PolishItersEd, c.PolishIters)
	s.TauSlider.Value = valToSlider(float32(orFloat(c.PolishTau1, 0.08)), tauLo, tauHi)

	s.Alpha.Value = derefBool(c.Alpha, true)
	floor := c.AlphaMin
	if floor < 0 {
		floor = 0.40
	}
	s.AlphaMinSlider.Value = valToSlider(float32(floor), alphaFloorLo, alphaFloorHi)

	s.WeightedOn.Value = derefBool(c.Weighted, true)
	wstr := c.WeightStrength
	if wstr < 0 {
		wstr = 0.15
	}
	s.WeightStrSlider.Value = valToSlider(float32(wstr), weightStrLo, weightStrHi)

	s.Boundary.Value = derefBool(c.Boundary, false)
	s.Backfit.Value = derefBool(c.Backfit, false)
	s.CompactOn.Value = derefBool(c.Compact, true)

	if c.Aspect >= 0 {
		s.AspectEd.SetText(formatFloat(c.Aspect))
	} else {
		s.AspectEd.SetText("")
	}
	kinds := strings.TrimSpace(c.Kinds)
	if kinds == "" {
		kinds = strings.Join(preset.KindNames, ",") // empty = the default all-kinds mix
	}
	s.KindsSel.SetCSV(kinds)
	// Distribute the weights (parallel to the kinds CSV) into the per-kind editors.
	for i := range s.KindWeightEds {
		s.KindWeightEds[i].SetText("")
	}
	wkinds := strings.Split(kinds, ",")
	wvals := strings.Split(c.KindWeights, ",")
	for j, kn := range wkinds {
		if idx := kindIndex(strings.TrimSpace(kn)); idx >= 0 && j < len(wvals) {
			if v := strings.TrimSpace(wvals[j]); v != "" {
				s.KindWeightEds[idx].SetText(v)
			}
		}
	}
	s.StandoutSlider.Value = valToSlider(float32(c.StandoutTol), standoutLo, standoutHi)

	setEditorInt(&s.RandomEd, c.Random)
	setEditorInt(&s.MutatedEd, c.Mutated)
	setEditorInt(&s.SampleEd, c.SampleBudget)
	setEditorInt(&s.MaxNIEd, c.MaxNoImprove)
	setEditorInt(&s.GridEd, c.Grid)
	if c.Overdraw > 1 {
		s.OverdrawEd.SetText(formatFloat(c.Overdraw))
	} else {
		s.OverdrawEd.SetText("")
	}
}

// applyModeKnobs fills the expert knobs with the selected built-in mode's concrete defaults for the
// loaded image's content (palette + cutout), so the controls always read like a real preset.
func (s *AppState) applyModeKnobs() {
	s.applyKnobs(preset.ModeKnobDefaults(s.baseMode, s.ContentColors, s.ContentCutout))
}

// kindWeightsCSV reads the per-kind weight editors for the currently selected kinds, in selection
// order, so the CSV stays parallel to the kinds CSV. A blank entry leaves the engine on a uniform mix.
func (s *AppState) kindWeightsCSV() string {
	checked := s.KindsSel.Value()
	ws := make([]string, 0, len(checked))
	for _, name := range checked {
		if idx := kindIndex(name); idx >= 0 {
			ws = append(ws, strings.TrimSpace(s.KindWeightEds[idx].Text()))
		}
	}
	return strings.Join(ws, ",")
}

func kindIndex(name string) int {
	for i, k := range preset.KindNames {
		if strings.EqualFold(k, name) {
			return i
		}
	}
	return -1
}
