// Package library persists completed Studio generations under the user's home folder so reconstructions
// can be browsed, re-injected, and re-exported without regenerating. Each generation is one directory:
// geometry.json (importer-compatible shapes), preview.png (the reconstruction), thumb.png (a <=128px row
// thumbnail), and meta.json (Entry). Pure Go (no Gio) so it is unit-testable.
package library

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"

	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
)

// Entry is the metadata for one saved generation (meta.json).
type Entry struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`              // display name (source basename, no ext)
	Source      string    `json:"source"`            // original source path (informational)
	Preset      string    `json:"preset"`            // anime|photo|flat
	Shapes      int       `json:"shapes"`            // FH6 shape count = len(shapes)-1 (excludes background)
	Width       int       `json:"width"`             // render width  (CanvasMap on inject)
	Height      int       `json:"height"`            // render height
	Budget      int       `json:"budget"`            // requested shape budget
	Seed        int64     `json:"seed"`
	InjectScale float64   `json:"injectScale"` // scale used at gen time (default for inject)
	Created     time.Time `json:"created"`
	Kind        string    `json:"kind"` // always KindDecal
}

const (
	KindDecal = "decal"
	thumbMax  = 128
)

// Store is a library rooted at a directory.
type Store struct{ Root string }

// Open returns a Store at root (created lazily on first Save).
func Open(root string) *Store { return &Store{Root: root} }

// DefaultRoot returns <home>/FH6PaintStudio/library, creating it if missing.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, "FH6PaintStudio", "library")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

func (s *Store) Dir(id string) string          { return filepath.Join(s.Root, id) }
func (s *Store) GeometryPath(id string) string { return filepath.Join(s.Root, id, "geometry.json") }
func (s *Store) PreviewPath(id string) string  { return filepath.Join(s.Root, id, "preview.png") }
func (s *Store) ThumbPath(id string) string    { return filepath.Join(s.Root, id, "thumb.png") }

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(name string) string {
	s := slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "gen"
	}
	return s
}

// newID builds "<YYYYMMDD-HHMMSS>-<slug>" from the entry's Created + Name.
func newID(e Entry) string { return e.Created.Format("20060102-150405") + "-" + slug(e.Name) }

// mkEntryDir creates a unique entry directory under Root for meta and returns its path.
func (s *Store) mkEntryDir(meta Entry) (string, error) {
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return "", err
	}
	base := newID(meta)
	id := base
	for i := 1; ; i++ {
		dir := filepath.Join(s.Root, id)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return dir, os.MkdirAll(dir, 0o755)
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

// Save writes geometry.json, preview.png, thumb.png, meta.json under a new ID dir and returns the stored
// Entry (ID + Shapes + Kind filled). shapes[0] is the background (excluded from the count).
func (s *Store) Save(shapes []model.Shape, preview *image.NRGBA, meta Entry) (Entry, error) {
	meta.Kind = KindDecal
	meta.Shapes = len(shapes) - 1
	if meta.Shapes < 0 {
		meta.Shapes = 0
	}
	dir, err := s.mkEntryDir(meta)
	if err != nil {
		return Entry{}, err
	}
	meta.ID = filepath.Base(dir)
	if err := imageio.WriteGeometry(filepath.Join(dir, "geometry.json"), model.Geometry{Shapes: shapes}); err != nil {
		return Entry{}, err
	}
	if err := writePNG(filepath.Join(dir, "preview.png"), preview); err != nil {
		return Entry{}, err
	}
	if err := writePNG(filepath.Join(dir, "thumb.png"), thumbnail(preview)); err != nil {
		return Entry{}, err
	}
	return meta, writeMeta(dir, meta)
}

// LoadGeometry reads a saved decal's shapes.
func (s *Store) LoadGeometry(id string) (model.Geometry, error) {
	b, err := os.ReadFile(s.GeometryPath(id))
	if err != nil {
		return model.Geometry{}, err
	}
	var g model.Geometry
	return g, json.Unmarshal(b, &g)
}

func writeMeta(dir string, meta Entry) error {
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644)
}

func writePNG(path string, img image.Image) error {
	if img == nil {
		return nil
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// List scans Root and returns entries newest-first (by Created). Dirs without a valid meta.json are
// skipped. A missing Root returns (nil, nil) — an empty library, not an error.
func (s *Store) List() ([]Entry, error) {
	des, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.Root, de.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var e Entry
		if json.Unmarshal(b, &e) != nil {
			continue
		}
		e.ID = de.Name() // the dir name is authoritative
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// Delete removes an entry directory. The id must be a bare dir name (no path separators).
func (s *Store) Delete(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("library: invalid id %q", id)
	}
	return os.RemoveAll(filepath.Join(s.Root, id))
}

// Rename updates a saved generation's display name (rewriting meta.json). The directory and ID are
// left unchanged so on-disk paths and any cached references stay valid — only the Name field moves.
// The id must be a bare dir name; the name is trimmed and must be non-empty. Returns the updated entry.
func (s *Store) Rename(id, name string) (Entry, error) {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return Entry{}, fmt.Errorf("library: invalid id %q", id)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Entry{}, fmt.Errorf("library: empty name")
	}
	dir := filepath.Join(s.Root, id)
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal(b, &e); err != nil {
		return Entry{}, err
	}
	e.ID, e.Name = id, name
	return e, writeMeta(dir, e)
}

// thumbnail downscales src so its long side is <= thumbMax (CatmullRom, matching imageio); returns src
// unchanged when already small enough, nil when src is nil.
func thumbnail(src *image.NRGBA) *image.NRGBA {
	if src == nil {
		return nil
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= thumbMax && h <= thumbMax {
		return src
	}
	scale := float64(thumbMax) / float64(max(w, h))
	dw, dh := max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
	return dst
}
