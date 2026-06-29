package ui

import (
	"image"
	"image/color"
	"io"
	"strings"

	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"fh6-paint-studio/internal/i18n"
)

// console is the shared bottom drawer: a one-line status/activity strip that is ALWAYS visible (in
// Studio AND Library), expandable to the full activity feed. It replaces the old status bar and the
// right-column Log card, so a run's — or an inject's — messages are visible everywhere, inject errors
// included (they used to be hidden behind the Library view).
func (s *AppState) console(gtx C) D {
	th := s.Th
	if s.ConsoleToggle.Clicked(gtx) {
		s.ConsoleOpen = !s.ConsoleOpen
	}
	return layout.Background{}.Layout(gtx,
		func(gtx C) D {
			sz := gtx.Constraints.Min
			paint.FillShape(gtx.Ops, th.Surface, clip.Rect{Max: sz}.Op())
			paint.FillShape(gtx.Ops, th.Border, clip.Rect{Max: image.Pt(sz.X, 1)}.Op()) // top hairline
			return D{Size: sz}
		},
		func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			var children []layout.FlexChild
			if s.ConsoleOpen {
				children = append(children,
					layout.Rigid(s.consoleToolbar),
					layout.Rigid(func(gtx C) D {
						h := gtx.Dp(196)
						gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = h, h
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return layout.UniformInset(8).Layout(gtx, func(gtx C) D {
							return th.LogView(gtx, &s.LogList, s.filteredLog())
						})
					}),
					layout.Rigid(func(gtx C) D {
						sz := image.Pt(gtx.Constraints.Max.X, 1)
						paint.FillShape(gtx.Ops, th.Border, clip.Rect{Max: sz}.Op())
						return D{Size: sz}
					}),
				)
			}
			children = append(children, layout.Rigid(s.consoleStrip))
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		},
	)
}

// consoleStrip is the always-visible one-line bar: a severity dot + the live status/activity on the
// left, the Log expand/collapse toggle and the Sound preference on the right.
func (s *AppState) consoleStrip(gtx C) D {
	th := s.Th
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.UniformInset(10).Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				lvl := LogInfo
				if n := len(s.Log); n > 0 {
					lvl = s.Log[n-1].Level
				}
				return fillDot(gtx, th.levelColor(lvl), 7)
			}),
			layout.Rigid(GapH(8).Layout),
			layout.Flexed(1, func(gtx C) D { return th.Dim(gtx, s.consoleStatus()) }),
			layout.Rigid(s.consoleToggle),
			layout.Rigid(GapH(14).Layout),
			layout.Rigid(func(gtx C) D {
				cb := material.CheckBox(th.M, &s.SoundOn, i18n.T("console.sound_on_finish"))
				cb.Color, cb.IconColor, cb.TextSize = th.TextDim, th.Accent, 13
				return cb.Layout(gtx)
			}),
		)
	})
}

func (s *AppState) consoleToggle(gtx C) D {
	th := s.Th
	arrow := "▴"
	if s.ConsoleOpen {
		arrow = "▾"
	}
	label := arrow + "  " + i18n.T("console.log")
	if n := len(s.Log); n > 0 {
		label = arrow + "  " + i18n.T("console.log_n", n)
	}
	return material.Clickable(gtx, &s.ConsoleToggle, func(gtx C) D {
		return layout.UniformInset(4).Layout(gtx, func(gtx C) D { return th.Lbl(gtx, 13, label, th.Accent) })
	})
}

// consoleToolbar is the row above the feed (when expanded): a heading + the level filter, Copy and
// Clear actions.
func (s *AppState) consoleToolbar(gtx C) D {
	th := s.Th
	if s.LogClearBtn.Clicked(gtx) {
		s.Log = nil
	}
	if s.LogFilterBtn.Clicked(gtx) {
		s.LogFilterErrors = !s.LogFilterErrors
	}
	if s.LogCopyBtn.Clicked(gtx) {
		if txt := logText(s.filteredLog()); txt != "" {
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(txt))})
			s.Toast = i18n.T("console.copied")
		}
	}
	filterLabel, filterCol := i18n.T("console.all_levels"), th.TextDim
	if s.LogFilterErrors {
		filterLabel, filterCol = i18n.T("console.errors_only"), th.Warn
	}
	return layout.Inset{Left: 10, Right: 10, Top: 6, Bottom: 4}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 12, i18n.T("console.activity_heading"), th.TextDim) }),
			layout.Flexed(1, spacerW),
			layout.Rigid(func(gtx C) D { return s.txtBtn(gtx, &s.LogFilterBtn, filterLabel, filterCol) }),
			layout.Rigid(GapH(4).Layout),
			layout.Rigid(func(gtx C) D { return s.txtBtn(gtx, &s.LogCopyBtn, i18n.T("console.copy"), th.Accent) }),
			layout.Rigid(GapH(4).Layout),
			layout.Rigid(func(gtx C) D { return s.txtBtn(gtx, &s.LogClearBtn, i18n.T("console.clear"), th.Accent) }),
		)
	})
}

// txtBtn is a compact clickable text label (the console's lightweight toolbar actions).
func (s *AppState) txtBtn(gtx C, btn *widget.Clickable, label string, col color.NRGBA) D {
	return material.Clickable(gtx, btn, func(gtx C) D {
		return layout.UniformInset(4).Layout(gtx, func(gtx C) D { return s.Th.Lbl(gtx, 12, label, col) })
	})
}

// filteredLog returns the feed entries honouring the level filter.
func (s *AppState) filteredLog() []LogEntry {
	if !s.LogFilterErrors {
		return s.Log
	}
	out := make([]LogEntry, 0, len(s.Log))
	for _, e := range s.Log {
		if e.Level == LogWarn || e.Level == LogErr {
			out = append(out, e)
		}
	}
	return out
}

// logText renders entries as plain "HH:MM:SS message" lines for the clipboard.
func logText(es []LogEntry) string {
	var b strings.Builder
	for _, e := range es {
		b.WriteString(e.Time.Format("15:04:05"))
		b.WriteByte(' ')
		b.WriteString(e.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// consoleStatus is the strip's left text: a transient toast, else the latest activity message while a
// run / error is live (so "what's happening" shows without expanding), else the resting phase status.
func (s *AppState) consoleStatus() string {
	if s.Toast != "" {
		return s.Toast
	}
	if n := len(s.Log); n > 0 && (s.Phase == PhaseRunning || s.Phase == PhaseError) {
		return s.Log[n-1].Text
	}
	return s.statusText()
}
