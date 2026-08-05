// Package ipc is the wire protocol between the engine daemon and a UI client.
//
// The UI is being replaced (Gio today, Flutter next), so the engine has to stop being a library the
// UI links against and become a service it talks to. Everything the current UI actually asks of the
// core is small — start a run, cancel it, receive progress/preview/done, plus the library and the
// injector — so the surface here is deliberately narrow rather than a general RPC layer.
//
// Two kinds of traffic share one connection:
//
//   - CONTROL is JSON. Requests carry an id; every reply and every event carries that id back, so a
//     client can have more than one run in flight without correlating by timing.
//   - FRAMES are raw. The polish emits a preview roughly every 50ms, and at 1100px that is ~4.8 MB
//     of RGBA per frame. Base64 inside JSON would triple it and cost a parse on both sides, so a
//     frame is a length-prefixed binary message with a fixed 12-byte header instead.
//
// Framing is length-prefixed rather than newline-delimited precisely because the binary payload can
// contain any byte, including a newline.
package ipc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"fh6-paint-studio/internal/model"
)

// Message kinds. The kind byte follows the length prefix, so a reader knows how to decode the
// payload before touching it.
const (
	KindJSON  byte = 1
	KindFrame byte = 2
)

// MaxMessage bounds a single message. A preview frame at the 4096px ceiling with RGBA is ~67 MB;
// this leaves room for that and still refuses a corrupt length outright instead of trying to
// allocate it.
const MaxMessage = 96 << 20

// FrameHeader precedes a frame's pixels: which run it belongs to, and its dimensions. The pixels
// are straight-alpha RGBA, 4 bytes per pixel, w*h*4 in total.
type FrameHeader struct {
	ID   int32
	W, H int32
}

const frameHeaderSize = 12

// Request is a client -> daemon call. ID must be non-zero and unique among in-flight requests; the
// daemon echoes it on every reply and event so a long-running method can stream.
type Request struct {
	ID     int32           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is a daemon -> client message. Exactly one of Event, Result or Error is set. Event names
// the streamed update ("progress", "status", "log", "done", "failed"); a Result closes a
// non-streaming call.
type Response struct {
	ID     int32           `json:"id"`
	Event  string          `json:"event,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Event payloads. These mirror runner.Event one for one; the duplication is deliberate — the wire
// format is a contract with a client we do not control, and it must not change just because an
// internal struct is refactored.
type (
	// ProgressEvent is emitted per placed shape.
	ProgressEvent struct {
		Shapes    int     `json:"shapes"`
		Total     int     `json:"total"`
		Err       float64 `json:"err"`
		ElapsedMs int64   `json:"elapsedMs"`
	}
	// StatusEvent names the current post-greedy phase; an empty stage clears it.
	StatusEvent struct {
		Stage string `json:"stage"`
	}
	// LogEvent is one human-readable line.
	LogEvent struct {
		Line string `json:"line"`
	}
	// DoneEvent closes a successful run.
	//
	// The shapes travel INLINE. A client needs them for everything that happens after a run — the
	// editor, export, injection, the library — and a path would make the daemon and the client share
	// a filesystem, which stops being true the moment one of them is containerised or remote. A
	// 3000-shape document is a few hundred KB of JSON, far below the message limit.
	//
	// The final canvas is NOT here: it is pixels, and it arrives as a frame just before this event,
	// through the same binary path every preview uses.
	DoneEvent struct {
		Backend      string          `json:"backend"`
		ShapeCount   int             `json:"shapeCount"`
		InitialError float64         `json:"initialError"`
		FinalError   float64         `json:"finalError"`
		ElapsedMs    int64           `json:"elapsedMs"`
		Geometry     *model.Geometry `json:"geometry,omitempty"`
		GeometryPath string          `json:"geometryPath,omitempty"` // written only when the client asked
	}
)

// WriteJSON frames and writes one JSON message.
func WriteJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("ipc: marshal: %w", err)
	}
	return writeFrame(w, KindJSON, b)
}

// WriteFrame writes a preview frame. pix must be w*h*4 straight-alpha RGBA bytes; the header is
// prepended here so callers never assemble the layout themselves.
func WriteFrame(w io.Writer, h FrameHeader, pix []byte) error {
	if len(pix) != int(h.W)*int(h.H)*4 {
		return fmt.Errorf("ipc: frame %dx%d needs %d bytes, got %d", h.W, h.H, int(h.W)*int(h.H)*4, len(pix))
	}
	buf := make([]byte, frameHeaderSize+len(pix))
	binary.BigEndian.PutUint32(buf[0:], uint32(h.ID))
	binary.BigEndian.PutUint32(buf[4:], uint32(h.W))
	binary.BigEndian.PutUint32(buf[8:], uint32(h.H))
	copy(buf[frameHeaderSize:], pix)
	return writeFrame(w, KindFrame, buf)
}

func writeFrame(w io.Writer, kind byte, payload []byte) error {
	if len(payload)+1 > MaxMessage {
		return fmt.Errorf("ipc: message of %d bytes exceeds the %d limit", len(payload)+1, MaxMessage)
	}
	var head [5]byte
	binary.BigEndian.PutUint32(head[0:], uint32(len(payload)+1))
	head[4] = kind
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ErrTooLarge is returned when a declared length exceeds MaxMessage. It is separated out because a
// client should close the connection on it rather than try to resynchronise: a length that large
// means the stream is no longer framed correctly.
var ErrTooLarge = errors.New("ipc: message exceeds the size limit")

// Read reads one message. Exactly one of the returned payloads is non-nil, matching the kind.
func Read(r io.Reader) (kind byte, payload []byte, err error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(head[0:]))
	if n < 1 || n > MaxMessage {
		return 0, nil, ErrTooLarge
	}
	payload = make([]byte, n-1)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return head[4], payload, nil
}

// DecodeFrame splits a KindFrame payload into its header and pixels. The pixels alias the payload
// rather than being copied — the caller owns the buffer Read returned and may keep it.
func DecodeFrame(payload []byte) (FrameHeader, []byte, error) {
	if len(payload) < frameHeaderSize {
		return FrameHeader{}, nil, fmt.Errorf("ipc: frame payload of %d bytes is shorter than its header", len(payload))
	}
	h := FrameHeader{
		ID: int32(binary.BigEndian.Uint32(payload[0:])),
		W:  int32(binary.BigEndian.Uint32(payload[4:])),
		H:  int32(binary.BigEndian.Uint32(payload[8:])),
	}
	pix := payload[frameHeaderSize:]
	if want := int(h.W) * int(h.H) * 4; len(pix) != want {
		return h, nil, fmt.Errorf("ipc: frame %dx%d declares %d pixel bytes, payload carries %d", h.W, h.H, want, len(pix))
	}
	return h, pix, nil
}
