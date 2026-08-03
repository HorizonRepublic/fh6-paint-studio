// Command studio is the FH6 Paint Studio desktop GUI: open an image, pick a budget /
// mode / quality, watch the reconstruction build live, then export the geometry. The engine
// runs in-process on a worker goroutine (internal/runner); the CUDA backend is used when
// built with -tags cuda (DLL beside the exe), else the CPU reference.
//
//	go build -tags cuda -o bin/fh6-paint-studio.exe ./cmd/studio   (release)
//	go build           -o bin/fh6-paint-studio-cpu.exe ./cmd/studio (dev/headless)
package main

import (
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/hybrid"
	"fh6-paint-studio/internal/i18n"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/inject"
	"fh6-paint-studio/internal/library"
	"fh6-paint-studio/internal/metric"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/preset"
	"fh6-paint-studio/internal/runner"
	"fh6-paint-studio/internal/ui"
	"fh6-paint-studio/internal/userpreset"
)

// version is the release version, injected at build time via -ldflags "-X main.version=...". It stays
// "dev" for a plain `go build`, and matches the version stamped into the Windows resource (see icon.go).
var version = "dev"

const (
	githubURL = "https://github.com/HorizonRepublic/fh6-paint-studio"
	nexusURL  = "https://www.nexusmods.com/forzahorizon6/mods/314"
)

