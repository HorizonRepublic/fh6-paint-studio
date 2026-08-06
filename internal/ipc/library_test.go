package ipc

import (
	"bytes"
	"image/png"
	"net"
	"testing"
	"time"

	"fh6-paint-studio/internal/library"
	"fh6-paint-studio/internal/model"
)

// The library is the daemon's on-disk FORMAT, so a client must be able to do everything through the
// protocol that it used to do through the store. This walks the whole life of an entry — save, list,
// read back the geometry, read back both images, rename, delete — because a client that can save but
// not read what it saved is worse than one that cannot save at all.
//
// No GPU here on purpose: none of this touches the engine, and a test that needs a backend would not
// run in the places this needs to.
func TestLibraryOverTheWire(t *testing.T) {
	c, stop := testClient(t)
	defer stop()

	shapes := []model.Shape{
		{Type: 3, Data: []float64{0, 0, 16, 16}, Color: []int{10, 20, 30, 255}},
		{Type: 1, Data: []float64{8, 8, 4, 4, 0}, Color: []int{200, 100, 50, 200}},
	}
	preview := LibraryImage{W: 4, H: 3, Pix: make([]byte, 4*3*4)}
	for i := range preview.Pix {
		preview.Pix[i] = byte(i * 7)
	}

	entry, err := c.LibrarySave(LibrarySaveParams{
		Shapes:  shapes,
		Preview: preview,
		Entry:   library.Entry{Name: "roundtrip", Preset: "anime", Width: 4, Height: 3, Budget: 2},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if entry.ID == "" {
		t.Fatal("save returned no id — the client cannot refer to what it just stored")
	}
	// The daemon assigns the shape count; the FH6 count excludes the background rectangle.
	if entry.Shapes != len(shapes)-1 {
		t.Errorf("entry reports %d shapes, want %d", entry.Shapes, len(shapes)-1)
	}

	entries, err := c.LibraryList()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("list returned %+v, want the entry just saved", entries)
	}

	g, err := c.LibraryGeometry(entry.ID)
	if err != nil {
		t.Fatalf("geometry: %v", err)
	}
	if len(g.Shapes) != len(shapes) {
		t.Fatalf("geometry came back with %d shapes, want %d", len(g.Shapes), len(shapes))
	}
	if g.Shapes[1].Color[0] != 200 {
		t.Errorf("geometry colour survived as %v, want the 200 it went in with", g.Shapes[1].Color)
	}

	// Both images must decode: the preview travels as raw pixels and is encoded daemon-side, and the
	// thumbnail is derived there too, so this is the only place either encoding is exercised.
	for _, which := range []string{"preview", "thumb"} {
		b, err := c.LibraryImage(entry.ID, which)
		if err != nil {
			t.Fatalf("image %s: %v", which, err)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("image %s did not decode: %v", which, err)
		}
		if which == "preview" {
			if got := img.Bounds(); got.Dx() != preview.W || got.Dy() != preview.H {
				t.Errorf("preview came back %v, want %dx%d", got, preview.W, preview.H)
			}
		}
	}

	renamed, err := c.LibraryRename(entry.ID, "renamed")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.Name != "renamed" {
		t.Errorf("rename produced %q", renamed.Name)
	}

	if err := c.LibraryDelete(entry.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if entries, err := c.LibraryList(); err != nil || len(entries) != 0 {
		t.Errorf("after delete: %d entries, err %v", len(entries), err)
	}
}

// Replace is what makes "save this design again under the same name" an edit rather than a
// duplicate. Without it a user who saves twice quietly accumulates copies.
func TestLibrarySaveReplacesByName(t *testing.T) {
	c, stop := testClient(t)
	defer stop()

	shapes := []model.Shape{{Type: 3, Data: []float64{0, 0, 2, 2}, Color: []int{0, 0, 0, 255}}}
	preview := LibraryImage{W: 2, H: 2, Pix: make([]byte, 2*2*4)}
	meta := library.Entry{Name: "design", Width: 2, Height: 2}

	first, err := c.LibrarySave(LibrarySaveParams{Shapes: shapes, Preview: preview, Entry: meta})
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	second, err := c.LibrarySave(LibrarySaveParams{Shapes: shapes, Preview: preview, Entry: meta, Replace: true})
	if err != nil {
		t.Fatalf("replacing save: %v", err)
	}

	entries, err := c.LibraryList()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entries after a replacing save, want 1", len(entries))
	}
	if entries[0].ID != second.ID {
		t.Errorf("the surviving entry is %q, want the newer %q (older was %q)", entries[0].ID, second.ID, first.ID)
	}
}

// A malformed preview must come back as an error, not a panic: the pixels arrive from another
// process, and the length is the one thing a caller can get wrong by accident.
func TestLibrarySaveRejectsAShortPreview(t *testing.T) {
	c, stop := testClient(t)
	defer stop()

	_, err := c.LibrarySave(LibrarySaveParams{
		Shapes:  []model.Shape{{Type: 3, Data: []float64{0, 0, 2, 2}, Color: []int{0, 0, 0, 255}}},
		Preview: LibraryImage{W: 10, H: 10, Pix: []byte{1, 2, 3}},
		Entry:   library.Entry{Name: "short"},
	})
	if err == nil {
		t.Fatal("a 3-byte preview for a 10x10 image was accepted")
	}
}

// An unknown method must be refused rather than ignored. A client waiting on a reply that never
// comes is the failure mode with no diagnostic at all.
func TestUnknownMethodFails(t *testing.T) {
	c, stop := testClient(t)
	defer stop()

	done := make(chan error, 1)
	go func() { done <- c.Call("library.nonesuch", nil, nil) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("an unknown method reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("an unknown method never answered")
	}
}

// InjectState must answer even where injection is impossible, so a UI can disable the button with a
// reason instead of failing at the moment the user clicks it.
func TestInjectStateAnswers(t *testing.T) {
	c, stop := testClient(t)
	defer stop()

	var st InjectState
	if err := c.Call("inject.state", nil, &st); err != nil {
		t.Fatalf("inject.state: %v", err)
	}
	// Availability is platform-dependent; that it ANSWERED is the contract.
	_ = st
}

// testClient wires a client to a server whose library lives in a temp directory. Pointing the store
// somewhere else matters: the default is the user's real library, and a test must never write there.
func testClient(t *testing.T) (*Client, func()) {
	t.Helper()
	cliConn, srvConn := net.Pipe()
	srv := NewServer(srvConn, srvConn)
	srv.SetLibraryRoot(t.TempDir())
	go func() { _ = srv.Serve() }()

	c := NewClient(cliConn, cliConn)
	go func() { _ = c.Listen() }()
	return c, func() { cliConn.Close(); srvConn.Close() }
}
