/// The app's state: what is loaded, what is running, and what the engine said.
///
/// Deliberately one object rather than a state-management framework. Everything
/// here is driven by a single event stream from one process, so the thing that
/// actually needs care is not dependency wiring — it is making sure a terminal
/// event always lands, including when the engine dies mid-run.
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/foundation.dart';
import 'package:flutter/painting.dart' show ImageProvider, MemoryImage;

import '../engine/engine_client.dart';
import '../engine/protocol.dart';
import '../ui/window.dart';
import 'logfile.dart';
import 'prefs.dart';

enum Phase { empty, loading, running, done, failed }

class LogLine {
  LogLine(this.time, this.level, this.text);
  final String time;
  final String level;
  final String text;
}

class Studio extends ChangeNotifier {
  EngineClient? _engine;
  RunHandle? _run;
  Prefs? _prefs;

  Phase phase = Phase.empty;
  String? sourcePath;
  String? sourceName;

  /// The live preview. Decoded from raw RGBA off the wire, so there is no PNG
  /// round trip between the engine finishing a frame and it reaching the screen.
  ui.Image? preview;

  /// The source, decoded HERE and only for display: the compare wipe and the
  /// crop rectangle both need something to show. The engine still decodes the
  /// file itself for fitting, so this copy never influences a result.
  ui.Image? sourceImage;
  int sourceW = 0;
  int sourceH = 0;

  /// The crop, as an absolute rectangle in the RAW file's coordinates:
  /// [x, y, w, h]. Absolute for the same reason the protocol is — a crop
  /// composed onto a previous crop makes "a fraction of what" ambiguous.
  List<int>? region;

  /// How much of the result the compare wipe reveals: 0 source, 1 result.
  double compare = 1;

  int shapes = 0;
  int total = 0;
  double error = 0;
  double error0 = 0;
  Duration elapsed = Duration.zero;
  String stage = '';

  /// When each phase ended, measured from the start of the run. Null while the
  /// phase is still ahead or in progress.
  Duration? buildEnd;
  Duration? polishEnd;
  String backend = '';
  double? ssim;
  double? deltaE;
  String? failure;

  final log = <LogLine>[];
  final entries = <Map<String, dynamic>>[];

  /// The finished geometry, kept exactly as the engine sent it. Not re-modelled
  /// into Dart classes: it goes back out to export and to injection unchanged,
  /// and every field this client does not understand today still has to survive
  /// the round trip.
  Map<String, dynamic>? geometry;
  int resultW = 0;
  int resultH = 0;

  /// The saved run currently on the canvas, if it came from the library.
  String? selectedRunId;

  /// Whether an injection can be attempted at all here. Nothing about
  /// privileges: writing the game's memory needs none, and the app has never
  /// run elevated.
  bool injectAvailable = false;
  bool injecting = false;

  /// True once a write has landed, until the next one starts. The injector
  /// takes seconds and says nothing while it works; without this the button
  /// simply came back and you could press it ten more times without ever
  /// learning whether the first one worked.
  bool injectSucceeded = false;
  String? injectError;

  /// The FH6 template's layer count. There is no sane default: it has to match
  /// the group the user actually has open, and a wrong number writes into the
  /// wrong layers.
  int injectLayers = 0;
  double injectScale = 1.0;

  /// Run-level settings that are not part of Choices — the engine takes them on
  /// the request because they describe how to PREPARE the image, not how to fit
  /// it.
  bool sourceRes = false;
  bool keepInside = true;

  /// What the app does when a fit lands. A run takes minutes, so by the time it
  /// finishes the user is usually looking at something else — these are how it
  /// gets their attention back.
  bool soundOnFinish = true;
  bool flashOnFinish = true;