func main() {
	// Colour blend is always LINEAR (the editor's compositing space): it visibly de-clutters
	// semi-transparent gradients and is a no-op for opaque content. Set before any image decode here,
	// so selftest/--fh6-locate and the GUI loop all inherit it.
	model.LinearLight = true

	if len(os.Args) > 1 && os.Args[1] == "--selftest" {
		selftest()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--fh6-locate" {
		fh6Locate()
		return
	}
	applog.Init("fh6-paint-studio.log")
	defer applog.Close()
	applog.Printf("FH6 Paint Studio %s", version)

	go func() {
		// Restore the last window size (clamped to the minimum); default to 1280×820 on first run.
		winW, winH := 1280, 820
		if cfg := loadConfig(); cfg.WindowW >= 960 && cfg.WindowH >= 640 {
			winW, winH = cfg.WindowW, cfg.WindowH
		}
		w := new(app.Window)
		w.Option(app.Title("FH6 Paint Studio"), app.Size(unit.Dp(winW), unit.Dp(winH)), app.MinSize(unit.Dp(960), unit.Dp(640)))
		if err := loop(w); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}

func loop(w *app.Window) error {
	th := ui.NewTheme()
	st := ui.NewAppState(th)
	st.Version = version
	backends := backendOptions()
	st.SetBackends(backends)               // a picker when >1 GPU backend works (allgpu build), else a static label
	runner.BackendPreference = backends[0] // default to the system's preferred (CUDA where present, else Vulkan)
	st.UpdateCheckEnabled = updateCheckEnabled
	st.Elevated = inject.Elevated()
	prefs := loadConfig()
	st.SoundOn.Value = prefs.SoundOn() // restore the persisted "sound on finish" preference
	st.AutoUpdate.Value = prefs.CheckUpdatesEnabled()
	st.LastSeen = prefs.LastSeenVersion

	// Warm the shell file-dialog infrastructure in the background so the first Open shows the native
	// dialog without a cold-start delay.
	prewarmDialogs()

	// The generation library (auto-saved completed runs) lives under ~/FH6PaintStudio/library. A failed
	// open is non-fatal: Studio still works, the Library tab shows its empty/error state.
	var store *library.Store
	if root, err := library.DefaultRoot(); err != nil {
		st.AppendLog("library: " + err.Error())
	} else {
		store = library.Open(root)
		reloadLibrary(st, store)
	}
	var pendingExportID string // set when a library Export "Save as…" dialog is in flight

	// Custom presets (saved generation settings, reloadable from the Preset dropdown).
	var presetStore *userpreset.Store
	if root, err := userpreset.DefaultRoot(); err != nil {
		st.AppendLog("presets: " + err.Error())
	} else {
		presetStore = userpreset.Open(root)
		reloadPresets(st, presetStore)
	}

	// Restore the last selection AFTER presets load, so a custom-preset name resolves in the dropdown.
	// SelectPreset sets the engine mode and loads the snapshot; the persisted budget and keep-inside then
	// override it, so the exact last state is restored even where it diverged from the preset.
	if prefs.Preset != "" {
		st.SelectPreset(prefs.Preset)
	}
	if prefs.Budget > 0 {
		st.RestoreBudget(prefs.Budget)
	}
	if prefs.KeepInside != nil {
		st.KeepInside.Value = *prefs.KeepInside
	}
	if prefs.SourceRes != nil {
		st.SourceRes.Value = *prefs.SourceRes
	}

	// Language: an explicit saved choice wins; otherwise match the OS UI language on first run, falling
	// back to English. The picker reflects whatever we land on.
	if prefs.Locale != "" {
		i18n.SetLocale(prefs.Locale)
	} else if tag, ok := i18n.Detect(); ok {
		i18n.SetLocale(tag)
	}
	st.Lang.Set(i18n.EndonymOf(i18n.Current()))

	q := newEventQueue()
	var ops op.Ops

	var curPrep *imageio.Prepared // engine input for the loaded image (always decoded in linear light)
	var curGen *imageio.Prepared  // engine input for the ACTIVE run — curPrep, or a crop of it
	var hybridInk int             // hybrid mode: FDoG ink budget reserved this run, appended in Done
	// viewAbs is the absolute source rect (raw-file coords) that the current working image covers — the
	// original's auto-crop rect after Open/Reset, or the composed crop rect after Apply crop. It is the
	// base a new crop selection composes against, so repeated crops re-decode the original at full res.
	var viewAbs image.Rectangle
	// "Keep shapes inside image": when the active run used a transparent surround, these record the
	// border (px) and the pre-pad dims so the finished geometry + canvas are mapped back to the original
	// size (TranslateShapes/UnpadCanvas) — a clean, frame-free result. runPadPx==0 means no surround.
	var runPadPx, runOrigW, runOrigH int
	var cancelRun func()
	var runCancelled bool        // set when the user cancels; the engine still emits Done, so the Done handler must not treat a cancelled run as a finished one
	var lastShapes []model.Shape // final base geometry, for export
	var lastW, lastH int

	// Async image open: pickFile (native modal dialog) runs inline, but the decode/downscale is
	// off-loaded to a worker so the UI never freezes after a file is chosen. `opening` guards re-entry.
	var il imageLoad
	var opening bool

	// Native dialogs run on a worker goroutine (NOT inline) so they never block the event loop /
	// deadlock the frame handshake; picking/saving guard re-entry, the picks deliver the chosen path.
	var openPick, savePick pathPick
	var picking, saving bool
	var injDone injectHolder // inject worker -> UI loop hand-off (drives the per-row spinner + tick/cross)

	// Taskbar progress (the green fill on the app's taskbar button during a run) + a completion flash.
	// Enabled lazily once the first Win32 view event hands us the native window handle; all calls are
	// nil-safe so the non-Windows / pre-HWND paths are no-ops. winHWND is also used to flash on Done.
	var tb *taskbar
	var winHWND uintptr

	// wall-clock start of the current run, for a live-ticking elapsed during the (uncounted)
	// post-greedy phases.
	var runStart time.Time

	demo := len(os.Args) > 1 && os.Args[1] == "--demo"
	demoStarted := false

	var winW, winH int              // latest window size in dp, tracked from FrameEvent and saved on exit
	lastTitle := "FH6 Paint Studio" // window title, updated with run progress (shown in the taskbar tooltip / Alt-Tab)
	lastUpdateCheck := prefs.LastUpdateCheck

	// savePrefs persists the UI preferences (window size + sound) to studio.json — called on exit and
	// whenever a persisted toggle changes, so a preference survives even an unclean shutdown.
	savePrefs := func() {
		on := st.SoundOn.Value
		keep := st.KeepInside.Value
		srcRes := st.SourceRes.Value
		chk := st.AutoUpdate.Value
		c := studioConfig{SoundOnDone: &on, Preset: st.Mode.Value(), Budget: st.BudgetShapes(),
			KeepInside: &keep, SourceRes: &srcRes, CheckUpdates: &chk, LastUpdateCheck: lastUpdateCheck,
			LastSeenVersion: st.LastSeen, Locale: i18n.Current()}
		if winW >= 960 && winH >= 640 {
			c.WindowW, c.WindowH = winW, winH
		}
		saveConfig(c)
	}
	post := func(e runner.Event) { q.push(e); w.Invalidate() }

	upd := newUpdater()
	upd.startup(version, st.AutoUpdate.Value, lastUpdateCheck, w)

	// beginOpen starts an async decode of an image path (from the Open dialog or a file drop) off the
	// UI thread, so the window never freezes; opening guards re-entry. The result is applied via il.take.
	beginOpen := func(p string) {
		if p == "" || opening {
			return
		}
		opening = true
		st.Toast = "Loading…"
		go func(path string) {
			prep, img, rect, err := loadImage(path)
			il.put(loadedImage{prep: prep, img: img, path: path, rect: rect, err: err})
			w.Invalidate()
		}(p)
	}

	for {
		ev := w.Event()
		applyWindowIcon(ev) // windows-only: set the title-bar/taskbar icon on the first view event
		if winHWND == 0 {   // first Win32 view event: capture the HWND + enable taskbar progress
			if h := viewHWND(ev); h != 0 {
				winHWND = h
				tb = newTaskbar(h)
			}
		}
		switch e := ev.(type) {
		case app.DestroyEvent:
			if cancelRun != nil {
				cancelRun()
			}
			tb.clear()
			tb.close()
			savePrefs()
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			if e.Metric.PxPerDp > 0 { // remember the window size (dp) for next launch
				winW = int(float32(e.Size.X) / e.Metric.PxPerDp)
				winH = int(float32(e.Size.Y) / e.Metric.PxPerDp)
			}
			// A minimized window reports a zero frame size. Re-laying-out and presenting a zero-size
			// surface every tick while a run drives ~8 fps invalidations can stall the GPU and starve the
			// Win32 message pump on some drivers — the window then refuses to restore and Windows paints it
			// "Not Responding" (issue #29). Detect it here; the run still advances (engine events are
			// drained below), the UI just stops drawing until the window is visible again.
			minimized := e.Size.X == 0 || e.Size.Y == 0

			// --demo: auto-load the sample image and start a quick run on the first frame
			// (used to capture a live/finished real-window screenshot for verification).
			if demo && !demoStarted {
				demoStarted = true
				const demoPath = "testdata/super-image.jpg"
				if prep, img, rect, err := loadImage(demoPath); err == nil {
					curPrep = prep
					st.ContentColors, st.ContentCutout = contentDescriptor(curPrep)
					viewAbs = rect
					st.SetSource(img, demoPath)
					st.SetBudgetShapes(500)
					st.RandomEd.SetText("600") // fast demo via custom override (quality is auto now)
					st.MutatedEd.SetText("400")
					st.Phase = ui.PhaseRunning
					runCancelled = false
					runStart = time.Now()
					curGen = curPrep
					r := preset.Resolve(*curPrep, st.Choices())
					st.Stats = ui.RunStats{Total: r.Options.StopAt}
					cancelRun = runner.RunAsync(*curPrep, r, post)
				}
			}

			// Drain engine events into the UI state.
			for _, ev := range q.drain() {
				switch ev := ev.(type) {
				case runner.Log:
					st.AppendLog(ev.Line)
				case runner.Status:
					st.Stats.Stage = ev.Stage // post-greedy phase name (polish/standout/economy); drives the indeterminate bar
					if ev.Stage != "" {
						// The post-greedy phases emit no Progress, so the determinate fill would sit static
						// near 100%. Switch the taskbar button to the animated indeterminate marquee — a live
						// "still working" loader (like Explorer on an indeterminate op) through polish.
						tb.indeterminate()
					}
				case runner.Progress:
					applyProgress(st, ev)
					tb.set(uint64(ev.Shapes), uint64(ev.Total))
				case runner.Frame:
					img := ev.Img
					if runPadPx > 0 { // strip the transparent surround so the live preview is frame-free
						img = imageio.UnpadCanvas(img, runPadPx, runOrigW, runOrigH)
					}
					st.SetPreview(img)
				case runner.Done:
					shapes, canvas := ev.Result.Shapes, ev.Canvas
					if hybridInk > 0 && curGen != nil { // hybrid: lay the FDoG designed outline ON TOP of the fill
						if lines := hybrid.Ink(curGen, hybridInk, false); len(lines) > 0 {
							shapes = append(shapes, lines...)
							buf := imageio.RenderFH6(shapes, curGen.HasTransparency, curGen.W, curGen.H, 2)
							img := image.NewNRGBA(image.Rect(0, 0, curGen.W, curGen.H))
							for i, v := range buf {
								if v < 0 {
									v = 0
								} else if v > 1 {
									v = 1
								}
								img.Pix[i] = uint8(v*255 + 0.5)
							}
							canvas = img
							st.AppendLog(fmt.Sprintf("hybrid: +%d FDoG lines on top of the fill", len(lines)))
						}
						hybridInk = 0
					}
					// Perceptual quality of the finished render vs the source (still at the run dims, before
					// the keep-inside unpad) — shown as a friendly badge in the Activity panel.
					if curGen != nil && canvas != nil && canvas.Bounds().Dx() == curGen.W && canvas.Bounds().Dy() == curGen.H {
						src := imageio.EncodeForDisplay(curGen.Pixels)
						de, _ := metric.DeltaE76(src, nrgbaToFloat(canvas), curGen.W, curGen.H)
						ss := metric.SSIM(src, nrgbaToFloat(canvas), curGen.W, curGen.H)
						st.SetQuality(de, ss)
						st.AppendLog(fmt.Sprintf("quality: ΔE76 %.2f · SSIM %.3f", de, ss))
					}
					if curGen != nil { // crop run -> crop dims; whole-image run -> full dims
						lastW, lastH = curGen.W, curGen.H
					}
					if runPadPx > 0 { // map the transparent-surround run back to the original size
						shapes = imageio.TranslateShapes(shapes, -float64(runPadPx), -float64(runPadPx))
						canvas = imageio.UnpadCanvas(canvas, runPadPx, runOrigW, runOrigH)
						lastW, lastH = runOrigW, runOrigH
					}
					st.SetPreview(canvas)
					if n := len(shapes); n > 0 {
						st.Stats.Shapes = n - 1
					}
					st.Stats.Err = ev.Result.FinalError
					st.Stats.ETA = 0
					lastShapes = shapes
					cancelRun = nil
					st.Stats.Stage = ""
					tb.clear()
					if runCancelled {
						// The engine emits Done even on a cooperative cancel (it keeps the partial result).
						// Treat it honestly: show the partial preview, but DON'T announce success, label it
						// "optimal", save it to the library, or chime — none of that is true for a cancel.
						st.AppendLog(fmt.Sprintf("cancelled at %d shapes — partial result kept (not saved)", st.Stats.Shapes))
						st.Phase = ui.PhaseIdle
					} else {
						doneMsg := fmt.Sprintf("done: %d shapes, error %.1f in %s",
							st.Stats.Shapes, ev.Result.FinalError, fmtSecs(st.Stats.Elapsed))
						if st.Stats.Cap > 0 && st.Stats.Shapes < st.Stats.Cap*9/10 {
							doneMsg += fmt.Sprintf(" — auto-trimmed from %d (optimal for this image)", st.Stats.Cap)
						}
						st.AppendLog(doneMsg)
						st.Phase = ui.PhaseDone
						flashWindow(winHWND) // nudge the taskbar button if the user is in another window (e.g. FH6)
						if st.SoundOn.Value {
							playDoneSound()
						}
						savePrefs() // persist the preset + budget that just produced a result
						saveDecalToLibrary(st, store, lastShapes, lastW, lastH)
					}
				case runner.Failed:
					st.Phase = ui.PhaseError
					st.Stats.Stage = ""
					st.Toast = "Error: " + ev.Err.Error()
					st.AppendLog("ERROR: " + ev.Err.Error())
					cancelRun = nil
					tb.clear()
				}
			}

			// Apply a finished async image open (decoded off the UI thread).
			if res, ok := il.take(); ok {
				opening = false
				if res.err != nil {
					st.Toast = "Failed to open: " + res.err.Error()
				} else {
					curPrep = res.prep
					st.ContentColors, st.ContentCutout = contentDescriptor(curPrep)
					viewAbs = res.rect // base for crop selections; SetSource clears Cropped
					st.SetSource(res.img, res.path)
					st.Toast = ""
					st.Log = nil
				}
			}

			// Handle action buttons (their click areas come from the previous frame).
			if st.ElevateBtn.Clicked(gtx) {
				if err := inject.RelaunchElevated(); err != nil {
					st.Toast = "Could not elevate: " + err.Error()
				} else {
					return nil // the elevated instance has launched; exit this one
				}
			}
			// Open from the side-panel button OR the clickable empty-state preview card.
			if (st.OpenBtn.Clicked(gtx) || st.PreviewOpen.Clicked(gtx)) && !opening && !picking {
				// Run the native dialog on a worker goroutine so the event loop returns immediately (the
				// window stays live, no frame-handshake deadlock). owner=0 (UNOWNED): an owner HWND on
				// Gio's main thread would make the modal SendMessage to that thread and deadlock.
				picking = true
				go func() { openPick.put(pickFile(0)); w.Invalidate() }()
			}
			if p, ok := openPick.take(); ok {
				picking = false
				beginOpen(p) // "" (cancel) and the opening guard are handled inside beginOpen
			}
			// Crop toggle / clear (above Generate so a fresh crop is picked up the same frame).
			if st.CropBtn.Clicked(gtx) {
				st.EnterCropMode()
			}
			if st.CropCancelBtn.Clicked(gtx) {
				st.ExitCropMode()
			}
			if st.CropApplyBtn.Clicked(gtx) {
				if fx, fy, fw, fh, ok := st.CropSelection(); ok && st.ImgPath != "" {
					abs := imageio.SubAbs(viewAbs, fx, fy, fw, fh)
					if prep, img, err := loadCropRegion(st.ImgPath, abs); err == nil {
						curPrep = prep
						st.ContentColors, st.ContentCutout = contentDescriptor(curPrep)
						st.SetSource(img, st.ImgPath) // clears crop mode + Cropped
						viewAbs = abs
						st.Cropped = true
						st.AppendLog(fmt.Sprintf("cropped to %dx%d (source rect %d,%d %dx%d)", prep.W, prep.H, abs.Min.X, abs.Min.Y, abs.Dx(), abs.Dy()))
					} else {
						st.Toast = "Crop failed: " + err.Error()
					}
				}
				st.ExitCropMode()
			}
			if st.ResetBtn.Clicked(gtx) && st.ImgPath != "" {
				if prep, img, rect, err := loadImage(st.ImgPath); err == nil {
					curPrep = prep
					st.ContentColors, st.ContentCutout = contentDescriptor(curPrep)
					viewAbs = rect
					st.SetSource(img, st.ImgPath) // clears Cropped
					st.AppendLog("reset to the original image")
				} else {
					st.Toast = "Reset failed: " + err.Error()
				}
			}
			// Shape editor: enter from a finished generation (Edit) or a blank canvas (New); Apply
			// commits the edited shapes back as the working geometry + preview, Cancel discards.
			if st.EditBtn.Clicked(gtx) && len(lastShapes) > 0 {
				st.EnterEditor(lastShapes, lastW, lastH) // loads the generated design into the editor
			}
			if st.NewBlankBtn.Clicked(gtx) {
				st.EnterEditor(nil, 1024, 1024)
			}
			if st.EditorTab.Clicked(gtx) {
				if st.EditorMode {
					st.View = ui.ViewEditor // continue the current session
				} else {
					st.EnterEditor(nil, 1024, 1024)
				}
			}
			if st.EditSaveBtn.Clicked(gtx) { // persist the edited design to the library (injectable)
				name := st.SaveDesignName()
				exists := false
				if store != nil {
					if entries, err := store.List(); err == nil {
						for _, e := range entries {
							if e.Name == name {
								exists = true
								break
							}
						}
					}
				}
				if exists {
					st.RequestOverride(name) // ask before overwriting a same-named design
				} else {
					saveEditedDesign(st, store, name, false)
				}
			}
			if st.EditOverrideBtn.Clicked(gtx) {
				saveEditedDesign(st, store, st.PendingSaveName(), true)
			}
			if st.EditSaveCancelBtn.Clicked(gtx) {
				st.CancelOverride()
			}
			if st.GenBtn.Clicked(gtx) && curPrep != nil && st.Phase != ui.PhaseRunning && !opening {
				st.Toast = ""
				st.Log = nil
				// The working source IS the (optionally cropped) image, so generate on curPrep directly.
				genPrep := curPrep
				ch := st.Choices()
				// Hi-res fit (flat/anime): re-decode the ENGINE input at up to genMaxRes — thin strokes
				// at the display resolution degrade to ~1px of gray AA the search can neither detect nor
				// cover. The display (and crop UI) stays at studioMaxRes.
				if hi := hiResPrep(st, ch.Mode, viewAbs, genPrep.W, genPrep.H); hi != nil {
					genPrep = hi
					st.AppendLog(fmt.Sprintf("hi-res fit: engine input %dx%d (display stays at %dpx)", hi.W, hi.H, studioMaxRes))
				}
				runPadPx, runOrigW, runOrigH = 0, genPrep.W, genPrep.H
				// Keep shapes inside image: always wrap the target in a transparent surround so the spill
				// penalty bounds every shape on all four edges, then map the geometry/canvas back to the
				// original size on Done. Always-pad (not gated on a transparency fraction) because content
				// can touch an edge even when the middle is transparent, and a cutout silhouette touches its
				// bbox after auto-crop. Quality-neutral: the empty margin draws no shapes.
				if st.KeepInside.Value {
					padded, padPx := imageio.PadTransparent(genPrep, framePadFrac)
					genPrep, runPadPx = padded, padPx
					st.AppendLog(fmt.Sprintf("keep-inside: transparent surround %dpx (%dx%d), bounding shapes on all edges", padPx, genPrep.W, genPrep.H))
				}
				curGen = genPrep
				st.Phase = ui.PhaseRunning
				runCancelled = false
				runStart = time.Now()
				tb.indeterminate() // instant taskbar feedback until the first progress tick
				r := preset.Resolve(*genPrep, ch)
				hybridInk = 0
				if preset.IsHybridMode(ch.Mode) { // reserve part of the budget for the FDoG ink (appended in Done)
					hybridInk = preset.InkBudget(ch.InkRatio, ch.Shapes)
					if hybridInk > 0 {
						r.Options.StopAt = ch.Shapes - hybridInk
						if r.Options.StopAt < 1 {
							r.Options.StopAt = 1
						}
					}
					st.AppendLog(fmt.Sprintf("%s: %d ink lines + %d fill (%.0f%% lines)", ch.Mode, hybridInk, r.Options.StopAt, ch.InkRatio*100))
				}
				st.ClearQuality() // drop the previous run's quality badge
				st.Stats = ui.RunStats{Total: r.Options.StopAt}
				st.Stats.Cap = ch.Shapes // remember the requested cap so Done can show the auto-picked optimal count
				cancelRun = runner.RunAsync(*genPrep, r, post)
			}
			if st.CancelBtn.Clicked(gtx) && cancelRun != nil {
				cancelRun()
				runCancelled = true
				tb.clear()
				st.AppendLog("cancelling…")
			}
			// Top-level tab switching.
			if st.StudioTab.Clicked(gtx) {
				st.View = ui.ViewStudio
			}
			if st.LibraryTab.Clicked(gtx) {
				st.View = ui.ViewLibrary
				reloadLibrary(st, store) // pick up anything saved since
				prefillInjectLayers(st)  // seed the FH6-template field so it isn't a blank footgun
			}
			if st.OpenFolderBtn.Clicked(gtx) && store != nil {
				openInExplorer(store.Root)
			}

			if st.AboutBtn.Clicked(gtx) {
				if seen := st.OpenAbout(); seen != "" {
					savePrefs() // persist LastSeen
				}
			}
			if st.AboutClose.Clicked(gtx) {
				st.CloseAbout()
			}
			if st.DownloadBtn.Clicked(gtx) && st.Update != nil {
				openURL(st.Update.URL)
			}
			if st.GitHubBtn.Clicked(gtx) {
				openURL(githubURL)
			}
			if st.NexusBtn.Clicked(gtx) {
				openURL(nexusURL)
			}
			if st.CheckNowBtn.Clicked(gtx) {
				st.UpdateStatus = "Checking…"
				st.Update = nil
				upd.kick(w)
			}
			if st.AutoUpdate.Update(gtx) {
				savePrefs()
			}

			// Custom-preset actions (save the current settings, rename/delete the selected preset).
			if st.SavePresetBtn.Clicked(gtx) && presetStore != nil {
				name := strings.TrimSpace(st.PresetNameEd.Text())
				switch {
				case name == "":
					st.Toast = "Enter a preset name first"
				case ui.IsBuiltinMode(name):
					st.Toast = "That name is reserved for a built-in preset"
				default:
					p := userpreset.Preset{Name: name, Created: time.Now(),
						KeepInside: st.KeepInside.Value, Choices: st.Choices()}
					if _, err := presetStore.Save(p); err != nil {
						st.Toast = "Save failed: " + err.Error()
					} else {
						reloadPresets(st, presetStore)
						st.SelectPreset(name)
						st.PresetNameEd.SetText("")
						st.Toast = "Preset saved"
					}
				}
			}
			if st.DeletePresetBtn.Clicked(gtx) && presetStore != nil {
				if sel := st.SelectedPreset(); sel != nil {
					if err := presetStore.Delete(sel.ID); err != nil {
						st.Toast = "Delete failed: " + err.Error()
					} else {
						reloadPresets(st, presetStore)
						st.SelectPreset("anime")
						st.Toast = "Preset deleted"
					}
				}
			}

			// commitRename writes the edited name to disk and reloads (which rebuilds the rows and
			// exits edit mode). A blank or unchanged name just closes the editor with no write.
			// needReload defers reloadLibrary until AFTER the row loop: reloadLibrary REPLACES
			// st.LibRows (a shorter slice after Delete), which would corrupt this for-range and panic.
			needReload := false
			commitRename := func(i int) {
				r := &st.LibRows[i]
				name := strings.TrimSpace(r.NameEd.Text())
				if name == "" || name == r.Entry.Name || store == nil {
					r.Editing = false
					return
				}
				if _, err := store.Rename(r.Entry.ID, name); err != nil {
					st.Toast = "Rename failed: " + err.Error()
					r.Editing = false
					return
				}
				st.Toast = "Renamed"
				needReload = true
			}

			// Library row actions: rename (inline) / Inject / Export JSON / Delete.
			for i := range st.LibRows {
				r := &st.LibRows[i]
				if r.Editing { // Enter in the name field commits the rename
					committed := false
					for {
						ev, ok := r.NameEd.Update(gtx)
						if !ok {
							break
						}
						if _, ok := ev.(widget.SubmitEvent); ok {
							commitRename(i)
							committed = true
							break
						}
					}
					if committed {
						continue
					}
				}
				if r.Rename.Clicked(gtx) {
					if r.Editing {
						commitRename(i)
						continue
					}
					st.ArmRename(i)
					gtx.Execute(key.FocusCmd{Tag: &r.NameEd})
					continue
				}
				if r.RenameCancel.Clicked(gtx) {
					r.Editing = false
					continue
				}
				if r.Inject.Clicked(gtx) && store != nil && !st.InjectBusy() {
					layers, scale := injectParams(st)
					switch {
					case layers <= 0 && r.Entry.Shapes > 0:
						// Auto-fill the FH6-layers field from this generation's shape count — a safe default
						// that fits all its shapes. The user confirms with a second click (or edits it to
						// match their actual in-game template; the field MUST equal the live template count,
						// or the locator won't find the group).
						st.InjectLayers.SetText(strconv.Itoa(r.Entry.Shapes))
						st.InjectLayersErr = false
						st.Toast = fmt.Sprintf("FH6 layers set to %d (this generation's shapes) — adjust to your in-game template if different, then Inject", r.Entry.Shapes)
					case layers <= 0:
						st.InjectLayersErr = true
						st.Toast = "Enter the FH6 template layer count (top-right) before injecting"
					default:
						if g, err := store.LoadGeometry(r.Entry.ID); err == nil {
							id := r.Entry.ID
							entryW, entryH := r.Entry.Width, r.Entry.Height
							shapes := g.Shapes
							st.BeginInject(id) // spinner on this row's button + block the others
							if layers < r.Entry.Shapes {
								// LOUD warning: the injector caps writes at the template size, so the overflow
								// shapes are silently dropped in-game. Surface it so the user can raise the template.
								drop := r.Entry.Shapes - layers
								st.Toast = fmt.Sprintf("Injecting — WARNING: %d of %d shapes will be DROPPED (template only %d). Raise the FH6 template.", drop, r.Entry.Shapes, layers)
								st.AppendLog(fmt.Sprintf("WARNING: FH6 layers %d < %d shapes — %d shapes will be DROPPED in-game; use a larger template", layers, r.Entry.Shapes, drop))
							} else {
								st.Toast = "Injecting into FH6…"
							}
							st.AppendLog(fmt.Sprintf("injecting %q into FH6 (%d layers, scale %.2f)…", r.Entry.Name, layers, scale))
							go func() {
								err := runInject(post, shapes, entryW, entryH, layers, scale)
								injDone.put(injectOutcome{id: id, err: err})
								w.Invalidate()
							}()
						} else {
							st.Toast = "Load failed: " + err.Error()
						}
					}
				}
				if r.Edit.Clicked(gtx) && store != nil {
					if g, err := store.LoadGeometry(r.Entry.ID); err == nil {
						ew, eh := r.Entry.Width, r.Entry.Height
						if ew <= 0 || eh <= 0 {
							ew, eh = 1024, 1024
						}
						st.EnterEditor(g.Shapes, ew, eh)
						st.EditName.SetText(r.Entry.Name)
					} else {
						st.Toast = "Load failed: " + err.Error()
					}
					continue
				}
				if r.Export.Clicked(gtx) && store != nil && !saving {
					saving = true
					pendingExportID = r.Entry.ID
					suggested := r.Entry.Name + ".forza.json"
					go func() { savePick.put(pickSaveFile(0, suggested)); w.Invalidate() }()
				}
				if r.Delete.Clicked(gtx) && store != nil {
					if r.ConfirmDelete {
						if err := store.Delete(r.Entry.ID); err != nil {
							st.Toast = "Delete failed: " + err.Error()
						} else {
							st.Toast = "Deleted"
							needReload = true
						}
					} else {
						st.ArmDelete(i)
					}
				}
				if r.ThumbBtn.Clicked(gtx) && store != nil { // open the full preview in the lightbox
					if st.LightboxOn {
						// A click while the preview is open dismisses it. Gio only re-resolves the pointer
						// hit-target on a MOVE, so a click that doesn't move the cursor lands back on the
						// thumb that opened it (not the scrim) — handle that here so it closes on first click.
						st.HideLightbox()
					} else if img, err := loadPreviewImage(store, r.Entry.ID); err == nil {
						st.ShowLightbox(img)
					} else {
						st.Toast = "Preview unavailable: " + err.Error()
					}
				}
			}
			// (the lightbox now dismisses itself via its own pointer capture in lightboxOverlay)
			if needReload {
				reloadLibrary(st, store)
			}
			if p, ok := savePick.take(); ok {
				saving = false
				if p != "" && pendingExportID != "" {
					st.Toast = exportLibraryEntry(store, pendingExportID, p)
				}
				pendingExportID = ""
			}
			// Inject worker finished: flip the row to tick/cross (lingering ~6s) + toast. Success surfaces the
			// save+reload reminder (FH6 re-derives meshes only on vinyl save+reload); failure shows why.
			if o, ok := injDone.take(); ok {
				st.FinishInject(o.id, o.err == nil, gtx.Now.Add(6*time.Second))
				if o.err != nil {
					st.Toast = "Inject failed: " + o.err.Error()
					st.AppendLogLvl(ui.LogErr, "inject failed: "+o.err.Error())
					st.ConsoleOpen = true // surface the failure — the Library view has no Activity card
				} else {
					st.Toast = "Injected — now Save & reload the vinyl in FH6 to apply"
					st.AppendLogLvl(ui.LogGood, "injected OK — Save & reload the vinyl in FH6 to apply")
				}
			}

			if t, ok := upd.drain(st, version); ok {
				lastUpdateCheck = t
				savePrefs()
			}

			// While a run is active, keep the elapsed clock live and the indeterminate stage bar
			// animating even through the post-greedy phases (which emit no Progress events): refresh
			// elapsed from the run start and schedule the next frame ~8 fps.
			if st.Phase == ui.PhaseRunning {
				if !runStart.IsZero() {
					st.Stats.Elapsed = time.Since(runStart)
				}
				if !minimized { // don't self-wake to redraw a hidden window; engine events still wake us
					gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(120 * time.Millisecond)})
				}
			}
			// Keep the frame ticking while an inject spinner is up; revert a lingering tick/cross pill on time.
			if st.InjectBusy() {
				gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(80 * time.Millisecond)})
			}
			st.MaybeClearInjectResult(gtx.Now)
			if st.InjectResultID != "" {
				gtx.Execute(op.InvalidateCmd{At: st.InjectResultUntil})
			}

			if st.SoundOn.Update(gtx) { // persist the "sound on finish" toggle the moment it changes
				savePrefs()
			}
			if st.SourceRes.Update(gtx) { // persist the "use source resolution" toggle the moment it changes
				savePrefs()
			}
			if st.InjectLayersErr { // clear the red FH6-layers highlight once a valid count is entered
				if l, _ := injectParams(st); l > 0 {
					st.InjectLayersErr = false
				}
			}
			if title := runTitle(st); title != lastTitle { // reflect run progress in the window title
				w.Option(app.Title(title))
				lastTitle = title
			}
			if minimized { // nothing visible to draw — acknowledge the frame cheaply (see the zero-size note)
				e.Frame(gtx.Ops)
				break
			}
			st.Layout(gtx)
			if st.Backend != nil && st.Backend.Changed() { // engine picker -> bias the next run's backend
				runner.BackendPreference = st.Backend.Value()
				st.BackendLabel = "shape engine · " + st.Backend.Value()
			}
			if st.Lang.Changed() { // user picked a language -> switch live and persist
				if tag := i18n.TagForEndonym(st.Lang.Value()); tag != "" {
					i18n.SetLocale(tag)
					savePrefs()
				}
			}
			e.Frame(gtx.Ops)
		}
	}
}

