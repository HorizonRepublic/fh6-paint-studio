// Command engined runs the reconstruction engine as a standalone service.
//
// The studio can host the same service itself (`fh6-paint-studio.exe --engine-service`), so this
// binary is for the cases where a separate process is the point: a client that is not the studio, a
// debugging session where the engine should outlive the UI, or a deployment that ships them apart.
// Both entry points call internal/enginesvc, so there is one implementation of the protocol.
package main

import (
	"flag"
	"fmt"
	"os"

	"fh6-paint-studio/internal/applog"
	"fh6-paint-studio/internal/enginesvc"
	"fh6-paint-studio/internal/model"
)

func main() {
	// A client spawns "the engine" without caring which binary it got: the studio
	// hosts the service behind --engine-service, and this command IS the service.
	// Accepting the flag as a no-op means one calling convention works for both —
	// otherwise flag.Parse rejects it, usage goes to stderr, and the client sees a
	// silent stdout and no explanation at all.
	if len(os.Args) > 1 && (os.Args[1] == "--engine-service" || os.Args[1] == "-engine-service") {
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	addr := flag.String("listen", "127.0.0.1:0", "address to listen on; port 0 takes an ephemeral one")
	linear := flag.Bool("linear", true, "composite in LINEAR light — the space the game renders in. Must match the client's expectation; the studio has always run linear.")
	lib := flag.String("library", "", "directory for saved generations; empty uses the default under the user's home")
	idle := flag.Duration("idle-timeout", 0, "exit after this long with no client connected (0 = never). A client that spawns the daemon should set it so a crashed UI cannot leave the process behind.")
	flag.Parse()

	model.LinearLight = *linear
	// The log is the only thing a user can send back when the engine misbehaves,
	// so it has to survive every way this process can end: a clean return, a
	// fatal error, or a panic on the GPU thread.
	applog.Init("engined.log")
	defer applog.Close()
	defer applog.Recover()

	err := enginesvc.Serve(enginesvc.Options{
		Listen:      *addr,
		IdleTimeout: *idle,
		LibraryRoot: *lib,
		// This binary is always somebody's child. Outliving them is never useful:
		// it holds the GPU and the next launch starts a second one.
		ExitWithParent: true,
	})
	if err != nil {
		applog.Printf("FATAL: %v", err)
		fmt.Fprintln(os.Stderr, "engined:", err)
		applog.Close()
		os.Exit(1)
	}
}
