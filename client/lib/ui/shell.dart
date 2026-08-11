/// The canvas-first shell.
///
/// The picture is the app. Everything else floats over it: a 52px header that
/// never boxes the canvas in, a 76px rail of previous runs down the left, a
/// command bar along the bottom and an activity card at the right. Nothing gets
/// a docked panel with a hard edge, because the moment the layout is a set of
/// boxes the image stops being the subject.
library;

import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../state/editor.dart';
import '../state/studio.dart';
import 'about.dart';
import 'activity.dart';
import 'canvas.dart';
import 'confirm.dart';
import 'editor.dart';
import 'gallery.dart';
import 'help.dart';
import 'inject.dart';
import 'popovers.dart';
import 'sheet.dart';
import 'source.dart';
import 'strings.dart';
import 'tokens.dart';
import 'window.dart';

/// Which overlay is open, if any. One at a time: they occupy the same part of
/// the screen, and two open at once would be two answers to one question.
enum _Pop {
  none,
  style,
  detail,
  inject,
  sheet,
  settings,
  gallery,
  about,
  language,
  help,
}

/// Crop is a MODE, not an overlay: while it is on, the canvas shows the source
/// rather than the result, because a rectangle dragged over a reconstruction
/// would be a rectangle in the wrong picture.

class Shell extends StatefulWidget {
  const Shell({
    super.key,
    required this.studio,
    required this.lang,
    required this.onLanguage,
  });

  final Studio studio;
  final Lang lang;
  final void Function(Lang) onLanguage;

  @override
  State<Shell> createState() => _ShellState();
}

class _ShellState extends State<Shell> {
  _Pop _pop = _Pop.none;
  bool _cropping = false;
  bool _logOpen = false;
  Editor? _editor;
  final _window = WindowState();

  /// The question currently on screen, if any. One at a time, and it owns the
  /// whole window while it is up: a confirmation you can click past is not one.
  Confirm? _confirm;

  @override
  void dispose() {
    _window.dispose();
    super.dispose();
  }

  void _ask(Confirm c) => setState(() => _confirm = c);

  /// Opening a run replaces what is on the canvas, and there is no undo for
  /// that, so it asks first — with the run's own picture in the question,
  /// because the rail is a set of pictures and a name means nothing here.
  void _askOpenRun(Map<String, dynamic> entry) {
    final id = entry['id'] as String;
    if (studio.selectedRunId == id) return;
    _ask(
      Confirm(
        title: context.s('loadRunTitle'),
        body: context.s('loadRunBody'),
        action: context.s('load'),
        thumb: studio.thumbProvider(id),
        onConfirm: () => studio.openRun(entry),
      ),
    );
  }

  /// Opens the shape editor on the current result. The editor owns a COPY of the
  /// document until the user saves: an edit that is abandoned must not have
  /// quietly changed what would be injected.
  void _edit() {
    final g = studio.geometry;
    final engine = studio.engine;
    if (g == null || engine == null) return;
    final ed = Editor(engine)..load(g, studio.resultW, studio.resultH);
    setState(() {
      _editor = ed;
      _pop = _Pop.none;
    });
  }

  /// Opens the editor on an empty document, so a livery can be built by hand
  /// without a fit behind it. The canvas starts at a neutral square; loading a
  /// reference in the editor resizes it to the picture being traced.
  void _editBlank() {
    final engine = studio.engine;
    if (engine == null) return;
    final ed = Editor(engine)..loadBlank(1000, 1000);
    setState(() {
      _editor = ed;
      _pop = _Pop.none;
    });
  }

  void _closeEditor() {
    final ed = _editor;
    setState(() => _editor = null);
    ed?.dispose();
  }

  Studio get studio => widget.studio;

  void _toggle(_Pop p) => setState(() => _pop = _pop == p ? _Pop.none : p);

  Future<void> _open() async {
    final path = await pickImage();
    if (path != null) studio.setSource(path);
  }

  Future<void> _export() async {
    final path = await saveGeometryTo(studio.sourceName ?? 'design');
    if (path != null) await studio.exportTo(path);
  }

