package enginesvc

import (
	"go/parser"
	"go/token"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fh6-paint-studio/internal/ipc"
)

// The service must not listen on anything. It used to bind 127.0.0.1 and guard the port with a
// token, which is the only reason it ever needed a CSPRNG — and a background process holding an
// open port is a shape worth not having. The client spawns it, so the pipes the OS already set up
// between them are private by construction.
func TestServiceOpensNoPort(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	banned := []string{`"net"`, `"crypto/rand"`, `"crypto/`}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, imp := range parsed.Imports {
			for _, b := range banned {
				if strings.HasPrefix(imp.Path.Value, b) {
					t.Errorf("%s imports %s; the service neither listens nor needs a token", f, imp.Path.Value)
				}
			}
		}
	}
}

// And it must actually answer over whatever streams it is handed, since that is now the only way in.
func TestServeAnswersOverTheStreams(t *testing.T) {
	srvConn, cliConn := net.Pipe()
	defer cliConn.Close()

	go func() { _ = serve(srvConn, srvConn, Options{LibraryRoot: t.TempDir()}) }()

	cli := ipc.NewClient(cliConn, cliConn)
	go func() { _ = cli.Listen() }()

	done := make(chan error, 1)
	go func() {
		var out struct {
			Backends []string `json:"backends"`
		}
		done <- cli.Call("backends", nil, &out)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the service did not answer over its streams: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the service never answered")
	}
}
