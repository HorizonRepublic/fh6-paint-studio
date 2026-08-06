package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"image"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/inject"
	"fh6-paint-studio/internal/ipc"
	"fh6-paint-studio/internal/runner"
	"fh6-paint-studio/internal/session"
)

// The studio is on its way off Gio, so the engine has to stop being a library it links against.
// This is the seam: both drivers below emit the SAME runner.Event values, so every switch in the UI
// keeps working untouched whether the engine runs in this process or in another one.
//
// Keeping the local driver is not hedging. Until the remote path has run on real work it is the
// reference: any difference in behaviour between them is a bug in the boundary, and having both
// behind one interface is what makes that difference observable instead of theoretical.
type engineDriver interface {
	// Generate starts a run described in the user's terms. Both drivers prepare it the same way —
	// locally that is a direct call, remotely it is the same call on the other side of the socket —
	// so a run cannot come out different depending on where the engine happens to live.
	Generate(req session.Request, onEvent func(runner.Event)) (cancel func(), err error)

	// Inject writes the shapes into the running game. It BLOCKS, and the caller runs it off the UI
	// goroutine. This is on the driver rather than called directly because the write has to happen in
	// whichever process holds the Windows handles — and once the UI is Flutter, that is not the UI.
	Inject(p ipc.InjectParams, onLog func(string)) error

	// InjectState reports whether an injection is possible where the WRITING happens.
	InjectState() ipc.InjectState

	// Library is the saved-generation store, which lives wherever the engine does.
	Library() libraryAPI

	// Presets is the custom-preset store, on the same terms.
	Presets() presetsAPI

	// Close releases anything the driver owns. Safe on a driver that never started a run.
	Close()
}

// localDriver runs the engine in this process — the behaviour the studio has always had.
type localDriver struct {
	lib  libraryAPI
	pres presetsAPI
}

func newLocalDriver() *localDriver {
	return &localDriver{lib: openLocalLibrary(), pres: openLocalPresets()}
}

func (d *localDriver) Library() libraryAPI { return d.lib }
func (d *localDriver) Presets() presetsAPI { return d.pres }

func (*localDriver) Generate(req session.Request, onEvent func(runner.Event)) (func(), error) {
	run, err := session.Prepare(req)
	if err != nil {
		return nil, err
	}
	return run.Start(onEvent), nil
}

func (*localDriver) Inject(p ipc.InjectParams, onLog func(string)) error {
	return inject.Apply(p.Shapes, p.Width, p.Height, p.Layers, p.Scale, onLog)
}

func (*localDriver) InjectState() ipc.InjectState {
	return ipc.InjectState{Available: inject.NewFH6().Available(), Elevated: inject.Elevated()}
}

func (*localDriver) Close() {}

// remoteDriver talks to an engined process over the loopback protocol.
type remoteDriver struct {
	cmd  *exec.Cmd
	conn net.Conn
	cli  *ipc.Client
	lib  libraryAPI
	pres presetsAPI
}

// engineIdleTimeout stops a spawned service that nobody connects to. The studio connects within
// milliseconds, so anything reaching this means the studio died on the way — and an engine process
// left holding the GPU is worse than a slow start.
const engineIdleTimeout = 30 * time.Second

