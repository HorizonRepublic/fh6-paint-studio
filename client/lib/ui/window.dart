/// The window buttons the design draws itself.
///
/// They are ordinary Flutter widgets, not non-client buttons: the runner's hit
/// test deliberately stops short of them so they receive real pointer events and
/// can hover, depress and animate like everything else. The actions travel back
/// to Win32 over the `fh6/window` channel, and the runner pushes the maximized
/// state back so the middle glyph is right however the window got there.
///
/// The drag band ends where these controls begin, and the runner learns that
/// boundary from [CaptionControls] rather than a hard-coded number — the
/// language button is as wide as the language's own name for it.
library;

import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

import '../state/logfile.dart';
import 'tokens.dart';

const captionHeight = 52.0;
const captionButtonWidth = 46.0;

/// Full caption height, flush to the corner — the shape every Windows app
/// uses. A 32px pill floating in the middle of a 52px bar reads as misaligned
/// against a close button that touches the edge of the screen.
const captionButtonHeight = captionHeight;

const _channel = MethodChannel('fh6/window');

/// The window's own state, as far as the caption needs to know it.
class WindowState extends ChangeNotifier {
  WindowState() {
    _channel.setMethodCallHandler((call) async {
      if (call.method == 'maximized') {
        maximized = call.arguments == true;
        notifyListeners();
      }
    });
    _channel.invokeMethod<bool>('isMaximized').then((v) {
      if (v != null && v != maximized) {
        maximized = v;
        notifyListeners();
      }
    });
  }

  bool maximized = false;

  static Future<void> minimize() => _channel.invokeMethod('minimize');
  static Future<void> toggleMaximize() =>
      _channel.invokeMethod('toggleMaximize');
  static Future<void> close() => _channel.invokeMethod('close');

  /// Reports where the drag band has to stop, in logical pixels from the right.
  static Future<void> setControlsWidth(double w) =>
      _channel.invokeMethod('setControlsWidth', w.ceil());

  /// The system's own notification sound. Not a bundled asset: on Windows this
  /// is whatever the user has chosen for a notification, which is the sound
  /// they already recognise and already know how to silence.
  static Future<void> chime() => _channel.invokeMethod('chime');

  /// Flashes the taskbar button, and only while the window is in the
  /// background — the runner checks, so a finished run cannot flash at someone
  /// who is watching it finish.
  static Future<void> flash() => _channel.invokeMethod('flash');

  /// Fills the taskbar button as the fit advances. A run takes minutes and the
  /// user is usually in the game by then, so the taskbar is the only place the
  /// progress can actually reach them.
  ///
  /// [value] is 0..1. Errors are swallowed: a shell that will not give out its
  /// taskbar object must not take the run down with it.
  static Future<void> progress(double value) =>
      _channel.invokeMethod('progress', value).catchError((Object _) {});

  static Future<void> progressDone() =>
      _channel.invokeMethod('progress', -1.0).catchError((Object _) {});

  static Future<void> progressFailed() =>
      _channel.invokeMethod('progress', -2.0).catchError((Object _) {});
}

/// Wraps everything at the right of the title bar and keeps the runner told how
/// wide it is. Without this the drag region is a guess, which shows up either as
/// a strip of title bar that will not drag or as buttons that drag the window
/// instead of doing what they say.
class CaptionControls extends StatefulWidget {
  const CaptionControls({super.key, required this.children});

  final List<Widget> children;

  @override
  State<CaptionControls> createState() => _CaptionControlsState();
}

class _CaptionControlsState extends State<CaptionControls> {
  final _key = GlobalKey();
  double _reported = -1;
  bool _inFlight = false;

  @override
  void initState() {
    super.initState();
    // A font arriving relayouts the labels without rebuilding this widget, and
    // a static screen schedules no further frames — so the width report would
    // stay at the pre-font measurement forever.
    PaintingBinding.instance.systemFonts.addListener(_fontsChanged);
  }

  @override
  void dispose() {
    PaintingBinding.instance.systemFonts.removeListener(_fontsChanged);
    super.dispose();
  }

  void _fontsChanged() {
    WidgetsBinding.instance.addPostFrameCallback(_measure);
    WidgetsBinding.instance.scheduleFrame();
  }

  void _measure(Duration _) {
    _report();
  }

  /// The width is only remembered once the runner has ANSWERED. A report can be
  /// lost — the first frame can complete before the runner registers the
  /// channel — and a lost report used to be cached as delivered: the runner
  /// kept its startup guess and every caption button left of that line dragged
  /// the window instead of clicking, until the app was restarted.
  Future<void> _report() async {
    if (_inFlight) return;
    _inFlight = true;
    try {
      while (mounted) {
        final box = _key.currentContext?.findRenderObject() as RenderBox?;
        if (box == null || !box.hasSize) break;
        // The DISTANCE from the window's right edge to the cluster's left
        // edge, not the cluster's width: the runner subtracts this from the
        // window's right, so it must describe where the cluster actually IS.
        // The two only coincide while the layout truly pins the cluster to
        // the edge — and one header regression already proved they can drift
        // apart, handing the buttons to the drag band.
        if (!mounted) break;
        final w =
            MediaQuery.sizeOf(context).width -
            box.localToGlobal(Offset.zero).dx;
        if ((w - _reported).abs() < 0.5) break;
        try {
          await WindowState.setControlsWidth(w);
          _reported = w;
          AppLog.write('debug', 'controls band $w: acked');
        } catch (e) {
          AppLog.write('debug', 'controls band $w: lost ($e)');
          await Future<void>.delayed(const Duration(milliseconds: 500));
        }
      }
    } finally {
      _inFlight = false;
    }
  }