  /// The engine's run settings, verbatim as it sent them.
  ///
  /// The keys are Go FIELD names — `Shapes`, `Mode` — because `preset.Choices`
  /// carries no json tags and is also the on-disk format for saved presets, so
  /// renaming them on the wire would rewrite every preset a user already has.
  /// Read them through the accessors below rather than by hand: a lowercase key
  /// here silently does nothing, which is exactly the kind of bug that survives
  /// a demo.
  Map<String, dynamic> choices = const {};

  static const keyShapes = 'Shapes';
  static const keyMode = 'Mode';

  int get budget => (choices[keyShapes] as num?)?.toInt() ?? 0;
  String get mode => choices[keyMode] as String? ?? '';

  void setBudget(int n) {
    choices = Map<String, dynamic>.of(choices)..[keyShapes] = n;
    _prefs?.set('budget', n);
    notifyListeners();
  }

  void setMode(String m) {
    choices = Map<String, dynamic>.of(choices)..[keyMode] = m;
    _prefs?.set('mode', m);
    notifyListeners();
  }

  /// The connection, for the parts of the app that talk to the engine directly —
  /// the shape editor asks it to render. Null until connect() succeeds.
  EngineClient? get engine => _engine;

  bool get isRunning => phase == Phase.running || phase == Phase.loading;
  double get progress => total == 0 ? 0 : (shapes / total).clamp(0.0, 1.0);

  /// Connects to the engine service and loads what a first screen needs.
  Future<void> connect(String executable) async {
    _executable = executable;
    _prefs = await Prefs.load();
    _engine = await EngineClient.spawn(executable);
    choices = await _engine!.defaults();
    _restore();
    injectAvailable = Platform.isWindows;
    await refreshLibrary();
    await refreshPresets();
    _note('info', 'engine service connected');
    notifyListeners();
  }

  /// Applies remembered settings ON TOP of the engine's defaults, never instead
  /// of them: a preference the user never set must follow the preset, and a
  /// preset the engine retunes has to reach an existing install.
  void _restore() {
    final p = _prefs;
    if (p == null) return;
    final mode = p.get<String>('mode');
    final budget = p.get<int>('budget');
    if (mode != null) choices = Map.of(choices)..[keyMode] = mode;
    if (budget != null) choices = Map.of(choices)..[keyShapes] = budget;
    final saved = p.get<Map<String, dynamic>>('choices');
    if (saved != null) {
      _overrides.addAll(saved);
      choices = Map<String, dynamic>.of(choices)..addAll(saved);
    }
    sourceRes = p.get<bool>('sourceRes') ?? sourceRes;
    keepInside = p.get<bool>('keepInside') ?? keepInside;
    soundOnFinish = p.get<bool>('soundOnFinish') ?? soundOnFinish;
    flashOnFinish = p.get<bool>('flashOnFinish') ?? flashOnFinish;
    injectLayers = p.get<int>('injectLayers') ?? injectLayers;
    injectScale = p.get<double>('injectScale') ?? injectScale;
  }

  /// Starts a fresh engine and picks up where the old one died.
  ///
  /// Guarded against pile-ups: a broken connection tends to fail several
  /// pending requests at once, and each of them would otherwise spawn a service.
  bool _reconnecting = false;

  Future<void> reconnect() async {
    if (_reconnecting || _executable == null) return;
    _reconnecting = true;
    _note('warn', 'the engine went away — starting it again');
    try {
      _engine = null;
      await connect(_executable!);
      _note('info', 'engine back');
    } catch (err) {
      _note('error', 'could not restart the engine: $err');
    }
    _reconnecting = false;
    notifyListeners();
  }

  String? _executable;

  Future<void> refreshLibrary() async {
    final e = _engine;
    if (e == null) return;
    try {
      final list = await e.libraryList();
      entries
        ..clear()
        ..addAll(list);
      // Drop cached thumbnails for runs that no longer exist, so deleting one
      // cannot leave its picture behind if an id is ever reused.
      final ids = {for (final e in list) e['id'] as String};
      _thumbs.removeWhere((id, _) => !ids.contains(id));
      _thumbBytes.removeWhere((id, _) => !ids.contains(id));
    } catch (err) {
      _note('warn', 'library: $err');
    }
    notifyListeners();
  }

