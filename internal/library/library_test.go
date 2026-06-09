package library

import (
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fh6-paint-studio/internal/model"
)

func TestOpenAndPaths(t *testing.T) {
	root := t.TempDir()
	s := Open(root)
	if s.Root != root {
		t.Fatalf("Root = %q, want %q", s.Root, root)
	}
	if got := s.GeometryPath("abc"); got != filepath.Join(root, "abc", "geometry.json") {
		t.Fatalf("GeometryPath = %q", got)
	}
}

func TestNewIDFormat(t *testing.T) {
	e := Entry{Name: "Super Image!.jpg", Created: time.Date(2026, 6, 2, 14, 30, 12, 0, time.UTC)}
	id := newID(e)
	if want := "20260602-143012-super-image-jpg"; id != want {
		t.Fatalf("newID = %q, want %q", id, want)
	}
}

func TestSaveDecalRoundTrip(t *testing.T) {
	s := Open(t.TempDir())
	src := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	shapes := []model.Shape{
		{Type: 2, Data: []float64{0, 0, 1, 1}, Color: []int{0, 0, 0, 255}}, // bg
		{Type: 16, Data: []float64{10, 10, 5, 5}, Color: []int{255, 0, 0, 255}},
	}
	meta := Entry{Name: "pic.jpg", Preset: "anime", Width: 300, Height: 200, Budget: 1000,
		Seed: 1, InjectScale: 1.0, Created: time.Date(2026, 6, 2, 14, 30, 12, 0, time.UTC)}
	got, err := s.Save(shapes, src, meta)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.Kind != KindDecal || got.Shapes != 1 {
		t.Fatalf("entry = %+v, want Kind=decal Shapes=1 nonempty ID", got)
	}
	for _, p := range []string{"geometry.json", "preview.png", "thumb.png", "meta.json"} {
		if _, err := os.Stat(filepath.Join(s.Dir(got.ID), p)); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
	tf, err := os.Open(s.ThumbPath(got.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer tf.Close()
	cfg, _, err := image.DecodeConfig(tf)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width > thumbMax || cfg.Height > thumbMax {
		t.Fatalf("thumb %dx%d exceeds %d", cfg.Width, cfg.Height, thumbMax)
	}
	g, err := s.LoadGeometry(got.ID)
	if err != nil || len(g.Shapes) != 2 {
		t.Fatalf("LoadGeometry = %d shapes, %v", len(g.Shapes), err)
	}
}

func TestListOrderingAndDelete(t *testing.T) {
	s := Open(t.TempDir())
	mk := func(name string, when time.Time) Entry {
		e, err := s.Save([]model.Shape{{}, {}}, image.NewNRGBA(image.Rect(0, 0, 8, 8)),
			Entry{Name: name, Created: when})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	old := mk("old", time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC))
	_ = mk("new", time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC))
	list, err := s.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %d, %v", len(list), err)
	}
	if list[0].Name != "new" { // newest first
		t.Fatalf("order: got %q first", list[0].Name)
	}
	_ = os.MkdirAll(filepath.Join(s.Root, "garbage"), 0o755) // no meta.json -> skipped
	if l2, _ := s.List(); len(l2) != 2 {
		t.Fatalf("corrupt dir not skipped: %d", len(l2))
	}
	if err := s.Delete(old.ID); err != nil {
		t.Fatal(err)
	}
	if l3, _ := s.List(); len(l3) != 1 || l3[0].Name != "new" {
		t.Fatalf("after delete: %+v", l3)
	}
}

func TestRename(t *testing.T) {
	s := Open(t.TempDir())
	e, err := s.Save([]model.Shape{{}, {}}, image.NewNRGBA(image.Rect(0, 0, 8, 8)),
		Entry{Name: "old name", Created: time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Rename(e.ID, "  My Livery  ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "My Livery" { // trimmed
		t.Fatalf("Rename name = %q, want %q", got.Name, "My Livery")
	}
	if got.ID != e.ID { // ID/dir stays stable
		t.Fatalf("Rename changed ID %q -> %q", e.ID, got.ID)
	}
	list, _ := s.List()
	if len(list) != 1 || list[0].Name != "My Livery" {
		t.Fatalf("after rename List = %+v", list)
	}
	if g, err := s.LoadGeometry(e.ID); err != nil || len(g.Shapes) != 2 {
		t.Fatalf("geometry after rename = %d shapes, %v", len(g.Shapes), err)
	}
	if _, err := s.Rename(e.ID, "   "); err == nil {
		t.Fatal("empty name should error")
	}
	if _, err := s.Rename("bad/id", "x"); err == nil {
		t.Fatal("invalid id should error")
	}
}

func TestListMissingRoot(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "nope"))
	if l, err := s.List(); err != nil || l != nil {
		t.Fatalf("List on missing root = %v, %v (want nil,nil)", l, err)
	}
}
