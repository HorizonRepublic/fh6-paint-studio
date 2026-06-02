package ui

import (
	"image"
	"strconv"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/gesture"
	"gioui.org/layout"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"fh6-paint-studio/internal/preset"
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
)

// RunStats is the live telemetry shown in the run panel.
type RunStats struct {
	Shapes, Total int
	Err, Err0     float64
	Elapsed, ETA  time.Duration
	History       []float64 // error per progress tick, for the sparkline
	Stage         string    // current post-greedy phase (polish/standout/economy); "" during the greedy build — drives the indeterminate "still working" indicator instead of a bar stuck at 100%
}

// AppState holds every widget state plus the loaded image and run telemetry. The panel
// Layout methods read and mutate it; the main loop feeds runner events into it.
type AppState struct {
	Th *Theme

	// loaded image
	ImgPath   string
	Source    *image.NRGBA
	SourceOp  paint.ImageOp
	Preview   *image.NRGBA
	PreviewOp paint.ImageOp

	// run
	Phase   Phase
	Stats   RunStats
	Log     []string
	Backend string
	Toast   string

	// settings widgets
	Budget     widget.Float
	BudgetEd   widget.Editor // manual shape-count entry, two-way synced with Budget
	lastBudget int
	Mode       *Dropdown
	Alpha      widget.Bool
	Polish     widget.Bool
	Economy    widget.Bool // opt-in post-polish trim of redundant layers (default off = full budget / max quality)
	Standout   widget.Bool // opt-in post-polish perceptual standout suppression (default off): blend/fade shapes whose rim stands out against a smooth target
	Backfit    widget.Bool
	Boundary   widget.Bool // boundary-aware radius — smoother gradients on character/photo liveries (opt-in)
	KeepInside widget.Bool // generate against a transparent surround so the spill-penalty keeps every shape INSIDE the image (no edge bleed); the result is mapped back to the original size (no frame artefact)
	Seed       widget.Editor

	AlphaHint      Hint
	PolishHint     Hint
	EconomyHint    Hint
	StandoutHint   Hint
	BackfitHint    Hint
	BoundaryHint   Hint
	KeepInsideHint Hint
	BudgetHint     Hint
	ModeHint       Hint
	SeedHint       Hint

	alphaTouched    bool
	backfitTouched  bool
	boundaryTouched bool

	// advanced
	AdvClick  widget.Clickable
	AdvOpen   bool
	RandomEd  widget.Editor
	MutatedEd widget.Editor
	SampleEd  widget.Editor
	SeedFocus bool

	// actions
	OpenBtn      widget.Clickable
	PreviewOpen  widget.Clickable // the empty-state preview area doubles as an Open button
	GenBtn       widget.Clickable
	CancelBtn    widget.Clickable
	InjectLayers    widget.Editor // exact FH6 template layer count for injection (library inject controls)
	InjectLayersErr bool          // the FH6-layers field was empty/invalid on an Inject click — draw it red until a valid count is entered
	InjectScale     widget.Editor // uniform scale of the injected art on the decal (1.0 = fit the canvas; <1 shrinks toward centre so it fits a zoomed-in editor view)
	ElevateBtn   widget.Clickable
	Elevated     bool        // process is running as administrator (set by main at startup)
	Shield       image.Image // system UAC shield icon (nil if unavailable)
	ShieldOp     paint.ImageOp

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

	// preferences (persisted in studio.json, surfaced in the status bar — not the generator settings)
	SoundOn widget.Bool // play a chime when a generation finishes

	// inject status (library): the in-flight inject shows a spinner on its row button + disables the
	// others; the result lingers as a green ✓ / red ✗ pill until InjectResultUntil, then reverts.
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
}