  /// Thumbnails, fetched once and remembered.
  ///
  /// A FutureBuilder starts a NEW future on every rebuild, so an uncached call
  /// here meant every repaint — every frame of a slider drag — re-requested the
  /// whole rail over the socket. That is what made the app look like it was
  /// reloading itself continuously.
  final _thumbs = <String, Future<Uint8List>>{};
  final _thumbBytes = <String, Uint8List>{};

  Future<Uint8List> thumb(String id) {
    final e = _engine;
    // Never dereferences a null engine: the gallery is buildable without one in
    // tests, and a thumbnail is not worth crashing a frame over.
    if (e == null) return Future.value(Uint8List(0));
    return _thumbs[id] ??= e
        .libraryImage(id, 'thumb')
        .then((b) => _thumbBytes[id] = b);
  }

  /// The same picture as [thumb], for somewhere that cannot await — the
  /// confirmation dialog, which has to name a run by showing it. Null until the
  /// rail has fetched it, which by then it has.
  ImageProvider? thumbProvider(String id) {
    final bytes = _thumbBytes[id];
    return bytes == null ? null : MemoryImage(bytes);
  }

  /// Points the studio at an image. The pixels are NOT read for fitting: the
  /// engine decodes the file itself at the resolution the preset wants, so a
  /// decode here would only be a second, different answer. The copy loaded below
  /// is for display — the compare wipe and the crop rectangle need something to
  /// show.
  ///
  /// The phase stays idle rather than becoming "loading": loading counts as
  /// running everywhere else, and leaving it there would replace the Generate
  /// button with a Stop button for a run that never started.
  void setSource(String path) {
    sourcePath = path;
    sourceName = path.split(RegExp(r'[\\/]')).last;
    phase = Phase.empty;
    preview?.dispose();
    preview = null;
    sourceImage?.dispose();
    sourceImage = null;
    sourceW = 0;
    sourceH = 0;
    region = null;
    geometry = null;
    resultW = 0;
    resultH = 0;
    selectedRunId = null;
    compare = 1;
    shapes = 0;
    total = 0;
    error = 0;
    error0 = 0;
    stage = '';
    elapsed = Duration.zero;
    ssim = null;
    deltaE = null;
    failure = null;
    log.clear();
    _note('info', 'source: $sourceName');
    notifyListeners();
    _loadSource(path);
  }

  Future<void> _loadSource(String path) async {
    try {
      final bytes = await File(path).readAsBytes();
      final codec = await ui.instantiateImageCodec(bytes);
      final frame = await codec.getNextFrame();
      if (sourcePath != path) {
        frame.image.dispose(); // the user moved on while this was decoding
        return;
      }
      sourceImage = frame.image;
      sourceW = frame.image.width;
      sourceH = frame.image.height;
      _note('info', 'source is ${sourceW}x$sourceH');
    } catch (err) {
      _note('warn', 'could not preview the source: $err');
    }
    notifyListeners();
  }

  void setRegion(List<int>? r) {
    region = r;
    notifyListeners();
  }

  void setCompare(double v) {
    compare = v;
    notifyListeners();
  }