// dialEngine spawns the engine service and connects to it. By default it spawns THIS binary with the
// --engine-service subcommand, which is why the release is two files rather than three; FH6_ENGINED
// points at a separate engined.exe instead. Either way the service prints its address and token as
// the first line of stdout, so there is no port to configure and no race on a fixed one — several
// studios can run side by side.
func dialEngine(exePath string) (*remoteDriver, error) {
	args := []string{"-idle-timeout", engineIdleTimeout.String()}
	if exePath == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, err
		}
		exePath, args = self, []string{"--engine-service"}
	}
	cmd := exec.Command(exePath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", exePath, err)
	}

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("engine daemon did not announce itself: %w", err)
	}
	var hello struct {
		Addr  string `json:"addr"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(line), &hello); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("engine daemon said %q: %w", line, err)
	}

	conn, err := net.DialTimeout("tcp", hello.Addr, 5*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	if err := ipc.WriteJSON(conn, ipc.Request{
		ID: 1, Method: "hello", Params: json.RawMessage(`{"token":"` + hello.Token + `"}`),
	}); err != nil {
		conn.Close()
		_ = cmd.Process.Kill()
		return nil, err
	}
	if _, _, err := ipc.Read(conn); err != nil { // the handshake reply; a rejection closes the conn
		conn.Close()
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("engine daemon refused the handshake: %w", err)
	}

	d := &remoteDriver{cmd: cmd, conn: conn, cli: ipc.NewClient(conn, conn)}
	go func() { _ = d.cli.Listen() }()
	return d, nil
}

func (d *remoteDriver) Generate(req session.Request, onEvent func(runner.Event)) (func(), error) {
	// The final canvas arrives as the LAST frame, one message before done. Hold it so the Done event
	// the UI receives carries a canvas exactly as the in-process runner's does — otherwise every
	// consumer of Done would need a second code path just for the remote driver.
	var canvas frameHolder
	p := ipc.GenerateParams{
		Path:       req.Path,
		DisplayRes: req.DisplayRes,
		SourceRes:  req.SourceRes,
		KeepInside: req.KeepInside,
		Choices:    req.Choices,
	}
	if req.Region != nil {
		r := *req.Region
		p.Region = &[4]int{r.Min.X, r.Min.Y, r.Dx(), r.Dy()}
	}
	return d.cli.Generate(p, func(u ipc.Update) {
		switch u.Kind {
		case "log":
			onEvent(runner.Log{Line: u.Line})
		case "status":
			onEvent(runner.Status{Stage: u.Stage})
		case "progress":
			onEvent(runner.Progress{
				Shapes:  u.Progress.Shapes,
				Total:   u.Progress.Total,
				Err:     u.Progress.Err,
				Elapsed: time.Duration(u.Progress.ElapsedMs) * time.Millisecond,
			})
		case "frame":
			canvas.set(u.Frame)
			onEvent(runner.Frame{Img: u.Frame})
		case "failed":
			onEvent(runner.Failed{Err: u.Err})
		case "done":
			res := engine.Result{InitialError: u.Done.InitialError, FinalError: u.Done.FinalError}
			if u.Done.Geometry != nil {
				res.Shapes = u.Done.Geometry.Shapes
			}
			ev := runner.Done{
				Result: res, Canvas: canvas.get(), Backend: u.Done.Backend,
				Width: u.Done.Width, Height: u.Done.Height,
			}
			if u.Done.DeltaE > 0 || u.Done.SSIM > 0 {
				ev.Quality = &runner.Quality{DeltaE: u.Done.DeltaE, SSIM: u.Done.SSIM}
			}
			onEvent(ev)
		}
	})
}

// frameHolder keeps the most recent preview so it can be handed over as the final canvas. Guarded
// because frames arrive on the client's read goroutine while the UI may still be reading the last
// one it was given.
type frameHolder struct {
	mu  sync.Mutex
	img *image.NRGBA
}

func (f *frameHolder) set(img *image.NRGBA) {
	f.mu.Lock()
	f.img = img
	f.mu.Unlock()
}

func (f *frameHolder) get() *image.NRGBA {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.img
}

func (d *remoteDriver) Inject(p ipc.InjectParams, onLog func(string)) error {
	return d.cli.Inject(p, onLog)
}

// InjectState asks the SERVICE, not this process. The service is a child, so it inherits this
// process's elevation and today the two always agree — but the answer belongs to the side that
// opens the game's memory, and the day the service is started some other way it will differ.
func (d *remoteDriver) InjectState() ipc.InjectState {
	var st ipc.InjectState
	if err := d.cli.Call("inject.state", nil, &st); err != nil {
		return ipc.InjectState{}
	}
	return st
}

// Library talks to the daemon's store. The root is fetched once at connect time so the "open the
// folder" button can stay a plain string in the UI.
func (d *remoteDriver) Library() libraryAPI {
	if d.lib == nil {
		root, _ := d.cli.LibraryRoot()
		d.lib = &remoteLibrary{cli: d.cli, root: root}
	}
	return d.lib
}

func (d *remoteDriver) Presets() presetsAPI {
	if d.pres == nil {
		d.pres = &remotePresets{cli: d.cli}
	}
	return d.pres
}

func (d *remoteDriver) Close() {
	if d.conn != nil {
		d.conn.Close()
	}
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
		_ = d.cmd.Wait()
	}
}
