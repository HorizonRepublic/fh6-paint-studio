package ipc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"testing"
)

// TestRoundTripMixedStream is the property the whole framing exists for: JSON and binary share one
// connection, and a frame's pixels can contain any byte — including the newline a line-delimited
// protocol would split on, and including bytes that look like a length prefix. Interleaving the two
// and reading them back in order is what proves the reader never has to guess.
func TestRoundTripMixedStream(t *testing.T) {
	var buf bytes.Buffer
	req := Request{ID: 7, Method: "generate"}
	if err := WriteJSON(&buf, req); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	// Pixels chosen to contain \n, \r and 0xFF runs that a naive delimiter scan would trip on.
	pix := make([]byte, 2*3*4)
	for i := range pix {
		pix[i] = []byte{'\n', '\r', 0xFF, 0x00}[i%4]
	}
	if err := WriteFrame(&buf, FrameHeader{ID: 7, W: 2, H: 3}, pix); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	if err := WriteJSON(&buf, Response{ID: 7, Event: "done"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	kind, payload, err := Read(&buf)
	if err != nil || kind != KindJSON {
		t.Fatalf("first message: kind=%d err=%v", kind, err)
	}
	var got Request
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if got.ID != 7 || got.Method != "generate" || got.Params != nil {
		t.Errorf("request round-trip = %+v", got)
	}

	kind, payload, err = Read(&buf)
	if err != nil || kind != KindFrame {
		t.Fatalf("second message: kind=%d err=%v", kind, err)
	}
	h, gotPix, err := DecodeFrame(payload)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if h != (FrameHeader{ID: 7, W: 2, H: 3}) {
		t.Errorf("frame header = %+v", h)
	}
	if !bytes.Equal(gotPix, pix) {
		t.Error("frame pixels did not survive the round trip")
	}

	kind, payload, err = Read(&buf)
	if err != nil || kind != KindJSON {
		t.Fatalf("third message: kind=%d err=%v", kind, err)
	}
	var resp Response
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Event != "done" {
		t.Errorf("event = %q, want done", resp.Event)
	}
	if _, _, err := Read(&buf); err != io.EOF {
		t.Errorf("stream did not end cleanly: %v", err)
	}
}

// TestFrameSizeIsChecked guards both directions of the one mistake that would corrupt the stream
// silently: a header that disagrees with the payload. Writing must refuse it, and reading must not
// hand back pixels it cannot vouch for.
func TestFrameSizeIsChecked(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, FrameHeader{W: 4, H: 4}, make([]byte, 10)); err == nil {
		t.Error("WriteFrame accepted a payload that does not match its dimensions")
	}
	bad := make([]byte, frameHeaderSize+8)
	binary.BigEndian.PutUint32(bad[4:], 4) // W=4
	binary.BigEndian.PutUint32(bad[8:], 4) // H=4, so 64 pixel bytes are declared
	if _, _, err := DecodeFrame(bad); err == nil {
		t.Error("DecodeFrame accepted a header that overstates the payload")
	}
	if _, _, err := DecodeFrame([]byte{1, 2}); err == nil {
		t.Error("DecodeFrame accepted a payload shorter than the header")
	}
}

// TestReadRejectsAnAbsurdLength: a corrupt or hostile length must fail immediately rather than make
// the process try to allocate it. Resynchronising is not possible once framing is lost, so the
// distinct error tells the caller to close rather than retry.
func TestReadRejectsAnAbsurdLength(t *testing.T) {
	var head [5]byte
	binary.BigEndian.PutUint32(head[0:], 0xFFFFFFF0)
	head[4] = KindJSON
	if _, _, err := Read(bytes.NewReader(head[:])); err != ErrTooLarge {
		t.Errorf("Read error = %v, want ErrTooLarge", err)
	}
	binary.BigEndian.PutUint32(head[0:], 0)
	if _, _, err := Read(bytes.NewReader(head[:])); err != ErrTooLarge {
		t.Errorf("zero-length message error = %v, want ErrTooLarge", err)
	}
}

// TestPartialMessageIsAnError: a connection dropped mid-message must surface as an error, not as a
// short read the caller might mistake for a valid small frame.
func TestPartialMessageIsAnError(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, Request{ID: 1, Method: "backends"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	truncated := buf.Bytes()[:buf.Len()-3]
	if _, _, err := Read(bytes.NewReader(truncated)); err == nil {
		t.Error("Read accepted a truncated message")
	}
}