  /// Starts a reconstruction. Everything about HOW to fit it belongs to the
  /// engine; this only says which file and which preset.
  void generate({int? budget, String? mode}) {
    final e = _engine;
    final path = sourcePath;
    if (e == null || path == null || isRunning) return;

    final ch = Map<String, dynamic>.of(choices);
    if (budget != null) ch[keyShapes] = budget;
    if (mode != null) ch[keyMode] = mode;
    choices = ch;

    _cancelled = false;
    phase = Phase.running;
    buildEnd = null;
    polishEnd = null;
    shapes = 0;
    total = (ch[keyShapes] as num?)?.toInt() ?? 0;
    error = 0;
    error0 = 0;
    elapsed = Duration.zero;
    stage = '';
    ssim = null;
    deltaE = null;
    failure = null;
    log.clear();
    notifyListeners();

    // Every run carries a sequence number and its updates are dropped once a
    // newer one exists.
    //
    // A cancelled run does not stop instantly: the engine finishes the batch it
    // is in and keeps emitting frames and a terminal event afterwards. Those
    // were landing in the NEXT run's state — which is why stopping and starting
    // again showed a garbled picture and then claimed to be finished in a
    // second. The flag alone could not fix it, because starting a run cleared
    // the flag before the old run had said its last word.
    final seq = ++_runSeq;
    _startClock();
    _run = e.generate(
      path: path,
      choices: ch,
      displayRes: 1100,
      region: region,
      sourceRes: sourceRes,
      keepInside: keepInside,
    );
    _run!.updates.listen((u) {
      if (seq != _runSeq) return;
      _onUpdate(u);
    });
  }

  int _runSeq = 0;

  /// The run's clock, kept HERE rather than taken from the engine's events.
  ///
  /// The phase rows were showing zeroes: the elapsed field on a progress event
  /// is whatever the engine happened to measure, it does not arrive on a fixed
  /// beat, and nothing arrives at all between the last shape and the first
  /// polish line. A local stopwatch ticking four times a second is a clock the
  /// UI can actually show.
  final _watch = Stopwatch();
  Timer? _ticker;

  void _startClock() {
    _watch
      ..reset()
      ..start();
    _ticker?.cancel();
    _ticker = Timer.periodic(const Duration(milliseconds: 250), (_) {
      if (!isRunning) return;
      elapsed = _watch.elapsed;
      notifyListeners();
    });
  }

  void _stopClock() {
    _watch.stop();
    _ticker?.cancel();
    _ticker = null;
    elapsed = _watch.elapsed;
  }

  /// Stops the run and throws the partial result away.
  ///
  /// The engine returns the shapes it managed to place, and keeping them was the
  /// original behaviour — but a half-fitted picture is not a thing anyone wants
  /// to inject, and offering it made "stop" look like it had done nothing. So a
  /// stop ends with the canvas back where it was.
  void cancel() {
    if (!isRunning) return;
    _cancelled = true;
    _stopClock();
    // Retire the sequence too: everything this run says from here on belongs to
    // a run the user has already dismissed.
    _runSeq++;
    _run?.cancel();
    phase = Phase.empty;
    stage = '';
    preview?.dispose();
    preview = null;
    geometry = null;
    shapes = 0;
    _note('info', 'stopped');
    notifyListeners();
  }

  bool _cancelled = false;