// runTitle is the window title for the current run state — the greedy percentage, the post-greedy phase
// name, or the plain app name when idle. Shown in the taskbar tooltip / Alt-Tab switcher.
func runTitle(st *ui.AppState) string {
	const base = "FH6 Paint Studio"
	if st.Phase != ui.PhaseRunning {
		return base
	}
	if st.Stats.Stage != "" {
		return base + " — " + st.Stats.Stage + "…"
	}
	if st.Stats.Total > 0 {
		pct := st.Stats.Shapes * 100 / st.Stats.Total
		if pct > 100 {
			pct = 100
		}
		return fmt.Sprintf("%s — %d%%", base, pct)
	}
	return base + " — working…"
}

func applyProgress(st *ui.AppState, ev runner.Progress) {
	st.Stats.Shapes = ev.Shapes
	st.Stats.Total = ev.Total
	st.Stats.Err = ev.Err
	st.Stats.Elapsed = ev.Elapsed
	if st.Stats.Err0 == 0 && ev.Err > 0 {
		st.Stats.Err0 = ev.Err
	}
	st.Stats.History = append(st.Stats.History, ev.Err)
	if len(st.Stats.History) > 400 {
		st.Stats.History = st.Stats.History[len(st.Stats.History)-400:]
	}
	// Recent-rate ETA (front-loaded cost makes the old linear elapsed/frac wildly overestimate early).
	st.Stats.UpdateETA(ev.Shapes, ev.Total, ev.Elapsed)
}

