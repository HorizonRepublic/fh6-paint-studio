package ui

import (
	_ "embed"

	"gioui.org/font"
	"gioui.org/font/opentype"
	"gioui.org/text"
)

//go:embed fonts/NotoSansCJKsc-Regular.otf
var cjkOTF []byte

// cjkFontFace parses the bundled Noto Sans CJK face once, for the shaper's fallback chain: Latin and
// Cyrillic come from the Go fonts (listed first), and Chinese/Japanese/Korean runes fall back to this.
// Returns nil if parsing fails, so the app still runs (CJK would just render as tofu boxes).
func cjkFontFace() []text.FontFace {
	face, err := opentype.Parse(cjkOTF)
	if err != nil {
		return nil
	}
	return []text.FontFace{{Font: font.Font{Typeface: "Noto Sans CJK SC"}, Face: face}}
}
