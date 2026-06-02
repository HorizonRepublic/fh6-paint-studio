package ui

import (
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// LogView renders the execution log as a scrolling monospace list pinned to the latest line.
func (t *Theme) LogView(gtx C, list *widget.List, lines []string) D {
	list.Axis = layout.Vertical
	list.ScrollToEnd = true
	if len(lines) == 0 {
		// Fill the allocated area so the (Flexed) log card keeps its height when empty.
		gtx.Constraints.Min = gtx.Constraints.Max
		return layout.Center.Layout(gtx, func(gtx C) D { return t.Dim(gtx, "log will appear here…") })
	}
	return material.List(t.M, list).Layout(gtx, len(lines), func(gtx C, i int) D {
		l := material.Label(t.M, 12, lines[i])
		l.Color = t.TextDim
		l.Font.Typeface = "Go Mono" // monospace if present in the collection; falls back otherwise
		return layout.Inset{Top: 1, Bottom: 1}.Layout(gtx, l.Layout)
	})
}