// loadPreviewImage decodes a stored generation's full preview.png (for the library lightbox). The PNG
// decoder is already registered (reloadLibrary decodes thumbs), so a plain image.Decode suffices.
func loadPreviewImage(store *library.Store, id string) (image.Image, error) {
	f, err := os.Open(store.PreviewPath(id))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// nrgbaToFloat converts an sRGB NRGBA image to the packed []float32 RGBA (0..1) the metric package
// expects, honouring the row stride.
func nrgbaToFloat(img *image.NRGBA) []float32 {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	out := make([]float32, w*h*4)
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for x := 0; x < w*4; x++ {
			out[y*w*4+x] = float32(row[x]) / 255
		}
	}
	return out
}

// prefillInjectLayers seeds the FH6-template count (when blank) from the largest generation, capped to
// the per-group ceiling — a safe default that fits every entry, which the per-row fit badge then
// confirms. The user lowers it to match their real in-game template; the badges update to show drops.
func prefillInjectLayers(st *ui.AppState) {
	if strings.TrimSpace(st.InjectLayers.Text()) != "" {
		return
	}
	max := 0
	for i := range st.LibRows {
		if n := st.LibRows[i].Entry.Shapes; n > max {
			max = n
		}
	}
	if max <= 0 {
		return
	}
	if max > preset.MaxShapes {
		max = preset.MaxShapes
	}
	st.InjectLayers.SetText(strconv.Itoa(max))
}

