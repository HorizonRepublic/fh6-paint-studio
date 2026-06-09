package stylize

import (
	"strings"
	"testing"

	"fh6-paint-studio/internal/model"
)

func inkMethodOf(p Preset) string {
	for _, s := range p.Stages {
		if s.Engine == "ink" {
			return string(s.Config)
		}
	}
	return ""
}
func fillCfgOf(p Preset) string {
	for _, s := range p.Stages {
		if s.Engine == "fill" {
			return string(s.Config)
		}
	}
	return ""
}

func TestAutoPresetHatchingPicksXDoG(t *testing.T) {
	f := StyleFeatures{Sat: 0.03, White: 0.4, Edges: 0.30, Flat: 0.44, Colors: 40, Fine: 0.13}
	if m := inkMethodOf(AutoPreset(f)); !strings.Contains(m, "xdog") {
		t.Errorf("hatched content should use xdog, got %s", m)
	}
}

func TestAutoPresetCelPicksVivid(t *testing.T) {
	f := StyleFeatures{Sat: 0.13, White: 0.4, Edges: 0.10, Flat: 0.72, Colors: 110, Fine: 0.05}
	p := AutoPreset(f)
	if fc := fillCfgOf(p); !strings.Contains(fc, "labvivid") {
		t.Errorf("clean cel should use labvivid, got %s", fc)
	}
	if p.Smooth.Method != "dtadaptive" {
		t.Errorf("clean cel should use dtadaptive smooth, got %s", p.Smooth.Method)
	}
	if m := inkMethodOf(p); !strings.Contains(m, "fdog") {
		t.Errorf("cel should keep fdog lines, got %s", m)
	}
}

func TestAutoPresetLineArtStaysDefault(t *testing.T) {
	f := StyleFeatures{Sat: 0.00, White: 0.94, Edges: 0.10, Flat: 0.88, Colors: 8, Fine: 0.05}
	p := AutoPreset(f)
	if m := inkMethodOf(p); !strings.Contains(m, "fdog") { // mostly-white → not hatching → fdog
		t.Errorf("line-art should use fdog, got %s", m)
	}
	if fc := fillCfgOf(p); !strings.Contains(fc, `"lab"`) || strings.Contains(fc, "labvivid") {
		t.Errorf("line-art (low sat) should use plain lab, got %s", fc)
	}
	if p.Smooth.Method != "dt" {
		t.Errorf("line-art should use plain dt, got %s", p.Smooth.Method)
	}
}

func TestAutoPresetBusyColourNotHatching(t *testing.T) {
	// high edges but COLOURFUL → fdog (clean lines), not the mono-hatching xdog branch.
	f := StyleFeatures{Sat: 0.11, White: 0.06, Edges: 0.33, Flat: 0.43, Colors: 130, Fine: 0.14}
	if m := inkMethodOf(AutoPreset(f)); !strings.Contains(m, "fdog") {
		t.Errorf("busy colourful content should use fdog, got %s", m)
	}
}

func TestAnalyzeSeparatesLineArtFromColour(t *testing.T) {
	mk := func(colourful bool) *SrcImage {
		w, h := 64, 64
		pix := make([]model.RGBA, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if colourful {
					pix[y*w+x] = model.RGBA{R: 0.8, G: 0.2, B: 0.5, A: 1}
				} else {
					pix[y*w+x] = model.RGBA{R: 1, G: 1, B: 1, A: 1} // white field
				}
			}
		}
		if !colourful {
			for i := 0; i < w*h; i += 7 { // sparse dark line pixels
				pix[i] = model.RGBA{A: 1}
			}
		}
		return &SrcImage{W: w, H: h, Pix: pix}
	}
	lineart := Analyze(mk(false))
	colour := Analyze(mk(true))
	if lineart.Sat > 0.05 {
		t.Errorf("line-art sat should be ~0, got %.3f", lineart.Sat)
	}
	if colour.Sat < 0.2 {
		t.Errorf("colourful sat should be high, got %.3f", colour.Sat)
	}
	if lineart.White < 0.5 {
		t.Errorf("line-art white fraction should be high, got %.3f", lineart.White)
	}
}