  @override
  Widget build(BuildContext context) {
    final ed = _editor;
    if (ed != null) {
      // The editor keeps the caption. It used to replace the entire shell,
      // which took the window buttons with it — so while editing there was no
      // way to close or minimise the window, and the top strip still dragged it
      // because the runner's hit test does not know the header is gone.
      return Stack(
        fit: StackFit.expand,
        children: [
          const RepaintBoundary(child: _Desk()),
          Positioned(
            left: 0,
            top: captionHeight,
            right: 0,
            bottom: 0,
            child: EditorView(
              editor: ed,
              studio: studio,
              onClose: _closeEditor,
            ),
          ),
          Positioned(
            left: 0,
            top: 0,
            right: 0,
            child: AnimatedBuilder(
              animation: _window,
              builder: (context, _) => _Header(
                studio: studio,
                lang: widget.lang,
                window: _window,
                pop: _pop,
                // Nothing app-level is offered here: a language menu that opens
                // behind the editor would be a control that does nothing.
                compact: true,
                onAbout: _closeEditor,
                onHelp: _closeEditor,
                onPickLanguage: _closeEditor,
                onSettings: _closeEditor,
                onLog: _closeEditor,
                onCreate: _closeEditor,
                logOpen: false,
              ),
            ),
          ),
        ],
      );
    }
    // The shortcuts a desktop user tries without being told. Handled at the
    // shell so they work wherever focus happens to be.
    return CallbackShortcuts(
      bindings: {
        const SingleActivator(LogicalKeyboardKey.enter, control: true): () {
          if (!studio.isRunning) studio.generate();
        },
        const SingleActivator(LogicalKeyboardKey.escape): () => setState(() {
          // A question outranks everything else on screen, so Escape answers
          // that first and leaves the rest of the state alone.
          if (_confirm != null) {
            _confirm = null;
            return;
          }
          _pop = _Pop.none;
          _cropping = false;
          _logOpen = false;
        }),
        const SingleActivator(LogicalKeyboardKey.keyO, control: true): _open,
      },
      child: Focus(
        autofocus: true,
        child: AnimatedBuilder(
          animation: studio,
          builder: (context, _) => DropCatcher(
            onFile: studio.setSource,
            child: Stack(
              fit: StackFit.expand,
              children: [
                const RepaintBoundary(child: _Desk()),
                Positioned(
                  left: _Rail.width,
                  top: 52,
                  right: 0,
                  bottom: 0,
                  // The empty state and the canvas cross-fade rather than
                  // swapping, so dropping a file reads as the picture arriving
                  // rather than as the window being rebuilt.
                  child: AnimatedSwitcher(
                    duration: Motion.base,
                    switchInCurve: Motion.curve,
                    switchOutCurve: Motion.curveIn,
                    child: studio.sourceImage == null && studio.preview == null
                        ? _Empty(studio: studio, onCreate: _editBlank)
                        : CanvasView(
                            studio: studio,
                            // A crop rectangle over a running fit would be a
                            // selection you cannot apply, so the mode drops out
                            // for the duration rather than sitting there dead.
                            cropping: _cropping && !studio.isRunning,
                            onCropDone: () => setState(() => _cropping = false),
                          ),
                  ),
                ),
                Positioned(
                  left: 0,
                  top: 0,
                  right: 0,
                  child: AnimatedBuilder(
                    animation: _window,
                    builder: (context, _) => _Header(
                      studio: studio,
                      onAbout: () => _toggle(_Pop.about),
                      onHelp: () => _toggle(_Pop.help),
                      lang: widget.lang,
                      onPickLanguage: () => _toggle(_Pop.language),
                      window: _window,
                      pop: _pop,
                      onSettings: () => _toggle(_Pop.settings),
                      onLog: () => setState(() => _logOpen = !_logOpen),
                      onCreate: _editBlank,
                      logOpen: _logOpen,
                    ),
                  ),
                ),
                Positioned(
                  left: 0,
                  top: 52,
                  bottom: 0,
                  child: AnimatedOpacity(
                    duration: Motion.base,
                    opacity: studio.isRunning ? 0.4 : 1,
                    child: _Rail(
                      studio: studio,
                      enabled: !studio.isRunning,
                      onOpen: _askOpenRun,
                    ),
                  ),
                ),
                Positioned(
                  right: 18,
                  bottom: 86,
                  child: ActivityCard(
                    studio: studio,
                    logOpen: _logOpen,
                    onToggleLog: () => setState(() => _logOpen = !_logOpen),
                    onEdit: _edit,
                    onExport: _export,
                    onInject: () => _toggle(_Pop.inject),
                  ),
                ),
                if (_logOpen)
                  Positioned(
                    key: const ValueKey('log'),
                    left: _Rail.width + 16,
                    bottom: 86,
                    child: PopIn(
                      child: LogDrawer(
                        studio: studio,
                        onClose: () => setState(() => _logOpen = false),
                      ),
                    ),
                  ),
                // A click anywhere else closes an open popover, which is what every
                // menu on this platform does and the first thing a user will try.
                if (_pop != _Pop.none)
                  Positioned.fill(
                    key: const ValueKey('scrim'),
                    child: GestureDetector(
                      behavior: HitTestBehavior.translucent,
                      onTap: () => setState(() => _pop = _Pop.none),
                    ),
                  ),
                if (_pop == _Pop.style)
                  Positioned(
                    key: const ValueKey('style'),
                    left: _Rail.width + 28,
                    bottom: 86,
                    child: PopIn(
                      child: StylePopover(
                        studio: studio,
                        onPick: (m) {
                          studio.setMode(m);
                          setState(() => _pop = _Pop.none);
                        },
                      ),
                    ),
                  ),
                if (_pop == _Pop.detail)
                  Positioned(
                    key: const ValueKey('detail'),
                    left: _Rail.width + 184,
                    bottom: 86,
                    child: PopIn(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          DetailPopover(
                            studio: studio,
                            onPick: studio.setBudget,
                          ),
                          const SizedBox(height: 8),
                          SizedBox(
                            width: detailPopoverWidth,
                            child: Btn(
                              context.s('advanced'),
                              onTap: () => setState(() => _pop = _Pop.sheet),
                            ),
                          ),
                        ],
                      ),
                    ),
                  ),
                if (_pop == _Pop.inject)
                  Positioned(
                    key: const ValueKey('inject'),
                    right: 18,
                    bottom: 86,
                    child: PopIn(
                      child: InjectPopover(
                        studio: studio,
                        onClose: () => setState(() => _pop = _Pop.none),
                      ),
                    ),
                  ),
                // The modes live at the top left, out of the picture's way. In
                // the middle of the canvas the crop button sat on top of the
                // thing it was meant to help you look at.
                if (studio.sourceImage != null && !studio.isRunning)
                  Positioned(
                    key: const ValueKey('modes'),
                    left: _Rail.width + 16,
                    top: 66,
                    child: _ModeSwitch(
                      cropping: _cropping,
                      onPick: (c) => setState(() => _cropping = c),
                    ),
                  ),
                Positioned(
                  left: 10,
                  width: _Rail.width - 20,
                  height: commandBarHeight,
                  bottom: 16,
                  child: _AllRuns(
                    count: studio.entries.length,
                    onTap: studio.isRunning
                        ? null
                        : () => _toggle(_Pop.gallery),
                  ),
                ),
                Positioned(
                  left: _Rail.width + 16,
                  right: 18,
                  bottom: 16,
                  child: _CommandBar(
                    studio: studio,
                    pop: _pop,
                    onStyle: () => _toggle(_Pop.style),
                    onDetail: () => _toggle(_Pop.detail),
                    onInject: () => _toggle(_Pop.inject),
                    onExport: _export,
                    onEdit: _edit,
                    onOpen: _open,
                  ),
                ),
                if (_pop == _Pop.sheet)
                  Positioned.fill(
                    key: const ValueKey('sheet'),
                    child: PopIn(
                      from: Offset.zero,
                      child: ColoredBox(
                        color: const Color(0xC008090A),
                        child: AdvancedSheet(
                          studio: studio,
                          onClose: () => setState(() => _pop = _Pop.none),
                        ),
                      ),
                    ),
                  ),
                if (_pop == _Pop.settings)
                  Positioned.fill(
                    key: const ValueKey('settings'),
                    child: PopIn(
                      from: Offset.zero,
                      child: ColoredBox(
                        color: const Color(0xC008090A),
                        child: SettingsSheet(
                          studio: studio,
                          onClose: () => setState(() => _pop = _Pop.none),
                        ),
                      ),
                    ),
                  ),
                if (_pop == _Pop.language)
                  Positioned(
                    key: const ValueKey('language'),
                    right: 120,
                    top: 52,
                    child: PopIn(
                      from: const Offset(0, -6),
                      child: _LanguageMenu(
                        current: widget.lang,
                        onPick: (l) {
                          widget.onLanguage(l);
                          setState(() => _pop = _Pop.none);
                        },
                      ),
                    ),
                  ),
                if (_pop == _Pop.help)
                  Positioned.fill(
                    key: const ValueKey('help'),
                    child: PopIn(
                      from: Offset.zero,
                      child: ColoredBox(
                        color: const Color(0xC008090A),
                        child: HelpSheet(
                          onClose: () => setState(() => _pop = _Pop.none),
                        ),
                      ),
                    ),
                  ),
                if (_pop == _Pop.about)
                  Positioned.fill(
                    key: const ValueKey('about'),
                    child: PopIn(
                      from: Offset.zero,
                      child: ColoredBox(
                        color: const Color(0xC008090A),
                        child: AboutSheet(
                          studio: studio,
                          onClose: () => setState(() => _pop = _Pop.none),
                          onOpenLibraryFolder: studio.openLibraryFolder,
                        ),
                      ),
                    ),
                  ),
                if (_pop == _Pop.gallery)
                  Positioned.fill(
                    key: const ValueKey('gallery'),
                    child: PopIn(
                      from: Offset.zero,
                      child: Gallery(
                        studio: studio,
                        onClose: () => setState(() => _pop = _Pop.none),
                        onConfirm: _ask,
                      ),
                    ),
                  ),
                if (_confirm != null)
                  Positioned.fill(
                    key: const ValueKey('confirm'),
                    child: ConfirmDialog(
                      confirm: _confirm!,
                      onDismiss: () => setState(() => _confirm = null),
                    ),
                  ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

/// The desk: a near-black field with one pool of light behind the canvas.
/// It is what the glass panels blur, so without it they read as flat grey.
///
/// The glow is painted at the size of the WINDOW. It used to be a fixed 900x620
/// box in the middle, which on a large window was plainly a rectangle of
/// gradient sitting in a black field, with its edge visible.
class _Desk extends StatelessWidget {
  const _Desk();

  @override
  Widget build(BuildContext context) =>
      const CustomPaint(painter: _DeskPainter(), child: SizedBox.expand());
}

/// The surface everything else floats on.
///
/// Two two-stop ramps and nothing else. Every previous attempt added a shape or
/// an extra stop to make the light "interesting", and every one of them put an
/// edge on screen: a radial ends in a circle you can see, and a ramp with an
/// interior stop kinks where that stop is. A straight A-to-B ramp is the only
/// gradient with nothing in it to find.
///
/// The dither is not decoration: a smooth ramp across near-black bands badly at
/// 8 bits per channel, and a sparse lattice of barely-there dots is enough to
/// break the contours the eye latches onto.
class _DeskPainter extends CustomPainter {
  const _DeskPainter();

  @override
  void paint(Canvas canvas, Size size) {
    final rect = Offset.zero & size;
    canvas.drawRect(
      rect,
      Paint()
        ..shader = const LinearGradient(
          begin: Alignment.topLeft,
          end: Alignment.bottomRight,
          colors: [Color(0xFF101317), Color(0xFF070809)],
        ).createShader(rect),
    );
    canvas.drawRect(
      rect,
      Paint()
        ..shader = const LinearGradient(
          begin: Alignment.topCenter,
          end: Alignment.bottomCenter,
          colors: [Color(0x1654CBB8), Color(0x0054CBB8)],
        ).createShader(rect),
    );

    final dot = Paint()..color = const Color(0x06FFFFFF);
    var seed = 0x2545F491;
    for (var y = 0.0; y < size.height; y += 3) {
      for (var x = 0.0; x < size.width; x += 3) {
        seed = (seed * 1103515245 + 12345) & 0x7FFFFFFF;
        if (seed % 3 != 0) continue;
        canvas.drawRect(Rect.fromLTWH(x, y, 1, 1), dot);
      }
    }
  }

  @override
  bool shouldRepaint(_DeskPainter old) => false;
}

class _Header extends StatelessWidget {
  const _Header({
    required this.studio,
    required this.onAbout,
    required this.onHelp,
    required this.lang,
    required this.onPickLanguage,
    required this.window,
    required this.pop,
    required this.onSettings,
    required this.onLog,
    required this.onCreate,
    required this.logOpen,
    this.compact = false,
  });
  final Studio studio;
  final VoidCallback onAbout;
  final VoidCallback onHelp;
  final VoidCallback onSettings;
  final VoidCallback onLog;

  /// Opens the editor on a blank document — a livery from scratch, reachable
  /// without first fitting a picture.
  final VoidCallback onCreate;
  final bool logOpen;

  /// Drops everything but the identity and the window buttons. Used over the
  /// editor, which owns the rest of the screen.
  final bool compact;
  final Lang lang;
  final VoidCallback onPickLanguage;
  final WindowState window;
  final _Pop pop;

  @override
  Widget build(BuildContext context) {
    final name = studio.sourceName ?? context.s('noImage');
    final meta = switch (studio.phase) {
      Phase.empty => 'drop something in',
      Phase.loading => 'preparing…',
      Phase.running => '${studio.shapes} of ${studio.total} shapes',
      Phase.done => '${studio.shapes} shapes · ${studio.backend}',
      Phase.failed => studio.failure ?? 'failed',
    };
    return SizedBox(
      height: 52,
      child: Row(
        children: [
          const SizedBox(width: 14),
          const HalftoneMark(size: 26),
          const SizedBox(width: 10),
          Column(
            mainAxisAlignment: MainAxisAlignment.center,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                name,
                style: T.text(13, color: T.title, weight: FontWeight.w600),
              ),
              const SizedBox(height: 1),
              Text(meta, style: T.text(11, color: T.hint)),
            ],
          ),
          const Spacer(),
          // Everything from here on is a control, and the runner is told how
          // wide this cluster is so the drag band stops exactly where it does.
          CaptionControls(
            children: [
              if (!compact) ...[
                // A way into the editor that does not start from a picture: the
                // canvas-first shell otherwise only reaches it through a result.
                _TopAction(context.s('createScratch'), onTap: onCreate),
                const SizedBox(width: 5),
                // The log is one click from anywhere, because the moment it is
                // wanted is the moment something has gone wrong.
                _TopAction(
                  context.s('logButton'),
                  onTap: onLog,
                  active: logOpen,
                ),
                const SizedBox(width: 5),
                // The app's own settings — sound, notifications, where the log
                // is. What the ENGINE does lives under Detail, with the numbers
                // it changes.
                _TopAction(
                  '⚙',
                  onTap: onSettings,
                  active: pop == _Pop.settings,
                ),
                const SizedBox(width: 5),
                _TopAction(
                  lang.endonym,
                  onTap: onPickLanguage,
                  active: pop == _Pop.language,
                ),
                const SizedBox(width: 5),
                _TopAction(
                  context.s('help'),
                  onTap: onHelp,
                  active: pop == _Pop.help,
                ),
                const SizedBox(width: 5),
                _TopAction('?', onTap: onAbout, active: pop == _Pop.about),
                const SizedBox(width: 12),
              ],
              WindowButtons(state: window),
            ],
          ),
        ],
      ),
    );
  }
}

/// The twelve languages, as a list. A cycling button would be twelve clicks to
/// get back to where you started.
class _LanguageMenu extends StatelessWidget {
  const _LanguageMenu({required this.current, required this.onPick});
  final Lang current;
  final void Function(Lang) onPick;

  @override
  Widget build(BuildContext context) => Glass(
    child: SizedBox(
      width: 190,
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          for (final l in languages)
            MouseRegion(
              cursor: SystemMouseCursors.click,
              child: GestureDetector(
                onTap: () => onPick(l),
                child: Container(
                  height: 32,
                  width: double.infinity,
                  margin: const EdgeInsets.symmetric(
                    horizontal: 6,
                    vertical: 1,
                  ),
                  padding: const EdgeInsets.symmetric(horizontal: 9),
                  alignment: Alignment.centerLeft,
                  decoration: BoxDecoration(
                    color: l.code == current.code ? T.tealWash : null,
                    borderRadius: BorderRadius.circular(7),
                  ),
                  child: Text(
                    l.endonym,
                    style: T.text(
                      12.5,
                      color: l.code == current.code ? T.tealBright : T.body,
                    ),
                  ),
                ),
              ),
            ),
          const SizedBox(height: 6),
        ],
      ),
    ),
  );
}

class _TopAction extends StatelessWidget {
  const _TopAction(this.label, {required this.onTap, this.active = false});
  final String label;
  final VoidCallback onTap;

  /// True while the thing this opens is on screen, so the button holds a lit
  /// state instead of the panel appearing from nowhere.
  final bool active;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      height: 27,
      padding: const EdgeInsets.symmetric(horizontal: 11),
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: active ? T.tealWash : hoverOver(T.fillSoft, hover, down),
        borderRadius: BorderRadius.circular(8),
        border: Border.all(color: hover || active ? T.border : T.hairline),
      ),
      child: Text(
        label,
        style: T.text(
          12,
          color: active ? T.tealBright : (hover ? T.title : T.dim),
        ),
      ),
    ),
  );
}

