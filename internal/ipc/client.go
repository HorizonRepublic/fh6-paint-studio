package ipc

import (
	"encoding/json"
	"fmt"
	"image"
	"io"
	"sync"
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
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.handler[id] = onUpdate
	c.mu.Unlock()

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