  void _onUpdate(Update u) {
    // The engine finishes what it was doing and still reports a result after a
    // cancel. Ignoring everything but the log keeps a stopped run stopped.
    if (_cancelled) {
      if (u.kind == 'done' || u.kind == 'failed') _cancelled = false;
      return;
    }
    switch (u.kind) {
      case 'log':
        _note('info', u.line ?? '');
      case 'status':
        // The first status line is the engine leaving the greedy loop, so it
        // is also the moment the build phase ended. Recording it here is what
        // lets the card show three DIFFERENT times instead of printing the
        // whole run's elapsed on all three rows.
        buildEnd ??= elapsed;
        stage = u.line ?? '';
      case 'progress':
        final d = u.data ?? const {};
        shapes = (d['shapes'] as num?)?.toInt() ?? shapes;
        total = (d['total'] as num?)?.toInt() ?? total;
        error = (d['err'] as num?)?.toDouble() ?? error;
        if (error0 == 0 && error > 0) error0 = error;
      // The clock is local — see _startClock. The engine reports its own
      // elapsed, but only when it happens to send a progress event, so it
      // stands still through the whole polish.
      case 'frame':
        _decode(u.frame!);
        return; // _decode notifies once the image is ready
      case 'done':
        final d = u.data ?? const {};
        phase = Phase.done;
        stage = '';
        backend = d['backend'] as String? ?? backend;
        shapes = (d['shapeCount'] as num?)?.toInt() ?? shapes;
        error = (d['finalError'] as num?)?.toDouble() ?? error;
        ssim = (d['ssim'] as num?)?.toDouble();
        deltaE = (d['deltaE'] as num?)?.toDouble();
        geometry = d['geometry'] as Map<String, dynamic>?;
        resultW = (d['width'] as num?)?.toInt() ?? 0;
        resultH = (d['height'] as num?)?.toInt() ?? 0;
        selectedRunId = null;
        _stopClock();
        polishEnd = elapsed;
        _note('done', 'done: $shapes shapes in ${_secs(elapsed)}');
        // A fit runs for minutes, so whoever started it is somewhere else by
        // now. Both of these are off in one tick from the settings panel.
        if (soundOnFinish) WindowState.chime();
        if (flashOnFinish) WindowState.flash();
        refreshLibrary();
      case 'failed':
        _stopClock();
        phase = Phase.failed;
        stage = '';
        failure = u.error;
        _note('error', u.error ?? 'failed');
        // A dropped socket is not a failed fit, it is a dead engine — and every
        // later request would fail the same way with no way back short of
        // restarting the app. Reconnecting is the only useful response.
        if ((u.error ?? '').contains('disconnected')) unawaited(reconnect());
    }
    notifyListeners();
  }

  /// Turns wire pixels into an image. decodeImageFromPixels is asynchronous, so
  /// a frame that arrives while the previous one is still decoding would land
  /// out of order — the guard drops the stale one rather than letting the
  /// preview flicker backwards.
  bool _decoding = false;
  PreviewFrame? _pendingFrame;
  void _decode(PreviewFrame f) {
    // A frame arriving mid-decode is stashed, not dropped: the LAST frame of a
    // run is the finished geometry, and dropping it left the canvas one frame
    // behind until an unrelated repaint. Only the newest pending frame is kept —
    // older ones are already stale.
    if (_decoding) {
      _pendingFrame = f;
      return;
    }
    _decoding = true;
    ui.decodeImageFromPixels(
      f.pixels,
      f.width,
      f.height,
      ui.PixelFormat.rgba8888,
      (img) {
        preview?.dispose();
        preview = img;
        _decoding = false;
        notifyListeners();
        final next = _pendingFrame;
        if (next != null) {
          _pendingFrame = null;
          _decode(next);
        }
      },
    );
  }

  /// Puts a saved run on the canvas: its preview, its geometry, its dimensions.
  /// Everything an injection or an export needs comes from the library, so a run
  /// from three days ago behaves exactly like one that just finished.
  Future<void> openRun(Map<String, dynamic> entry) async {
    final e = _engine;
    if (e == null) return;
    final id = entry['id'] as String;
    try {
      final geo = await e.libraryGeometry(id);
      final png = await e.libraryImage(id, 'preview');
      final decoded = await ui.instantiateImageCodec(png);
      final frame = await decoded.getNextFrame();

      // A library run carries no source: it was fitted from a file that may not
      // even be on this machine any more. Clearing it is what stops the compare
      // wipe from pairing this result with the PREVIOUS image, which is the
      // nonsense of half an old picture beside half a new one.
      sourceImage?.dispose();
      sourceImage = null;
      sourcePath = null;
      sourceW = 0;
      sourceH = 0;
      region = null;
      compare = 1;

      geometry = geo;
      resultW = (entry['width'] as num?)?.toInt() ?? frame.image.width;
      resultH = (entry['height'] as num?)?.toInt() ?? frame.image.height;
      shapes = (entry['shapes'] as num?)?.toInt() ?? 0;
      sourceName = entry['name'] as String?;
      selectedRunId = id;
      injectScale = (entry['injectScale'] as num?)?.toDouble() ?? injectScale;
      preview?.dispose();
      preview = frame.image;
      phase = Phase.done;
      ssim = null;
      deltaE = null;
      _note('info', 'opened $sourceName from the library');
    } catch (err) {
      _note('error', 'could not open the run: $err');
    }
    notifyListeners();
  }

