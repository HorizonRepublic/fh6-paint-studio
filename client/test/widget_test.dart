// The protocol is the part of this client with a contract to keep: the engine on
// the other end frames bytes exactly this way, and a mistake here shows up as a
// hang rather than as a visible error.

import 'dart:convert';
import 'dart:typed_data';

import 'package:fh6_paint_studio/engine/protocol.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  test('a JSON message survives a round trip', () {
    final wire = encodeJson({'id': 7, 'method': 'hello'});
    final msgs = MessageReader().add(wire);
    expect(msgs.length, 1);
    expect(msgs.first.kind, kindJson);
    final decoded =
        jsonDecode(utf8.decode(msgs.first.payload)) as Map<String, dynamic>;
    expect(decoded['method'], 'hello');
  });

  test('messages split across chunks are reassembled', () {
    // A socket splits and coalesces writes freely. This is the case that works
    // by accident on small JSON and fails the first time a frame arrives.
    final wire = encodeJson({'id': 1, 'method': 'defaults'});
    final reader = MessageReader();
    for (var i = 0; i < wire.length - 1; i++) {
      expect(reader.add([wire[i]]), isEmpty, reason: 'byte $i completed early');
    }
    expect(reader.add([wire.last]).length, 1);
  });

  test('two messages in one chunk both come out', () {
    final a = encodeJson({'id': 1, 'method': 'a'});
    final b = encodeJson({'id': 2, 'method': 'b'});
    final msgs = MessageReader().add([...a, ...b]);
    expect(msgs.length, 2);
  });

  test('a frame decodes to its declared size', () {
    const w = 3, h = 2;
    final payload = Uint8List(12 + w * h * 4);
    ByteData.view(payload.buffer)
      ..setInt32(0, 42)
      ..setInt32(4, w)
      ..setInt32(8, h);
    payload[12 + 5] = 200;

    final frame = decodeFrame(payload);
    expect(frame.id, 42);
    expect(frame.width, w);
    expect(frame.height, h);
    expect(frame.pixels.length, w * h * 4);
    expect(frame.pixels[5], 200);
  });

  test('a frame whose pixels do not match its header is rejected', () {
    final payload = Uint8List(12 + 4);
    ByteData.view(payload.buffer)
      ..setInt32(4, 10)
      ..setInt32(8, 10);
    expect(() => decodeFrame(payload), throwsFormatException);
  });
}
