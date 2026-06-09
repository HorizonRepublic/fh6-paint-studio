package ui

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Dropdown is a compact enum selector: a surface box showing the current value that, when
// clicked, floats an option list over the rest of the frame (via op.Defer). Selecting an
// option closes it and flips Changed().
type Dropdown struct {
	options []string
	sel     int
	open    bool
	changed bool
	builtin int // count of leading built-in options; the rest are custom, shown under a divider header
	btn     widget.Clickable
	items   []widget.Clickable
}

// NewDropdown builds a dropdown over options with an initial selection index.
func NewDropdown(options []string, sel int) *Dropdown {
	if sel < 0 || sel >= len(options) {
		sel = 0
	}
	return &Dropdown{options: options, sel: sel, builtin: len(options), items: make([]widget.Clickable, len(options))}
}

// Value returns the currently selected option string ("" if there are no options).
func (d *Dropdown) Value() string {
	if d.sel < 0 || d.sel >= len(d.options) {
		return ""
	}
	return d.options[d.sel]
}

// Set selects the option matching value (a no-op if it is not an option). It does NOT raise the
// changed flag — it is for programmatic restore (e.g. the persisted preference), not a user action.
func (d *Dropdown) Set(value string) {
	for i, o := range d.options {
		if o == value {
			d.sel = i
			return
		}
	}
}

// SetOptions replaces the option list (rebuilding the per-item click state), keeping the current
// selection by value when it still exists, else falling back to the first option. builtin is the count
// of leading built-in options (the rest render under a "Custom" header). Does not raise the changed flag.
func (d *Dropdown) SetOptions(options []string, builtin int) {
	cur := ""
	if d.sel >= 0 && d.sel < len(d.options) {
		cur = d.options[d.sel]
	}
	d.options = options
	d.builtin = builtin
	d.items = make([]widget.Clickable, len(options))
	d.sel = 0
	for i, o := range options {
		if o == cur {
			d.sel = i
			break
		}
	}
	d.open = false
}

// ForceOpen opens the option popup (used by the headless screenshot harness to capture it).
func (d *Dropdown) ForceOpen() { d.open = true }

// Changed reports (and clears) whether the selection changed since the last call.
func (d *Dropdown) Changed() bool {
	c := d.changed
	d.changed = false
	return c
}

// Layout draws the closed control and, when open, the deferred option popup.
func (d *Dropdown) Layout(gtx C, th *Theme) D {
	if d.btn.Clicked(gtx) {
		d.open = !d.open
	}
	for i := range d.items {
		if d.items[i].Clicked(gtx) {
			if i != d.sel {
				d.sel = i
				d.changed = true
			}
			d.open = false
		}
	}

	dims := ddBox(gtx, th, &d.btn, d.Value())
	if d.open {
		macro := op.Record(gtx.Ops)
		off := op.Offset(image.Pt(0, dims.Size.Y+4)).Push(gtx.Ops)
		d.popup(gtx, th, dims.Size.X)
		off.Pop()
		op.Defer(gtx.Ops, macro.Stop())
	}
	return dims
}

// ddBox is the closed control: a rounded surface box with the value and a chevron.
func ddBox(gtx C, th *Theme, btn *widget.Clickable, text string) D {
	return material.Clickable(gtx, btn, func(gtx C) D {
		return layout.Background{}.Layout(gtx,
			func(gtx C) D {
				fillRRect(gtx, th.SurfaceHi, gtx.Constraints.Min, 8)
				return D{Size: gtx.Constraints.Min}
			},
			func(gtx C) D {
				return layout.UniformInset(9).Layout(gtx, func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D { return th.Lbl(gtx, 14, text, th.Text) }),
						layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 12, "▾", th.TextDim) }),
					)
				})
			},
		)
	})
}

// popup is the open option list, fixed to the control's width. When custom options follow the built-in
// ones, each group gets a dim header so the user's presets read apart from the built-in modes.
func (d *Dropdown) popup(gtx C, th *Theme, width int) D {
	gtx.Constraints.Min.X = width
	gtx.Constraints.Max.X = width
	grouped := d.builtin > 0 && d.builtin < len(d.options)
	header := func(text string) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: 6, Bottom: 2, Left: 8, Right: 8}.Layout(gtx, func(gtx C) D {
				return th.Lbl(gtx, 11, text, th.TextDim)
			})
		})
	}
	item := func(i int) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			return material.Clickable(gtx, &d.items[i], func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				col := th.Text
				if i == d.sel {
					col = th.Accent
				}
				return layout.UniformInset(8).Layout(gtx, func(gtx C) D {
					return th.Lbl(gtx, 14, d.options[i], col)
				})
			})
		})
	}
	return th.CardBg(gtx, th.SurfaceHi, 4, func(gtx C) D {
		ch := make([]layout.FlexChild, 0, len(d.options)+2)
		if grouped {
			ch = append(ch, header("Built-in"))
		}
		for i := range d.options {
			if grouped && i == d.builtin {
				ch = append(ch, header("Custom"))
			}
			ch = append(ch, item(i))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
	})
}
