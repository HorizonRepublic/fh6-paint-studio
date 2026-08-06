package ipc

import (
	"encoding/json"
	"fmt"
	"image"
	"io"
	"sync"
	"time"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/inject"
	"fh6-paint-studio/internal/library"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/preset"
	"fh6-paint-studio/internal/runner"
	"fh6-paint-studio/internal/session"
	"fh6-paint-studio/internal/userpreset"
)

// Server answers one client connection. One connection, not many: the engine holds a GPU context
// and a single canvas, and two clients driving that at once would interleave runs on the same
// device. A second client is a separate daemon.
//
// Everything the UI needs is reachable through runner.RunAsync plus the image loaders, so the
// server is mostly a translation layer: it turns a JSON request into the same call the Gio studio
// makes today, and turns the typed events back into wire messages. Keeping it that thin is what
// lets the existing UI move onto the protocol without changing behaviour.
type Server struct {
	r io.Reader
	w io.Writer

	mu   sync.Mutex // serialises writes: events arrive from run goroutines
	runs map[int32]func()

	storeOnce sync.Once
	store     *library.Store
	storeErr  error

	presetOnce sync.Once
	presets    *userpreset.Store
	presetErr  error
}

// NewServer wires a server to a duplex connection.
func NewServer(r io.Reader, w io.Writer) *Server {
	return &Server{r: r, w: w, runs: map[int32]func(){}}
}

// Serve reads requests until the connection closes or a framing error makes the stream
// unrecoverable. Cancels every run still in flight on the way out, so closing the client window
// cannot leave the GPU busy with work nobody will collect.
func (s *Server) Serve() error {
	defer s.cancelAll()
	for {
		kind, payload, err := Read(s.r)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if kind != KindJSON {
			return fmt.Errorf("ipc: client sent a %d-kind message; only JSON requests are accepted", kind)
		}
		var req Request
		if err := json.Unmarshal(payload, &req); err != nil {
			s.fail(0, fmt.Errorf("malformed request: %w", err))
			continue
		}
		s.dispatch(req)
	}
}

func (s *Server) dispatch(req Request) {
	switch req.Method {
	case "backends":
		s.reply(req.ID, map[string]any{"backends": runner.AvailableBackends()})
	case "defaults":
		s.reply(req.ID, preset.DefaultChoices())
	case "generate":
		s.generate(req)
	case "cancel":
		s.cancel(req)
	case "inject.state":
		s.reply(req.ID, InjectState{Available: inject.NewFH6().Available(), Elevated: inject.Elevated()})
	case "inject":
		s.inject(req)
	case "shapes.catalog":
		s.shapesMethod(req)
	case "render":
		s.render(req)
	default:
		if !s.libraryMethod(req) && !s.presetMethod(req) {
			s.fail(req.ID, fmt.Errorf("unknown method %q", req.Method))
		}
	}
}

// GenerateParams is the client's run request. The image is named by PATH rather than sent: the
// daemon has to decode it anyway, and shipping a 20 MB PNG through the socket to hand it straight
// back to the decoder buys nothing.
//
// Everything here is stated in the user's terms — which file, which region of it, which preset —
// and none of it in the engine's. The daemon owns the fit resolution, the transparent surround and
// the hybrid ink split, so a client cannot get a run subtly wrong by reproducing them differently,
// and a client that cannot decode images at all is still a first-class client.
type GenerateParams struct {
	Path string `json:"path"`

	// Region is an absolute rectangle in the raw file's coordinates: x, y, w, h. Nil means the whole
	// image. Absolute rather than fractional because a crop composed onto a previous crop makes "a
	// fraction of what" ambiguous, and the wrong answer to that is a convincing reconstruction of the
	// wrong part of the picture.
	Region *[4]int `json:"region,omitempty"`

	DisplayRes int  `json:"displayRes,omitempty"` // what the client shows the source at; 0 = the default
	SourceRes  bool `json:"sourceRes,omitempty"`  // fit at the source's own resolution
	KeepInside bool `json:"keepInside,omitempty"` // bound every shape to the content rectangle

	Choices preset.Choices `json:"choices"`
	Output  string         `json:"output,omitempty"` // where to write the geometry; empty = do not write
}

func (s *Server) generate(req Request) {
	var p GenerateParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req.ID, fmt.Errorf("bad generate params: %w", err))
			return
		}
	}

	sr := session.Request{
		Path:       p.Path,
		DisplayRes: p.DisplayRes,
		SourceRes:  p.SourceRes,
		KeepInside: p.KeepInside,
		Choices:    p.Choices,
	}
	if p.Region != nil {
		r := *p.Region
		rect := image.Rect(r[0], r[1], r[0]+r[2], r[1]+r[3])
		sr.Region = &rect
	}
	run, err := session.Prepare(sr)
	if err != nil {
		s.fail(req.ID, err)
		return
	}

	start := time.Now()
	cancel := run.Start(func(ev runner.Event) {
		s.emit(req.ID, ev, start, p.Output)
	})

	s.mu.Lock()
	s.runs[req.ID] = cancel
	s.mu.Unlock()
}