// reloadLibrary refreshes the UI library rows from the store (newest first, thumbs decoded on the UI
// goroutine — cheap for <=128px PNGs).
func reloadLibrary(st *ui.AppState, store *library.Store) {
	if store == nil {
		return
	}
	entries, err := store.List()
	if err != nil {
		st.AppendLog("library list: " + err.Error())
		return
	}
	rows := make([]ui.LibraryRow, 0, len(entries))
	for _, e := range entries {
		row := ui.LibraryRow{Entry: e}
		if f, err := os.Open(store.ThumbPath(e.ID)); err == nil {
			if img, _, err := image.Decode(f); err == nil {
				row.Thumb = paint.NewImageOp(img)
			}
			f.Close()
		}
		rows = append(rows, row)
	}
	st.SetLibrary(rows)
}

// reloadPresets refreshes the studio's custom-preset list (and the Preset dropdown) from the store.
func reloadPresets(st *ui.AppState, store *userpreset.Store) {
	if store == nil {
		return
	}
	ps, err := store.List()
	if err != nil {
		st.AppendLog("presets list: " + err.Error())
		return
	}
	st.SetPresets(ps)
}

// libMeta builds the library Entry metadata from the current studio state + render dims.
func libMeta(st *ui.AppState, w, h int) library.Entry {
	return library.Entry{
		Name:        strings.TrimSuffix(filepath.Base(st.ImgPath), filepath.Ext(st.ImgPath)),
		Source:      st.ImgPath,
		Preset:      st.Mode.Value(),
		Width:       w,
		Height:      h,
		Budget:      st.BudgetShapes(),
		Seed:        parseSeed(st),
		InjectScale: injectScaleOf(st),
		Created:     time.Now(),
	}
}

