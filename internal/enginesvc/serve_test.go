package enginesvc

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"fh6-paint-studio/internal/ipc"
)

// The handshake is the only thing standing between the port and GPU work, and both of its outcomes
// are silent from the outside: an accepted client just proceeds, a rejected one just sees a closed
// connection. Neither shows up in a run, so they get covered here.
func TestHandshake(t *testing.T) {
	const token = "0123456789abcdef"

	t.Run("accepts the right token", func(t *testing.T) {
		err := exchange(t, token, ipc.Request{
			ID: 1, Method: "hello", Params: json.RawMessage(`{"token":"` + token + `"}`),
		})
		if err != nil {
			t.Fatalf("handshake rejected a valid client: %v", err)
		}
	})

	t.Run("refuses a wrong token", func(t *testing.T) {
		if err := exchange(t, token, ipc.Request{
			ID: 1, Method: "hello", Params: json.RawMessage(`{"token":"not-it"}`),
		}); err == nil {
			t.Fatal("handshake accepted a client with the wrong token")
		}
	})

	t.Run("refuses a client that opens with something else", func(t *testing.T) {
		if err := exchange(t, token, ipc.Request{ID: 1, Method: "generate"}); err == nil {
			t.Fatal("handshake accepted a request that was not a hello — that is GPU work from an unknown caller")
		}
	})
}

// A connection that opens and says nothing must not hold the single client slot: the service serves
// one client at a time, so a silent socket would otherwise lock out the real UI.
func TestHandshakeDeadlineFreesTheSlot(t *testing.T) {
	srv, cli := net.Pipe()
	defer cli.Close()

	done := make(chan error, 1)
	go func() { done <- handshake(srv, "token") }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a silent client completed the handshake")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a silent client held the connection open past the deadline")
	}
}

func exchange(t *testing.T, token string, req ipc.Request) error {
	t.Helper()
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()

	done := make(chan error, 1)
	go func() { done <- handshake(srv, token) }()

	if err := ipc.WriteJSON(cli, req); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	// Drain whatever the server replies so its write cannot block on an unread pipe.
	go func() { _, _, _ = ipc.Read(cli) }()

	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("handshake never returned")
		return nil
	}
}
