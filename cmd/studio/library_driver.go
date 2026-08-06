package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"

	"fh6-paint-studio/internal/ipc"
	"fh6-paint-studio/internal/library"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/userpreset"
)

// libraryAPI is the generation library as the UI needs it. Going through the driver rather than
// touching the store directly is what makes the library reachable from a UI that is not this one:
// the format lives in one place, and the next client asks for entries and bytes instead of learning
// the directory layout.
type libraryAPI interface {
	// Root is the on-disk location, for "open the folder". Empty when that means nothing.
	Root() string
	List() ([]library.Entry, error)
	Geometry(id string) (model.Geometry, error)
	// Image returns a stored PNG: which is "thumb" or "preview".
	Image(id, which string) ([]byte, error)
	// Save stores a design. replace deletes same-named entries first; the returned entry carries the
	// id that was actually assigned.
	Save(shapes []model.Shape, preview *image.NRGBA, meta library.Entry, replace bool) (library.Entry, error)
	Delete(id string) error
	Rename(id, name string) (library.Entry, error)
}

// localLibrary is the store in this process. It holds the open error rather than a nil store so
// every call reports the same reason, instead of the UI having to guard each one.
type localLibrary struct {
	store *library.Store
	err   error
}

func openLocalLibrary() libraryAPI {
	root, err := library.DefaultRoot()
	if err != nil {
		return &localLibrary{err: fmt.Errorf("library unavailable: %w", err)}
	}
	return &localLibrary{store: library.Open(root)}
}

func (l *localLibrary) Root() string {
	if l.store == nil {
		return ""
	}
	return l.store.Root
}

func (l *localLibrary) List() ([]library.Entry, error) {
	if l.err != nil {
		return nil, l.err
	}
	return l.store.List()
}

func (l *localLibrary) Geometry(id string) (model.Geometry, error) {
	if l.err != nil {
		return model.Geometry{}, l.err
	}
	return l.store.LoadGeometry(id)
}

func (l *localLibrary) Image(id, which string) ([]byte, error) {
	if l.err != nil {
		return nil, l.err
	}
	path := l.store.ThumbPath(id)
	if which == "preview" {
		path = l.store.PreviewPath(id)
	}
	return os.ReadFile(path)
}

func (l *localLibrary) Save(shapes []model.Shape, preview *image.NRGBA, meta library.Entry, replace bool) (library.Entry, error) {
	if l.err != nil {
		return library.Entry{}, l.err
	}
	if replace {
		if entries, err := l.store.List(); err == nil {
			for _, e := range entries {
				if e.Name == meta.Name {
					_ = l.store.Delete(e.ID)
				}
			}
		}
	}
	return l.store.Save(shapes, preview, meta)
}

func (l *localLibrary) Delete(id string) error {
	if l.err != nil {
		return l.err
	}
	return l.store.Delete(id)
}

func (l *localLibrary) Rename(id, name string) (library.Entry, error) {
	if l.err != nil {
		return library.Entry{}, l.err
	}
	return l.store.Rename(id, name)
}

// remoteLibrary is the same library on the other side of the socket.
type remoteLibrary struct {
	cli  *ipc.Client
	root string
}

func (r *remoteLibrary) Root() string { return r.root }

func (r *remoteLibrary) List() ([]library.Entry, error) { return r.cli.LibraryList() }

func (r *remoteLibrary) Geometry(id string) (model.Geometry, error) {
	return r.cli.LibraryGeometry(id)
}

func (r *remoteLibrary) Image(id, which string) ([]byte, error) {
	return r.cli.LibraryImage(id, which)
}

func (r *remoteLibrary) Save(shapes []model.Shape, preview *image.NRGBA, meta library.Entry, replace bool) (library.Entry, error) {
	img, err := rawPixels(preview)
	if err != nil {
		return library.Entry{}, err
	}
	return r.cli.LibrarySave(ipc.LibrarySaveParams{
		Shapes: shapes, Entry: meta, Preview: img, Replace: replace,
	})
}

func (r *remoteLibrary) Delete(id string) error { return r.cli.LibraryDelete(id) }

func (r *remoteLibrary) Rename(id, name string) (library.Entry, error) {
	return r.cli.LibraryRename(id, name)
}

// rawPixels flattens a preview for the wire. Rows are copied individually because an NRGBA's Stride
// may exceed its row width, and handing over Pix wholesale would ship the padding as pixels.
func rawPixels(img *image.NRGBA) (ipc.LibraryImage, error) {
	if img == nil {
		return ipc.LibraryImage{}, fmt.Errorf("library save: no preview to store")
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		copy(pix[y*w*4:(y+1)*w*4], img.Pix[y*img.Stride:y*img.Stride+w*4])
	}
	return ipc.LibraryImage{W: w, H: h, Pix: pix}, nil
}

// decodePNG turns the bytes a libraryAPI returns back into an image.
func decodePNG(b []byte) (image.Image, error) {
	return png.Decode(bytes.NewReader(b))
}

// presetsAPI is the custom-preset store, on the same terms as the library: on-disk state that
// belongs to the engine side so its format has exactly one definition.
type presetsAPI interface {
	List() ([]userpreset.Preset, error)
	Save(p userpreset.Preset) (userpreset.Preset, error)
	Delete(id string) error
}

type localPresets struct {
	store *userpreset.Store
	err   error
}

func openLocalPresets() presetsAPI {
	root, err := userpreset.DefaultRoot()
	if err != nil {
		return &localPresets{err: fmt.Errorf("presets unavailable: %w", err)}
	}
	return &localPresets{store: userpreset.Open(root)}
}

func (l *localPresets) List() ([]userpreset.Preset, error) {
	if l.err != nil {
		return nil, l.err
	}
	return l.store.List()
}

func (l *localPresets) Save(p userpreset.Preset) (userpreset.Preset, error) {
	if l.err != nil {
		return userpreset.Preset{}, l.err
	}
	return l.store.Save(p)
}

func (l *localPresets) Delete(id string) error {
	if l.err != nil {
		return l.err
	}
	return l.store.Delete(id)
}

type remotePresets struct{ cli *ipc.Client }

func (r *remotePresets) List() ([]userpreset.Preset, error) { return r.cli.PresetList() }

func (r *remotePresets) Save(p userpreset.Preset) (userpreset.Preset, error) {
	return r.cli.PresetSave(p)
}

func (r *remotePresets) Delete(id string) error { return r.cli.PresetDelete(id) }
