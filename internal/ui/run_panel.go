package ui

import (
	"fmt"
	"image"
	"math"
	"strings"
	"time"

	"gioui.org/layout"

	"fh6-paint-studio/internal/preset"
)

// spacerW is a flex spacer that occupies its full allocated width (so trailing rigids hug the right
// edge) while contributing no height.
func spacerW(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} }

// runPanel is the right column: the Activity card (state-driven progress with a phase stepper). The
// log lives in the shared bottom console, so this column is a single progress panel.
func (s *AppState) runPanel(gtx C) D {
	th := s.Th
	gtx.Constraints.Min = gtx.Constraints.Max // fill the column height
	return th.Card(gtx, s.activityCard)
}

func (s *AppState) activityCard(gtx C) D {
	th := s.Th
	gtx.Constraints.Min.Y = gtx.Constraints.Max.Y // fill the column height (content sits at the top)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Title(gtx, "Activity") }),
		layout.Rigid(GapV(16).Layout),
		layout.Rigid(s.activityBody),
		layout.Flexed(1, func(gtx C) D { return D{} }),
	)
}

// activityBody picks the distinct idle / running / success / error state, each styled differently.
func (s *AppState) activityBody(gtx C) D {
	switch s.Phase {
	case PhaseError:
		return s.activityError(gtx)
	case PhaseDone:
		return s.activityDone(gtx)
	case PhaseRunning:
		return s.activityRunning(gtx)
	default:
		return s.activityIdle(gtx)
	}
}

func (s *AppState) activityIdle(gtx C) D {
	th := s.Th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 15, "Ready", th.Text) }),
		layout.Rigid(GapV(6).Layout),
		layout.Rigid(func(gtx C) D {
			hint := "Pick a style on the left and hit Generate."
			if s.Source == nil {
				hint = "Open an image, pick a style, then Generate."
			}
			return th.Dim(gtx, hint)
		}),
		layout.Rigid(GapV(18).Layout),
		layout.Rigid(func(gtx C) D { return s.phaseStepper(gtx, s.phases(), -1, false) }),
	)
}

func (s *AppState) activityRunning(gtx C) D {
	th := s.Th
	st := s.Stats
	staging := st.Stage != "" // post-greedy phase (no shape counter) -> indeterminate sweep
	gaussian := s.Mode.Value() == "gaussian"
	pct := int(s.progressFrac()*100 + 0.5)

	head := "Building shapes"
	switch {
	case staging:
		head = friendlyStage(st.Stage)
	case gaussian:
		head = "Training"
	}
	phases := s.phases()
	cur := s.currentPhaseIdx(phases)

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 16, head, th.Accent) }),
				layout.Flexed(1, spacerW),
				layout.Rigid(func(gtx C) D {
					if staging {
						return th.Dim(gtx, "working…")
					}
					return th.Lbl(gtx, 16, fmt.Sprintf("%d%%", pct), th.Text)
				}),
			)
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			if staging {
				return th.ProgressIndeterminate(gtx, math.Mod(st.Elapsed.Seconds(), 1.4)/1.4)
			}
			return th.Progress(gtx, s.progressFrac())
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			var l2 string
			if gaussian {
				l2 = fmt.Sprintf("%s elapsed", fmtDur(st.Elapsed))
			} else if staging {
				l2 = fmt.Sprintf("%s · %s shapes placed", fmtDur(st.Elapsed), group(st.Total))
			} else {
				l2 = fmt.Sprintf("%s / %s shapes · %s · %s left", group(st.Shapes), group(st.Total), fmtDur(st.Elapsed), etaShort(st))
			}
			return th.Dim(gtx, l2)
		}),
		layout.Rigid(GapV(18).Layout),
		layout.Rigid(func(gtx C) D { return s.phaseStepper(gtx, phases, cur, false) }),
	)
}

func (s *AppState) activityDone(gtx C) D {
	th := s.Th
	st := s.Stats
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 16, "✓  Done", th.Good) }),
		layout.Rigid(GapV(6).Layout),
		layout.Rigid(func(gtx C) D {
			return th.Dim(gtx, fmt.Sprintf("%s shapes in %s · err %s", group(st.Shapes), fmtDur(st.Elapsed), fmtErr(st.Err)))
		}),
		// Auto-budget: when the knee finished BELOW the requested cap, surface that the app picked the
		// optimal count itself (the user's number is a ceiling, not a fixed target).
		layout.Rigid(func(gtx C) D {
			if st.Cap <= 0 || st.Shapes >= st.Cap*9/10 {
				return D{}
			}
			return layout.Inset{Top: 8}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return fillDot(gtx, th.Accent, 8) }),
					layout.Rigid(GapH(8).Layout),
					layout.Rigid(func(gtx C) D {
						return th.Lbl(gtx, 13, fmt.Sprintf("Auto: %s shapes — optimal (cap %s)", group(st.Shapes), group(st.Cap)), th.Accent)
					}),
				)
			})
		}),
		layout.Rigid(func(gtx C) D {
			if !s.Quality.Set {
				return D{}
			}
			return layout.Inset{Top: 12}.Layout(gtx, s.qualityBadge)
		}),
		layout.Rigid(GapV(18).Layout),
		layout.Rigid(func(gtx C) D { return s.phaseStepper(gtx, s.phases(), len(s.phases())-1, true) }),
	)
}

// QualityInfo is the perceptual score of a finished generation (vs the source), with a friendly label.
type QualityInfo struct {
	Set    bool
	DeltaE float64 // ΔE76 mean (lower = closer colour)
	SSIM   float64 // structural similarity (1 = identical)
	Label  string
}

