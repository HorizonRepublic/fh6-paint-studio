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

	"fh6-paint-studio/internal/library"
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
	// PhaseEvent is run-wide progress with a time estimate. ProgressEvent counts shapes, which stops
	// meaning anything once placement ends; this one spans the polish and the post-passes too.
	PhaseEvent struct {
		Phase     string  `json:"phase"`
		PhaseFrac float64 `json:"phaseFrac"`
		Overall   float64 `json:"overall"`
		EtaMs     int64   `json:"etaMs"`
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

		// Width/Height are the dimensions the geometry is expressed in — the working image, with any
		// keep-inside surround already removed. A client needs them to export or inject, and deriving
		// them from the source file would be wrong for a cropped or high-resolution fit.
		Width  int `json:"width"`
		Height int `json:"height"`

		// DeltaE/SSIM score the finished render against the source. Measured where the fit happened,
		// which the client cannot reproduce without the padded target, so it travels with the result.
		DeltaE float64 `json:"deltaE,omitempty"`
		SSIM   float64 `json:"ssim,omitempty"`
	}
)

// Library requests. The library is the daemon's because it is an on-disk FORMAT — geometry, preview,
// thumbnail and metadata in one directory — and a second implementation of that format in a second
// language is a second thing to keep in step. A client asks for entries and bytes.
type (
	// LibraryImage is raw straight-alpha RGBA, w*h*4 bytes, base64 in JSON. Raw rather than PNG so a
	// client needs no encoder to save a generation: it already has these pixels, they came from a
	// preview frame. Sent once per save, so the encoding overhead does not matter here the way it
	// does on the twenty-frames-a-second path.
	LibraryImage struct {
		W   int    `json:"w"`
		H   int    `json:"h"`
		Pix []byte `json:"pix"`
	}

	// LibrarySaveParams stores one finished design. The daemon derives the id, the shape count and
	// the thumbnail; the caller supplies what only it knows — what the user called it, what settings
	// produced it.
	LibrarySaveParams struct {
		Shapes  []model.Shape `json:"shapes"`
		Entry   library.Entry `json:"entry"`
		Preview LibraryImage  `json:"preview"`
		// Replace deletes every existing entry with the same NAME first. Manual designs are saved by
		// name and may be overwritten; an auto-saved generation never is.
		Replace bool `json:"replace,omitempty"`
	}

	// LibraryImageParams asks for one stored PNG. Which is "thumb" or "preview".
	LibraryImageParams struct {
		ID    string `json:"id"`
		Which string `json:"which"`
	}
)

// InjectParams is a write into the LIVE game process. It is the one request here with a side effect
// outside this program, so it carries everything the write needs explicitly — the shapes, the
// dimensions they are expressed in, the template's layer count and the canvas scale — rather than
// letting the engine infer any of it from a previous run. An injection derived from stale state
// writes plausible garbage into someone's artwork.
type InjectParams struct {
	Shapes []model.Shape `json:"shapes"`
	Width  int           `json:"width"`
	Height int           `json:"height"`
	Layers int           `json:"layers"` // exact template layer count of the open FH6 group
	Scale  float64       `json:"scale,omitempty"`
}

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
	// Payload = the kind byte + the 12-byte frame header + the pixels. The header
	// goes out in one small write and the pixels follow straight after, so a
	// multi-megabyte frame is never copied into a second buffer just to prepend
	// twelve bytes. The bytes on the wire are byte-for-byte what buffering them
	// produced.
	payloadLen := 1 + frameHeaderSize + len(pix)
	if payloadLen > MaxMessage {
		return fmt.Errorf("ipc: message of %d bytes exceeds the %d limit", payloadLen, MaxMessage)
	}
	var head [5 + frameHeaderSize]byte
	binary.BigEndian.PutUint32(head[0:], uint32(payloadLen))
	head[4] = KindFrame
	binary.BigEndian.PutUint32(head[5:], uint32(h.ID))
	binary.BigEndian.PutUint32(head[9:], uint32(h.W))
	binary.BigEndian.PutUint32(head[13:], uint32(h.H))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	_, err := w.Write(pix)
	return err
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
