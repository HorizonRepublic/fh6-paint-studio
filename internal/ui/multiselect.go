package ui

import (
	"image"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// MultiSelect is a combobox that floats a checklist popup: zero or more options can be ticked, and the
// closed control summarises the selection. Used for shape kinds so a new primitive just extends the
// option list with no other UI rework.
type MultiSelect struct {
	options []string
	checked []bool
	open    bool
	btn     widget.Clickable
	boxes   []widget.Bool
}

// NewMultiSelect builds a checklist over options, with the parallel checked slice as the initial ticks.
func NewMultiSelect(options []string, checked []bool) *MultiSelect {
	m := &MultiSelect{options: options}
	m.checked = make([]bool, len(options))
	m.boxes = make([]widget.Bool, len(options))
	for i := range options {
		on := i < len(checked) && checked[i]
		m.checked[i] = on
		m.boxes[i].Value = on
	}
	return m
}

// Value returns the ticked options in their declared order.
func (m *MultiSelect) Value() []string {
	out := make([]string, 0, len(m.options))
	for i, o := range m.options {
		if m.checked[i] {
			out = append(out, o)
		}
	}
	return out
}

// ValueCSV joins the ticked options with commas.
func (m *MultiSelect) ValueCSV() string { return strings.Join(m.Value(), ",") }

// IsOn reports whether the option (case-insensitive) is currently ticked.
func (m *MultiSelect) IsOn(name string) bool {
	for i, o := range m.options {
		if strings.EqualFold(o, name) {
			return m.checked[i]
		}
	}
	return false
}

// SetOn ticks/unticks a single option by name, keeping the popup checkbox in agreement so the expert
// checklist and the per-kind weight fields stay consistent with this (the icon-row) view.
func (m *MultiSelect) SetOn(name string, v bool) {
	for i, o := range m.options {
		if strings.EqualFold(o, name) {
			m.checked[i] = v
			m.boxes[i].Value = v
			return
		}
	}
}

// OnCount returns how many options are ticked.
func (m *MultiSelect) OnCount() int {
	n := 0
	for _, c := range m.checked {
		if c {
			n++
		}
	}
	return n
}

// SetCSV ticks exactly the options named in csv (case-insensitive); unknown names are ignored.
func (m *MultiSelect) SetCSV(csv string) {
	want := map[string]bool{}
	for _, p := range strings.Split(csv, ",") {
		want[strings.ToLower(strings.TrimSpace(p))] = true
	}
	for i, o := range m.options {
		on := want[strings.ToLower(o)]
		m.checked[i] = on
		m.boxes[i].Value = on
	}
}

func (m *MultiSelect) summary() string {
	v := m.Value()
	switch {
	case len(v) == 0:
		return "none"
	case len(v) == len(m.options):
		return "all"
	default:
		return strings.Join(v, ", ")
	}
}

// Layout draws the closed control and, when open, the deferred checklist popup.
func (m *MultiSelect) Layout(gtx C, th *Theme) D {
	if m.btn.Clicked(gtx) {
		m.open = !m.open
	}
	for i := range m.boxes {
		if m.boxes[i].Update(gtx) {
			m.checked[i] = m.boxes[i].Value
		}
	}
	dims := ddBox(gtx, th, &m.btn, m.summary())
	if m.open {
		macro := op.Record(gtx.Ops)
		off := op.Offset(image.Pt(0, dims.Size.Y+4)).Push(gtx.Ops)
		m.popup(gtx, th, dims.Size.X)
		off.Pop()
		op.Defer(gtx.Ops, macro.Stop())
	}
	return dims
}

func (m *MultiSelect) popup(gtx C, th *Theme, width int) D {
	gtx.Constraints.Min.X = width
	gtx.Constraints.Max.X = width
	return th.CardBg(gtx, th.SurfaceHi, 4, func(gtx C) D {
		ch := make([]layout.FlexChild, 0, len(m.options))
		for i := range m.options {
			i := i
			ch = append(ch, layout.Rigid(func(gtx C) D {
				return layout.UniformInset(8).Layout(gtx, func(gtx C) D {
					cb := material.CheckBox(th.M, &m.boxes[i], m.options[i])
					cb.Color = th.Text
					cb.IconColor = th.Accent
					cb.TextSize = 14
					return cb.Layout(gtx)
				})
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
	})
}
