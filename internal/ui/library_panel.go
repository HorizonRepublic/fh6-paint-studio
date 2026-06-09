package ui

import (
	"fmt"
	"image"

	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"fh6-paint-studio/internal/library"
)

// LibraryRow is the per-entry widget state for the library list (rebuilt when the list changes).
type LibraryRow struct {
	Entry         library.Entry
	Thumb         paint.ImageOp    // decoded thumb.png (zero value -> placeholder box)
	ThumbBtn      widget.Clickable // click the thumb to open the full-preview lightbox
	Inject        widget.Clickable
	Export        widget.Clickable
	Rename        widget.Clickable // enter inline-edit mode; doubles as "Save" while Editing
	RenameCancel  widget.Clickable
	NameEd        widget.Editor // inline name editor, shown in place of the name label while Editing
	Editing       bool
	Delete        widget.Clickable
	ConfirmDelete bool
}

func entryCountLabel(e library.Entry) string {
	return fmt.Sprintf("%d shapes", e.Shapes)
}

// SetLibrary replaces the library rows (already decoded by the caller).
func (s *AppState) SetLibrary(rows []LibraryRow) { s.LibRows = rows }

// ArmDelete sets the confirm flag on row i and clears it on all others (one armed at a time).
func (s *AppState) ArmDelete(i int) {
	for j := range s.LibRows {
		s.LibRows[j].ConfirmDelete = j == i
	}
}

// ArmRename puts row i into inline name-edit mode (seeding the editor with the current name) and takes
// every other row out of edit/confirm mode, so at most one row is being edited at a time.
func (s *AppState) ArmRename(i int) {
	for j := range s.LibRows {
		r := &s.LibRows[j]
		if j == i {
			r.Editing = true
			r.ConfirmDelete = false
			r.NameEd.SingleLine = true
			r.NameEd.Submit = true // commit on Enter
			r.NameEd.SetText(r.Entry.Name)
		} else {
			r.Editing = false
		}
	}
}

// libraryScreen renders the saved-generation library: a header (count + inject controls + open-folder)
// over a scrollable list of entry rows. Branched in from AppState.Layout when View == ViewLibrary.
func (s *AppState) libraryScreen(gtx C) D {
	th := s.Th
	gtx.Constraints.Min = gtx.Constraints.Max
	return th.Card(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(s.libraryHeader),
			layout.Rigid(GapV(8).Layout),
			layout.Rigid(s.injectGuide),
			layout.Rigid(GapV(10).Layout),
			layout.Flexed(1, s.libraryList),
		)
	})
}

// injectGuide is the collapsible "How injecting works" strip in the Library — the inject ritual is
// FH6's biggest footgun (wrong template size silently drops shapes; the game only re-derives the mesh on
// a vinyl save+reload), so the steps are spelled out one click away.
func (s *AppState) injectGuide(gtx C) D {
	th := s.Th
	if s.InjectGuideClick.Clicked(gtx) {
		s.InjectGuideOpen = !s.InjectGuideOpen
	}
	arrow := "▸"
	if s.InjectGuideOpen {
		arrow = "▾"
	}
	head := func(gtx C) D {
		return material.Clickable(gtx, &s.InjectGuideClick, func(gtx C) D {
			return th.Lbl(gtx, 12, arrow+"  How injecting works", th.Accent)
		})
	}
	if !s.InjectGuideOpen {
		return head(gtx)
	}
	step := func(n, text string) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: 3, Left: 4}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 12, n, th.Accent) }),
					layout.Rigid(GapH(8).Layout),
					layout.Rigid(func(gtx C) D { return th.Dim(gtx, text) }),
				)
			})
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(head),
		layout.Rigid(GapV(4).Layout),
		step("1", "In FH6, add a vinyl group with at least as many layers as the generation's shapes."),
		step("2", "Set “FH6 template” (top-right) to that group's layer count."),
		step("3", "Click Inject on the generation — the app needs admin + FH6 running."),
		step("4", "In FH6, Save & reload the vinyl — the mesh only appears after a reload."),
	)
}