/// Previous runs, down the left.
///
/// It shows as many as FIT and no more. A scrolling column of a hundred
/// thumbnails is a second gallery in the corner of the screen: this is for
/// recognising the last few at a glance, and everything older is one click away
/// under it. Sizing to the window also means the rail never has a scrollbar,
/// which is what made it read as a list rather than a strip.
class _Rail extends StatelessWidget {
  const _Rail({
    required this.studio,
    required this.onOpen,
    required this.enabled,
  });

  final Studio studio;
  final void Function(Map<String, dynamic>) onOpen;

  /// False while a fit is running: loading another run mid-run would throw the
  /// running one's canvas away without stopping it.
  final bool enabled;

  static const width = 92.0;
  static const _thumbW = 68.0;
  static const _thumbH = 90.0;
  static const _gap = 8.0;

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      width: width,
      child: LayoutBuilder(
        builder: (context, box) {
          const header = 22.0;
          // The button is no longer part of this column, so the room it takes
          // has to be stated here: 40 was the old button's height and a thumb
          // was overlapping the new one.
          const footer = commandBarHeight + 16 + 10;
          final room = box.maxHeight - header - footer;
          // At least one, so a short window still shows the most recent run
          // rather than an empty strip.
          final fits = math.max(1, ((room + _gap) / (_thumbH + _gap)).floor());
          final shown = studio.entries.take(fits).toList();

          return Column(
            children: [
              const SizedBox(height: 6),
              Text(context.s('runs').toUpperCase(), style: T.label),
              const SizedBox(height: 8),
              for (final e in shown)
                Padding(
                  padding: const EdgeInsets.only(bottom: _gap),
                  child: _RunThumb(
                    studio: studio,
                    entry: e,
                    selected: studio.selectedRunId == e['id'],
                    size: const Size(_thumbW, _thumbH),
                    onTap: enabled ? () => onOpen(e) : null,
                  ),
                ),
              const Spacer(),
              // The button itself is NOT here: it is positioned in the shell's
              // own stack, at the same anchor and the same height as the
              // command bar, so the two are one row across the foot of the
              // window by construction rather than by two paddings happening to
              // agree. This only reserves its room.
              const SizedBox(height: commandBarHeight + 16),
            ],
          );
        },
      ),
    );
  }
}

