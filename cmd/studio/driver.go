package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"image"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"fh6-paint-studio/internal/engine"
	"fh6-paint-studio/internal/imageio"
	"fh6-paint-studio/internal/ipc"
	"fh6-paint-studio/internal/preset"
	"fh6-paint-studio/internal/runner"
)

// The studio is on its way off Gio, so the engine has to stop being a library it links against.
// This is the seam: both drivers below emit the SAME runner.Event values, so every switch in the UI
// keeps working untouched whether the engine runs in this process or in another one.
//
// Keeping the local driver is not hedging. Until the remote path has run on real work it is the
// reference: any difference in behaviour between them is a bug in the boundary, and having both
// behind one interface is what makes that difference observable instead of theoretical.
type engineDriver interface {
	// Generate starts a run. prep and resolved are what the local engine needs; path and choices are
	// what a remote one needs, since it re-loads the image itself. Callers have both at the call
	// site, and passing both keeps the interface honest about that rather than hiding a re-decode.
	Generate(req driverRequest, onEvent func(runner.Event)) (cancel func(), err error)
	// Close releases anything the driver owns. Safe on a driver that never started a run.
	Close()
}

// driverRequest is one run, described both ways: as the loaded pixels the in-process engine wants,
// and as the source + settings a separate process needs to reproduce them.
type driverRequest struct {
	Prep     imageio.Prepared
	Resolved preset.Resolved

	Path    string         // source image on disk
	MaxRes  int            // the working resolution the prep was built at
	Crop    *[4]float64    // fractional crop, nil for the whole image
	Choices preset.Choices // what the user set, before Resolve

	// Cropped says the working image is a REGION of the file, whether or not Crop expresses it. The
	// studio tracks its crop as an absolute rectangle in raw-file coordinates while the protocol
	// carries a fraction, so the two can disagree — and a driver that silently ran the whole image
	// there would return a perfectly plausible reconstruction of the wrong thing. The remote driver
	// refuses instead.
	Cropped bool
}

// localDriver runs the engine in this process — the behaviour the studio has always had.
type localDriver struct{}

func (localDriver) Generate(req driverRequest, onEvent func(runner.Event)) (func(), error) {
	return runner.RunAsync(req.Prep, req.Resolved, onEvent), nil
}

func (localDriver) Close() {}

// remoteDriver talks to an engined process over the loopback protocol.
type remoteDriver struct {
	cmd  *exec.Cmd
	conn net.Conn
	cli  *ipc.Client
}

// dialEngine spawns the daemon and connects to it. The daemon prints its address and token as the
// first line of stdout, so there is no port to configure and no race on a fixed one — several
// studios can run side by side.
func dialEngine(exePath string) (*remoteDriver, error) {
	if exePath == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, err
		}
		exePath = filepath.Join(filepath.Dir(self), "engined.exe")
	}
	// The daemon exits on its own if the studio dies before connecting, so a crash at startup cannot
	// leave an orphan holding the GPU.
	cmd := exec.Command(exePath, "-idle-timeout", "30s")
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

func (d *remoteDriver) Generate(req driverRequest, onEvent func(runner.Event)) (func(), error) {
	// The final canvas arrives as the LAST frame, one message before done. Hold it so the Done event
	// the UI receives carries a canvas exactly as the in-process runner's does — otherwise every
	// consumer of Done would need a second code path just for the remote driver.
	if req.Cropped && req.Crop == nil {
		return nil, fmt.Errorf("the engine service cannot run a crop it was not given: the studio's crop is an absolute rectangle and the protocol takes a fraction")
	}
	var canvas frameHolder
	return d.cli.Generate(ipc.GenerateParams{
		Path:    req.Path,
		MaxRes:  req.MaxRes,
		Crop:    req.Crop,
		Choices: req.Choices,
	}, func(u ipc.Update) {
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
			onEvent(runner.Done{Result: res, Canvas: canvas.get(), Backend: u.Done.Backend})
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

func (d *remoteDriver) Close() {
	if d.conn != nil {
		d.conn.Close()
	}
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
		_ = d.cmd.Wait()
	}
}
