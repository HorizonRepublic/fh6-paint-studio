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
			layout.Rigid(GapV(10).Layout),
			layout.Flexed(1, s.libraryList),
		)
	})
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
			return layout.Inset{Right: 6}.Layout(gtx, func(gtx C) D { return th.Dim(gtx, "FH6 layers") })
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

func (s *AppState) libraryList(gtx C) D {
	th := s.Th
	if len(s.LibRows) == 0 {
		return layout.Center.Layout(gtx, func(gtx C) D {
			return th.Dim(gtx, "No generations yet — reconstruct an image in Studio.")
		})
	}
	return material.List(th.M, &s.LibScroll).Layout(gtx, len(s.LibRows), func(gtx C, i int) D {
		return s.libraryRow(gtx, &s.LibRows[i])
	})
}

func (s *AppState) libraryRow(gtx C, r *LibraryRow) D {
	th := s.Th
	return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
		return th.Card(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return s.rowThumb(gtx, r) }),
				layout.Rigid(GapH(12).Layout),
				layout.Flexed(1, func(gtx C) D {
					// While renaming, the name area is JUST the editor (the meta line is hidden) so the
					// row keeps its normal height — the 54dp thumbnail stays the tallest element, and the
					// editor lines up with the Save/Cancel buttons instead of pushing the row taller.
					if r.Editing {
						// A small right gap so the field doesn't butt up against the Save button.
						return layout.Inset{Right: 10}.Layout(gtx, func(gtx C) D {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							return th.editorBox(gtx, &r.NameEd, "name") // natural single-line height
						})
					}
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 14, r.Entry.Name, th.Text) }),
						layout.Rigid(GapV(3).Layout),
						layout.Rigid(func(gtx C) D {
							meta := fmt.Sprintf("%s · %s · %s", r.Entry.Preset, entryCountLabel(r.Entry),
								r.Entry.Created.Format("02.01 15:04"))
							return th.Dim(gtx, meta)
						}),
					)
				}),
				layout.Rigid(func(gtx C) D { return s.rowActions(gtx, r) }),
			)
		})
	})
}

// injectButton renders the row's Inject control by inject state: a spinner while in flight, then a
// transient green tick / red cross pill, otherwise the normal button.
func (s *AppState) injectButton(gtx C, r *LibraryRow) D {
	th := s.Th
	switch {
	case s.InjectingID == r.Entry.ID:
		return th.BusyPill(gtx, "Injecting…")
	case s.InjectResultID == r.Entry.ID && s.InjectOK:
		return th.StatusPill(gtx, "✓ Injected", th.Good)
	case s.InjectResultID == r.Entry.ID:
		return th.StatusPill(gtx, "✗ Failed", th.Bad)
	default:
		return th.SecondaryButton(gtx, &r.Inject, "Inject into FH6", true)
	}
}

func (s *AppState) rowThumb(gtx C, r *LibraryRow) D {
	th := s.Th
	sz := image.Pt(gtx.Dp(72), gtx.Dp(54))
	// The thumb is a click target — opens the full preview in a lightbox.
	return r.ThumbBtn.Layout(gtx, func(gtx C) D {
		fillRRect(gtx, th.SurfaceHi, sz, 6)
		pointer.CursorPointer.Add(gtx.Ops) // hand cursor over the clickable thumb
		gtx.Constraints = layout.Exact(sz)
		if (r.Thumb != paint.ImageOp{}) {
			defer clip.UniformRRect(image.Rectangle{Max: sz}, 6).Push(gtx.Ops).Pop()
			widget.Image{Src: r.Thumb, Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
		}
		return D{Size: sz}
	})
}

func (s *AppState) rowActions(gtx C, r *LibraryRow) D {
	th := s.Th
	// While renaming, the row's actions collapse to Save / Cancel — Inject/Export/Delete don't make
	// sense mid-edit. Save is an accent button at the SAME compact size as the other row buttons (a
	// full-size PrimaryButton was taller and made the row jump).
	if r.Editing {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return th.AccentButton(gtx, &r.Rename, "Save") }),
			layout.Rigid(GapH(8).Layout),
			layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &r.RenameCancel, "Cancel", true) }),
		)
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return s.injectButton(gtx, r) }),
		layout.Rigid(GapH(8).Layout),
		layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &r.Export, "Export JSON", true) }),
		layout.Rigid(GapH(8).Layout),
		layout.Rigid(func(gtx C) D { return th.SecondaryButton(gtx, &r.Rename, "Rename", true) }),
		layout.Rigid(GapH(8).Layout),
		layout.Rigid(func(gtx C) D {
			// Armed delete turns red (DangerButton) to make the destructive confirm unmistakable.
			if r.ConfirmDelete {
				return th.DangerButton(gtx, &r.Delete, "Confirm?")
			}
			return th.SecondaryButton(gtx, &r.Delete, "Delete", true)
		}),
	)
}
