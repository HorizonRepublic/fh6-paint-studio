package stylize

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"fh6-paint-studio/internal/model"
)

// dummyEngine emits one full-canvas rect of a configured colour — a stand-in to test the DI wiring.
type dummyEngine struct{ r, g, b int }

func (d *dummyEngine) Name() string { return "dummytest" }
func (d *dummyEngine) Generate(ctx *Context) ([]model.Shape, error) {
	return []model.Shape{{Type: model.TypeRotatedRectangle, Color: []int{d.r, d.g, d.b, 255},
		Data: []float64{float64(ctx.Src.W) / 2, float64(ctx.Src.H) / 2, 5, 5, 0}}}, nil
}

func TestRunComposesPresetStages(t *testing.T) {
	RegisterEngine("dummytest", func(cfg json.RawMessage) (Engine, error) {
		var c struct{ R, G, B int }
		_ = json.Unmarshal(cfg, &c)
		return &dummyEngine{c.R, c.G, c.B}, nil
	})
	RegisterPreset(Preset{Name: "dummytest", Stages: []Stage{
		{Engine: "dummytest", Config: json.RawMessage(`{"R":255}`)},
	}})

	src := &SrcImage{W: 8, H: 8, Pix: make([]model.RGBA, 64)}
	for i := range src.Pix {
		src.Pix[i] = model.RGBA{R: 0, G: 0, B: 1, A: 1} // all-blue source
	}
	g, err := Run(src, "dummytest", 3000)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Shapes) != 2 {
		t.Fatalf("want bg + 1 dummy = 2 shapes, got %d", len(g.Shapes))
	}
	if g.Shapes[0].Color[2] < 200 || g.Shapes[0].Color[0] > 40 {
		t.Errorf("bg should be ~blue (avg of source), got %v", g.Shapes[0].Color)
	}
	if g.Shapes[1].Color[0] != 255 {
		t.Errorf("dummy stage should be red, got %v", g.Shapes[1].Color)
	}
}

func TestRunUnknownPreset(t *testing.T) {
	if _, err := Run(&SrcImage{W: 1, H: 1, Pix: []model.RGBA{{}}}, "nope-not-registered", 100); err == nil {
		t.Error("expected error for unknown preset")
	}
}

func TestLoadDownscale(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{200, 30, 30, 255})
		}
	}
	p := filepath.Join(t.TempDir(), "x.png")
	f, _ := os.Create(p)
	_ = png.Encode(f, img)
	_ = f.Close()

	s, err := Load(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if s.W != 10 || s.H != 5 {
		t.Fatalf("downscale = %dx%d, want 10x5", s.W, s.H)
	}
	c := s.Pix[s.W*s.H/2]
	if c.R < 0.6 || c.G > 0.3 {
		t.Errorf("centre pixel not reddish: %+v", c)
	}
}
