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

  void _measure(Duration _) {
    final box = _key.currentContext?.findRenderObject() as RenderBox?;
    if (box == null || !box.hasSize) return;
    final w = box.size.width;
    if ((w - _reported).abs() < 0.5) return;
    _reported = w;
    WindowState.setControlsWidth(w);
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

    return MouseRegion(
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
        // The back sheet is drawn first and clipped by the front one, which is
        // what makes it read as two stacked windows rather than a grid.
        canvas.drawRect(Rect.fromLTWH(left + 2, top, s - 2, s - 2), p);
        canvas.drawRect(
          Rect.fromLTWH(left, top + 2, s - 2, s - 2),
          Paint()..color = color.withValues(alpha: 0),
        );
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