// saveDecalToLibrary auto-saves a finished whole-image generation.
func saveDecalToLibrary(st *ui.AppState, store *library.Store, shapes []model.Shape, w, h int) {
	if store == nil || len(shapes) == 0 {
		return
	}
	if _, err := store.Save(shapes, st.Preview, libMeta(st, w, h)); err != nil {
		st.AppendLog("library save: " + err.Error())
		return
	}
	st.Toast = "Saved to Library"
	reloadLibrary(st, store)
}

// saveEditedDesign saves an edited design to the library under name, optionally overwriting same-named
// entries first (manual designs allow override; auto-generations never do). Shows in-editor feedback.
func saveEditedDesign(st *ui.AppState, store *library.Store, name string, override bool) {
	if store == nil || len(st.EditShapes) == 0 {
		return
	}
	st.SetPreview(imageio.RenderFH6Image(st.EditShapes, true, st.EditW, st.EditH, 1))
	if override {
		if entries, err := store.List(); err == nil {
			for _, e := range entries {
				if e.Name == name {
					_ = store.Delete(e.ID)
				}
			}
		}
	}
	meta := libMeta(st, st.EditW, st.EditH)
	meta.Name = name
	if _, err := store.Save(st.EditShapes, st.Preview, meta); err != nil {
		st.AppendLog("library save: " + err.Error())
		st.Toast = "Save failed: " + err.Error()
		st.CancelOverride()
		return
	}
	reloadLibrary(st, store)
	st.SetSavedFeedback(name)
}