  @override
  Widget build(BuildContext context) {
    WidgetsBinding.instance.addPostFrameCallback(_measure);
    return Row(
      key: _key,
      mainAxisSize: MainAxisSize.min,
      children: widget.children,
    );
  }
}

/// Minimise / maximise / close, top right.
class WindowButtons extends StatelessWidget {
  const WindowButtons({super.key, required this.state});

  final WindowState state;

  @override
  Widget build(BuildContext context) => SizedBox(
    height: captionButtonHeight,
    child: Row(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      mainAxisSize: MainAxisSize.min,
      children: [
        _CaptionButton(glyph: _Glyph.minimize, onTap: WindowState.minimize),
        _CaptionButton(
          glyph: state.maximized ? _Glyph.restore : _Glyph.maximize,
          onTap: WindowState.toggleMaximize,
        ),
        _CaptionButton(
          glyph: _Glyph.close,
          danger: true,
          onTap: WindowState.close,
        ),
      ],
    ),
  );
}

enum _Glyph { minimize, maximize, restore, close }

class _CaptionButton extends StatefulWidget {
  const _CaptionButton({
    required this.glyph,
    required this.onTap,
    this.danger = false,
  });

  final _Glyph glyph;
  final VoidCallback onTap;
  final bool danger;

  @override
  State<_CaptionButton> createState() => _CaptionButtonState();
}

class _CaptionButtonState extends State<_CaptionButton> {
  bool _hover = false;
  bool _down = false;

  @override
  Widget build(BuildContext context) {
    // Close goes red, the others grey — the convention every Windows app
    // follows and users read without looking. Pressing deepens rather than
    // changes colour, so the button never looks like a different control.
    final Color bg;
    if (widget.danger) {
      bg = _down
          ? const Color(0xFF8E1F14)
          : (_hover ? const Color(0xFFC42B1C) : const Color(0x00000000));
    } else {
      bg = _down ? T.fill : (_hover ? T.fillSoft : const Color(0x00000000));
    }
    final fg = widget.danger && (_hover || _down)
        ? const Color(0xFFFFFFFF)
        : (_hover || _down ? T.title : T.soft);

    // The window buttons are painted glyphs with no text anywhere, so without a
    // label a screen reader announces nothing at all for Close. Not run through
    // the app's own translations: these are the OS's own controls conceptually,
    // and the assistive layer reads them alongside other window chrome.
    final label = switch (widget.glyph) {
      _Glyph.minimize => 'Minimize',
      _Glyph.maximize => 'Maximize',
      _Glyph.restore => 'Restore',
      _Glyph.close => 'Close',
    };

    return Semantics(
      label: label,
      button: true,
      container: true,
      child: MouseRegion(
        cursor: SystemMouseCursors.click,
        onEnter: (_) => setState(() => _hover = true),
        onExit: (_) => setState(() {
          _hover = false;
          _down = false;
        }),
        child: Listener(
          onPointerDown: (_) => setState(() => _down = true),
          onPointerUp: (_) => setState(() => _down = false),
          child: GestureDetector(
            onTap: widget.onTap,
            child: AnimatedContainer(
              duration: const Duration(milliseconds: 90),
              width: captionButtonWidth,
              height: captionButtonHeight,
              color: bg,
              child: CustomPaint(painter: _GlyphPainter(widget.glyph, fg)),
            ),
          ),
        ),
      ),
    );
  }
}

/// The glyphs are drawn, not typed. A font character depends on which Segoe is
/// installed and lands a pixel or two off centre; a 10px box stroked at one
/// device pixel is the same on every machine.
class _GlyphPainter extends CustomPainter {
  _GlyphPainter(this.glyph, this.color);

  final _Glyph glyph;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    final p = Paint()
      ..color = color
      ..strokeWidth = 1
      ..style = PaintingStyle.stroke;
    final cx = size.width / 2;
    final cy = size.height / 2;
    const s = 10.0;
    final left = (cx - s / 2).roundToDouble() + 0.5;
    final top = (cy - s / 2).roundToDouble() + 0.5;

    switch (glyph) {
      case _Glyph.minimize:
        canvas.drawLine(
          Offset(left, cy.roundToDouble() + 0.5),
          Offset(left + s, cy.roundToDouble() + 0.5),
          p,
        );
      case _Glyph.maximize:
        canvas.drawRect(Rect.fromLTWH(left, top, s, s), p);
      case _Glyph.restore:
        // The back sheet, drawn as just the two edges that peek out behind the
        // front one (top and right) — its other two edges are occluded, so
        // drawing the full square left internal lines crossing the front and it
        // read as a grid. The alpha-0 "eraser" rect that was meant to hide them
        // painted nothing. The front sheet is then a clean full square on top.
        canvas.drawLine(Offset(left + 2, top), Offset(left + s, top), p);
        canvas.drawLine(Offset(left + s, top), Offset(left + s, top + s - 2), p);
        canvas.drawRect(Rect.fromLTWH(left, top + 2, s - 2, s - 2), p);
      case _Glyph.close:
        canvas.drawLine(Offset(left, top), Offset(left + s, top + s), p);
        canvas.drawLine(Offset(left + s, top), Offset(left, top + s), p);
    }
  }

  @override
  bool shouldRepaint(_GlyphPainter old) =>
      old.glyph != glyph || old.color != color;
}
