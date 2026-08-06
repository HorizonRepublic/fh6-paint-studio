package ipc

import (
	"encoding/json"
	"fmt"
	"image"
	"io"
	"sync"

	"fh6-paint-studio/internal/library"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/userpreset"
)

// Client is the UI side of the protocol. It exists so a UI can be written against the same shape of
// API it uses today — start a run, get typed callbacks, cancel — without knowing there is a socket
// underneath. That is what lets the current Gio studio move onto the daemon as a mechanical change
// rather than a rewrite, and it is why the callback signature mirrors runner.RunAsync.
type Client struct {
	w io.Writer
	r io.Reader

	wmu sync.Mutex // one writer at a time; requests come from UI event handlers

	mu      sync.Mutex
	nextID  int32
	handler map[int32]func(Update)
}

// Update is one thing that happened to a run. Exactly one field is meaningful, selected by Kind.
// A closed sum type would be tidier, but a UI switches on this in one place and a flat struct keeps
// the callback signature stable as events are added.
type Update struct {
	Kind     string // "progress" | "status" | "log" | "frame" | "done" | "failed"
	Progress ProgressEvent
	Stage    string
	Line     string
	Frame    *image.NRGBA
	Done     DoneEvent
	Err      error
}

// NewClient wires a client to a duplex connection. Call Listen once, on its own goroutine.
func NewClient(r io.Reader, w io.Writer) *Client {
	return &Client{r: r, w: w, handler: map[int32]func(Update){}}
}

// Listen pumps the connection until it closes. Every run still waiting is told the connection
// dropped, because a UI that never hears back would otherwise show a progress bar forever — the
// daemon dying is a normal event to a client, not a panic.
func (c *Client) Listen() error {
	err := c.pump()
	c.mu.Lock()
	handlers := c.handler
	c.handler = map[int32]func(Update){}
	c.mu.Unlock()
	reason := err
	if reason == nil {
		reason = io.EOF
	}
	for _, h := range handlers {
		h(Update{Kind: "failed", Err: fmt.Errorf("engine connection lost: %w", reason)})
	}
	return err
}

func (c *Client) pump() error {
	for {
		kind, payload, err := Read(c.r)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch kind {
		case KindFrame:
			h, pix, err := DecodeFrame(payload)
			if err != nil {
				return err
			}
			c.deliver(h.ID, Update{Kind: "frame", Frame: frameImage(h, pix)})
		case KindJSON:
			var resp Response
			if err := json.Unmarshal(payload, &resp); err != nil {
				return err
			}
			c.route(resp)
		default:
			return fmt.Errorf("ipc: unexpected message kind %d", kind)
		}
	}
}

func (c *Client) route(resp Response) {
	switch resp.Event {
	case "":
		// A plain result closes a one-shot call; those are handled by Call, not by a run handler.
		c.deliver(resp.ID, Update{Kind: "result", Line: string(resp.Result)})
	case "progress":
		var p ProgressEvent
		_ = json.Unmarshal(resp.Result, &p)
		c.deliver(resp.ID, Update{Kind: "progress", Progress: p})
	case "status":
		var p StatusEvent
		_ = json.Unmarshal(resp.Result, &p)
		c.deliver(resp.ID, Update{Kind: "status", Stage: p.Stage})
	case "log":
		var p LogEvent
		_ = json.Unmarshal(resp.Result, &p)
		c.deliver(resp.ID, Update{Kind: "log", Line: p.Line})
	// Deliver BEFORE forgetting. The terminal event is the one the UI most needs — it is what
	// stops the progress bar and shows the result — and dropping the handler first means looking up
	// a key that is no longer there, so the run simply goes quiet forever.
	case "done":
		var p DoneEvent
		_ = json.Unmarshal(resp.Result, &p)
		c.deliver(resp.ID, Update{Kind: "done", Done: p})
		c.forget(resp.ID)
	case "ok":
		// The terminal event for a streaming call that produces narration and then simply succeeds.
		c.deliver(resp.ID, Update{Kind: "ok"})
		c.forget(resp.ID)
	case "failed":
		c.deliver(resp.ID, Update{Kind: "failed", Err: fmt.Errorf("%s", resp.Error)})
		c.forget(resp.ID)
	}
}

// deliver looks the handler up under the lock but calls it outside: a UI handler may post to its own
// event loop and block, and holding the map lock across that would stall every other run's events.
func (c *Client) deliver(id int32, u Update) {
	c.mu.Lock()
	h := c.handler[id]
	c.mu.Unlock()
	if h != nil {
		h(u)
	}
}

func (c *Client) forget(id int32) {
	c.mu.Lock()
	delete(c.handler, id)
	c.mu.Unlock()
}

