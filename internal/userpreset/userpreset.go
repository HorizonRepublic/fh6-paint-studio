// Package userpreset persists user-defined generation presets under the home folder, one JSON file
// per preset (<id>.json). A preset is a full studio configuration snapshot (preset.Choices plus the
// keep-inside toggle) saved under a name, so it can be reloaded from the Preset dropdown. Pure Go (no
// Gio), so it is unit-testable.
package userpreset

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"fh6-paint-studio/internal/preset"
)

// Preset is one saved configuration. ID is the bare filename (no extension); the dropdown shows Name.
type Preset struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Created    time.Time      `json:"created"`
	KeepInside bool           `json:"keepInside"` // the studio toggle (not part of Choices)
	Choices    preset.Choices `json:"choices"`
}

// Store is a preset folder rooted at a directory.
type Store struct{ Root string }

// Open returns a Store at root (created lazily on first Save).
func Open(root string) *Store { return &Store{Root: root} }

// DefaultRoot returns <home>/FH6PaintStudio/presets, creating it if missing.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, "FH6PaintStudio", "presets")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

func (s *Store) path(id string) string { return filepath.Join(s.Root, id+".json") }

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(name string) string {
	s := slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "preset"
	}
	return s
}

// Save writes p as <id>.json. An empty ID gets a unique one from Created + Name (a new preset); a set
// ID overwrites that file (an update). Returns the stored preset with its ID filled.
func (s *Store) Save(p Preset) (Preset, error) {
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return Preset{}, err
	}
	if p.ID == "" {
		base := p.Created.Format("20060102-150405") + "-" + slug(p.Name)
		id := base
		for i := 1; ; i++ {
			if _, err := os.Stat(s.path(id)); os.IsNotExist(err) {
				break
			}
			id = fmt.Sprintf("%s-%d", base, i)
		}
		p.ID = id
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return Preset{}, err
	}
	return p, os.WriteFile(s.path(p.ID), b, 0o644)
}

// List scans Root and returns presets newest-first. Unparsable files are skipped; a missing Root
// returns (nil, nil) — an empty set, not an error.
func (s *Store) List() ([]Preset, error) {
	des, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Preset
	for _, de := range des {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.Root, de.Name()))
		if err != nil {
			continue
		}
		var p Preset
		if json.Unmarshal(b, &p) != nil {
			continue
		}
		p.ID = strings.TrimSuffix(de.Name(), ".json") // the filename is authoritative
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// Delete removes a preset file. The id must be a bare name (no path separators).
func (s *Store) Delete(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("userpreset: invalid id %q", id)
	}
	return os.Remove(s.path(id))
}
