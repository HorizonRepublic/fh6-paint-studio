// Package enginesvc runs the reconstruction engine as a service on a loopback socket.
//
// It lives in a package rather than in one command because there are two ways to start it and they
// have to be the same service: `engined.exe` for a client that spawns a separate binary, and a hidden
// subcommand of the studio for a client that would rather ship one file. A second implementation of
// the handshake would be a second place for the protocol to drift.
//
// Transport is loopback TCP with an ephemeral port. A named pipe is the more idiomatic local IPC on
// Windows, but the intended client is Flutter and dart:io speaks TCP natively while named pipes need
// FFI on the Dart side. Binding to 127.0.0.1 keeps it off the network; the port and a per-process
// token are printed to stdout as one JSON line for the parent to read.
//
// The token is not a security boundary against a determined local attacker — anything running as the
// same user can read this process's stdout. It is there so an unrelated program that happens to
// connect to the port cannot start GPU work by accident.
package enginesvc

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/ipc"
)

// Options configure a service run.
type Options struct {
	// Listen is the address to bind. Empty means 127.0.0.1 on an ephemeral port.
	Listen string
	// IdleTimeout exits the process after this long with no client connected. Zero never exits. A
	// client that spawns the service should set it, so a crashed UI cannot leave the process behind
	// holding the GPU.
	IdleTimeout time.Duration
	// LibraryRoot overrides where saved generations are kept. Empty uses the default under the
	// user's home.
	LibraryRoot string

	// ExitWithParent ends the process when the parent that spawned it goes away, by watching stdin
	// for EOF. The idle timeout is meant to cover this and does not always: a client that dies
	// without closing its socket cleanly can leave the connection half-open, and the service then
	// sits in Serve holding the GPU with nobody to talk to. Stdin closing is the one signal that
	// arrives whatever the client did — the OS closes the pipe when the parent exits. Off for the
	// studio hosting the service itself, which would be telling itself to quit.
	ExitWithParent bool
}

// Serve listens, announces itself on stdout, and answers clients one at a time until the idle
// timeout expires. It returns only on a fatal listen/accept error or a clean idle exit.
func Serve(opt Options) error {
	addr := opt.Listen
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	token, err := newToken()
	if err != nil {
		return err
	}

	// The parent reads exactly this line to learn where to connect. It goes out before anything else
	// can reach stdout, so a client can block on the first line without racing log output.
	hello, _ := json.Marshal(map[string]string{"addr": ln.Addr().String(), "token": token})
	fmt.Println(string(hello))
	_ = os.Stdout.Sync()
	applog.Printf("engine service listening on %s", ln.Addr())
	// Only when stdin is actually a pipe from a parent. Run by hand from a
	// shell — or double-clicked — stdin is a console or nothing at all, the read
	// returns immediately, and the service would exit the moment it started.
	if opt.ExitWithParent && parentPipe() {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			applog.Printf("engine service: the parent went away, exiting")
			applog.Close()
			os.Exit(0)
		}()
	}

	for {
		if opt.IdleTimeout > 0 {
			if l, ok := ln.(*net.TCPListener); ok {
				_ = l.SetDeadline(time.Now().Add(opt.IdleTimeout))
			}
		}
		conn, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				applog.Printf("engine service: no client for %s, exiting", opt.IdleTimeout)
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		// One client at a time, and serially: the engine owns a GPU context and a canvas, so two
		// clients driving it at once would interleave runs on the same device. A second connection
		// waits here rather than being refused, which is what a client restarting after a crash does.
		serve(conn, token, opt.LibraryRoot)
	}
}

// serve completes the handshake and hands the connection to the protocol server.
func serve(conn net.Conn, token, libraryRoot string) {
	defer conn.Close()
	if err := handshake(conn, token); err != nil {
		applog.Printf("engine service: handshake failed: %v", err)
		return
	}
	applog.Printf("engine service: client connected from %s", conn.RemoteAddr())
	srv := ipc.NewServer(conn, conn)
	if libraryRoot != "" {
		srv.SetLibraryRoot(libraryRoot)
	}
	if err := srv.Serve(); err != nil {
		applog.Printf("engine service: connection ended: %v", err)
	} else {
		applog.Printf("engine service: client disconnected")
	}
}

// handshake requires the first message to be a hello carrying the token. It runs under a deadline so
// a connection that opens and says nothing cannot hold the single client slot forever.
func handshake(conn net.Conn, token string) error {
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	kind, payload, err := ipc.Read(conn)
	if err != nil {
		return err
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	if kind != ipc.KindJSON {
		return fmt.Errorf("first message was kind %d, not a hello", kind)
	}
	var req ipc.Request
	if err := json.Unmarshal(payload, &req); err != nil {
		return err
	}
	if req.Method != "hello" {
		return fmt.Errorf("first message was %q, not a hello", req.Method)
	}
	var params struct {
		Token string `json:"token"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}
	if params.Token != token {
		// Deliberately vague on the wire, precise in the log: the client's author needs the detail, a
		// stray connection does not.
		_ = ipc.WriteJSON(conn, ipc.Response{ID: req.ID, Event: "failed", Error: "rejected"})
		return fmt.Errorf("token mismatch from %s", conn.RemoteAddr())
	}
	return ipc.WriteJSON(conn, ipc.Response{ID: req.ID, Result: json.RawMessage(`{"ok":true}`)})
}

// parentPipe reports whether stdin is a pipe, i.e. something a parent process
// holds the other end of.
func parentPipe() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeNamedPipe != 0
}

func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
