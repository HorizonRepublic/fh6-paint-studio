package ui

import (
	"image"
	"image/color"
	"math"

	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"fh6-paint-studio/internal/i18n"
	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/model"
)

const bankCols = 4 // thumbnails per row in the picker grid

// bankRow is one entry in the picker's flat scroll plan: either a category header or a row of thumbnails.
type bankRow struct {
	header string // i18n key of a category header; empty for a thumbnail row
	thumbs []int  // maskbank.All() indices for a thumbnail row
}

// ensureBankBuilt lazily renders every word's thumbnail and the category row plan, once per session.
func (s *AppState) ensureBankBuilt() {
	if s.bankThumbsBuilt {
		return
	}
	entries := maskbank.All()
	s.bankBtns = make([]widget.Clickable, len(entries))
	s.bankThumbs = make([]paint.ImageOp, len(entries))
	for i, e := range entries {
		s.bankThumbs[i] = paint.NewImageOp(bankThumbImage(e, 64, s.Th.Text))
	}
	s.bankRows = buildBankRows(entries, bankCols)
	s.bankList.Axis = layout.Vertical
	s.bankThumbsBuilt = true
}

// buildBankRows groups entries by category (in display order) into header + chunked thumbnail rows. The
// hard primitives are skipped — they live in the always-present quick-add row.
func buildBankRows(entries []maskbank.Entry, cols int) []bankRow {
	order := []struct{ cat, key string }{
		{"letter", "editor.cat_letters"},
		{"curve", "editor.cat_curves"},
		{"decal", "editor.cat_decals"},
	}
	var rows []bankRow
	for _, o := range order {
		var idxs []int
		for i, e := range entries {
			if e.Category == o.cat {
				idxs = append(idxs, i)
			}
		}
		if len(idxs) == 0 {
			continue
		}
		rows = append(rows, bankRow{header: o.key})
		for i := 0; i < len(idxs); i += cols {
			end := i + cols
			if end > len(idxs) {
				end = len(idxs)
			}
			rows = append(rows, bankRow{thumbs: append([]int(nil), idxs[i:end]...)})
		}
	}
	return rows
}

// handleBankActions processes every thumbnail click (insert the word).
func (s *AppState) handleBankActions(gtx C) {
	entries := maskbank.All()
	for i := range s.bankBtns {
		if s.bankBtns[i].Pressed() && s.bankCandKind == 0 && !s.bankDragging && i < len(entries) {
			s.bankCandKind, s.bankCandWord = 1, int(entries[i].Word) // a press that may become a drag
		}
		if s.bankBtns[i].Clicked(gtx) && s.doubleClicked(1, i, gtx.Now) {
			s.insertBankWord(i)
		}
	}
}

// bankGrid is the scrollable list of category headers + thumbnail rows.
func (s *AppState) bankGrid(gtx C) D {
	th := s.Th
	return material.List(th.M, &s.bankList).Layout(gtx, len(s.bankRows), func(gtx C, i int) D {
		row := s.bankRows[i]
		if row.header != "" {
			return s.bankHeader(gtx, i18n.T(row.header), i == 0)
		}
		return s.bankThumbRow(gtx, row.thumbs)
	})
}

// bankHeader is a prominent category divider: an accent label with a rule line, so each group reads as a
// distinct block.
func (s *AppState) bankHeader(gtx C, text string, first bool) D {
	th := s.Th
	top := 10
	if first {
		top = 2
	}
	return layout.Inset{Top: unit.Dp(top), Bottom: 6}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return th.Lbl(gtx, 14, text, th.Accent) }),
			layout.Rigid(GapV(4).Layout),
			layout.Rigid(func(gtx C) D {
				w := gtx.Constraints.Max.X
				paint.FillShape(gtx.Ops, th.Border, clip.Rect(image.Rect(0, 0, w, gtx.Dp(1))).Op())
				return D{Size: image.Pt(w, gtx.Dp(1))}
			}),
		)
	})
}

// bankThumbRow lays up to bankCols thumbnail cells in a row (padding empty cells to keep cell width).
func (s *AppState) bankThumbRow(gtx C, idxs []int) D {
	children := make([]layout.FlexChild, 0, bankCols*2)
	for j := 0; j < bankCols; j++ {
		if j > 0 {
			children = append(children, layout.Rigid(GapH(6).Layout))
		}
		if j < len(idxs) {
			i := idxs[j]
			children = append(children, layout.Flexed(1, func(gtx C) D { return s.bankThumbBtn(gtx, i) }))
		} else {
			children = append(children, layout.Flexed(1, func(gtx C) D { return D{} }))
		}
	}
	return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx C) D {
		return layout.Flex{}.Layout(gtx, children...)
	})
}

