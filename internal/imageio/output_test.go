package imageio

import (
	"os"
	"path/filepath"
	"testing"

	"fh6-paint-studio/internal/model"
)

func TestWriteGeometryRoundtrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "g.json")
	g := model.Geometry{Shapes: []model.Shape{{Type: 16, Data: []float64{1, 2, 3, 4, 0}, Color: []int{1, 2, 3, 255}}}}
	if err := WriteGeometry(p, g); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

func TestSavePreview(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.png")
	px := make([]float32, 4*4*4)
	for i := range px {
		px[i] = 1
	}
	if err := SavePreview(p, px, 4, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("preview not written: %v", err)
	}
}
