package ipc

import (
	"encoding/json"
	"fmt"
	"image"
	"os"

	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/library"
	"fh6-paint-studio/internal/maskbank"
	"fh6-paint-studio/internal/metric"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/preset"
	"fh6-paint-studio/internal/userpreset"
)

// The library methods. They are here rather than in server.go because they share one concern the
// generate path does not: they all need the store, and a store that failed to open must degrade to
// a clear error PER CALL instead of stopping the daemon from starting. A user with an unwritable
// home directory can still generate and inject.

// SetLibraryRoot points the store at a directory instead of the default under the user's home. The
// daemon exposes it as a flag; a caller that keeps its generations somewhere else says so once.
func (s *Server) SetLibraryRoot(root string) {
	s.storeOnce.Do(func() { s.store = library.Open(root) })
}

func (s *Server) libStore() (*library.Store, error) {
	s.storeOnce.Do(func() {
		root, err := library.DefaultRoot()
		if err != nil {
			s.storeErr = fmt.Errorf("library unavailable: %w", err)
			return
		}
		s.store = library.Open(root)
	})
	return s.store, s.storeErr
}

// libraryMethod handles every "library.*" call, returning false when the method is not one of them.
func (s *Server) libraryMethod(req Request) bool {
	store, err := s.libStore()
	handle := func(fn func(*library.Store) (any, error)) {
		if err != nil {
			s.fail(req.ID, err)
			return
		}
		res, ferr := fn(store)
		if ferr != nil {
			s.fail(req.ID, ferr)
			return
		}
		s.reply(req.ID, res)
	}

	switch req.Method {
	case "library.root":
		handle(func(st *library.Store) (any, error) {
			// Informational: a local client uses it to open the folder in the file manager. It means
			// nothing to a client that does not share the daemon's filesystem, which is why nothing
			// else in the protocol depends on it.
			return map[string]string{"root": st.Root}, nil
		})
	case "library.list":
		handle(func(st *library.Store) (any, error) {
			entries, err := st.List()
			if err != nil {
				return nil, err
			}
			return map[string]any{"entries": entries}, nil
		})
	case "library.geometry":
		handle(func(st *library.Store) (any, error) {
			var p struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(req.Params, &p)
			return st.LoadGeometry(p.ID)
		})
	case "library.image":
		handle(func(st *library.Store) (any, error) {
			var p LibraryImageParams
			_ = json.Unmarshal(req.Params, &p)
			path := st.ThumbPath(p.ID)
			if p.Which == "preview" {
				path = st.PreviewPath(p.ID)
			}
			// PNG bytes straight off disk: every client already decodes PNG, and re-encoding here
			// would only lose the thumbnail's already-chosen quality.
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			return map[string]any{"png": b}, nil
		})
	case "library.save":
		handle(func(st *library.Store) (any, error) {
			var p LibrarySaveParams
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return nil, err
			}
			if len(p.Shapes) == 0 {
				return nil, fmt.Errorf("library save: no shapes")
			}
			img, err := decodeLibraryImage(p.Preview)
			if err != nil {
				return nil, err
			}
			if p.Replace {
				if entries, err := st.List(); err == nil {
					for _, e := range entries {
						if e.Name == p.Entry.Name {
							_ = st.Delete(e.ID)
						}
					}
				}
			}
			entry, err := st.Save(p.Shapes, img, p.Entry)
			if err != nil {
				return nil, err
			}
			return map[string]any{"entry": entry}, nil
		})
	case "library.delete":
		handle(func(st *library.Store) (any, error) {
			var p struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(req.Params, &p)
			return map[string]any{"deleted": p.ID}, st.Delete(p.ID)
		})
	case "library.rename":
		handle(func(st *library.Store) (any, error) {
			var p struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &p)
			entry, err := st.Rename(p.ID, p.Name)
			if err != nil {
				return nil, err
			}
			return map[string]any{"entry": entry}, nil
		})
	default:
		return false
	}
	return true
}