  /// Takes an edited document as the current result, so exporting or injecting
  /// after an edit uses what the user actually made. Saving it to the library is
  /// a separate act — a saved run is a thing you chose to keep.
  void adoptEdited(
    Map<String, dynamic> edited,
    ui.Image? rendered, {
    int? width,
    int? height,
    String? name,
  }) {
    geometry = edited;
    shapes = ((edited['shapes'] as List?)?.length ?? 1) - 1;
    if (rendered != null) {
      preview?.dispose();
      preview = rendered.clone();
    }
    // A design built from scratch has no run behind it: without its own
    // dimensions the result is 0×0, which an inject and an export both need.
    if (width != null && width > 0) resultW = width;
    if (height != null && height > 0) resultH = height;
    if (sourceName == null && name != null && name.isNotEmpty) {
      sourceName = name;
    }
    selectedRunId = null;
    phase = Phase.done;
    compare = 1;
    _note('done', 'edited: $shapes shapes');
    notifyListeners();
  }

  /// Saves the current result to the library so it appears in Runs. This is
  /// what the editor's "save to runs" calls: a design edited by hand or built
  /// from scratch has no entry of its own until this writes one — a fit is
  /// persisted by the engine when it finishes, an edit is not.
  ///
  /// Non-destructive: every save is a NEW entry. Editing a run and saving keeps
  /// the original beside it, and two from-scratch designs never overwrite each
  /// other on a shared name.
  Future<void> saveToLibrary(String name) async {
    final e = _engine;
    final g = geometry;
    final img = preview;
    if (e == null || g == null || img == null) return;
    // The library names a run by its source basename with no extension — "car",
    // not "car.png" — so a from-scratch design named after its reference reads
    // the same as a fitted one.
    final display = name
        .replaceFirst(
          RegExp(r'\.(png|jpe?g|webp|bmp|tiff?|gif)$', caseSensitive: false),
          '',
        )
        .trim();
    final safe = display.isEmpty ? 'Untitled' : display;
    try {
      final bytes = await img.toByteData(format: ui.ImageByteFormat.rawRgba);
      if (bytes == null) return;
      final res = await e.librarySave(
        shapes: (g['shapes'] as List?) ?? const [],
        entry: {
          'name': safe,
          'preset': mode,
          'width': resultW,
          'height': resultH,
          'budget': budget,
          'injectScale': injectScale,
          'created': DateTime.now().toUtc().toIso8601String(),
        },
        pixels: bytes.buffer.asUint8List(),
        pixelW: img.width,
        pixelH: img.height,
      );
      // Point the rail at the run just written, the way opening one does.
      final id = (res?['entry'] as Map?)?['id'] as String?;
      if (id != null) selectedRunId = id;
      _note('done', 'saved "$safe" to runs');
      await refreshLibrary();
    } catch (err) {
      _note('error', 'save failed: $err');
    }
    notifyListeners();
  }

  /// Opens the engine's library directory in the file manager. Meaningful only
  /// while the engine shares this machine, which it does today; a remote engine
  /// would report a path that means nothing here, so a missing directory is
  /// simply reported rather than treated as a failure.
  Future<void> openLibraryFolder() async {
    final e = _engine;
    if (e == null) return;
    try {
      final res = await e.call('library.root');
      final root = res?['root'] as String? ?? '';
      if (root.isEmpty || !Directory(root).existsSync()) {
        _note('warn', 'the library folder is not on this machine');
      } else {
        await Process.start('explorer', [root]);
      }
    } catch (err) {
      _note('warn', 'could not open the folder: $err');
    }
    notifyListeners();
  }

