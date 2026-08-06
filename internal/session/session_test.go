package session

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"fh6-paint-studio/internal/preset"
)

// writeTestImage writes a w×h PNG whose every border pixel differs from its neighbours, so the
// auto-crop is a guaranteed no-op and the prepared dims depend only on the fit resolution.
func writeTestImage(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x*7 + y*13), uint8(x * 3), uint8(y * 5), 255})
		}
	}
	p := filepath.Join(t.TempDir(), "src.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func prepare(t *testing.T, req Request) *Run {
	t.Helper()
	run, err := Prepare(req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return run
}

// The fit resolution must engage exactly where it was measured to pay: flat and anime fit at native
// because thin strokes at the display cap degrade to a pixel of gray the search cannot recover;
// photo measured a wash and stays where the client is showing the image.
func TestFitResolution(t *testing.T) {
	big := writeTestImage(t, 1400, 2400) // a display load would cap this at 641x1100

	for _, mode := range []string{"flat", "anime"} {
		run := prepare(t, Request{Path: big, DisplayRes: DisplayRes, Choices: choices(mode)})
		if run.Prep.W != 1400 || run.Prep.H != 2400 {
			t.Errorf("%s fitted at %dx%d, want native 1400x2400", mode, run.Prep.W, run.Prep.H)
		}
	}

	run := prepare(t, Request{Path: big, DisplayRes: DisplayRes, Choices: choices("photo")})
	if run.Prep.W != 641 || run.Prep.H != 1100 {
		t.Errorf("photo fitted at %dx%d, want the display cap 641x1100", run.Prep.W, run.Prep.H)
	}

	// "Use source resolution" overrides the mode, photo included.
	run = prepare(t, Request{Path: big, DisplayRes: DisplayRes, SourceRes: true, Choices: choices("photo")})
	if run.Prep.W != 1400 || run.Prep.H != 2400 {
		t.Errorf("source-res photo fitted at %dx%d, want native 1400x2400", run.Prep.W, run.Prep.H)
	}

	// A source the display cap never truncated is unaffected by the fit resolution.
	small := writeTestImage(t, 800, 900)
	run = prepare(t, Request{Path: small, DisplayRes: DisplayRes, Choices: choices("flat")})
	if run.Prep.W != 800 || run.Prep.H != 900 {
		t.Errorf("small source fitted at %dx%d, want its own 800x900", run.Prep.W, run.Prep.H)
	}
}

// A region is an ABSOLUTE rectangle in the raw file. This is the property the fractional form could
// not hold: the studio composes a crop onto a previous crop, so "a fraction" has no fixed referent,
// and getting it wrong returns a convincing reconstruction of the wrong part of the picture.
func TestRegionIsAbsolute(t *testing.T) {
	big := writeTestImage(t, 1400, 2400)
	top := image.Rect(0, 0, 1400, 1200)
	run := prepare(t, Request{Path: big, DisplayRes: DisplayRes, Region: &top, Choices: choices("anime")})
	if run.Prep.W != 1400 || run.Prep.H != 1200 {
		t.Errorf("region run fitted at %dx%d, want the rect's own 1400x1200", run.Prep.W, run.Prep.H)
	}

	// An off-origin rect must be taken from where it says, not from the top-left.
	mid := image.Rect(200, 600, 1000, 1400)
	run = prepare(t, Request{Path: big, DisplayRes: DisplayRes, Region: &mid, Choices: choices("anime")})
	if run.Prep.W != 800 || run.Prep.H != 800 {
		t.Errorf("off-origin region fitted at %dx%d, want 800x800", run.Prep.W, run.Prep.H)
	}
}

// The surround is engine-side detail: it changes what the engine fits, and the view dimensions the
// results come back in must not move with it.
func TestKeepInsideSurround(t *testing.T) {
	src := writeTestImage(t, 800, 900)
	plain := prepare(t, Request{Path: src, DisplayRes: DisplayRes, Choices: choices("flat")})
	padded := prepare(t, Request{Path: src, DisplayRes: DisplayRes, KeepInside: true, Choices: choices("flat")})

	if padded.PadPx <= 0 {
		t.Fatalf("keep-inside produced no surround (PadPx %d)", padded.PadPx)
	}
	if padded.ViewW != plain.Prep.W || padded.ViewH != plain.Prep.H {
		t.Errorf("view dims %dx%d, want the unpadded %dx%d", padded.ViewW, padded.ViewH, plain.Prep.W, plain.Prep.H)
	}
	if wantW := plain.Prep.W + 2*padded.PadPx; padded.Prep.W != wantW {
		t.Errorf("padded width %d, want %d", padded.Prep.W, wantW)
	}
	if wantH := plain.Prep.H + 2*padded.PadPx; padded.Prep.H != wantH {
		t.Errorf("padded height %d, want %d", padded.Prep.H, wantH)
	}
}

// A hybrid preset holds part of the budget back for the ink lines appended after the fill. Losing
// this split does not fail loudly — it silently overruns the budget the game enforces.
func TestHybridReservesInkBudget(t *testing.T) {
	src := writeTestImage(t, 400, 400)
	ch := choices("anime-ink")
	ch.Shapes = 500
	ch.InkRatio = 0.20
	run := prepare(t, Request{Path: src, DisplayRes: DisplayRes, Choices: ch})

	want := preset.InkBudget(ch.InkRatio, ch.Shapes)
	if want <= 0 {
		t.Fatalf("test setup: InkBudget returned %d", want)
	}
	if run.Ink != want {
		t.Errorf("reserved %d ink lines, want %d", run.Ink, want)
	}
	if got := run.Resolved.Options.StopAt; got != ch.Shapes-want {
		t.Errorf("fill budget %d, want %d — ink plus fill must not exceed the requested %d",
			got, ch.Shapes-want, ch.Shapes)
	}
}

func TestPrepareRejectsAnEmptyPath(t *testing.T) {
	if _, err := Prepare(Request{Choices: choices("flat")}); err == nil {
		t.Error("Prepare accepted a request with no source")
	}
}

func choices(mode string) preset.Choices {
	ch := preset.DefaultChoices()
	ch.Mode = mode
	ch.Shapes = 50
	return ch
}
