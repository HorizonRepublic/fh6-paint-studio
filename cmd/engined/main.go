// Command engined runs the reconstruction engine as a service.
//
// The UI is moving off Gio, so the engine stops being a library the UI links against and becomes a
// process it talks to. That boundary is worth having on its own terms: the client can be rewritten,
// crash, or be replaced entirely without touching the GPU code, and the engine can be updated
// without rebuilding the client.
//
// Transport is loopback TCP with an ephemeral port. A named pipe would be the more idiomatic local
// IPC on Windows, but the intended client is Flutter and dart:io speaks TCP natively while named
// pipes would need FFI on the Dart side. Binding to 127.0.0.1 keeps it off the network; the port and
// a per-process token are printed to stdout as one JSON line for the parent to read.
//
// The token is not a security boundary against a determined local attacker — anything running as
// the same user can read this process's stdout. It is there so that an unrelated program that
// happens to connect to the port cannot start GPU work by accident.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/ipc"
	"fh6-paint-studio/internal/model"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:0", "address to listen on; port 0 takes an ephemeral one")
	linear := flag.Bool("linear", true, "composite in LINEAR light — the space the game renders in. Must match the client's expectation; the studio has always run linear.")
	idle := flag.Duration("idle-timeout", 0, "exit after this long with no client connected (0 = never). A client that spawns the daemon should set it so a crashed UI cannot leave the process behind.")
	flag.Parse()

	model.LinearLight = *linear
	applog.Init("engined")

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fatal("listen: %v", err)
	}
	token := newToken()

	// The parent reads exactly this line to learn where to connect. It is flushed before anything
	// else can be written to stdout, so a client can block on the first line without racing logs.
	hello, _ := json.Marshal(map[string]string{"addr": ln.Addr().String(), "token": token})
	fmt.Println(string(hello))
	os.Stdout.Sync()
	applog.Printf("engined listening on %s", ln.Addr())

	for {
		if *idle > 0 {
			if l, ok := ln.(*net.TCPListener); ok {
				_ = l.SetDeadline(time.Now().Add(*idle))
			}
		}
		conn, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				applog.Printf("engined: no client for %s, exiting", *idle)
				return
			}
			fatal("accept: %v", err)
		}
		// One client at a time, and serially: the engine owns a GPU context and a canvas, so two
		// clients driving it at once would interleave runs on the same device. A second connection
		// waits here rather than being refused, which is what a client restarting after a crash does.
		serve(conn, token)
	}
}

// serve completes the handshake and then hands the connection to the protocol server.
func serve(conn net.Conn, token string) {
	defer conn.Close()
	if err := handshake(conn, token); err != nil {
		applog.Printf("engined: handshake failed: %v", err)
		return
	}
	applog.Printf("engined: client connected from %s", conn.RemoteAddr())
	if err := ipc.NewServer(conn, conn).Serve(); err != nil {
		applog.Printf("engined: connection ended: %v", err)
	} else {
		applog.Printf("engined: client disconnected")
	}
}

// handshake requires the first message to be a hello carrying the token. It runs under a deadline so
// a connection that opens and says nothing cannot hold the daemon's single client slot forever.
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
	var params struct {
		Token string `json:"token"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}
	if req.Method != "hello" {
		return fmt.Errorf("first message was %q, not a hello", req.Method)
	}
	if params.Token != token {
		// Deliberately vague on the wire, precise in the log: the client's author needs the detail,
		// a stray connection does not.
		_ = ipc.WriteJSON(conn, ipc.Response{ID: req.ID, Event: "failed", Error: "rejected"})
		return fmt.Errorf("token mismatch from %s", conn.RemoteAddr())
	}
	return ipc.WriteJSON(conn, ipc.Response{ID: req.ID, Result: json.RawMessage(`{"ok":true}`)})
}

func newToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		fatal("token: %v", err)
	}
	return hex.EncodeToString(b[:])
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "engined: "+format+"\n", args...)
	os.Exit(1)
}