  /// Deletes one run. [refresh] is off for a bulk delete, which re-lists the
  /// library once at the end instead of once per run.
  Future<void> deleteRun(String id, {bool refresh = true}) async {
    final e = _engine;
    if (e == null) return;
    try {
      await e.libraryDelete(id);
      if (selectedRunId == id) selectedRunId = null;
      if (refresh) await refreshLibrary();
    } catch (err) {
      _note('error', 'delete failed: $err');
      notifyListeners();
    }
  }

  /// Writes the geometry where the user asked. The document goes out exactly as
  /// it came in, so a field this client never reads is still exported.
  Future<void> exportTo(String path) async {
    final g = geometry;
    if (g == null) return;
    try {
      await File(
        path,
      ).writeAsString(const JsonEncoder.withIndent('  ').convert(g));
      _note('done', 'exported to \$path');
    } catch (err) {
      _note('error', 'export failed: \$err');
    }
    notifyListeners();
  }

  /// Writes the shapes into the running game.
  ///
  /// The layer count is required and not guessed: it has to match the template
  /// group the user has open, and the write walks that table directly. After it
  /// lands the vinyl must be saved and reloaded in-game before it looks right —
  /// FH6 only re-derives the mesh from the word on reload.
  /// The shapes worth writing into the game: fully-transparent ones — a
  /// from-scratch vinyl's invisible background backing (alpha 0) — are dropped,
  /// so the game receives only what is actually drawn and the layer budget is
  /// spent on real shapes. A generation's opaque base fill stays.
  List<dynamic> get injectShapes {
    final all = (geometry?['shapes'] as List?) ?? const [];
    return all.where((s) {
      final c = s is Map ? s['color'] as List? : null;
      return c == null || c.length < 4 || (c[3] as num) > 0;
    }).toList();
  }

  Future<void> inject() async {
    final e = _engine;
    final g = geometry;
    if (e == null || g == null || injecting) return;
    if (injectLayers <= 0) {
      _note('error', 'enter the FH6 template layer count before injecting');
      notifyListeners();
      return;
    }
    injecting = true;
    injectSucceeded = false;
    injectError = null;
    notifyListeners();
    try {
      await e.inject(
        shapes: (g['shapes'] as List?) ?? const [],
        width: resultW,
        height: resultH,
        layers: injectLayers,
        scale: injectScale,
        onLog: (line) => _note('info', line),
      );
      injectSucceeded = true;
    } catch (err) {
      injectError = '$err';
      _note('error', '$err');
    }
    injecting = false;
    notifyListeners();
  }

  /// Forget the previous write's outcome, so the panel opens ready rather than
  /// still showing the last result. The success state turns the button into
  /// "close", which meant a second injection — a different shape count, a
  /// re-run, the same picture after an edit — was impossible without restarting
  /// the app: nothing left on screen could clear the flag.
  void clearInjectResult() {
    if (!injectSucceeded && injectError == null) return;
    injectSucceeded = false;
    injectError = null;
    notifyListeners();
  }

  void setInjectLayers(int n) {
    injectLayers = n;
    _prefs?.set('injectLayers', n);
    notifyListeners();
  }

  void setInjectScale(double v) {
    injectScale = v;
    _prefs?.set('injectScale', v);
    notifyListeners();
  }

  void setSourceRes(bool v) {
    sourceRes = v;
    _prefs?.set('sourceRes', v);
    notifyListeners();
  }

  void setKeepInside(bool v) {
    keepInside = v;
    _prefs?.set('keepInside', v);
    notifyListeners();
  }

  void setSoundOnFinish(bool v) {
    soundOnFinish = v;
    _prefs?.set('soundOnFinish', v);
    notifyListeners();
  }

