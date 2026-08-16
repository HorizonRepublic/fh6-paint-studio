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
    refreshKnobDefaults();
  }

  /// The connection, for the parts of the app that talk to the engine directly —
  /// the shape editor asks it to render. Null until connect() succeeds.
  EngineClient? get engine => _engine;

  bool get isRunning => phase == Phase.running || phase == Phase.loading;
  /// Fraction of the WHOLE run. The shape counter only describes placement, so
  /// a bar driven by it reached 100% and then sat there for the polish and the
  /// post-passes — close to half the run. The engine reports its own overall
  /// progress across every phase; the shape ratio stays as the fallback for an
  /// older engine and for the moment before the first phase event lands.
  double get progress => overall > 0
      ? overall.clamp(0.0, 1.0)
      : (total == 0 ? 0 : (shapes / total).clamp(0.0, 1.0));

  /// Connects to the engine service and loads what a first screen needs.
  Future<void> connect(String executable) async {
    _executable = executable;
    _prefs = await Prefs.load();
    _engine = await EngineClient.spawn(executable);
    // Any death of THIS client — mid-run or idle — restarts the engine. Keying
    // on the run stream's 'failed' only ever saw mid-run deaths; a driver reset
    // while the app sat idle left every later request throwing 'connection is
    // closed' with no way back short of relaunching.
    _engine!.onDeath = (_) => unawaited(reconnect());
    final v = await _engine!.hello();
    if (v != EngineClient.protocolVersion) {
      _note('warn',
          'engine speaks protocol v$v, this client expects v${EngineClient.protocolVersion} — mixed versions? replace both files together');
    }
    choices = await _engine!.defaults();
    _defaults = Map<String, dynamic>.of(choices);
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
      // Close the dead client first: a hung-but-alive process would otherwise
      // keep its GPU/VRAM allocation next to the fresh one forever.
      final old = _engine;
      _engine = null;
      if (old != null) {
        try {
          await old.close();
        } catch (_) {}
      }
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
    // A rejected future must not be remembered: one transient engine hiccup used
    // to leave that tile blank for the rest of the session. The catch must be
    // CHAINED, not cascaded: `..catchError` returns the pre-catch future, so the
    // caller still got the rejection (a broken tile) and only the cache healed.
    return _thumbs[id] ??= e
        .libraryImage(id, 'thumb')
        .then((b) => _thumbBytes[id] = b)
        .catchError((Object err) {
          _thumbs.remove(id);
          return Uint8List(0);
        });
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
    // A drop mid-run used to silently abandon the run (UI empty, engine still
    // fitting the OLD image, Generate re-armed → two concurrent fits). Stop the
    // run first — for EVERY entry, .json included: the JSON branch used to return
    // above this, so a .forza.json dropped mid-fit left the run alive to clobber
    // the imported result (and inject the wrong geometry). Its retired sequence
    // keeps its late events out.
    if (isRunning) cancel();
    _retarget();
    // A geometry export is not a picture to fit — it IS a result. Routing it
    // here means every way a file enters the app (drop, picker, source chip)
    // can also bring a shared .forza.json back to life.
    if (path.toLowerCase().endsWith('.json')) {
      unawaited(importGeometry(path));
      return;
    }
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
    phaseName = '';
    overall = 0;
    engineEta = null;
    elapsed = Duration.zero;
    ssim = null;
    deltaE = null;
    failure = null;
    log.clear();
    _note('info', 'source: $sourceName');
    notifyListeners();
    _loadSource(path);
    refreshKnobDefaults();
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
      sourceImage?.dispose(); // two in-flight decodes of the SAME path: don't leak the first
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
    phaseName = '';
    overall = 0;
    engineEta = null;
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
    // Guarded: a dead engine client throws out of generate(), and before this
    // guard that exception escaped the tap callback AFTER phase was set to
    // running — the UI wedged at 0% forever with Stop unable to clear it.
    try {
      _run = e.generate(
        path: path,
        choices: ch,
        displayRes: 1100,
        region: region,
        sourceRes: sourceRes,
        keepInside: keepInside,
      );
    } catch (err) {
      _stopClock();
      phase = Phase.failed;
      failure = '$err';
      _note('error', '$err');
      notifyListeners();
      return;
    }
    _run!.updates.listen((u) {
      if (seq != _runSeq) return;
      _onUpdate(u);
    });
  }

  int _runSeq = 0;

  /// Bumped whenever the canvas is repointed at something new (a source, a saved
  /// run, an imported JSON). The async openers capture it before their first
  /// await and abort if it moved — otherwise a slow open landing after the user
  /// moved on would overwrite the newer result and dispose its freshly-loaded
  /// image out from under the UI.
  int _openEpoch = 0;

  /// Everything that must happen when the canvas is pointed somewhere new.
  ///
  /// Kept in one place because the three openers kept drifting apart: a pending
  /// auto-save belonging to the previous fit would fire against the new target,
  /// and the taskbar's red error state — deliberately left standing after a
  /// failed run — survived loading a fresh picture, so the button stayed red
  /// through editing and exporting until some later run happened to clear it.
  void _retarget() {
    _openEpoch++;
    _persistPending = false;
    if (phase == Phase.failed) WindowState.progressDone();
  }

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
      // Driven from the clock rather than from frame events: 4 Hz is plenty for
      // a taskbar bar, and it keeps the shell call off the per-shape path.
      WindowState.progress(progress);
      notifyListeners();
    });
  }

  void _stopClock() {
    _watch.stop();
    _ticker?.cancel();
    _ticker = null;
    elapsed = _watch.elapsed;
    // Whatever ended the run, the taskbar must not keep showing it as working.
    // The failure case overrides this immediately after.
    WindowState.progressDone();
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
    // State is reset BEFORE the cancel call: a dead engine makes _run.cancel()
    // throw, and throwing first used to leave the UI wedged in "running" with
    // a Stop button that could never work.
    phase = Phase.empty;
    stage = '';
    preview?.dispose();
    preview = null;
    geometry = null;
    shapes = 0;
    _note('info', 'stopped');
    notifyListeners();
    try {
      _run?.cancel();
    } catch (_) {
      // The engine is gone; the run died with it. The reconnect path handles it.
    }
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
      case 'phase':
        final d = u.data ?? const {};
        phaseName = (d['phase'] as String?) ?? phaseName;
        overall = (d['overall'] as num?)?.toDouble() ?? overall;
        final ms = (d['etaMs'] as num?)?.toInt() ?? 0;
        engineEta = ms > 0 ? Duration(milliseconds: ms) : null;
      case 'progress':
        final d = u.data ?? const {};
        shapes = (d['shapes'] as num?)?.toInt() ?? shapes;
        total = (d['total'] as num?)?.toInt() ?? total;
        error = (d['err'] as num?)?.toDouble() ?? error;
        if (error0 == 0 && error > 0) error0 = error;
        // NO notify: progress fires once per placed shape (up to hundreds/s) and every notify
        // rebuilt the whole shell. The 4Hz run ticker repaints these counters; frames and
        // phase/terminal events still notify immediately, so nothing visible lags.
        return;
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
        // Keep it. Until now a finished fit lived only on the canvas and was
        // lost the instant the next picture arrived.
        _persistPending = true;
        _persistFinishedRun();
      case 'failed':
        _stopClock();
        // Red on the taskbar, so a run that died while the user was in the game
        // says so from the taskbar rather than only inside the window.
        WindowState.progressFailed();
        phase = Phase.failed;
        stage = '';
        failure = u.error;
        _note('error', u.error ?? 'failed');
        // A dead engine reconnects through EngineClient.onDeath now (wired in
        // connect), which fires for idle deaths too — not just the ones that
        // happen to arrive on a live run's stream, which is all this string
        // match ever caught.
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
    // The run's sequence is captured NOW: a decode that lands after Stop (or
    // after dispose) must not resurrect the abandoned picture onto the canvas.
    //
    // The OPEN epoch too. After 'done' the phase is no longer running, so
    // setSource/openRun skip their `if (isRunning) cancel()` and never bump
    // _runSeq — only _openEpoch. A file dropped while the run's last frame was
    // still decoding therefore passed the sequence check and painted the old
    // fit over the freshly opened picture, with the header naming the new one.
    final seq = _runSeq;
    final epoch = _openEpoch;
    ui.decodeImageFromPixels(
      f.pixels,
      f.width,
      f.height,
      ui.PixelFormat.rgba8888,
      (img) {
        _decoding = false;
        if (seq == _runSeq && epoch == _openEpoch && !_disposed) {
          preview?.dispose();
          preview = img;
          notifyListeners();
        } else {
          img.dispose(); // stale THIS decode — but the pending one may be current
        }
        // Always drain the stash and let _decode's own seq capture judge it: a
        // frame stashed for the NEW run was being nulled here just because the
        // decode it was waiting behind turned out to be stale.
        final next = _pendingFrame;
        _pendingFrame = null;
        if (next != null && !_disposed) {
          _decode(next);
        } else {
          // The last frame of the run has landed: the picture the library
          // should store is finally the finished one.
          _persistFinishedRun();
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
    // Opening a saved run mid-fit used to leave the run alive: its late frames
    // (whose sequence was never retired) then overwrote the opened preview and
    // geometry — an inject in that window wrote the wrong shapes into the game.
    if (isRunning) cancel();
    _retarget();
    final epoch = _openEpoch;
    final id = entry['id'] as String;
    try {
      final geo = await e.libraryGeometry(id);
      final png = await e.libraryImage(id, 'preview');
      final decoded = await ui.instantiateImageCodec(png);
      final frame = await decoded.getNextFrame();
      if (epoch != _openEpoch || _disposed) {
        frame.image.dispose(); // the user opened something else meanwhile
        return;
      }

      // Clear the previous source first: pairing this result with the picture
      // that happened to be open is the nonsense of half an old picture beside
      // half a new one.
      sourceImage?.dispose();
      sourceImage = null;
      sourcePath = null;
      sourceW = 0;
      sourceH = 0;
      region = null;
      compare = 1;

      // Then put the run's OWN source back if it is still on this machine. The
      // compare wipe — the app's main way of judging a result — was dead for
      // every saved run, because a run carried no record of what it was fitted
      // from. Runs saved before this simply have no `source` and behave as they
      // did; a file that has since moved is not an error, just no wipe.
      final src = entry['source'] as String?;
      if (src != null && src.isNotEmpty && File(src).existsSync()) {
        sourcePath = src;
        unawaited(_loadSource(src));
      }

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

  /// Loads a previously exported geometry JSON as the current result — the
  /// answer to "can I load the file Export wrote". The engine renders it (one
  /// renderer, one definition of a livery), so what lands on the canvas is what
  /// an inject will produce, not this client's guess. The canvas size comes
  /// from the background rectangle every generated document carries.
  Future<void> importGeometry(String path) async {
    final e = _engine;
    if (e == null) return;
    // setSource already cancelled any run and bumped the epoch before routing
    // here; capture it so a newer open landing first aborts this import.
    final epoch = _openEpoch;
    try {
      final doc = jsonDecode(await File(path).readAsString());
      final shapesList = doc is Map<String, dynamic> ? doc['shapes'] : null;
      if (shapesList is! List || shapesList.isEmpty) {
        _note('error', 'not a shapes JSON: no "shapes" list in $path');
        notifyListeners();
        return;
      }
      final first = shapesList.first;
      final data = first is Map ? first['data'] as List? : null;
      final w = data != null && data.length >= 4 ? (data[2] as num).round() : 0;
      final h = data != null && data.length >= 4 ? (data[3] as num).round() : 0;
      if ((first is Map ? (first['type'] as num?)?.toInt() : null) != 1 ||
          w <= 0 ||
          h <= 0) {
        _note(
          'error',
          'unsupported shapes JSON: it must start with the background '
              'rectangle a generated export carries',
        );
        notifyListeners();
        return;
      }
      final bgColor = first is Map ? first['color'] as List? : null;
      final transparent =
          bgColor != null && bgColor.length >= 4 && (bgColor[3] as num) == 0;
      final frame = await e.render(
        shapes: shapesList,
        width: w,
        height: h,
        transparent: transparent,
      );
      final done = Completer<ui.Image>();
      ui.decodeImageFromPixels(
        frame.pixels,
        frame.width,
        frame.height,
        ui.PixelFormat.rgba8888,
        done.complete,
      );
      final img = await done.future;
      if (epoch != _openEpoch || _disposed) {
        img.dispose(); // the user moved on while this was rendering/decoding
        return;
      }

      // Same reset as opening a saved run: an import carries no source image,
      // and pairing it with the previous picture in the compare wipe would show
      // half of one design beside half of another.
      sourceImage?.dispose();
      sourceImage = null;
      sourcePath = null;
      sourceW = 0;
      sourceH = 0;
      region = null;
      compare = 1;

      geometry = Map<String, dynamic>.of(doc as Map<String, dynamic>);
      resultW = w;
      resultH = h;
      shapes = shapesList.length - 1;
      sourceName = path.split(RegExp(r'[\\/]')).last;
      selectedRunId = null;
      preview?.dispose();
      preview = img;
      phase = Phase.done;
      ssim = null;
      deltaE = null;
      failure = null;
      _note('done', 'imported $sourceName: $shapes shapes');
    } catch (err) {
      _note('error', 'could not import the JSON: $err');
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

  /// Saves the current result to the library so it appears in Runs.
  ///
  /// Called two ways: by the editor's "save to runs", and automatically when a
  /// fit finishes. NOTHING persisted a finished fit before — the engine's done
  /// path only writes geometry if the caller asked for an output file, which
  /// this client never does — so the rail and the gallery only ever held runs
  /// the user had thought to save from inside the editor. Every other one was
  /// gone the moment the next picture was dropped.
  ///
  /// Non-destructive: every save is a NEW entry. Editing a run and saving keeps
  /// the original beside it, and two from-scratch designs never overwrite each
  /// other on a shared name.
  ///
  /// Returns whether it landed, so a caller that was about to close on the
  /// strength of it can stay open instead.
  Future<bool> saveToLibrary(String name, {bool quiet = false}) async {
    final e = _engine;
    final g = geometry;
    final img = preview;
    if (e == null || g == null || img == null) return false;
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
    // EVERY field of the entry is captured BEFORE the first await, the way `img`
    // and `g` already were. Reading them afterwards was survivable while saving
    // took a deliberate click; now that a finished fit saves itself, the readback
    // and the upload straddle whatever the user does next — and a file dropped
    // in that window sets resultW/H to 0 and sourcePath to the new file, so the
    // run was stored with zero dimensions (which openRun's `?? frame.width`
    // fallback cannot repair, 0 being non-null) and somebody else's source.
    final entry = <String, dynamic>{
      'name': safe,
      'preset': mode,
      'width': resultW,
      'height': resultH,
      'budget': budget,
      'injectScale': injectScale,
      // The picture it was fitted from. Kept so re-opening the run can put the
      // source back on the canvas, which is what makes the compare wipe work
      // for a saved run instead of being permanently dead there.
      if (sourcePath != null) 'source': sourcePath,
      'created': DateTime.now().toUtc().toIso8601String(),
    };
    final shapesSnapshot = (g['shapes'] as List?) ?? const [];
    try {
      final bytes = await img.toByteData(format: ui.ImageByteFormat.rawRgba);
      if (bytes == null) return false;
      final res = await e.librarySave(
        shapes: shapesSnapshot,
        entry: entry,
        pixels: bytes.buffer.asUint8List(),
        pixelW: img.width,
        pixelH: img.height,
      );
      // Point the rail at the run just written, the way opening one does — but
      // NOT for an automatic save, which must not move a selection the user has
      // meanwhile made themselves.
      final id = (res?['entry'] as Map?)?['id'] as String?;
      if (id != null && !quiet) selectedRunId = id;
      if (!quiet) _note('done', 'saved "$safe" to runs');
      await refreshLibrary();
      notifyListeners();
      return true;
    } catch (err) {
      _note('error', 'save failed: $err');
      notifyListeners();
      return false;
    }
  }

  /// Persists the fit that just finished. Deferred until the final frame has
  /// DECODED: the done event overtakes `_decode`'s async callback, so saving
  /// straight from the handler would store the second-to-last preview.
  bool _persistPending = false;

  void _persistFinishedRun() {
    if (!_persistPending) return;
    if (_decoding || preview == null || geometry == null) return;
    _persistPending = false;
    unawaited(saveToLibrary(sourceName ?? 'Untitled', quiet: true));
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
      _note('done', 'exported to $path');
    } catch (err) {
      _note('error', 'export failed: $err');
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

  /// The engine's pristine DefaultChoices, kept so clearing an override can
  /// put the SENTINEL back (-1, null, 0 — "follow the preset") rather than a
  /// concrete number that would freeze this install's copy of a default.
  Map<String, dynamic> _defaults = const {};

  /// The concrete values the current mode's defaults resolve to, fetched from
  /// the engine so the expert panel can show real numbers instead of "auto".
  /// Keyed like [choices]; empty until [refreshKnobDefaults] has run.
  Map<String, dynamic> knobDefaults = const {};

  int _knobSeq = 0;

  Future<void> refreshKnobDefaults() async {
    final e = _engine;
    if (e == null) return;
    // Sequenced: two quick mode flips can resolve out of order, leaving the
    // panel showing the EARLIER mode's defaults. Only the newest request lands.
    final seq = ++_knobSeq;
    try {
      // A quality override changes the engine's search counts wholesale, so an
      // untouched count field must show the number from THAT tier.
      final q = _overrides['Quality'] as String? ?? '';
      final d = await e.knobDefaults(mode, sourcePath, quality: q);
      if (seq != _knobSeq || _disposed) return;
      knobDefaults = d;
      notifyListeners();
    } catch (err) {
      _note('warn', 'knob defaults: $err');
    }
  }

  bool isOverridden(String key) => _overrides.containsKey(key);

  /// What the expert panel should display for [key]: the user's override when
  /// there is one, else the mode's concrete default.
  Object? knobValue(String key) =>
      _overrides.containsKey(key) ? _overrides[key] : knobDefaults[key];

  /// Forgets one override: the stored value returns to the engine's sentinel,
  /// so the knob follows the preset again — including presets the engine
  /// retunes in a later release.
  void clearChoice(String key) {
    if (!_overrides.containsKey(key)) return;
    _overrides.remove(key);
    choices = Map<String, dynamic>.of(choices)..[key] = _defaults[key];
    _prefs?.set('choices', _overrides);
    notifyListeners();
  }

  /// Forgets every override in [keys] at once — the expert panel's reset.
  void clearChoices(Iterable<String> keys) {
    var touched = false;
    for (final key in keys) {
      if (!_overrides.containsKey(key)) continue;
      _overrides.remove(key);
      choices = Map<String, dynamic>.of(choices)..[key] = _defaults[key];
      touched = true;
    }
    if (!touched) return;
    _prefs?.set('choices', _overrides);
    notifyListeners();
  }

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
    // A preset can change Mode/Quality — without a refresh the expert panel
    // kept showing the PREVIOUS tier's defaults, and the first slider touch
    // committed that wrong default as a real override.
    unawaited(refreshKnobDefaults());
    notifyListeners();
  }

  void clearLog() {
    log.clear();
    notifyListeners();
  }

  /// The engine's own estimate when it sends one: it spans the polish and the
  /// post-passes, which the shape counter below cannot see at all — it goes
  /// quiet the moment placement ends, and that is close to half the run.
  Duration? engineEta;

  /// Name of the phase the engine is in, and how far the whole run has got.
  String phaseName = '';
  double overall = 0;

  /// A rough time remaining. Prefers the engine's estimate; the shape-rate
  /// fallback keeps an older engine build working, and covers the window
  /// before the first phase event arrives.
  Duration? get eta {
    if (!isRunning) return null;
    if (engineEta != null) return engineEta;
    if (shapes < 20 || total <= shapes) return null;
    final rate = shapes / elapsed.inMilliseconds.clamp(1, 1 << 30);
    if (rate <= 0) return null;
    return Duration(milliseconds: ((total - shapes) / rate).round());
  }

  /// A dropped file the app cannot open — surfaced in the activity log so a
  /// drop that does nothing at least says why, instead of the overlay just
  /// fading with no trace.
  void noteRejectedDrop() {
    _note('warn', 'that file is not an image or a .forza.json export');
    notifyListeners();
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
    _disposed = true;
    _ticker?.cancel(); // a live 4Hz timer notifying a disposed notifier throws every 250ms
    _ticker = null;
    _runSeq++; // retire in-flight decodes/updates
    _engine?.close();
    preview?.dispose();
    sourceImage?.dispose();
    super.dispose();
  }

  bool _disposed = false;
}
