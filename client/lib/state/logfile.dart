/// The client's log file.
///
/// It exists so that "it did not work" can become a file the user attaches. The
/// engine already keeps `engined.log` beside itself; this is the other half —
/// what the UI saw, in the same folder, so one zip covers both processes.
///
/// Errors above all: an uncaught Flutter exception and an unhandled async error
/// both land here, which is the difference between a bug report and a shrug.
library;

import 'dart:async';
import 'dart:io';

import 'package:flutter/foundation.dart';

class AppLog {
  AppLog._();

  static IOSink? _sink;
  static String? _path;

  /// Where the log ended up, for the About panel to show. Null if no writable
  /// location was found, in which case the app carries on without a log rather
  /// than refusing to start.
  static String? get path => _path;

  static Directory? get folder => _path == null ? null : File(_path!).parent;

  /// Opens the log and takes over the framework's error reporting.
  ///
  /// Beside the executable first: that is where the engine writes, and one
  /// folder is what a user can be asked for over chat. If that folder is
  /// read-only — an install under Program Files — it falls back to the profile.
  static Future<void> init() async {
    for (final dir in _candidates()) {
      try {
        await dir.create(recursive: true);
        final file = File('${dir.path}${Platform.pathSeparator}studio.log');
        // One rotation, at 2 MB. A single old copy is enough to survive a
        // restart-and-reproduce, and unbounded logs on a user's disk are rude.
        if (await file.exists() && await file.length() > 2 * 1024 * 1024) {
          await file.rename('${file.path}.1');
        }
        _sink = file.openWrite(mode: FileMode.append);
        _path = file.path;
        break;
      } catch (_) {
        continue;
      }
    }

    write(
      'info',
      '==== session start ${DateTime.now().toIso8601String()} ====',
    );
    write('info', 'client ${Platform.operatingSystemVersion}');

    final previous = FlutterError.onError;
    FlutterError.onError = (details) {
      write('error', '${details.exceptionAsString()}\n${details.stack}');
      previous?.call(details);
    };
    PlatformDispatcher.instance.onError = (error, stack) {
      write('error', '$error\n$stack');
      return false;
    };
  }

  static Iterable<Directory> _candidates() sync* {
    yield File(Platform.resolvedExecutable).parent;
    final local = Platform.environment['LOCALAPPDATA'];
    if (local != null && local.isNotEmpty) {
      yield Directory('$local${Platform.pathSeparator}FH6PaintStudio');
    }
  }

  /// Opens the folder in Explorer. The user is going to attach the file, and
  /// telling them a path is not the same as putting it in front of them.
  static Future<void> reveal() async {
    final f = folder;
    if (f == null) return;
    try {
      await Process.start('explorer', [f.path]);
    } catch (e) {
      write('warn', 'could not open the log folder: $e');
    }
  }

  static void write(String level, String text) {
    final line = '${DateTime.now().toIso8601String()} $level $text';
    if (kDebugMode) debugPrint(line);
    try {
      _sink?.writeln(line);
    } catch (_) {
      // A log that throws is worse than no log.
    }
  }

  static Future<void> close() async {
    final s = _sink;
    _sink = null;
    await s?.flush();
    await s?.close();
  }
}
