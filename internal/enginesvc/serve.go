// Package enginesvc runs the reconstruction engine as a service for a client that spawned it.
//
// The client owns the process: it launches the engine and talks to it over the pipes the OS set up
// between them. Nothing listens on a port, so nothing else on the machine can reach it and there is
// no secret to guard.
package enginesvc

import (
	"fmt"
	"io"
	"os"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/ipc"
)

// Options configure a service run.
type Options struct {
	// LibraryRoot overrides where saved generations are kept. Empty uses the default under the
	// user's home.
	LibraryRoot string
}

// Serve answers one client over this process's own standard streams and returns when the stream
// ends.
//
// Pipes rather than a socket. The client spawns this process, so the OS has already given the two
// of them a private channel that no third program can reach and that nothing has to authenticate —
// which is why there is no port, no token and no handshake here any more. A listening socket needed
// all three, and needed a CSPRNG for the token, which pulled Go's FIPS module and 32 MB of BSS into
// a service that only ever talked to its own parent.
//
// It also removes the whole class of failure the idle timeout existed to cover: a client that dies
// without closing cleanly used to leave this process holding the GPU with a half-open socket. When
// the parent goes away the OS closes the pipe, the read fails, and this returns.
func Serve(opt Options) error {
	applog.Printf("engine service: talking to the parent over stdio")
	return serve(os.Stdin, os.Stdout, opt)
}

func serve(r io.Reader, w io.Writer, opt Options) error {
	srv := ipc.NewServer(r, w)
	if opt.LibraryRoot != "" {
		srv.SetLibraryRoot(opt.LibraryRoot)
	}
	if err := srv.Serve(); err != nil {
		applog.Printf("engine service: session ended: %v", err)
		return fmt.Errorf("serve: %w", err)
	}
	applog.Printf("engine service: the parent closed the connection")
	return nil
}
