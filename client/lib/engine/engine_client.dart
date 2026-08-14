/// Talks to the Go engine service.
///
/// The engine is a separate process, not a library this app links against. That
/// is what lets the UI be written in Dart at all: the GPU work, the image
/// decoding, the on-disk library and the memory injector all stay on the Go
/// side, and nothing here needs FFI.
///
/// The service is spawned as a child and announces itself on its first line of
/// stdout as `{"addr":"127.0.0.1:PORT","token":"..."}`. There is no fixed port
/// to configure and no race on one, so several copies can run side by side.
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'protocol.dart';

/// One thing that happened to a request. Exactly one field is meaningful,
/// selected by [kind]: `log`, `status`, `progress`, `frame`, `done`, `failed`,
/// `ok`, `result`.
class Update {
  Update(this.kind, {this.line, this.frame, this.data, this.error});
  final String kind;
  final String? line;
  final PreviewFrame? frame;
  final Map<String, dynamic>? data;
  final String? error;
}

/// A run in progress: its updates, and a way to stop it.
class RunHandle {
  RunHandle(this.updates, this._cancel);
  final Stream<Update> updates;
  final void Function() _cancel;

  /// Stops the run early. The engine keeps the shapes placed so far and still
  /// emits a terminal event, so a cancelled run is a result, not an error.
  void cancel() => _cancel();
}

String _detail(StringBuffer diagnostics) {
  final text = diagnostics.toString().trim();
  return text.isEmpty ? '' : ' — it said: $text';
}

class EngineException implements Exception {
  EngineException(this.message);
  final String message;
  @override
  String toString() => 'engine: $message';
}

class EngineClient {
  EngineClient._(this._process);

  final Process _process;

  final _reader = MessageReader();
  final _handlers = <int, void Function(Update)>{};
  int _nextId = 0;
  bool _closed = false;

  /// Spawns the service and talks to it over its own pipes.
  ///
  /// [executable] is the studio or `engined` binary. Passing the studio with
  /// `--engine-service` is the normal case.
  ///
  /// No socket, no port, no token: the OS gives a parent and its child a
  /// private channel that nothing else on the machine can reach, so there is
  /// nothing to authenticate and nothing to configure. It also means the engine
  /// cannot outlive us — when this process goes the pipe closes and its next
  /// read ends it.
  static Future<EngineClient> spawn(
    String executable, {
    List<String> args = const ['--engine-service'],
  }) async {
    final process = await Process.start(executable, args);

    // Keep the service's stderr. Bounded so a chatty service cannot grow it
    // without limit, and consumed either way so it cannot fill its pipe and
    // block the child. stdout carries the protocol now, so stderr is the only
    // place a human-readable complaint can appear — including the usage text a
    // binary prints when it rejects its arguments.
    final diagnostics = StringBuffer();
    process.stderr.transform(utf8.decoder).listen((s) {
      if (diagnostics.length < 4096) diagnostics.write(s);
    });

    final client = EngineClient._(process);
    client._listen();
    unawaited(
      process.exitCode.then(
        (code) => client._failAll(
          'the engine service exited (code $code)${_detail(diagnostics)}',
        ),
      ),
    );
    return client;
  }

  void _listen() {
    _process.stdout.listen(
      (chunk) {
        for (final msg in _reader.add(chunk)) {
          _dispatch(msg);
        }
      },
      onError: (Object e) => _failAll('connection error: $e'),
      onDone: () => _failAll('the engine service disconnected'),
      cancelOnError: true,
    );
  }