// bankThumbBtn is one square thumbnail cell that inserts its word on click.
func (s *AppState) bankThumbBtn(gtx C, i int) D {
	th := s.Th
	b := &s.bankBtns[i]
	return material.Clickable(gtx, b, func(gtx C) D {
		side := gtx.Constraints.Max.X
		gtx.Constraints = layout.Exact(image.Pt(side, side))
		return layout.Background{}.Layout(gtx,
			func(gtx C) D {
				sz := gtx.Constraints.Min
				bg := th.SurfaceHi
				if b.Hovered() {
					bg = th.Surface
				}
				fillRRect(gtx, bg, sz, 6)
				pointer.CursorPointer.Add(gtx.Ops)
				return D{Size: sz}
			},
			func(gtx C) D {
				return layout.UniformInset(7).Layout(gtx, func(gtx C) D {
					return widget.Image{Src: s.bankThumbs[i], Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
				})
			},
		)
	})
}

// insertBankWord adds the i-th bank word as a centred, native-aspect mask shape, selected, on top.
func (s *AppState) insertBankWord(i int) {
	entries := maskbank.All()
	if i < 0 || i >= len(entries) {
		return
	}
	s.addShape(defaultMaskShape(entries[i], s.EditW, s.EditH))
}

// defaultMaskShape builds a centred mask placement: Type = the dictionary word, Data = full extents
// (slots 2,3 are full width/height for masks) sized so the larger native axis is ~1/3 of the canvas.
func defaultMaskShape(e maskbank.Entry, w, h int) model.Shape {
	cx, cy := float64(w)/2, float64(h)/2
	target := math.Min(float64(w), float64(h)) / 3
	nw, nh := float64(e.NativeW), float64(e.NativeH)
	if nw <= 0 || nh <= 0 {
		nw, nh = 1, 1
	}
	var fw, fh float64
	if nw >= nh {
		fw, fh = target, target*nh/nw
	} else {
		fh, fw = target, target*nw/nh
	}
	return model.Shape{Type: int(e.Word), Data: []float64{cx, cy, fw, fh, 0, 0}, Color: []int{180, 180, 180, 255}}
}

// bankThumbImage renders a word's coverage into a size² thumbnail (foreground colour, alpha = coverage),
// preserving the word's native aspect and centring it.
func bankThumbImage(e maskbank.Entry, size int, fg color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	if e.W <= 0 || e.H <= 0 || len(e.Cov) < e.W*e.H {
		return img
	}
	scale := math.Min(float64(size)/float64(e.W), float64(size)/float64(e.H))
	dw, dh := int(scale*float64(e.W)), int(scale*float64(e.H))
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	ox, oy := (size-dw)/2, (size-dh)/2
	for y := 0; y < dh; y++ {
		sy := int(float64(y) / float64(dh) * float64(e.H))
		if sy >= e.H {
			sy = e.H - 1
		}
		for x := 0; x < dw; x++ {
			sx := int(float64(x) / float64(dw) * float64(e.W))
			if sx >= e.W {
				sx = e.W - 1
			}
			a := e.Cov[sy*e.W+sx]
			p := ((oy+y)*size + (ox + x)) * 4
			img.Pix[p+0] = fg.R
			img.Pix[p+1] = fg.G
			img.Pix[p+2] = fg.B
			img.Pix[p+3] = uint8(a*255 + 0.5)
		}
	}
	return img
}

// maskByWord caches a word→entry lookup for the whole bank (built once on first use).
var maskByWord map[uint16]maskbank.Entry

func maskEntryByWord(word uint16) (maskbank.Entry, bool) {
	if maskByWord == nil {
		maskByWord = make(map[uint16]maskbank.Entry)
		for _, e := range maskbank.All() {
			maskByWord[e.Word] = e
		}
	}
	e, ok := maskByWord[word]
	return e, ok
}

// maskThumbOp returns a cached glyph silhouette thumbnail for a mask word (theme-text coloured).
func (s *AppState) maskThumbOp(word uint16) (paint.ImageOp, bool) {
	if op, ok := s.layerThumbs[word]; ok {
		return op, true
	}
	e, ok := maskEntryByWord(word)
	if !ok {
		return paint.ImageOp{}, false
	}
	if s.layerThumbs == nil {
		s.layerThumbs = map[uint16]paint.ImageOp{}
	}
	op := paint.NewImageOp(bankThumbImage(e, 48, s.Th.Text))
	s.layerThumbs[word] = op
	return op, true
}

// layerIcon draws the layer-row glyph: the actual mask silhouette for mask shapes (a letter looks like a
// letter, not a generic circle), or the primitive icon otherwise.
func (s *AppState) layerIcon(gtx C, sh model.Shape) D {
	if model.IsMask(model.KindFromType(sh.Type)) {
		if op, ok := s.maskThumbOp(uint16(sh.Type)); ok {
			sz := gtx.Dp(16)
			gtx.Constraints = layout.Exact(image.Pt(sz, sz))
			return widget.Image{Src: op, Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
		}
	}
	return drawShapeIcon(gtx, iconForShape(sh), s.Th.Text, true)
}
