/// The wire protocol between this client and the Go engine service.
///
/// This is the Dart half of `internal/ipc`. The two ends have to agree byte for
/// byte, so the framing rules are restated here rather than inferred:
///
///  * Every message is `[4-byte big-endian length][1-byte kind][payload]`, where
///    the length counts the kind byte. Length-prefixed rather than newline
///    delimited because a preview frame's pixels contain every byte value,
///    newline included.
///  * Kind 1 is JSON control traffic. Kind 2 is a preview frame: a 12-byte
///    header of three big-endian int32s (run id, width, height) followed by
///    width*height*4 bytes of straight-alpha RGBA.
///
/// Frames are raw for a reason worth keeping in mind when extending this: the
/// polish emits one roughly every 50ms, and at 1100px that is ~4.8 MB. Base64
/// inside JSON would triple it and add a parse on both ends.
library;

import 'dart:convert';
import 'dart:typed_data';

const int kindJson = 1;
const int kindFrame = 2;

/// Bounds a single message. A preview at the 4096px ceiling is ~67 MB; anything
/// past this means the stream is no longer framed correctly, and the only safe
/// response is to close rather than try to resynchronise.
const int maxMessage = 96 << 20;

const int _frameHeaderSize = 12;

/// One decoded message off the wire.
class Message {
  const Message(this.kind, this.payload);
  final int kind;
  final Uint8List payload;
}

/// A preview frame: which run it belongs to, and its pixels.
class PreviewFrame {
  const PreviewFrame(this.id, this.width, this.height, this.pixels);
  final int id;
  final int width;
  final int height;

  /// Straight-alpha RGBA, `width * height * 4` bytes. Not copied out of the read
  /// buffer — the buffer is never reused, so the frame can own it.
  final Uint8List pixels;
}

/// Encodes one JSON message.
Uint8List encodeJson(Object value) {
  final body = utf8.encode(jsonEncode(value));
  return _frame(kindJson, body);
}

Uint8List _frame(int kind, List<int> payload) {
  final out = Uint8List(5 + payload.length);
  final view = ByteData.view(out.buffer);
  view.setUint32(0, payload.length + 1); // big-endian by default
  out[4] = kind;
  out.setRange(5, out.length, payload);
  return out;
}

/// Splits a kind-2 payload into its header and pixels.
PreviewFrame decodeFrame(Uint8List payload) {
  if (payload.length < _frameHeaderSize) {
    throw FormatException(
      'frame payload of ${payload.length} bytes is shorter than its header',
    );
  }
  final head = ByteData.view(
    payload.buffer,
    payload.offsetInBytes,
    _frameHeaderSize,
  );
  final id = head.getInt32(0);
  final w = head.getInt32(4);
  final h = head.getInt32(8);
  final want = w * h * 4;
  final pixels = Uint8List.sublistView(payload, _frameHeaderSize);
  if (pixels.length != want) {
    throw FormatException(
      'frame ${w}x$h declares $want pixel bytes, payload carries ${pixels.length}',
    );
  }
  return PreviewFrame(id, w, h, pixels);
}

/// Reassembles messages from a byte stream that arrives in arbitrary chunks.
///
/// A socket splits and coalesces writes freely, so a reader that assumed one
/// read equals one message would work perfectly on small JSON and then fail the
/// first time a multi-megabyte frame arrived. This buffers until a whole message
/// is present.
class MessageReader {
  final _chunks = <Uint8List>[];
  int _pending = 0;

  /// Feeds a chunk and returns every complete message it completed.
  ///
  /// O(total bytes): each byte is copied at most once, into its message's own
  /// buffer. The previous drain-and-put-back approach re-copied everything
  /// buffered so far on EVERY arriving chunk — quadratic in message size, and
  /// a full-resolution frame is hundreds of chunks, every copy on the UI
  /// isolate while pointer events waited behind it.
  List<Message> add(List<int> chunk) {
    if (chunk.isEmpty) return const [];
    _chunks.add(chunk is Uint8List ? chunk : Uint8List.fromList(chunk));
    _pending += chunk.length;

    final out = <Message>[];
    while (_pending >= 5) {
      final length = _peekLength();
      if (length < 1 || length > maxMessage) {
        throw FormatException('message length $length is out of range');
      }
      final total = 4 + length;
      if (_pending < total) break;
      final msg = _take(total);
      out.add(Message(msg[4], Uint8List.sublistView(msg, 5)));
    }
    return out;
  }

  /// The 4-byte length prefix, without consuming anything.
  int _peekLength() {
    final first = _chunks.first;
    if (first.length >= 4) {
      return ByteData.sublistView(first, 0, 4).getUint32(0);
    }
    final head = Uint8List(4);
    var n = 0;
    for (final c in _chunks) {
      for (var i = 0; i < c.length && n < 4; i++) {
        head[n++] = c[i];
      }
      if (n == 4) break;
    }
    return ByteData.sublistView(head).getUint32(0);
  }

  /// Removes exactly [n] bytes from the front, as one contiguous buffer.
  Uint8List _take(int n) {
    final first = _chunks.first;
    // Inside the first chunk: views, no copying at all.
    if (first.length == n) {
      _chunks.removeAt(0);
      _pending -= n;
      return first;
    }
    if (first.length > n) {
      _chunks[0] = Uint8List.sublistView(first, n);
      _pending -= n;
      return Uint8List.sublistView(first, 0, n);
    }
    final msg = Uint8List(n);
    var filled = 0;
    while (filled < n) {
      final c = _chunks.first;
      final need = n - filled;
      if (c.length <= need) {
        msg.setRange(filled, filled + c.length, c);
        filled += c.length;
        _chunks.removeAt(0);
      } else {
        msg.setRange(filled, filled + need, c);
        _chunks[0] = Uint8List.sublistView(c, need);
        filled = n;
      }
    }
    _pending -= n;
    return msg;
  }
}