/// The way to everything the rail cannot show.
///
/// It carries the library's own accent rather than the neutral button fill: it
/// is the one control in the rail, and against a column of pictures a grey box
/// simply disappears.
class _AllRuns extends StatelessWidget {
  const _AllRuns({required this.count, required this.onTap});

  final int count;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      height: commandBarHeight,
      width: double.infinity,
      decoration: BoxDecoration(
        color: onTap == null
            ? T.fillSoft
            : (down
                  ? const Color(0x4454CBB8)
                  : (hover ? T.tealWash : T.tealFaint)),
        borderRadius: BorderRadius.circular(15),
        border: Border.all(color: hover ? T.teal : T.tealFaint),
      ),
      child: Column(
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          Flexible(
            child: Text(
              context.s('allRuns'),
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: T.text(
                12,
                color: onTap == null ? T.faint : T.tealBright,
                weight: FontWeight.w600,
              ),
            ),
          ),
          const SizedBox(height: 2),
          // The count is the point: it says how much more there is than the
          // strip above can show.
          Text(
            '$count',
            style: T.monoText(
              13,
              color: onTap == null ? T.faint : T.tealBright,
            ),
          ),
        ],
      ),
    ),
  );
}

class _RunThumb extends StatelessWidget {
  const _RunThumb({
    required this.studio,
    required this.entry,
    required this.selected,
    required this.size,
    required this.onTap,
  });