  void _dispatch(Message msg) {
    if (msg.kind == kindFrame) {
      final frame = decodeFrame(msg.payload);
      _handlers[frame.id]?.call(Update('frame', frame: frame));
      return;
    }
    final resp = jsonDecode(utf8.decode(msg.payload)) as Map<String, dynamic>;
    final id = resp['id'] as int? ?? 0;
    final event = resp['event'] as String? ?? '';
    final handler = _handlers[id];
    if (handler == null) return;

    switch (event) {
      case '':
        handler(Update('result', data: _asMap(resp['result'])));
        _handlers.remove(id);
      case 'log':
        handler(
          Update('log', line: _asMap(resp['result'])?['line'] as String?),
        );
      case 'status':
        handler(
          Update('status', line: _asMap(resp['result'])?['stage'] as String?),
        );
      case 'progress':
        handler(Update('progress', data: _asMap(resp['result'])));
      case 'phase':
        handler(Update('phase', data: _asMap(resp['result'])));
      // Deliver before forgetting: the terminal event is the one the UI most
      // needs, and dropping the handler first makes the run go quiet forever.
      case 'done':
        handler(Update('done', data: _asMap(resp['result'])));
        _handlers.remove(id);
      case 'ok':
        handler(Update('ok'));
        _handlers.remove(id);
      case 'failed':
        handler(Update('failed', error: resp['error'] as String? ?? 'failed'));
        _handlers.remove(id);
    }
  }

  static Map<String, dynamic>? _asMap(Object? v) =>
      v is Map<String, dynamic> ? v : null;

  void _failAll(String reason) {
    if (_closed) return;
    _closed = true;
    final handlers = List.of(_handlers.values);
    _handlers.clear();
    for (final h in handlers) {
      h(Update('failed', error: reason));
    }
  }

  int _claim(void Function(Update) handler) {
    final id = ++_nextId;
    _handlers[id] = handler;
    return id;
  }

  void _send(int id, String method, [Object? params]) {
    if (_closed) throw EngineException('the connection is closed');
    _process.stdin.add(
      encodeJson(<String, dynamic>{
        'id': id,
        'method': method,
        'params': ?params,
      }),
    );
  }

  /// Makes a one-shot request and returns its result.
  Future<Map<String, dynamic>?> call(String method, [Object? params]) {
    final completer = Completer<Map<String, dynamic>?>();
    final id = _claim((u) {
      if (completer.isCompleted) return;
      if (u.kind == 'failed') {
        completer.completeError(EngineException(u.error ?? 'failed'));
      } else if (u.kind == 'result' || u.kind == 'ok') {
        completer.complete(u.data);
      }
    });
    _send(id, method, params);
    return completer.future;
  }

  /// Starts a reconstruction. The parameters are stated in the user's terms —
  /// which file, which region of it, which preset — because the engine owns the
  /// fit resolution, the transparent surround and the hybrid ink split, and
  /// reproducing any of those here would be a second chance to get them wrong.
  RunHandle generate({
    required String path,
    required Map<String, dynamic> choices,
    List<int>? region,
    int? displayRes,
    bool sourceRes = false,
    bool keepInside = false,
    String? output,
  }) {
    final controller = StreamController<Update>();
    final id = _claim((u) {
      controller.add(u);
      if (u.kind == 'done' || u.kind == 'failed') controller.close();
    });
    _send(id, 'generate', <String, dynamic>{
      'path': path,
      'choices': choices,
      'sourceRes': sourceRes,
      'keepInside': keepInside,
      'region': ?region,
      'displayRes': ?displayRes,
      'output': ?output,
    });
    return RunHandle(controller.stream, () {
      _send(++_nextId, 'cancel', {'runId': id});
    });
  }

  /// Writes shapes into the running game. Completes when the write finishes;
  /// there is deliberately no cancel, because stopping halfway through a live
  /// layer table leaves the user's vinyl half-overwritten.
  ///
  /// No elevation anywhere in this path. The game is an ordinary process owned
  /// by the same user at the same integrity level, and Windows grants a
  /// full-access handle to that freely.
  Future<void> inject({
    required List<dynamic> shapes,
    required int width,
    required int height,
    required int layers,
    double scale = 1.0,
    void Function(String)? onLog,
  }) {
    final completer = Completer<void>();
    final id = _claim((u) {
      switch (u.kind) {
        case 'log':
          onLog?.call(u.line ?? '');
        case 'ok':
          completer.complete();
        case 'failed':
          completer.completeError(EngineException(u.error ?? 'inject failed'));
      }
    });
    _send(id, 'inject', {
      'shapes': shapes,
      'width': width,
      'height': height,
      'layers': layers,
      'scale': scale,
    });
    return completer.future;
  }