// exportLibraryEntry copies a stored generation's geometry to dst (+ preview.png beside it).
func exportLibraryEntry(store *library.Store, id, dst string) string {
	if store == nil {
		return "No library"
	}
	g, err := store.LoadGeometry(id)
	if err != nil {
		return "Export failed: " + err.Error()
	}
	if err := imageio.WriteGeometry(dst, g); err != nil {
		return "Export failed: " + err.Error()
	}
	if src, err := os.Open(store.PreviewPath(id)); err == nil {
		defer src.Close()
		if out, err := os.Create(strings.TrimSuffix(dst, filepath.Ext(dst)) + ".png"); err == nil {
			_, _ = io.Copy(out, src)
			out.Close()
		}
	}
	return "Exported " + filepath.Base(dst)
}

// injectParams reads the library header's FH6-layers + scale editors.
func injectParams(st *ui.AppState) (layers int, scale float64) {
	if n, err := strconv.Atoi(strings.TrimSpace(st.InjectLayers.Text())); err == nil {
		layers = n
	}
	scale = 1.0
	if v, err := strconv.ParseFloat(strings.TrimSpace(st.InjectScale.Text()), 64); err == nil && v > 0 {
		scale = v
	}
	return layers, scale
}

func parseSeed(st *ui.AppState) int64 {
	if v, err := strconv.ParseInt(strings.TrimSpace(st.Seed.Text()), 10, 64); err == nil {
		return v
	}
	return 0
}

func injectScaleOf(st *ui.AppState) float64 {
	_, scale := injectParams(st)
	return scale
}

// fh6Locate is a read-only diagnostic: `--fh6-locate N` finds FH6 and validates the live layer
// table for an N-layer template, WITHOUT writing — confirm the locator before importing.
func fh6Locate() {
	n := 0
	if len(os.Args) > 2 {
		n, _ = strconv.Atoi(os.Args[2])
	}
	inj := inject.NewFH6()
	inj.Layers = n
	res, err := inj.Locate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fh6-locate:", err)
		os.Exit(1)
	}
	fmt.Println("OK", res)
}

// runInject performs a live FH6 injection on a worker goroutine, streaming log lines to the UI and
// returning the outcome. The geometry JSON is already saved by the caller; injection can fail (game
// not running, no table, wrong layer count) and the error is both logged and returned for the UI.
func runInject(post func(runner.Event), shapes []model.Shape, w, h, layers int, scale float64) error {
	inj := inject.NewFH6()
	inj.Layers = layers
	inj.Log = func(line string) { post(runner.Log{Line: line}) }
	if scale <= 0 {
		scale = 1.0
	}
	cm := inject.NewCanvasMap(w, h, float32(scale), inject.ScaleBase)
	inj.Canvas = &cm
	if err := inj.Inject(shapes, w, h); err != nil {
		post(runner.Log{Line: "inject: " + err.Error()})
		return err
	}
	post(runner.Log{Line: "inject: OK — Save & reload the vinyl in FH6 to apply"})
	return nil
}