// NewAppState builds the initial app state with sensible defaults.
func NewAppState(th *Theme) *AppState {
	s := &AppState{
		Th: th,
		// 3 manual content presets (chosen explicitly; there is no auto content-classifier).
		// Default = anime, the best general-purpose preset.
		Mode: NewDropdown([]string{"anime", "photo", "flat"}, 0),
	}
	s.Budget.Value = shapesToFrac(1000)
	s.BudgetEd.SingleLine = true
	s.BudgetEd.SetText("1000")
	s.lastBudget = 1000
	s.Polish.Value = true
	s.KeepInside.Value = true // bound shapes inside the image by default (no in-game edge bleed)
	s.SoundOn.Value = true    // chime on finish by default (overridden by the saved preference)
	s.Seed.SingleLine = true
	s.Seed.SetText("1")
	s.RandomEd.SingleLine = true
	s.MutatedEd.SingleLine = true
	s.SampleEd.SingleLine = true
	s.InjectLayers.SingleLine = true
	s.InjectScale.SingleLine = true
	s.InjectScale.SetText("1.0")
	s.Wipe.Value = 0.5 // before/after wipe centred by default
	s.LeftScroll.Axis = layout.Vertical
	s.LibScroll.Axis = layout.Vertical
	s.LogList.Axis = layout.Vertical
	s.LogList.ScrollToEnd = true
	if img := loadShield(); img != nil {
		s.Shield = img
		s.ShieldOp = paint.NewImageOp(img)
	}
	return s
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

// FinishInject records an inject outcome as a transient ✓/✗ pill that reverts at `until`.
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

// MaybeClearInjectResult drops the lingering ✓/✗ pill once its display window has elapsed.
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

// SetPreview replaces the current reconstruction frame (and its paint op).
func (s *AppState) SetPreview(img *image.NRGBA) {
	if img == nil {
		return
	}
	s.Preview = img
	s.PreviewOp = paint.NewImageOp(img)
}

// AppendLog adds a line to the execution log (capped to the last 600 lines).
func (s *AppState) AppendLog(line string) {
	s.Log = append(s.Log, line)
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
	c.Mode = s.Mode.Value()
	c.Quality = "quality" // the high-quality knee (50k candidates); advanced fields override per-knob
	pol := s.Polish.Value
	c.Polish = &pol
	c.Economy = s.Economy.Value
	c.Standout = s.Standout.Value
	if s.alphaTouched {
		a := s.Alpha.Value
		c.Alpha = &a
	}
	if s.backfitTouched {
		b := s.Backfit.Value
		c.Backfit = &b
	}
	if s.boundaryTouched {
		b := s.Boundary.Value
		c.Boundary = &b
	}
	if v, err := strconv.ParseInt(strings.TrimSpace(s.Seed.Text()), 10, 64); err == nil {
		c.Seed = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(s.RandomEd.Text())); err == nil && v > 0 {
		c.Random = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(s.MutatedEd.Text())); err == nil && v > 0 {
		c.Mutated = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(s.SampleEd.Text())); err == nil && v > 0 {
		c.SampleBudget = v
	}
	return c
}

// syncAutoToggles updates the alpha/backfit checkboxes to the current mode's default while
// the user has not touched them, and flips the touched flag once they interact.
func (s *AppState) syncAutoToggles(gtx C) {
	if s.Alpha.Update(gtx) {
		s.alphaTouched = true
	}
	if s.Backfit.Update(gtx) {
		s.backfitTouched = true
	}
	if s.Boundary.Update(gtx) {
		s.boundaryTouched = true
	}
	m := s.Mode.Value()
	if !s.alphaTouched {
		s.Alpha.Value = modeAlphaDefault(m)
	}
	if !s.backfitTouched {
		s.Backfit.Value = modeBackfitDefault(m)
	}
	if !s.boundaryTouched {
		s.Boundary.Value = modeBoundaryDefault(m)
	}
	// A mode change should re-apply auto defaults: clear touched when the dropdown changed.
	if s.Mode.Changed() {
		s.alphaTouched = false
		s.backfitTouched = false
		s.boundaryTouched = false
	}
}

func modeAlphaDefault(mode string) bool {
	switch mode {
	case "flat", "logo", "line", "cutout":
		return false
	default:
		return true
	}
}

func modeBackfitDefault(mode string) bool {
	switch mode {
	case "flat", "logo", "line", "cutout":
		return true
	default:
		return false
	}
}

// modeBoundaryDefault mirrors preset.boundaryDefault for the advanced toggle's displayed state:
// boundary-aware radius defaults ON for character/illustration content (and "auto", which is usually
// anime in FH6), OFF for flat/photo/logo where it frays or is mixed. The engine re-decides at run
// time from the resolved mode, so for "auto" this is just the checkbox hint until the user touches it.
func modeBoundaryDefault(mode string) bool {
	switch mode {
	case "anime", "shaded", "illustration", "realism", "auto", "":
		return true
	default: // photo, flat, logo, line, cutout
		return false
	}
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