// Generate starts a run and streams its updates to onUpdate. The returned cancel is safe to call
// after the run has finished; the daemon answers with a failed event for an unknown run, which the
// handler no longer receives.
func (c *Client) Generate(p GenerateParams, onUpdate func(Update)) (cancel func(), err error) {
	id := c.claim(onUpdate)
	if err := c.request(Request{ID: id, Method: "generate"}, p); err != nil {
		c.forget(id)
		return nil, err
	}
	return func() {
		c.mu.Lock()
		c.nextID++
		cid := c.nextID
		c.mu.Unlock()
		_ = c.request(Request{ID: cid, Method: "cancel"}, map[string]int32{"runId": id})
	}, nil
}

// Inject writes shapes into the running game. It blocks until the write finishes; there is
// deliberately no cancel, because stopping halfway through a live layer table leaves the user's
// vinyl half-overwritten.
func (c *Client) Inject(p InjectParams, onLog func(string)) error {
	done := make(chan error, 1)
	id := c.claim(func(u Update) {
		switch u.Kind {
		case "log":
			if onLog != nil {
				onLog(u.Line)
			}
		case "ok":
			done <- nil
		case "failed":
			done <- u.Err
		}
	})
	if err := c.request(Request{ID: id, Method: "inject"}, p); err != nil {
		c.forget(id)
		return err
	}
	return <-done
}

// Call makes a one-shot request and decodes the result into out. Used for the small queries a UI
// needs before it can decide what to show — which backends exist, what the defaults are, whether an
// injection is even possible here.
func (c *Client) Call(method string, params, out any) error {
	res := make(chan Update, 1)
	id := c.claim(func(u Update) {
		switch u.Kind {
		case "result", "failed":
			res <- u
		}
	})
	if err := c.request(Request{ID: id, Method: method}, params); err != nil {
		c.forget(id)
		return err
	}
	u := <-res
	c.forget(id)
	if u.Err != nil {
		return u.Err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(u.Line), out)
}

// LibraryRoot returns the daemon's library directory. Informational — see the server side.
func (c *Client) LibraryRoot() (string, error) {
	var out struct {
		Root string `json:"root"`
	}
	err := c.Call("library.root", nil, &out)
	return out.Root, err
}

// LibraryList returns every saved generation, newest first.
func (c *Client) LibraryList() ([]library.Entry, error) {
	var out struct {
		Entries []library.Entry `json:"entries"`
	}
	err := c.Call("library.list", nil, &out)
	return out.Entries, err
}

// LibraryGeometry loads a saved design's shapes.
func (c *Client) LibraryGeometry(id string) (model.Geometry, error) {
	var g model.Geometry
	err := c.Call("library.geometry", map[string]string{"id": id}, &g)
	return g, err
}

// LibraryImage returns a stored PNG: which is "thumb" or "preview".
func (c *Client) LibraryImage(id, which string) ([]byte, error) {
	var out struct {
		PNG []byte `json:"png"`
	}
	err := c.Call("library.image", LibraryImageParams{ID: id, Which: which}, &out)
	return out.PNG, err
}

// LibrarySave stores a finished design and returns the entry the daemon actually wrote — the id and
// shape count are assigned there, so the caller must use what comes back rather than what it sent.
func (c *Client) LibrarySave(p LibrarySaveParams) (library.Entry, error) {
	var out struct {
		Entry library.Entry `json:"entry"`
	}
	err := c.Call("library.save", p, &out)
	return out.Entry, err
}

// LibraryDelete removes a saved generation.
func (c *Client) LibraryDelete(id string) error {
	return c.Call("library.delete", map[string]string{"id": id}, nil)
}

// LibraryRename renames one and returns the updated entry.
func (c *Client) LibraryRename(id, name string) (library.Entry, error) {
	var out struct {
		Entry library.Entry `json:"entry"`
	}
	err := c.Call("library.rename", map[string]string{"id": id, "name": name}, &out)
	return out.Entry, err
}

// PresetList returns the user's saved custom presets.
func (c *Client) PresetList() ([]userpreset.Preset, error) {
	var out struct {
		Presets []userpreset.Preset `json:"presets"`
	}
	err := c.Call("presets.list", nil, &out)
	return out.Presets, err
}

// PresetSave stores one and returns it with the id the daemon assigned.
func (c *Client) PresetSave(p userpreset.Preset) (userpreset.Preset, error) {
	var out struct {
		Preset userpreset.Preset `json:"preset"`
	}
	err := c.Call("presets.save", p, &out)
	return out.Preset, err
}

// PresetDelete removes one.
func (c *Client) PresetDelete(id string) error {
	return c.Call("presets.delete", map[string]string{"id": id}, nil)
}

func (c *Client) claim(h func(Update)) int32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	c.handler[c.nextID] = h
	return c.nextID
}

func (c *Client) request(req Request, params any) error {
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = b
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return WriteJSON(c.w, req)
}

// frameImage wraps the wire pixels in an NRGBA WITHOUT copying: the buffer came from Read and is
// not reused, so the image can own it. A copy here would double the cost of the one path that runs
// twenty times a second.
func frameImage(h FrameHeader, pix []byte) *image.NRGBA {
	return &image.NRGBA{
		Pix:    pix,
		Stride: int(h.W) * 4,
		Rect:   image.Rect(0, 0, int(h.W), int(h.H)),
	}
}