func fmtSecs(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// loadImage decodes a file into the engine's Prepared form plus a straight-alpha NRGBA for display.
// studioMaxRes is the render resolution the Studio loads images (and crops) at — content fills the
// render up to this long side. Crops reuse it so a region's detail occupies the full budget.
const studioMaxRes = 1100

// framePadFrac is the transparent surround added by the "Keep shapes inside image" toggle: a border of
// this fraction of max(W,H) on every side. The empty (alpha-0) margin makes the spill penalty bound
// every shape to the content rectangle; the result is mapped back to the original size on completion.
const framePadFrac = 0.10

func loadImage(path string) (*imageio.Prepared, *image.NRGBA, image.Rectangle, error) {
	// Auto-crop uniform/empty margins to the content bbox before downscale (IDENTICAL to the CLI's
	// -autocrop default): content fills the render -> more detail+shapes per feature. Guarded: a
	// no-op on full-bleed images, so it never crops what has no border. rect is the content rect in the
	// raw source's coordinates (the base for crop-selection composition).
	prep, rect, err := imageio.LoadAutoCropped(path, studioMaxRes)
	if err != nil {
		return nil, nil, image.Rectangle{}, err
	}
	return prep, nrgbaFromPrep(prep), rect, nil
}

// loadCropRegion synchronously re-decodes the original at an absolute source rect (full resolution),
// returning the prepared crop + its display image. Used by Apply crop / Reset (a bounded ~100-300ms
// decode triggered by a deliberate click, so it runs inline rather than via the open-worker).
func loadCropRegion(path string, abs image.Rectangle) (*imageio.Prepared, *image.NRGBA, error) {
	prep, err := imageio.LoadAbsRegion(path, studioMaxRes, abs)
	if err != nil {
		return nil, nil, err
	}
	return prep, nrgbaFromPrep(prep), nil
}

// genMaxRes is the engine-side fit resolution for the modes that benefit from it. Thin strokes at
// studioMaxRes degrade to ~1px of gray AA the search can neither detect nor cover; fitting the same
// budget at a higher long side recovers them. 2026-07-20 native-resolution ladder (out/resbench,
// img.png 3541px, cross-res scored at native): cap 2000 → 2800 → 4096 gave unweighted SSE
// 54088 → 38896 → 29034, a clean −46% at native with NO low-view tradeoff; wall 155 → 216 → 259s
// (1.67× on the largest source). Sources at/below the old 2000 cap are BYTE-IDENTICAL across caps
// (they already fit native), so raising the ceiling only slows genuinely-large sources — exactly
// where the gain is largest. So anime/flat now fit at NATIVE by default (owner decision 2026-07-20:
// "native resolution default"), capped by srcResCap. Photo measured a wash and stays at studioMaxRes.
const genMaxRes = srcResCap

// srcResCap bounds the "Use source resolution" toggle: measured on a 3541px line-art source the
// gain keeps growing all the way to native (−40% vs the 2000 cap, no low-view tradeoff, ~2× wall),
// so the toggle fits at the TRUE source size — this ceiling only protects time/VRAM from
// pathological scans.
const srcResCap = 4096

// hiResPrep re-decodes the current view for the ENGINE above the display cap when it pays: at
// genMaxRes for the modes measured to benefit (flat/anime), or at the source's own resolution for
// ANY mode when the user asks for maximum quality (the "Use source resolution" toggle). Returns nil
// to fit on the display-resolution prep: no benefit for the mode, sources at/below studioMaxRes, no
// source path (demo), or a failed re-decode. The re-derivation mirrors the state exactly: un-cropped
// views go through the same auto-crop (+checker-strip) pipeline as loadImage; crops re-use the crop
// tool's absolute-rect primitive, so the engine sees the same content rectangle at a higher
// resolution.
func hiResPrep(st *ui.AppState, mode string, viewAbs image.Rectangle, curW, curH int) *imageio.Prepared {
	capPx := srcResCap
	if preset.PresetMode(mode) == "pixel" {
		capPx = srcResCap // pixel-art ALWAYS fits at native: a working-res downscale destroys the grid
	} else if !st.SourceRes.Value {
		switch preset.PresetMode(mode) {
		case "flat", "anime":
			capPx = genMaxRes
		default:
			return nil
		}
	}
	if st.ImgPath == "" || (curW < studioMaxRes && curH < studioMaxRes) {
		return nil // demo source, or the display load never hit the cap — nothing extra to gain
	}
	var (
		prep *imageio.Prepared
		err  error
	)
	if st.Cropped {
		prep, err = imageio.LoadAbsRegion(st.ImgPath, capPx, viewAbs)
	} else {
		prep, _, err = imageio.LoadAutoCropped(st.ImgPath, capPx)
	}
	if err != nil {
		st.AppendLog("hi-res fit unavailable (" + err.Error() + "); fitting at display resolution")
		return nil
	}
	if prep.W <= curW && prep.H <= curH {
		return nil // source had no extra pixels beyond the display load
	}
	return prep
}

func nrgbaFromPrep(prep *imageio.Prepared) *image.NRGBA {
	// prep.Pixels are LINEAR in -linear mode; sRGB-encode for display so the source thumbnail/main
	// view shows true colours (linear-as-bytes looks dark/shifted, e.g. yellow->orange). EncodeForDisplay
	// is a no-op in sRGB mode and returns a fresh slice in linear mode, so prep.Pixels is never mutated.
	px := imageio.EncodeForDisplay(prep.Pixels)
	img := image.NewNRGBA(image.Rect(0, 0, prep.W, prep.H))
	for i := 0; i < prep.W*prep.H; i++ {
		img.Pix[i*4+0] = u8(px[i*4+0])
		img.Pix[i*4+1] = u8(px[i*4+1])
		img.Pix[i*4+2] = u8(px[i*4+2])
		img.Pix[i*4+3] = u8(px[i*4+3])
	}
	return img
}

// contentDescriptor reports the palette colour count and cutout flag of a prepared image, used to
// fill the expert knobs with the selected mode's concrete defaults. The cutout flag mirrors how
// Resolve derives `transparent` (a genuine cutout, NOT a keep-inside pad margin), so the knob
// values the panel shows match what the engine actually runs.
func contentDescriptor(p *imageio.Prepared) (colors int, cutout bool) {
	if p == nil {
		return 0, false
	}
	return metric.ContentClass(p.Pixels, p.W, p.H).Colors, p.HasTransparency && !p.PaddedOpaque
}

func u8(f float32) uint8 {
	if f <= 0 {
		return 0
	}
	if f >= 1 {
		return 255
	}
	return uint8(f*255 + 0.5)
}

// eventQueue is a thread-safe hand-off from the worker goroutine to the UI loop.
type eventQueue struct {
	mu    sync.Mutex
	items []runner.Event
}

func newEventQueue() *eventQueue { return &eventQueue{} }

// pathPick hands a chosen path from a native-dialog worker goroutine back to the UI loop. The dialog
// MUST run off the event goroutine: a modal common dialog pumps its own message loop, and running it
// inline would block the event goroutine while Gio's main thread waits on the frame handshake — a
// frozen window. The worker stashes the result (or "" on cancel) and the loop applies it.
type pathPick struct {
	mu    sync.Mutex
	ready bool
	path  string
}

func (p *pathPick) put(s string) {
	p.mu.Lock()
	p.path, p.ready = s, true
	p.mu.Unlock()
}

func (p *pathPick) take() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ready {
		return "", false
	}
	p.ready = false
	return p.path, true
}

// injectOutcome is the result of a live FH6 injection, handed from the inject worker goroutine back to
// the UI loop (keyed by the library entry ID so the right row updates).
type injectOutcome struct {
	id  string
	err error
}

// injectHolder is the thread-safe one-slot hand-off for an inject outcome (mirrors pathPick/imageLoad).
type injectHolder struct {
	mu    sync.Mutex
	ready bool
	res   injectOutcome
}

func (h *injectHolder) put(o injectOutcome) {
	h.mu.Lock()
	h.res, h.ready = o, true
	h.mu.Unlock()
}

func (h *injectHolder) take() (injectOutcome, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.ready {
		return injectOutcome{}, false
	}
	h.ready = false
	return h.res, true
}

// loadedImage is the result of decoding a source image on a worker goroutine. rect is the auto-crop
// content rectangle in the RAW source's coordinates — the absolute region the displayed image covers,
// used as the base for composing crop selections.
type loadedImage struct {
	prep *imageio.Prepared
	img  *image.NRGBA
	path string
	rect image.Rectangle
	err  error
}

// imageLoad hands a decoded source image from the open-worker goroutine to the UI loop, so the
// (potentially slow, ~100-300ms for a 4K photo) decode + downscale never blocks the event loop and
// the window keeps repainting.
type imageLoad struct {
	mu    sync.Mutex
	ready bool
	res   loadedImage
}

func (l *imageLoad) put(r loadedImage) {
	l.mu.Lock()
	l.res, l.ready = r, true
	l.mu.Unlock()
}

func (l *imageLoad) take() (loadedImage, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.ready {
		return loadedImage{}, false
	}
	l.ready = false
	return l.res, true
}

func (q *eventQueue) push(e runner.Event) {
	q.mu.Lock()
	q.items = append(q.items, e)
	q.mu.Unlock()
}

func (q *eventQueue) drain() []runner.Event {
	q.mu.Lock()
	it := q.items
	q.items = nil
	q.mu.Unlock()
	return it
}