  final Studio studio;
  final Map<String, dynamic> entry;
  final bool selected;
  final Size size;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    return Pressable(
      onTap: onTap,
      builder: (context, hover, down) => AnimatedContainer(
        duration: Motion.fast,
        width: size.width,
        height: size.height,
        clipBehavior: Clip.antiAlias,
        transform: Matrix4.translationValues(0, down ? 1 : 0, 0),
        decoration: BoxDecoration(
          color: const Color(0xFF1A1C1F),
          borderRadius: BorderRadius.circular(8),
          border: Border.all(
            color: selected ? T.teal : (hover ? T.tealFaint : T.border),
            width: 1.5,
          ),
          boxShadow: selected
              ? const [
                  BoxShadow(color: T.tealFaint, blurRadius: 0, spreadRadius: 3),
                ]
              : null,
        ),
        child: Stack(
          fit: StackFit.expand,
          children: [
            FutureBuilder(
              future: studio.thumb(entry['id'] as String),
              builder: (context, snap) => snap.hasData && snap.data!.isNotEmpty
                  ? Image.memory(snap.data!, fit: BoxFit.cover)
                  : const SizedBox.shrink(),
            ),
            // Dimming the ones you are not pointing at is what turns a column of
            // pictures into a list you can pick from.
            AnimatedContainer(
              duration: Motion.fast,
              color: hover || selected
                  ? const Color(0x00000000)
                  : const Color(0x38000000),
            ),
          ],
        ),
      ),
    );
  }
}