  /// Renders a shape list and returns the pixels.
  ///
  /// The engine draws it, never this client: one renderer, one definition of
  /// what a livery looks like. A local approximation is fine as a drag ghost but
  /// must never be what the user judges.
  Future<PreviewFrame> render({
    required List<dynamic> shapes,
    required int width,
    required int height,
    bool transparent = false,
  }) {
    final completer = Completer<PreviewFrame>();
    final id = _claim((u) {
      if (completer.isCompleted) return;
      switch (u.kind) {
        case 'frame':
          completer.complete(u.frame!);
        case 'failed':
          completer.completeError(EngineException(u.error ?? 'render failed'));
      }
    });
    _send(id, 'render', {
      'shapes': shapes,
      'width': width,
      'height': height,
      'transparent': transparent,
    });
    return completer.future;
  }

  Future<Map<String, dynamic>> defaults() async => await call('defaults') ?? {};

  Future<List<String>> backends() async {
    final res = await call('backends');
    return ((res?['backends'] as List?) ?? const []).cast<String>();
  }

  Future<List<Map<String, dynamic>>> libraryList() async {
    final res = await call('library.list');
    return ((res?['entries'] as List?) ?? const [])
        .cast<Map<String, dynamic>>();
  }

  Future<Map<String, dynamic>> libraryGeometry(String id) async =>
      await call('library.geometry', {'id': id}) ?? const {};

  /// Returns a stored PNG: [which] is `thumb` or `preview`.
  Future<Uint8List> libraryImage(String id, String which) async {
    final res = await call('library.image', {'id': id, 'which': which});
    return base64Decode(res?['png'] as String? ?? '');
  }

  /// Stores a design. The preview travels as raw pixels so this client needs no
  /// PNG encoder: it already has these bytes, they came off a preview frame.
  Future<Map<String, dynamic>?> librarySave({
    required List<dynamic> shapes,
    required Map<String, dynamic> entry,
    required Uint8List pixels,
    required int pixelW,
    required int pixelH,
    bool replace = false,
  }) => call('library.save', {
    'shapes': shapes,
    'entry': entry,
    'preview': {'w': pixelW, 'h': pixelH, 'pix': base64Encode(pixels)},
    'replace': replace,
  });

  Future<void> libraryDelete(String id) => call('library.delete', {'id': id});

  Future<Map<String, dynamic>?> libraryRename(String id, String name) =>
      call('library.rename', {'id': id, 'name': name});

  /// Saves the whole configuration under a name. The engine assigns the id from
  /// the name, so saving twice under one name replaces rather than duplicates.
  Future<void> presetSave(
    String name,
    Map<String, dynamic> choices,
    bool keepInside,
  ) => call('presets.save', {
    'name': name,
    'keepInside': keepInside,
    'choices': choices,
  });

  Future<void> presetDelete(String id) => call('presets.delete', {'id': id});

  /// Every kind the manual editor may place: the primitives plus the whole
  /// in-game bank, as the engine knows it.
  Future<List<Map<String, dynamic>>> shapeCatalog() async {
    final res = await call('shapes.catalog');
    final list = (res?['kinds'] as List?) ?? const [];
    return list.cast<Map<String, dynamic>>();
  }

  Future<List<Map<String, dynamic>>> presets() async {
    final res = await call('presets.list');
    return ((res?['presets'] as List?) ?? const [])
        .cast<Map<String, dynamic>>();
  }

  Future<void> close() async {
    _failAll('closed by the client');
    // Closing stdin is the polite ask: the service's next read fails, it
    // returns and exits on its own. The kill is the backstop for one that does
    // not.
    try {
      await _process.stdin.close();
    } catch (_) {}
    _process.kill();
  }
}
