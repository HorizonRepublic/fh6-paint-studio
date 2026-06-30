// Package maskbank holds the FH6 native-dictionary silhouettes ("words") as embedded 256² coverage
// textures. At init it decodes each mask and registers its word with the model (KindMaskBase+i, in
// manifest order), so the engine can render any dictionary shape 1:1 with the game. The assets are
// produced by the local calibration tooling (debug/, not published): exact vector silhouettes with
// sub-pixel edges and true gradient ramps, framed by the calibrated manifest extents.
package maskbank

import (
	"bytes"
	"embed"
	"encoding/json"
	"image/png"
	"strings"

	"fh6-paint-studio/internal/model"
)

//go:embed masks/*.png manifest.json
var assets embed.FS

// Entry is one decoded mask: a W×H coverage grid (0..1, row-major, v=0 = top) plus the word's native
// size and the ShapeKind it registered as. Category groups the word for the editor palette.
type Entry struct {
	Word             uint16
	Kind             model.ShapeKind
	NativeW, NativeH float32
	W, H             int
	Cov              []float32
	Label            string
	Category         string // "primitive" | "curve" | "decorative" | "glyph"
}

// letterWords is the curated set of alphabet glyph words in the bank (the "glyph" pool is otherwise a
// grab-bag of decorative decals). This is the contiguous lowercase block 0x04b1..0x04ca — a..z with the
// font's gaps (no e/p/q/u/y in this bank); there are no digit glyphs.
var letterWords = func() map[uint16]bool {
	m := map[uint16]bool{}
	for w := uint16(0x04b1); w <= 0x04ca; w++ {
		m[w] = true
	}
	return m
}()

// classify buckets a word for the editor palette: the three hard primitives (surfaced separately),
// curated letters, curve/arc shapes (by label), and everything else as decorative decals/symbols.
func classify(label string, word uint16) string {
	switch word {
	case 0x65, 0x66, 0x68:
		return "primitive"
	}
	if letterWords[word] {
		return "letter"
	}
	for _, p := range []string{"arc", "gentlearc", "straight", "crescent", "openC", "swoosh", "wave",
		"half", "quarter", "banana", "boomerang", "lens", "capsule", "dashed", "break", "checkmark"} {
		if strings.HasPrefix(label, p) {
			return "curve"
		}
	}
	return "decal" // glyph grab-bag (stars/flames/flags/…) + comma/ring/teardrop/gradbar
}

type manifestShape struct {
	Word    uint16  `json:"word"`
	Label   string  `json:"label"`
	NativeW float32 `json:"native_w"`
	NativeH float32 `json:"native_h"`
	File    string  `json:"file"`
}

type manifestDoc struct {
	Res    int             `json:"res"`
	Shapes []manifestShape `json:"shapes"`
}

var entries []Entry

func init() {
	raw, err := assets.ReadFile("manifest.json")
	if err != nil {
		panic("maskbank: read manifest: " + err.Error())
	}
	var doc manifestDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		panic("maskbank: parse manifest: " + err.Error())
	}
	for _, ms := range doc.Shapes {
		cov, w, h := decode(ms.File)
		kind := model.RegisterMaskWord(ms.Word, ms.NativeW, ms.NativeH)
		entries = append(entries, Entry{
			Word: ms.Word, Kind: kind, NativeW: ms.NativeW, NativeH: ms.NativeH,
			W: w, H: h, Cov: cov,
			Label: ms.Label, Category: classify(ms.Label, ms.Word),
		})
	}
}

func decode(file string) (cov []float32, w, h int) {
	raw, err := assets.ReadFile("masks/" + file)
	if err != nil {
		panic("maskbank: read mask " + file + ": " + err.Error())
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		panic("maskbank: decode mask " + file + ": " + err.Error())
	}
	b := img.Bounds()
	w, h = b.Dx(), b.Dy()
	cov = make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, _, _, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA() // grayscale -> r=g=b, 16-bit
			cov[y*w+x] = float32(r) / 65535
		}
	}
	return cov, w, h
}

// All returns the decoded mask bank in registration order (== KindMaskBase+i order).
func All() []Entry { return entries }