// SetQuality stores a perceptual score and derives a layperson label (driven by SSIM, the structural fit).
func (s *AppState) SetQuality(deltaE, ssim float64) {
	label := "Fair"
	switch {
	case ssim >= 0.95:
		label = "Excellent"
	case ssim >= 0.90:
		label = "Great"
	case ssim >= 0.82:
		label = "Good"
	}
	s.Quality = QualityInfo{Set: true, DeltaE: deltaE, SSIM: ssim, Label: label}
}

// ClearQuality drops the last score (called when a new run starts).
func (s *AppState) ClearQuality() { s.Quality = QualityInfo{} }

// qualityBadge renders the friendly quality verdict + the underlying ΔE/SSIM figures.
func (s *AppState) qualityBadge(gtx C) D {
	th := s.Th
	q := s.Quality
	col := th.Good
	switch q.Label {
	case "Fair":
		col = th.Warn
	case "Good":
		col = th.Accent
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return fillDot(gtx, col, 8) }),
				layout.Rigid(GapH(8).Layout),
				layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 14, "Quality: "+q.Label, col) }),
			)
		}),
		layout.Rigid(GapV(2).Layout),
		layout.Rigid(func(gtx C) D {
			return th.Dim(gtx, fmt.Sprintf("ΔE %.1f · SSIM %.2f", q.DeltaE, q.SSIM))
		}),
	)
}

func (s *AppState) activityError(gtx C) D {
	th := s.Th
	if s.OpenLogBtn.Clicked(gtx) {
		s.ConsoleOpen = true
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 16, "✗  Failed", th.Bad) }),
		layout.Rigid(GapV(6).Layout),
		layout.Rigid(func(gtx C) D { return th.Dim(gtx, "Something went wrong during generation.") }),
		layout.Rigid(GapV(12).Layout),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return th.SecondaryButton(gtx, &s.OpenLogBtn, "Open the log", true)
		}),
	)
}

// phases returns the pipeline stages for the current mode (the stepper labels). cur = the active stage.
func (s *AppState) phases() []string {
	switch {
	case s.Mode.Value() == "gaussian":
		return []string{"Train", "Done"}
	case preset.IsHybridMode(s.baseMode):
		return []string{"Build", "Polish", "Ink", "Done"}
	default:
		return []string{"Build", "Polish", "Done"}
	}
}

// currentPhaseIdx maps the live run state onto a stepper index. Ink has no live event (it's appended in
// the Done handler), so it lights only once the whole run finishes.
func (s *AppState) currentPhaseIdx(phases []string) int {
	if s.Phase == PhaseDone {
		return len(phases) - 1
	}
	if s.Phase != PhaseRunning {
		return -1
	}
	switch s.Stats.Stage {
	case "":
		return 0 // greedy build / gaussian training
	default:
		return indexOf(phases, "Polish") // polish / standout
	}
}

// phaseStepper draws the pipeline as a vertical list of dots + labels: done/current in accent, pending
// muted. cur<0 (idle) shows every step pending.
func (s *AppState) phaseStepper(gtx C, phases []string, cur int, allDone bool) D {
	children := make([]layout.FlexChild, 0, len(phases)*2)
	for i, name := range phases {
		i, name := i, name
		state := 0 // 0 pending, 1 current, 2 done
		switch {
		case allDone || (cur >= 0 && i < cur):
			state = 2
		case cur >= 0 && i == cur:
			state = 1
		}
		if i > 0 {
			children = append(children, layout.Rigid(GapV(2).Layout))
		}
		children = append(children, layout.Rigid(func(gtx C) D { return s.phaseRow(gtx, name, state) }))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func (s *AppState) phaseRow(gtx C, name string, state int) D {
	th := s.Th
	glyph, gcol, ncol, suffix := "○", th.TextDim, th.TextDim, ""
	switch state {
	case 1: // current
		glyph, gcol, ncol, suffix = "●", th.Accent, th.Accent, "…"
	case 2: // done
		glyph, gcol, ncol, suffix = "●", th.Accent, th.Text, "✓"
	}
	return layout.Inset{Top: 3, Bottom: 3}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 13, glyph, gcol) }),
			layout.Rigid(GapH(10).Layout),
			layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 14, name, ncol) }),
			layout.Flexed(1, spacerW),
			layout.Rigid(func(gtx C) D {
				if suffix == "" {
					return D{}
				}
				col := th.Accent
				if state == 2 {
					col = th.Good
				}
				return th.Lbl(gtx, 13, suffix, col)
			}),
		)
	})
}

// friendlyStage turns an internal stage name into a human phase label for the headline.
func friendlyStage(stage string) string {
	switch strings.ToLower(stage) {
	case "polish":
		return "Polishing"
	case "standout":
		return "Cleaning up"
	default:
		return stage
	}
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return 0
}

func (s *AppState) progressFrac() float64 {
	if s.Stats.Total <= 0 {
		return 0
	}
	return float64(s.Stats.Shapes) / float64(s.Stats.Total)
}

func etaShort(st RunStats) string {
	if st.ETA <= 0 {
		return "~ —"
	}
	return "~" + fmtDur(st.ETA)
}

// fmtDur formats a duration as mm:ss (or h:mm:ss past an hour).
func fmtDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	t := int(d.Seconds())
	h, m, sec := t/3600, (t%3600)/60, t%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

// fmtErr humanizes a (possibly large) SSE figure.
func fmtErr(e float64) string {
	switch {
	case e <= 0:
		return "—"
	case e >= 1e6:
		return fmt.Sprintf("%.2fM", e/1e6)
	case e >= 1e3:
		return fmt.Sprintf("%.1fk", e/1e3)
	default:
		return fmt.Sprintf("%.0f", e)
	}
}

// group formats an int with thousands separators (e.g. 1890 -> "1 890").
func group(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, c)
	}
	return string(out)
}