func (s *AppState) libraryHeader(gtx C) D {
	th := s.Th
	num := func(ed *widget.Editor, hint string, w int, errState bool) layout.FlexChild {
		return layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(unit.Dp(w)), gtx.Dp(unit.Dp(w))
			return th.editorBoxErr(gtx, ed, hint, errState)
		})
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return th.Title(gtx, fmt.Sprintf("Library — %d", len(s.LibRows)))
		}),
		layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Max.X, 0)} }),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Right: 6}.Layout(gtx, func(gtx C) D { return th.Dim(gtx, "FH6 template") })
		}),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Right: 6}.Layout(gtx, func(gtx C) D {
				return s.InjectHint.Layout(gtx, th, "The number of LAYERS in the in-game vinyl group you're injecting into — it must match. If it's smaller than the generation's shapes, the extra shapes are silently dropped in-game. Make a group with enough layers in FH6 first, then put that count here.")
			})
		}),
		num(&s.InjectLayers, "count", 72, s.InjectLayersErr),
		layout.Rigid(GapH(10).Layout),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Right: 6}.Layout(gtx, func(gtx C) D { return th.Dim(gtx, "Scale") })
		}),
		num(&s.InjectScale, "1.0", 64, false),
		layout.Rigid(GapH(10).Layout),
		layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &s.OpenFolderBtn, "Open folder", true) }),
	)
}

// libraryList lays the saved generations out as a responsive card GRID (a gallery): bigger thumbnails,
// 1-4 columns by width — so the space is used and the vinyls read as a showcase, not a thin list.
func (s *AppState) libraryList(gtx C) D {
	th := s.Th
	if len(s.LibRows) == 0 {
		return layout.Center.Layout(gtx, func(gtx C) D {
			return th.Dim(gtx, "No generations yet — reconstruct an image in Studio.")
		})
	}
	cols := libCols(gtx)
	rows := (len(s.LibRows) + cols - 1) / cols
	return material.List(th.M, &s.LibScroll).Layout(gtx, rows, func(gtx C, row int) D {
		return s.libraryGridRow(gtx, row, cols)
	})
}

// libCols picks the column count from the available width (cards target ~330dp).
func libCols(gtx C) int {
	c := gtx.Constraints.Max.X / gtx.Dp(330)
	if c < 1 {
		c = 1
	}
	if c > 4 {
		c = 4
	}
	return c
}

// libraryGridRow lays one row of up to cols equal-width cards. Empty trailing cells are padded so the
// cards keep a consistent width across rows.
func (s *AppState) libraryGridRow(gtx C, row, cols int) D {
	gap := gtx.Dp(12)
	cw := (gtx.Constraints.Max.X - gap*(cols-1)) / cols
	children := make([]layout.FlexChild, 0, cols*2)
	for c := 0; c < cols; c++ {
		if c > 0 {
			children = append(children, layout.Rigid(GapH(12).Layout))
		}
		idx := row*cols + c
		if idx >= len(s.LibRows) {
			children = append(children, layout.Rigid(func(gtx C) D { return D{Size: image.Pt(cw, 0)} }))
			continue
		}
		i := idx
		children = append(children, layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X, gtx.Constraints.Max.X = cw, cw
			return s.libraryCard(gtx, &s.LibRows[i])
		}))
	}
	return layout.Inset{Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
		return layout.Flex{}.Layout(gtx, children...)
	})
}

// libraryCard is one gallery tile: a big clickable thumbnail, the name (or inline editor), the meta
// line, and the action buttons.
func (s *AppState) libraryCard(gtx C, r *LibraryRow) D {
	th := s.Th
	return th.Card(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return s.cardThumb(gtx, r) }),
			layout.Rigid(GapV(10).Layout),
			layout.Rigid(func(gtx C) D {
				if r.Editing {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return th.editorBox(gtx, &r.NameEd, "name")
				}
				return th.Lbl(gtx, 15, r.Entry.Name, th.Text)
			}),
			layout.Rigid(GapV(3).Layout),
			layout.Rigid(func(gtx C) D {
				meta := fmt.Sprintf("%s · %s · %s", r.Entry.Preset, entryCountLabel(r.Entry),
					r.Entry.Created.Format("02.01 15:04"))
				return th.Dim(gtx, meta)
			}),
			layout.Rigid(GapV(10).Layout),
			layout.Rigid(func(gtx C) D { return s.cardActions(gtx, r) }),
		)
	})
}

