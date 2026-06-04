package ui

import (
	"fmt"
	"math"
	"time"

	"gioui.org/layout"
)

// runPanel is the right column: a Run telemetry card over a fill Log card. (The primary
// Generate / Stop action lives at the bottom of the left settings column, beside its inputs.)
func (s *AppState) runPanel(gtx C) D {
	th := s.Th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Card(gtx, s.runCard) }),
		layout.Rigid(GapV(th.Gap).Layout),
		layout.Flexed(1, func(gtx C) D {
			gtx.Constraints.Min = gtx.Constraints.Max // let the log card fill the column
			return th.Card(gtx, s.logCard)
		}),
	)
}

func (s *AppState) runCard(gtx C) D {
	th := s.Th
	running := s.Phase == PhaseRunning
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Title(gtx, "Run") }),
		layout.Rigid(GapV(12).Layout),
		layout.Rigid(func(gtx C) D {
			// During a post-greedy phase (polish/standout) the shape counter is
			// already at 100%, so show a sweeping indeterminate bar (animated from elapsed time) —
			// not a bar stuck full. The greedy build still uses the determinate fraction.
			if running && s.Stats.Stage != "" {
				phase := math.Mod(s.Stats.Elapsed.Seconds(), 1.4) / 1.4
				return th.ProgressIndeterminate(gtx, phase)
			}
			return th.Progress(gtx, s.progressFrac())
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(s.statsLines),
		layout.Rigid(GapV(12).Layout),
		layout.Rigid(func(gtx C) D { return th.Sparkline(gtx, s.Stats.History, 44) }),
	)
}

func (s *AppState) logCard(gtx C) D {
	th := s.Th
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return th.Title(gtx, "Log") }),
		layout.Rigid(GapV(8).Layout),
		layout.Flexed(1, func(gtx C) D { return th.LogView(gtx, &s.LogList, s.Log) }),
	)
}

func (s *AppState) progressFrac() float64 {
	if s.Stats.Total <= 0 {
		return 0
	}
	return float64(s.Stats.Shapes) / float64(s.Stats.Total)
}

func (s *AppState) statsLines(gtx C) D {
	th := s.Th
	st := s.Stats
	staging := s.Phase == PhaseRunning && st.Stage != ""
	// Gaussian mode has no greedy shape-placement: all glows train jointly, so the bar tracks TRAINING
	// ITERATIONS, not placed shapes. Showing "N / iters shapes" reads as a wrong shape count (the user set
	// e.g. 2033 glows but the iteration total is ~3000) — so label it as training progress instead.
	gaussian := s.Mode.Value() == "gaussian"
	var line1, line2 string
	switch {
	case staging:
		// Post-greedy: name the active stage + ticking elapsed (no ETA — these phases aren't counted).
		line1 = st.Stage
		line2 = fmt.Sprintf("%s elapsed    ·    err %s", fmtDur(st.Elapsed), fmtErr(st.Err))
	case gaussian:
		line1 = fmt.Sprintf("training… %d%%", int(s.progressFrac()*100+0.5))
		line2 = fmt.Sprintf("%s elapsed", fmtDur(st.Elapsed))
	default:
		line1 = fmt.Sprintf("%s / %s shapes", group(st.Shapes), group(st.Total))
		line2 = fmt.Sprintf("%s / %s    ·    err %s", fmtDur(st.Elapsed), etaStr(st), fmtErr(st.Err))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			if staging {
				return th.Lbl(gtx, 14, line1, th.Accent) // emphasise the active stage
			}
			return th.Body(gtx, line1)
		}),
		layout.Rigid(GapV(3).Layout),
		layout.Rigid(func(gtx C) D { return th.Dim(gtx, line2) }),
	)
}

func etaStr(st RunStats) string {
	if st.ETA <= 0 {
		return "~ —"
	}
	return "~ " + fmtDur(st.ETA)
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
