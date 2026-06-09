package preset

import "testing"

func TestIsHybridMode(t *testing.T) {
	for _, m := range []string{"lineart", "line-art", "anime-ink", "ANIME-INK", "hybrid"} {
		if !IsHybridMode(m) {
			t.Errorf("IsHybridMode(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"anime", "photo", "flat", "gaussian", ""} {
		if IsHybridMode(m) {
			t.Errorf("IsHybridMode(%q) = true, want false", m)
		}
	}
}

// The two hybrid presets differ ONLY in their fill mode: lineart fills OPAQUE (flat — no semi-transparent
// casts banding a white background), anime-ink fills semi-transparent (anime — alive eyes/gradients).
func TestHybridPresetMode(t *testing.T) {
	if got := PresetMode("lineart"); got != "flat" {
		t.Errorf("PresetMode(lineart) = %q, want flat (opaque fill, no white facets)", got)
	}
	if got := PresetMode("anime-ink"); got != "anime" {
		t.Errorf("PresetMode(anime-ink) = %q, want anime (semi-transparent fill)", got)
	}
}

func TestDefaultInkRatio(t *testing.T) {
	if line, ink := DefaultInkRatio("lineart"), DefaultInkRatio("anime-ink"); line <= ink {
		t.Errorf("lineart ratio (%.2f) should exceed anime-ink (%.2f)", line, ink)
	}
	if r := DefaultInkRatio("photo"); r != 0 {
		t.Errorf("DefaultInkRatio(photo) = %.2f, want 0 (not hybrid)", r)
	}
	if r := DefaultInkRatio("lineart"); r > MaxInkRatio {
		t.Errorf("DefaultInkRatio(lineart) = %.2f exceeds MaxInkRatio %.2f", r, MaxInkRatio)
	}
}

func TestInkBudget(t *testing.T) {
	cases := []struct {
		ratio  float64
		shapes int
		want   int
	}{
		{0, 500, 0},     // no lines -> pure geometrize
		{0.4, 500, 200}, // line-art default
		{0.2, 500, 100}, // anime-ink default
		{0.5, 500, 250}, // the cap
		{0.9, 500, 250}, // clamped to MaxInkRatio
		{-0.1, 500, 0},  // negative clamped to 0
		{0.5, 1, 0},     // always leave >=1 fill shape
	}
	for _, c := range cases {
		if got := InkBudget(c.ratio, c.shapes); got != c.want {
			t.Errorf("InkBudget(%.2f, %d) = %d, want %d", c.ratio, c.shapes, got, c.want)
		}
	}
}