// cardThumb is the large 16:10 preview filling the card width; clicking it opens the lightbox.
func (s *AppState) cardThumb(gtx C, r *LibraryRow) D {
	th := s.Th
	w := gtx.Constraints.Max.X
	sz := image.Pt(w, w*10/16)
	return r.ThumbBtn.Layout(gtx, func(gtx C) D {
		fillRRect(gtx, th.SurfaceHi, sz, 8)
		pointer.CursorPointer.Add(gtx.Ops)
		gtx.Constraints = layout.Exact(sz)
		if (r.Thumb != paint.ImageOp{}) {
			defer clip.UniformRRect(image.Rectangle{Max: sz}, 8).Push(gtx.Ops).Pop()
			widget.Image{Src: r.Thumb, Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
		}
		return D{Size: sz}
	})
}

// fitBadge shows, per row, whether the current FH6-template count covers this generation's shapes:
// "✓ fits" when the template is big enough, "⚠ −N" when N shapes would be dropped in-game. Hidden until
// a template count is entered.
func (s *AppState) fitBadge(gtx C, r *LibraryRow) D {
	th := s.Th
	layers := s.InjectLayersValue()
	if layers <= 0 || r.Entry.Shapes <= 0 {
		return D{}
	}
	txt, col := "✓ fits", th.Good
	if layers < r.Entry.Shapes {
		txt, col = fmt.Sprintf("−%d will drop", r.Entry.Shapes-layers), th.Warn
	}
	return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx C) D { return th.Lbl(gtx, 12, txt, col) })
}

// injectButton renders the row's Inject control by inject state: a spinner while in flight, then a
// transient green tick / red cross pill, otherwise the normal button.
func (s *AppState) injectButton(gtx C, r *LibraryRow) D {
	th := s.Th
	switch {
	case s.InjectingID != "" && s.InjectingID == r.Entry.ID:
		return th.BusyPill(gtx, "Injecting…")
	case s.InjectResultID != "" && s.InjectResultID == r.Entry.ID && s.InjectOK:
		return th.StatusPill(gtx, "✓ Injected", th.Good)
	case s.InjectResultID != "" && s.InjectResultID == r.Entry.ID:
		return th.StatusPill(gtx, "✗ Failed", th.Bad)
	default:
		return th.AccentButton(gtx, &r.Inject, "Inject into FH6") // the card's primary action
	}
}

// cardActions is the tile's button area: the fit badge + a full-width primary Inject, over a secondary
// Export / Rename / Delete row. While renaming it collapses to Save / Cancel.
func (s *AppState) cardActions(gtx C, r *LibraryRow) D {
	th := s.Th
	if r.Editing {
		return layout.Flex{}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return th.AccentButton(gtx, &r.Rename, "Save")
			}),
			layout.Rigid(GapH(8).Layout),
			layout.Flexed(1, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return th.SecondaryButton(gtx, &r.RenameCancel, "Cancel", true)
			}),
		)
	}
	secondary := func(btn *widget.Clickable, label string, danger bool) layout.FlexChild {
		return layout.Flexed(1, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			if danger {
				return th.DangerButton(gtx, btn, label)
			}
			return th.SecondaryButton(gtx, btn, label, true)
		})
	}
	delLabel := "Delete"
	if r.ConfirmDelete {
		delLabel = "Confirm?"
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return s.fitBadge(gtx, r) }),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return s.injectButton(gtx, r)
		}),
		layout.Rigid(GapV(8).Layout),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				secondary(&r.Export, "Export", false),
				layout.Rigid(GapH(8).Layout),
				secondary(&r.Rename, "Rename", false),
				layout.Rigid(GapH(8).Layout),
				secondary(&r.Delete, delLabel, r.ConfirmDelete),
			)
		}),
	)
}
