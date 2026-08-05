package ipc

import (
	"encoding/json"
	"fmt"
	"image"
	"io"
	"sync"
	"time"

	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/model"
	"fh6-paint-studio/internal/preset"
	"fh6-paint-studio/internal/runner"
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
	default:
		s.fail(req.ID, fmt.Errorf("unknown method %q", req.Method))
	}
}

// GenerateParams is the client's run request. The image is named by PATH rather than sent: the
// daemon has to decode it anyway, and shipping a 20 MB PNG through the socket to hand it straight
// back to the decoder buys nothing.
type GenerateParams struct {
	Path    string          `json:"path"`
	MaxRes  int             `json:"maxRes"`
	Crop    *[4]float64     `json:"crop,omitempty"` // fractional x,y,w,h of the source; nil = whole image
	Choices preset.Choices  `json:"choices"`
	Output  string          `json:"output,omitempty"` // where to write the geometry; empty = do not write
	Extra   json.RawMessage `json:"-"`
}

func (s *Server) generate(req Request) {
	var p GenerateParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.fail(req.ID, fmt.Errorf("bad generate params: %w", err))
			return
		}
	}
	if p.Path == "" {
		s.fail(req.ID, fmt.Errorf("generate needs a path"))
		return
	}
	if p.MaxRes <= 0 {
		p.MaxRes = 1100
	}

	var prep *imageio.Prepared
	var err error
	if p.Crop != nil {
		c := *p.Crop
		prep, _, err = imageio.LoadRegionAutoCropped(p.Path, p.MaxRes, c[0], c[1], c[2], c[3])
	} else {
		prep, _, err = imageio.LoadAutoCropped(p.Path, p.MaxRes)
	}
	if err != nil {
		s.fail(req.ID, err)
		return
	}
	resolved := preset.Resolve(*prep, p.Choices)

	start := time.Now()
	cancel := runner.RunAsync(*prep, resolved, func(ev runner.Event) {
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
		s.event(id, "done", DoneEvent{
			Backend:      e.Backend,
			ShapeCount:   len(e.Result.Shapes) - 1,
			InitialError: e.Result.InitialError,
			FinalError:   e.Result.FinalError,
			ElapsedMs:    time.Since(start).Milliseconds(),
			Geometry:     &doc,
			GeometryPath: path,
		})
	}
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

func (s *Server) fail(id int32, err error) {
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
