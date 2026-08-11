/// What the app remembers between runs.
///
/// Only the choices a user would be annoyed to make twice: the preset and
/// budget they always use, the toggles they set once, the layer count of their
/// template. Not window geometry — the OS restores that — and not anything the
/// engine owns, because a stale copy of an engine default is worse than no copy.
///
/// Stored as JSON beside the app's other per-user state rather than in the
/// registry: it is readable, portable, and deleting it is an obvious repair.
library;

import 'dart:convert';
import 'dart:io';

class Prefs {
  Prefs._(this._file, this._values);

  final File _file;
  final Map<String, dynamic> _values;

  static Future<Prefs> load() async {
    final file = File(_path());
    var values = <String, dynamic>{};
    try {
      if (file.existsSync()) {
        final decoded = jsonDecode(await file.readAsString());
        if (decoded is Map<String, dynamic>) values = decoded;
      }
    } catch (_) {
      // A corrupt file is not worth failing a launch over: start clean and let
      // the next save overwrite it.
    }
    return Prefs._(file, values);
  }

  static String _path() {
    final home =
        Platform.environment['USERPROFILE'] ??
        Platform.environment['HOME'] ??
        '.';
    return '$home${Platform.pathSeparator}FH6PaintStudio'
        '${Platform.pathSeparator}client.json';
  }

  T? get<T>(String key) {
    final v = _values[key];
    return v is T ? v : null;
  }

  void set(String key, Object? value) {
    if (value == null) {
      _values.remove(key);
    } else {
      _values[key] = value;
    }
    _scheduleSave();
  }

  Future<void>? _saving;
  bool _dirty = false;

  /// Fire-and-forget, but SERIALIZED: a burst of changes — a slider sweep firing
  /// `set` continuously — must not launch overlapping writes to the same file,
  /// which collide on Windows as sharing violations and were silently swallowed.
  /// One writer drains a dirty flag, so the last state always lands and no two
  /// writes run at once. Not awaited by the caller: losing the final preference
  /// on a crash is not worth making every toggle asynchronous.
  void _scheduleSave() {
    _dirty = true;
    _saving ??= _drain();
  }

  Future<void> _drain() async {
    while (_dirty) {
      _dirty = false;
      await _writeOnce();
    }
    _saving = null;
  }

  Future<void> _writeOnce() async {
    try {
      await _file.parent.create(recursive: true);
      // Temp-file then rename, so a crash mid-write leaves the old file intact
      // rather than a half-written one the next launch cannot parse.
      final tmp = File('${_file.path}.tmp');
      await tmp.writeAsString(jsonEncode(_values));
      await tmp.rename(_file.path);
    } catch (_) {
      // Preferences are a convenience. An unwritable home directory must not
      // stop the app from working.
    }
  }
}
