// Package ui holds the Gio widgets and layout for FH6 Paint Studio. Widgets live in
// this single package (not a sub-package) so they can share the Theme without an import
// cycle. The package is GPU/CGO-free: it renders through Gio, and is verified offscreen
// via gioui.org/gpu/headless (see cmd/studio-shot).
package ui

import (
	"image/color"

	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
)

// Short aliases for the two types every widget signature uses.
type (
	C = layout.Context
	D = layout.Dimensions
)

// Theme is the app's dark + electric-teal design system: a configured material.Theme
// plus the named palette and spacing the custom widgets draw with.
type Theme struct {
	M *material.Theme

	Bg        color.NRGBA // window background (deepest)
	Surface   color.NRGBA // card surface
	SurfaceHi color.NRGBA // raised/hover surface, slider/progress track
	Border    color.NRGBA // hairline card border
	Text      color.NRGBA // primary text
	TextDim   color.NRGBA // secondary/muted text
	Accent    color.NRGBA // teal — CTA, progress, active
	AccentDim color.NRGBA // disabled/!pressed accent
	Good      color.NRGBA // success / error-going-down
	Bad       color.NRGBA // error / failure
	OnAccent  color.NRGBA // text on an accent fill

	Pad unit.Dp // default card inner padding
	Gap unit.Dp // default gap between stacked items
}

func rgb(s uint32) color.NRGBA {
	return color.NRGBA{R: byte(s >> 16), G: byte(s >> 8), B: byte(s), A: 0xff}
}

// NewTheme builds the studio theme with the gofont collection.
func NewTheme() *Theme {
	m := material.NewTheme()
	m.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	m.TextSize = 14

	t := &Theme{
		M:         m,
		Bg:        rgb(0x15171C),
		Surface:   rgb(0x1E2128),
		SurfaceHi: rgb(0x262A33),
		Border:    rgb(0x333947),
		Text:      rgb(0xE6E9EF),
		TextDim:   rgb(0x9AA3B2),
		Accent:    rgb(0x2DD4BF),
		AccentDim: rgb(0x1B8C81),
		Good:      rgb(0x3DD68C),
		Bad:       rgb(0xF06A6A),
		OnAccent:  rgb(0x0B0E12),
		Pad:       14,
		Gap:       10,
	}
	m.Palette.Bg = t.Bg
	m.Palette.Fg = t.Text
	m.Palette.ContrastBg = t.Accent
	m.Palette.ContrastFg = t.OnAccent
	return t
}
