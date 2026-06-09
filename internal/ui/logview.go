package ui

import (
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// LogLevel classifies an activity entry for its severity colour in the feed.
type LogLevel int

const (
	LogInfo LogLevel = iota
	LogGood
	LogWarn
	LogErr
)

// LogEntry is one structured line in the activity feed: a wall-clock time, a severity, and the text.
type LogEntry struct {
	Time  time.Time
	Level LogLevel
	Text  string
}

// classifyLog infers a severity from a pre-formatted studio message (callers pass plain strings; the
// few that matter most — phase transitions, inject — also set the level explicitly via AppendLogLvl).
func classifyLog(s string) LogLevel {
	ls := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.HasPrefix(ls, "error") || strings.HasPrefix(ls, "panic") ||
		(strings.HasPrefix(ls, "inject:") && !strings.HasPrefix(ls, "inject: ok")):
		return LogErr
	case strings.HasPrefix(ls, "warning") || strings.HasPrefix(ls, "warn"):
		return LogWarn
	case strings.HasPrefix(ls, "done") || strings.HasPrefix(ls, "inject: ok") ||
		strings.Contains(ls, "injected") || strings.Contains(ls, " ok —"):
		return LogGood
	default:
		return LogInfo
	}
}

// levelColor maps a severity to its feed colour (info is muted; the rest are the semantic palette).
func (t *Theme) levelColor(l LogLevel) color.NRGBA {
	switch l {
	case LogGood:
		return t.Good
	case LogWarn:
		return t.Warn
	case LogErr:
		return t.Bad
	default:
		return t.TextDim
	}
}

// LogView renders the activity feed: one row per entry with a severity dot, a dim timestamp, and the
// message. Warning/error rows get a faint tinted background so they stand apart from the stream.
func (t *Theme) LogView(gtx C, list *widget.List, entries []LogEntry) D {
	list.Axis = layout.Vertical
	list.ScrollToEnd = true
	if len(entries) == 0 {
		gtx.Constraints.Min = gtx.Constraints.Max
		return layout.Center.Layout(gtx, func(gtx C) D { return t.Dim(gtx, "activity will appear here…") })
	}
	return material.List(t.M, list).Layout(gtx, len(entries), func(gtx C, i int) D {
		return t.logRow(gtx, entries[i])
	})
}

func (t *Theme) logRow(gtx C, e LogEntry) D {
	col := t.levelColor(e.Level)
	var tint color.NRGBA // faint row wash for warnings/errors so the eye catches them in the stream
	switch e.Level {
	case LogWarn:
		tint = color.NRGBA{R: t.Warn.R, G: t.Warn.G, B: t.Warn.B, A: 24}
	case LogErr:
		tint = color.NRGBA{R: t.Bad.R, G: t.Bad.G, B: t.Bad.B, A: 30}
	}
	row := func(gtx C) D {
		return layout.Inset{Top: 3, Bottom: 3, Left: 6, Right: 6}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return fillDot(gtx, col, 7) }),
				layout.Rigid(GapH(8).Layout),
				layout.Rigid(func(gtx C) D { return t.Lbl(gtx, 11, e.Time.Format("15:04:05"), t.TextDim) }),
				layout.Rigid(GapH(8).Layout),
				layout.Flexed(1, func(gtx C) D {
					l := material.Label(t.M, 12, e.Text)
					if e.Level == LogInfo {
						l.Color = t.Text
					} else {
						l.Color = col
					}
					l.Font.Typeface = "Go Mono" // monospace if present in the collection; falls back otherwise
					return l.Layout(gtx)
				}),
			)
		})
	}
	if (tint == color.NRGBA{}) {
		return row(gtx)
	}
	return layout.Background{}.Layout(gtx,
		func(gtx C) D { fillRRect(gtx, tint, gtx.Constraints.Min, 4); return D{Size: gtx.Constraints.Min} },
		row,
	)
}

// fillDot draws a filled circle of diameter ddp (dp) in col, sized to its own box (line-centred by the
// surrounding Flex's Middle alignment).
func fillDot(gtx C, col color.NRGBA, ddp int) D {
	d := gtx.Dp(unit.Dp(ddp))
	paint.FillShape(gtx.Ops, col, clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
	return D{Size: image.Pt(d, d)}
}
