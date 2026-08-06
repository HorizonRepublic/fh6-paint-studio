/// Drives the engine service exactly the way the app does, without a window.
/// Run it from the client directory:
///
///   dart run tool/probe.dart ..\bin\engined.exe
library;

import 'dart:io';

import 'package:fh6_paint_studio/engine/engine_client.dart';

Future<void> main(List<String> args) async {
  final exe = args.isEmpty ? r'..\bin\engined.exe' : args.first;
  stdout.writeln('spawning $exe');
  stdout.writeln('exists: ${File(exe).existsSync()}');

  final client = await EngineClient.spawn(exe);
  stdout.writeln('connected');
  stdout.writeln('backends: ${await client.backends()}');
  stdout.writeln('defaults: ${(await client.defaults()).keys.toList()}');
  stdout.writeln('library: ${(await client.libraryList()).length} entries');

  // A real run. The frame path is the part that cannot be checked any other
  // way: preview pixels arrive as raw binary alongside JSON on one socket, and a
  // framing mistake shows up as a hang rather than as an error.
  if (args.length > 1) {
    final defaults = await client.defaults();
    final choices = Map<String, dynamic>.of(defaults)..['Shapes'] = 120;
    var frames = 0, progress = 0;
    final run = client.generate(
      path: args[1],
      choices: choices,
      displayRes: 600,
    );
    await for (final u in run.updates) {
      switch (u.kind) {
        case 'frame':
          frames++;
        case 'progress':
          progress++;
        case 'log':
          stdout.writeln('  ${u.line}');
        case 'done':
          stdout.writeln(
            'DONE ${u.data!['shapeCount']} shapes, '
            '${u.data!['width']}x${u.data!['height']}, '
            'SSIM ${u.data!['ssim']}, dE ${u.data!['deltaE']} '
            '($progress progress, $frames frames)',
          );
        case 'failed':
          stdout.writeln('FAILED: ${u.error}');
      }
    }
  }

  // The editor's render path: a document in, pixels back. Exercised here because
  // the editor is the only caller and a framing mistake would look like a
  // frozen canvas rather than an error.
  final frame = await client.render(
    shapes: [
      {
        'type': 1,
        'data': [0, 0, 64, 48],
        'color': [20, 20, 24, 255],
      },
      {
        'type': 16,
        'data': [32, 24, 12, 8, 30],
        'color': [200, 90, 120, 255],
      },
    ],
    width: 64,
    height: 48,
  );
  stdout.writeln(
    'RENDER ${frame.width}x${frame.height}, ${frame.pixels.length} bytes',
  );

  await client.close();
}