/// One bar for the whole decision: what is being fitted, in what style, at what
/// detail, and the single action that starts it.
class _CommandBar extends StatelessWidget {
  const _CommandBar({
    required this.studio,
    required this.pop,
    required this.onStyle,
    required this.onDetail,
    required this.onInject,
    required this.onExport,
    required this.onEdit,
    required this.onOpen,
  });

  final Studio studio;
  final _Pop pop;
  final VoidCallback onStyle;
  final VoidCallback onDetail;
  final VoidCallback onInject;
  final VoidCallback onExport;
  final VoidCallback onEdit;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final budget = studio.budget;
    final mode = studio.mode;
    // Everything that would change what is being fitted is dead while a fit is
    // in progress. Only the progress readout and Stop stay live.
    final busy = studio.isRunning;

    return Glass(
      radius: 15,
      child: SizedBox(
        height: commandBarHeight,
        child: Row(
          children: [
            const SizedBox(width: 13),
            if (studio.sourceName != null) ...[
              _SourceChip(studio: studio, onTap: busy ? null : onOpen),
              const _ChipDivider(),
            ],
            _Chip(
              label: context.s('style'),
              value: _styleLabel(mode),
              onTap: busy ? null : onStyle,
              open: pop == _Pop.style,
            ),
            const _ChipDivider(),
            _Chip(
              label: context.s('detail'),
              value: '$budget',
              mono: true,
              onTap: busy ? null : onDetail,
              open: pop == _Pop.detail,
            ),
            // What you do to the RESULT sits with the thing it acts on, at the
            // left; what you do NEXT — put it in the game, or fit it again —
            // is at the far right behind a divider. Four buttons in one cluster
            // made "export" and "inject" look like the same kind of act.
            if (!studio.isRunning && studio.geometry != null) ...[
              const _ChipDivider(),
              Flexible(child: Btn(context.s('editShapes'), onTap: onEdit)),
              const SizedBox(width: 8),
              Flexible(child: Btn(context.s('export'), onTap: onExport)),
            ],
            const Spacer(),
            if (studio.isRunning) ...[
              Column(
                crossAxisAlignment: CrossAxisAlignment.end,
                mainAxisAlignment: MainAxisAlignment.center,
                children: [
                  Text(
                    '${(studio.progress * 100).round()}%',
                    style: T.monoText(
                      19,
                      color: T.title,
                      weight: FontWeight.w500,
                    ),
                  ),
                  Text(
                    _remaining(context, studio),
                    style: T.text(10.5, color: T.hint),
                  ),
                ],
              ),
              const SizedBox(width: 13),
              SizedBox(
                width: 140,
                child: ClipRRect(
                  borderRadius: BorderRadius.circular(3),
                  child: LinearProgressIndicator(
                    value: studio.progress,
                    minHeight: 5,
                    backgroundColor: T.fill,
                    valueColor: const AlwaysStoppedAnimation(T.teal),
                  ),
                ),
              ),
              const SizedBox(width: 13),
              // A full stop: the partial stack is thrown away rather than
              // offered for export, so the button says stop and nothing else.
              Btn(
                context.s('stopRun'),
                kind: BtnKind.danger,
                onTap: studio.cancel,
              ),
            ] else
              // Flexible so the cluster gives way instead of overrunning the
              // bar: in a narrow window with a wide language these labels are
              // wider than the space left after the chips.
              Flexible(
                child: Row(
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    if (studio.geometry != null) ...[
                      const _ChipDivider(),
                      Flexible(
                        child: Btn(
                          context.s('inject'),
                          onTap: studio.injectAvailable ? onInject : null,
                        ),
                      ),
                      const SizedBox(width: 8),
                    ],
                    Btn(
                      context.s('generate'),
                      kind: BtnKind.primary,
                      onTap: studio.sourcePath == null ? null : studio.generate,
                      trailing: Container(
                        padding: const EdgeInsets.symmetric(
                          horizontal: 5,
                          vertical: 1,
                        ),
                        decoration: BoxDecoration(
                          color: const Color(0x2606231F),
                          borderRadius: BorderRadius.circular(4),
                        ),
                        child: Text(
                          'Ctrl ⏎',
                          style: T.text(
                            9.5,
                            color: T.ink,
                            weight: FontWeight.w600,
                          ),
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            const SizedBox(width: 13),
          ],
        ),
      ),
    );
  }
}

/// What is being fitted, at the head of the bar. The crop is stated here rather
/// than only in the crop tool: a run against a region and a run against the
/// whole picture are different runs, and forgetting which is which wastes one.
class _SourceChip extends StatelessWidget {
  const _SourceChip({required this.studio, required this.onTap});
  final Studio studio;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final r = studio.region;
    // A run opened from the library has no source file — it may not be on this
    // machine any more — so the chip falls back to the run's own picture and
    // the size it was fitted at. Without this it read "0×0" beside a blank
    // square, which is the state the owner saw.
    final image = studio.sourceImage ?? studio.preview;
    final w = studio.sourceW != 0 ? studio.sourceW : studio.resultW;
    final h = studio.sourceH != 0 ? studio.sourceH : studio.resultH;
    final meta = r == null ? '$w×$h' : 'crop ${r[2]}×${r[3]}';
    return Pressable(
      onTap: onTap,
      builder: (context, hover, down) => AnimatedContainer(
        duration: Motion.fast,
        height: _chipHeight,
        margin: const EdgeInsets.symmetric(vertical: 7),
        padding: const EdgeInsets.fromLTRB(5, 0, 10, 0),
        decoration: BoxDecoration(
          color: hoverOver(const Color(0x00000000), hover, down),
          borderRadius: BorderRadius.circular(10),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            // The picture itself, not only its name: in a folder of similar
            // filenames the thumbnail is what actually identifies the run. It is
            // also the way back — clicking it opens another image, which is
            // otherwise only reachable from the empty state.
            if (image != null)
              Container(
                width: 32,
                height: 32,
                clipBehavior: Clip.antiAlias,
                decoration: BoxDecoration(
                  borderRadius: BorderRadius.circular(7),
                  border: Border.all(color: T.border),
                ),
                child: RawImage(
                  image: image,
                  fit: BoxFit.cover,
                  filterQuality: FilterQuality.low,
                ),
              ),
            const SizedBox(width: 9),
            SizedBox(
              width: 128,
              child: Padding(
                padding: const EdgeInsets.only(top: _chipTop),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  mainAxisSize: MainAxisSize.min,
                  children: [
                    // Two lines, like every chip in the bar: the size rides
                    // the label line. A third line never fit the shared box —
                    // it sat on the bottom edge and read as a mistake.
                    Row(
                      children: [
                        Text(context.s('source'), style: T.label),
                        const SizedBox(width: 6),
                        Expanded(
                          child: Text(
                            meta,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: T.monoText(
                              9.5,
                              color: r == null ? T.faint : T.teal,
                            ),
                          ),
                        ),
                      ],
                    ),
                    const SizedBox(height: 2),
                    Text(
                      studio.sourceName!,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: T.text(13, color: T.body, weight: FontWeight.w500),
                    ),
                  ],
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// Time remaining, in the user's language.
///
/// The duration is formatted separately from the sentence and substituted in,
/// because "about X left" puts the number in a different place in half of the
/// twelve languages.
String _remaining(BuildContext context, Studio studio) {
  final eta = studio.eta;
  if (eta == null) return context.s('estimating');
  final t = eta.inMinutes < 1
      ? '${eta.inSeconds}s'
      : '${eta.inMinutes}:${(eta.inSeconds % 60).toString().padLeft(2, '0')}';
  return context.s('etaLeft').replaceFirst('{t}', t);
}

/// The engine's mode string as the user named it. Falls back to the raw value
/// rather than hiding a mode the engine grew and this list has not caught up to.
String _styleLabel(String mode) {
  for (final s in styleOptions) {
    if (s.mode == mode) return s.label;
  }
  return mode.isEmpty ? '—' : mode;
}

/// A labelled value in the command bar that opens a popover.
///
/// The design draws these as bare text, and that is exactly how they read: as
/// captions, not controls. The owner had to guess where to click. So they keep
/// the design's typography and gain what makes a control a control — an outline,
/// a surface that lifts under the pointer, and a chevron that turns over while
/// the popover is open, which also says WHICH panel belongs to this chip.
/// The command bar's chips share one box: same height, same distance from the
/// top to the label, same distance to the value.
/// The height of the row along the foot of the window. The command bar and the
/// rail's own button are both exactly this tall at the same bottom offset.
const commandBarHeight = 58.0;

const _chipHeight = 44.0;
const _chipTop = 7.0;

class _Chip extends StatelessWidget {
  const _Chip({
    required this.label,
    required this.value,
    this.mono = false,
    this.onTap,
    this.open = false,
  });

  final String label;
  final String value;
  final bool mono;
  final VoidCallback? onTap;
  final bool open;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      // Every chip in the bar is the same box with the same inner offsets, so
      // the small caps line up across all of them and so do the values under
      // them. The source chip carries a third line and used to centre itself,
      // which put its label a few pixels above everyone else's — the thing
      // that made the whole bar look slightly out of true.
      height: _chipHeight,
      margin: const EdgeInsets.symmetric(vertical: 7),
      padding: const EdgeInsets.fromLTRB(11, _chipTop, 9, 0),
      decoration: BoxDecoration(
        color: open ? T.tealFaint : hoverOver(T.fillSoft, hover, down),
        borderRadius: BorderRadius.circular(9),
        border: Border.all(color: open || hover ? T.border : T.hairline),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        mainAxisSize: MainAxisSize.min,
        children: [
          Text(label, style: T.label),
          const SizedBox(height: 2),
          Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              Text(
                value,
                style: mono
                    ? T.monoText(13, color: open ? T.tealBright : T.body)
                    : T.text(
                        13,
                        color: open ? T.tealBright : T.body,
                        weight: FontWeight.w500,
                      ),
              ),
              const SizedBox(width: 7),
              AnimatedRotation(
                duration: Motion.base,
                curve: Motion.curve,
                turns: open ? 0.5 : 0,
                child: Text(
                  '▾',
                  style: T.text(9, color: open ? T.tealBright : T.soft),
                ),
              ),
            ],
          ),
        ],
      ),
    ),
  );
}

/// View or crop, as two segments of one control.
///
/// They are modes of the same canvas, not two commands, so they read as one
/// switch with one of them lit — the same shape the design uses at the top left.
class _ModeSwitch extends StatelessWidget {
  const _ModeSwitch({required this.cropping, required this.onPick});

  final bool cropping;
  final void Function(bool) onPick;

  @override
  Widget build(BuildContext context) => Glass(
    radius: 11,
    child: Padding(
      padding: const EdgeInsets.all(3),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          _Segment(
            label: context.s('view'),
            selected: !cropping,
            onTap: () => onPick(false),
          ),
          _Segment(
            label: context.s('crop'),
            selected: cropping,
            onTap: () => onPick(true),
          ),
        ],
      ),
    ),
  );
}

class _Segment extends StatelessWidget {
  const _Segment({
    required this.label,
    required this.selected,
    required this.onTap,
  });

  final String label;
  final bool selected;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      height: 27,
      padding: const EdgeInsets.symmetric(horizontal: 14),
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: selected
            ? T.tealWash
            : hoverOver(const Color(0x00000000), hover, down),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Text(
        label,
        style: T.text(
          12,
          color: selected ? T.tealBright : (hover ? T.title : T.dim),
          weight: selected ? FontWeight.w600 : FontWeight.w400,
        ),
      ),
    ),
  );
}

class _ChipDivider extends StatelessWidget {
  const _ChipDivider();
  @override
  Widget build(BuildContext context) => Container(
    width: 1,
    height: 30,
    margin: const EdgeInsets.symmetric(horizontal: 15),
    color: T.hairline,
  );
}

class _Empty extends StatelessWidget {
  const _Empty({required this.studio, required this.onCreate});
  final Studio studio;

  /// Opens the editor on a blank canvas, the other way to start: build a livery
  /// by hand instead of fitting one to a picture.
  final VoidCallback onCreate;

  @override
  Widget build(BuildContext context) {
    final loaded = studio.sourcePath != null;
    Future<void> choose() async {
      final path = await pickImage();
      if (path != null) studio.setSource(path);
    }

    // The whole card is the drop target, so clicking anywhere on it opens a
    // file. Someone who has not spotted the button still gets somewhere.
    return Center(
      child: Pressable(
        onTap: choose,
        builder: (context, hover, down) => AnimatedContainer(
          duration: Motion.base,
          curve: Motion.curve,
          width: 460,
          padding: const EdgeInsets.symmetric(vertical: 44),
          decoration: BoxDecoration(
            color: hover ? const Color(0x0AFFFFFF) : const Color(0x04FFFFFF),
            borderRadius: BorderRadius.circular(18),
            border: Border.all(color: hover ? T.tealFaint : T.border),
          ),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              AnimatedScale(
                duration: Motion.base,
                curve: Motion.curve,
                scale: hover ? 1.06 : 1,
                child: HalftoneMark(
                  size: 46,
                  tile: false,
                  color: hover ? T.tealBright : null,
                ),
              ),
              const SizedBox(height: 22),
              Text(
                loaded ? studio.sourceName! : context.s('dropTitle'),
                style: T.text(17, color: T.title, weight: FontWeight.w600),
              ),
              const SizedBox(height: 7),
              Text(
                loaded ? context.s('ready') : context.s('dropSub'),
                style: T.text(12.5, color: T.soft),
              ),
              const SizedBox(height: 20),
              Btn(
                loaded ? context.s('chooseAnother') : context.s('chooseFile'),
                onTap: choose,
              ),
              const SizedBox(height: 10),
              // The other way in: an empty canvas to draw on, for a livery that
              // starts from nothing rather than from a photo.
              Btn(context.s('createScratch'), onTap: onCreate),
            ],
          ),
        ),
      ),
    );
  }
}