  void setFlashOnFinish(bool v) {
    flashOnFinish = v;
    _prefs?.set('flashOnFinish', v);
    notifyListeners();
  }

  /// Sets one field of Choices by its Go field name.
  ///
  /// The change is remembered, but only as an OVERRIDE: what is stored is the
  /// keys the user actually touched, not the whole Choices map. Storing the map
  /// would freeze this install's copy of the engine's defaults, so a preset the
  /// engine retunes would never reach anyone who had opened this panel once.
  void setChoice(String key, Object? value) {
    choices = Map<String, dynamic>.of(choices)..[key] = value;
    _overrides[key] = value;
    _prefs?.set('choices', _overrides);
    notifyListeners();
  }

  final _overrides = <String, dynamic>{};

  // ---- user presets -------------------------------------------------------

  /// Configurations the user saved under a name. The engine owns the files, so
  /// they survive a reinstall of the client and are the same objects the old
  /// studio wrote.
  final userPresets = <Map<String, dynamic>>[];

  Future<void> refreshPresets() async {
    final e = _engine;
    if (e == null) return;
    try {
      final list = await e.presets();
      userPresets
        ..clear()
        ..addAll(list);
    } catch (err) {
      _note('warn', 'presets: $err');
    }
    notifyListeners();
  }

  /// Saves everything that decides a run under [name] — the whole Choices map
  /// this time, because a saved preset is meant to be a snapshot: reproducing a
  /// result months later must not depend on what the engine's defaults became.
  Future<void> savePreset(String name) async {
    final e = _engine;
    if (e == null || name.trim().isEmpty) return;
    try {
      await e.presetSave(name.trim(), choices, keepInside);
      await refreshPresets();
      _note('info', 'saved preset "$name"');
    } catch (err) {
      _note('warn', 'could not save the preset: $err');
      notifyListeners();
    }
  }

  Future<void> deletePreset(String id) async {
    final e = _engine;
    if (e == null) return;
    try {
      await e.presetDelete(id);
      await refreshPresets();
    } catch (err) {
      _note('warn', 'could not delete the preset: $err');
      notifyListeners();
    }
  }

  void applyPreset(Map<String, dynamic> p) {
    final c = p['choices'];
    if (c is Map<String, dynamic>) {
      choices = Map<String, dynamic>.of(c);
      _overrides
        ..clear()
        ..addAll(c);
      _prefs?.set('choices', _overrides);
    }
    final ki = p['keepInside'];
    if (ki is bool) setKeepInside(ki);
    notifyListeners();
  }

  void clearLog() {
    log.clear();
    notifyListeners();
  }

  /// A rough time remaining, from the RECENT rate rather than the average.
  /// Shape placement is front-loaded — the early ones are cheap — so an average
  /// over the whole run predicts a finish that keeps receding.
  Duration? get eta {
    if (!isRunning || shapes < 20 || total <= shapes) return null;
    final rate = shapes / elapsed.inMilliseconds.clamp(1, 1 << 30);
    if (rate <= 0) return null;
    return Duration(milliseconds: ((total - shapes) / rate).round());
  }

  void _note(String level, String text) {
    final now = DateTime.now();
    final t =
        '${now.hour.toString().padLeft(2, '0')}:'
        '${now.minute.toString().padLeft(2, '0')}:'
        '${now.second.toString().padLeft(2, '0')}';
    log.add(LogLine(t, level, text));
    if (log.length > 500) log.removeAt(0);
    // The drawer holds the last 500 lines; the file holds all of them. That is
    // the copy a user can send when the drawer has already scrolled past the
    // thing that went wrong.
    AppLog.write(level, text);
  }

  static String _secs(Duration d) =>
      '${(d.inMilliseconds / 1000).toStringAsFixed(1)}s';

  @override
  void dispose() {
    _engine?.close();
    preview?.dispose();
    sourceImage?.dispose();
    super.dispose();
  }
}
