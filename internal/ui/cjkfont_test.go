package ui

import "testing"

// The bundled CJK font must parse, otherwise Chinese/Japanese/Korean would silently render as tofu.
func TestCJKFontParses(t *testing.T) {
	if cjkFontFace() == nil {
		t.Fatal("bundled Noto Sans CJK font failed to parse")
	}
}