// emit translates one engine event onto the wire. Frames go out as binary; everything else is JSON.
func (s *Server) emit(id int32, ev runner.Event, start time.Time, output string) {
	switch e := ev.(type) {
	case runner.Progress:
		s.event(id, "progress", ProgressEvent{
			Shapes: e.Shapes, Total: e.Total, Err: e.Err, ElapsedMs: e.Elapsed.Milliseconds(),
		})
	case runner.Status:
		s.event(id, "status", StatusEvent{Stage: e.Stage})
	case runner.Log:
		s.event(id, "log", LogEvent{Line: e.Line})
	case runner.Frame:
		s.frame(id, e.Img)
	case runner.Failed:
		s.finish(id)
		s.fail(id, e.Err)
	case runner.Done:
		doc := model.Geometry{Shapes: e.Result.Shapes}
		var path string
		if output != "" {
			if err := imageio.WriteGeometry(output, doc); err != nil {
				s.event(id, "log", LogEvent{Line: "could not write geometry: " + err.Error()})
			} else {
				path = output
			}
		}
		s.frame(id, e.Canvas) // the final canvas travels the same path as a preview
		s.finish(id)
		done := DoneEvent{
			Backend:      e.Backend,
			ShapeCount:   len(e.Result.Shapes) - 1,
			InitialError: e.Result.InitialError,
			FinalError:   e.Result.FinalError,
			ElapsedMs:    time.Since(start).Milliseconds(),
			Geometry:     &doc,
			GeometryPath: path,
			Width:        e.Width,
			Height:       e.Height,
		}
		if e.Quality != nil {
			done.DeltaE, done.SSIM = e.Quality.DeltaE, e.Quality.SSIM
		}
		s.event(id, "done", done)
	}
}

// inject writes the shapes into the running game, streaming the injector's own narration as log
// lines. It runs on its own goroutine: the write takes seconds and blocking here would stop the
// server reading, so the client would look hung for the whole operation.
//
// There is deliberately no cancel. The write walks a live layer table in another process's memory;
// stopping halfway leaves the user's vinyl half-overwritten, which is worse than finishing.
func (s *Server) inject(req Request) {
	var p InjectParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req.ID, fmt.Errorf("bad inject params: %w", err))
			return
		}
	}
	if len(p.Shapes) == 0 {
		s.fail(req.ID, fmt.Errorf("inject needs shapes"))
		return
	}
	go func() {
		err := inject.Apply(p.Shapes, p.Width, p.Height, p.Layers, p.Scale, func(line string) {
			s.event(req.ID, "log", LogEvent{Line: line})
		})
		if err != nil {
			s.event(req.ID, "log", LogEvent{Line: "inject: " + err.Error()})
			s.fail(req.ID, err)
			return
		}
		s.event(req.ID, "ok", struct{}{})
	}()
}

// RenderParams asks for a picture of a shape list.
//
// The editor needs this because there must be exactly ONE renderer. A client
// that drew the shapes itself would be a second definition of what a livery
// looks like, and the two would drift the first time a primitive changed — the
// in-game-faithful render is the whole product.
type RenderParams struct {
	Shapes      []model.Shape `json:"shapes"`
	Width       int           `json:"width"`
	Height      int           `json:"height"`
	Transparent bool          `json:"transparent,omitempty"`
}

// render draws a shape list and sends it back as a frame. Synchronous on the
// read goroutine: it is CPU work measured in tens of milliseconds, and a client
// asks for it between edits rather than continuously.
func (s *Server) render(req Request) {
	var p RenderParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req.ID, fmt.Errorf("bad render params: %w", err))
			return
		}
	}
	if p.Width <= 0 || p.Height <= 0 {
		s.fail(req.ID, fmt.Errorf("render needs a size, got %dx%d", p.Width, p.Height))
		return
	}
	// ss=1: the game rasterises the geometry itself with hard edges, so a single
	// sample is the honest representation of what will be injected.
	s.frame(req.ID, imageio.RenderFH6Image(p.Shapes, p.Transparent, p.Width, p.Height, 1))
	s.event(req.ID, "ok", struct{}{})
}

func (s *Server) cancel(req Request) {
	var p struct {
		RunID int32 `json:"runId"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	s.mu.Lock()
	cancel := s.runs[p.RunID]
	s.mu.Unlock()
	if cancel == nil {
		s.fail(req.ID, fmt.Errorf("no run %d in flight", p.RunID))
		return
	}
	cancel()
	s.reply(req.ID, map[string]any{"cancelled": p.RunID})
}

func (s *Server) finish(id int32) {
	s.mu.Lock()
	delete(s.runs, id)
	s.mu.Unlock()
}

func (s *Server) cancelAll() {
	s.mu.Lock()
	runs := s.runs
	s.runs = map[int32]func(){}
	s.mu.Unlock()
	for _, cancel := range runs {
		cancel()
	}
}

func (s *Server) reply(id int32, result any) {
	b, err := json.Marshal(result)
	if err != nil {
		s.fail(id, err)
		return
	}
	s.send(Response{ID: id, Result: b})
}

func (s *Server) event(id int32, name string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		s.fail(id, err)
		return
	}
	s.send(Response{ID: id, Event: name, Result: b})
}

// fail answers a request with an error AND records it. Every failure the client
// ever sees passes through here, so this one line is what makes a user's log
// worth asking for: without it the client reports "failed" and the engine's own
// log says nothing at all about why.
func (s *Server) fail(id int32, err error) {
	applog.Printf("ERROR: request %d failed: %v", id, err)
	s.send(Response{ID: id, Event: "failed", Error: err.Error()})
}

func (s *Server) send(resp Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = WriteJSON(s.w, resp)
}

// frame writes a preview. NRGBA from the runner is already straight-alpha RGBA in memory, but its
// Stride may exceed the row width, so rows are copied individually rather than handing over Pix.
func (s *Server) frame(id int32, img *image.NRGBA) {
	if img == nil {
		return
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		copy(pix[y*w*4:(y+1)*w*4], img.Pix[y*img.Stride:y*img.Stride+w*4])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = WriteFrame(s.w, FrameHeader{ID: id, W: int32(w), H: int32(h)}, pix)
}