// contentDescriptor mirrors what Resolve will see for this source: the palette colour count and
// whether the image is a genuine cutout (not a keep-inside pad margin). Decoded at the studio's
// display resolution so the classification matches the old in-process panel. One-entry cache — the
// panel asks again on every open, the answer only changes with the file.
func (s *Server) contentDescriptor(path string) (colors int, cutout bool) {
	if path == "" {
		return 0, false
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	s.descMu.Lock()
	defer s.descMu.Unlock()
	if s.descPath == path && s.descMod.Equal(st.ModTime()) {
		return s.descColors, s.descCutout
	}
	prep, err := imageio.Load(path, 1100)
	if err != nil {
		return 0, false
	}
	colors = metric.ContentClass(prep.Pixels, prep.W, prep.H).Colors
	cutout = prep.HasTransparency && !prep.PaddedOpaque
	s.descPath, s.descMod, s.descColors, s.descCutout = path, st.ModTime(), colors, cutout
	return colors, cutout
}

func (s *Server) presetStore() (*userpreset.Store, error) {
	s.presetOnce.Do(func() {
		root, err := userpreset.DefaultRoot()
		if err != nil {
			s.presetErr = fmt.Errorf("presets unavailable: %w", err)
			return
		}
		s.presets = userpreset.Open(root)
	})
	return s.presets, s.presetErr
}

// presetMethod handles every "presets.*" call. Custom presets are on-disk state for the same reason
// the library is: the format is the daemon's, and a client that reimplemented it would be a second
// definition of what a saved preset means.
func (s *Server) presetMethod(req Request) bool {
	store, err := s.presetStore()
	handle := func(fn func(*userpreset.Store) (any, error)) {
		if err != nil {
			s.fail(req.ID, err)
			return
		}
		res, ferr := fn(store)
		if ferr != nil {
			s.fail(req.ID, ferr)
			return
		}
		s.reply(req.ID, res)
	}

	switch req.Method {
	case "presets.knobDefaults":
		// The concrete values behind "default", for the expert panel. Needs no store — but it does
		// need the image when one is open: flat's polish depth and kind mix depend on the palette
		// size, and a cutout drops the alpha floor, so defaults shown without looking at the image
		// would disagree with what the engine actually runs.
		var p struct {
			Mode    string `json:"mode"`
			Path    string `json:"path"`
			Quality string `json:"quality"`
		}
		_ = json.Unmarshal(req.Params, &p)
		colors, cutout := s.contentDescriptor(p.Path)
		ch := preset.ModeKnobDefaults(p.Mode, colors, cutout)
		// The search counts are the quality tier's; when the panel holds a quality override the
		// numbers it shows as "default" must come from THAT tier, not the studio's baked one.
		if p.Quality != "" && p.Quality != ch.Quality {
			ch.Quality = p.Quality
			ch.Random, ch.Mutated, ch.SampleBudget, ch.MaxNoImprove = preset.PresetCounts(p.Quality)
		}
		s.reply(req.ID, map[string]any{"choices": ch})
	case "presets.list":
		handle(func(st *userpreset.Store) (any, error) {
			ps, err := st.List()
			if err != nil {
				return nil, err
			}
			return map[string]any{"presets": ps}, nil
		})
	case "presets.save":
		handle(func(st *userpreset.Store) (any, error) {
			var p userpreset.Preset
			if err := json.Unmarshal(req.Params, &p); err != nil {
				return nil, err
			}
			saved, err := st.Save(p)
			if err != nil {
				return nil, err
			}
			return map[string]any{"preset": saved}, nil
		})
	case "presets.delete":
		handle(func(st *userpreset.Store) (any, error) {
			var p struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(req.Params, &p)
			return map[string]any{"deleted": p.ID}, st.Delete(p.ID)
		})
	default:
		return false
	}
	return true
}

// decodeLibraryImage rebuilds the preview from the wire. The length is checked rather than trusted:
// the pixels arrive from another process, and a short buffer would otherwise be a panic inside the
// PNG encoder rather than a message the caller can act on.
func decodeLibraryImage(li LibraryImage) (*image.NRGBA, error) {
	if li.W <= 0 || li.H <= 0 {
		return nil, fmt.Errorf("library save: preview is %dx%d", li.W, li.H)
	}
	if want := li.W * li.H * 4; len(li.Pix) != want {
		return nil, fmt.Errorf("library save: preview %dx%d needs %d bytes, got %d", li.W, li.H, want, len(li.Pix))
	}
	img := image.NewNRGBA(image.Rect(0, 0, li.W, li.H))
	copy(img.Pix, li.Pix)
	return img, nil
}

// shapesMethod answers "shapes.catalog": everything the manual editor is allowed to place.
//
// The bank is the engine's, not the client's: the words, their labels and their captured native
// sizes all live with the coverage grids that render them, and a second list on the Dart side would
// be a second answer to what the game can draw.
func (s *Server) shapesMethod(req Request) bool {
	if req.Method != "shapes.catalog" {
		return false
	}
	// The primitives first, in the order a person reaches for them. Their kinds are the same
	// numbers the document already stores, so the client can hand one straight back.
	// The number reported is the document's Type — the in-game WORD — not the
	// internal ShapeKind. They are different vocabularies and only one of them
	// round-trips: KindFromType silently answers KindEllipse for anything it
	// does not recognise, so handing out kinds makes every word in the bank
	// render as a full-size ellipse and look like a blank tile.
	kinds := []map[string]any{
		{"type": model.TypeRotatedEllipse, "label": "Ellipse", "category": "primitive"},
		{"type": model.TypeRotatedRectangle, "label": "Rectangle", "category": "primitive"},
		{"type": model.TypeTriangle, "label": "Triangle", "category": "primitive"},
		{"type": model.TypeGradGlow, "label": "Glow", "category": "primitive"},
		{"type": model.TypeGradDisk, "label": "Disk", "category": "primitive"},
	}
	for _, e := range maskbank.All() {
		kinds = append(kinds, map[string]any{
			"type":     int(e.Word),
			"label":    e.Label,
			"category": e.Category,
			"mask":     true,
			"nativeW":  e.NativeW,
			"nativeH":  e.NativeH,
		})
	}
	s.reply(req.ID, map[string]any{"kinds": kinds})
	return true
}
