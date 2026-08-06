package main

import (
	"fmt"
	"image"
	"io"
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

	// InjectState reports whether an injection is possible at all here.
	InjectState() injectState

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

func (*localDriver) InjectState() injectState {
	return injectState{Available: inject.NewFH6().Available()}
}

func (*localDriver) Close() {}

// injectState says whether an injection can be attempted here at all. There is nothing about
// privileges in it: the game is an ordinary same-user process and writing it needs none.
type injectState struct {
	Available bool
}

// remoteDriver talks to an engined process over the loopback protocol.
type remoteDriver struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
	cli  *ipc.Client
	lib  libraryAPI
	pres presetsAPI
}

// dialEngine spawns the engine service and talks to it over its own pipes. By default it spawns THIS
// binary with the --engine-service subcommand, which is why the release is two files rather than
// three; FH6_ENGINED points at a separate engined.exe instead.
//
// No socket, no port, no token. The OS gives a parent and its child a private channel that nothing
// else on the machine can reach, so there is nothing to authenticate and nothing to configure —
// and several studios can run side by side without arguing over a port.
func dialEngine(exePath string) (*remoteDriver, error) {
	var args []string
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
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", exePath, err)
	}

	d := &remoteDriver{cmd: cmd, in: stdin, out: stdout, cli: ipc.NewClient(stdout, stdin)}
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

// Inject happens HERE, never over the socket. The service deliberately cannot write another
// process's memory — that is the whole point of it being a plain compute daemon — so both drivers
// do the write in the studio's own process, which is where the Windows handles and the user's
// elevation already are.
// Inject goes over the protocol, so the write happens wherever the engine does.
func (d *remoteDriver) Inject(p ipc.InjectParams, onLog func(string)) error {
	return d.cli.Inject(p, onLog)
}

func (*remoteDriver) InjectState() injectState {
	return injectState{Available: inject.NewFH6().Available()}
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
	// Closing our end of its stdin is how the service is asked to stop: the read fails, Serve
	// returns and the process exits on its own. The kill below is the backstop for one that does not.
	if d.in != nil {
		_ = d.in.Close()
	}
	if d.out != nil {
		_ = d.out.Close()
	}
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
		_ = d.cmd.Wait()
	}
}
