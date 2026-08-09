/// The design tokens, transcribed from the canvas-first redesign.
///
/// They live in one file because the design's whole character is consistency:
/// one teal, one glass recipe, one set of greys that step evenly from title to
/// hint. A widget that reaches for its own `Color(0xFF...)` breaks that quietly,
/// so everything visual should come from here.
library;

import 'dart:ui' show ImageFilter;

import 'package:flutter/widgets.dart';

class T {
  T._();

  // The desk the app sits on, and the panels floating over it.
  static const desk = Color(0xFF08090A);
  static const panel = Color(0xB8181A1D); // rgba(24,26,29,.72)
  static const border = Color(0x1CFFFFFF); // rgba(255,255,255,.11)
  static const hairline = Color(0x14FFFFFF); // rgba(255,255,255,.08)
  static const fill = Color(0x17FFFFFF); // rgba(255,255,255,.09)
  static const fillSoft = Color(0x12FFFFFF); // rgba(255,255,255,.07)

  /// The single accent. Ink is what sits ON it — dark enough to read at 12.5px.
  static const teal = Color(0xFF54CBB8);
  static const tealBright = Color(0xFF7BDCCB);
  static const ink = Color(0xFF06231F);
  static const tealWash = Color(0x3354CBB8); // selection backgrounds
  static const tealFaint = Color(0x2454CBB8);

  static const green = Color(0xFF5FD08D);
  static const amber = Color(0xFFE0A84B);
  static const danger = Color(0xFFF0685F);

  // Text steps down in five stops; using them in order is what makes a dense
  // panel readable without borders between every row.
  static const title = Color(0xFFF2F3F5);
  static const body = Color(0xFFEDEFF2);
  static const dim = Color(0xFFC3C8CF);
  static const soft = Color(0xFF8C929A);
  static const hint = Color(0xFF7C828A);
  static const faint = Color(0xFF6E747C);

  static const mono = 'Consolas';

  static const radius = Radius.circular(9);
  static const panelRadius = Radius.circular(13);

  static TextStyle text(
    double size, {
    Color color = body,
    FontWeight weight = FontWeight.w400,
    double? spacing,
  }) => TextStyle(
    fontSize: size,
    color: color,
    fontWeight: weight,
    letterSpacing: spacing ?? -0.05,
    height: 1.25,
  );

  static TextStyle monoText(
    double size, {
    Color color = body,
    FontWeight weight = FontWeight.w400,
  }) => TextStyle(
    fontFamily: mono,
    fontSize: size,
    color: color,
    fontWeight: weight,
    height: 1.2,
  );

  /// A small caps label — the design uses it for every section heading.
  static TextStyle get label => const TextStyle(
    fontSize: 9.5,
    color: hint,
    fontWeight: FontWeight.w600,
    letterSpacing: 0.7,
  );

  // What a surface does under the pointer. One pair for the whole app, so a
  // control the user has never seen still reacts the way the last one did.
  static const hoverTint = Color(0x14FFFFFF);
  static const pressTint = Color(0x24FFFFFF);
}

/// How long anything is allowed to take.
///
/// The whole set is short on purpose. An animation is here to say WHICH thing
/// changed and where it came from; past about a fifth of a second it stops
/// informing and starts being a wait, and the app feels heavy.
class Motion {
  Motion._();

  /// Hover tints, press states — anything the pointer drives directly.
  static const fast = Duration(milliseconds: 90);

  /// Popovers, panels, things appearing and disappearing.
  static const base = Duration(milliseconds: 150);

  /// The odd larger surface that crosses most of the screen.
  static const slow = Duration(milliseconds: 200);

  static const curve = Curves.easeOutCubic;
  static const curveIn = Curves.easeInCubic;
}

/// Anything clickable, with the pointer feedback that makes it look clickable.
///
/// Every control in the app goes through this. The alternative — each widget
/// growing its own MouseRegion and its own idea of a hover colour — is how the
/// app ended up with controls that were impossible to tell from labels.
class Pressable extends StatefulWidget {
  const Pressable({
    super.key,
    required this.builder,
    this.onTap,
    this.onSecondaryTap,
    this.cursor = SystemMouseCursors.click,
    this.behavior = HitTestBehavior.opaque,
  });

  /// Receives hover and pressed so the child can tint itself. Both are false
  /// when [onTap] is null: a disabled control must not pretend to respond.
  final Widget Function(BuildContext context, bool hover, bool down) builder;

  final VoidCallback? onTap;
  final VoidCallback? onSecondaryTap;
  final MouseCursor cursor;
  final HitTestBehavior behavior;

  @override
  State<Pressable> createState() => _PressableState();
}

class _PressableState extends State<Pressable> {
  bool _hover = false;
  bool _down = false;

  @override
  Widget build(BuildContext context) {
    final enabled = widget.onTap != null || widget.onSecondaryTap != null;
    return MouseRegion(
      cursor: enabled ? widget.cursor : SystemMouseCursors.basic,
      onEnter: (_) => setState(() => _hover = true),
      onExit: (_) => setState(() {
        _hover = false;
        _down = false;
      }),
      child: Listener(
        onPointerDown: enabled ? (_) => setState(() => _down = true) : null,
        onPointerUp: enabled ? (_) => setState(() => _down = false) : null,
        onPointerCancel: enabled ? (_) => setState(() => _down = false) : null,
        child: GestureDetector(
          behavior: widget.behavior,
          onTap: widget.onTap,
          onSecondaryTap: widget.onSecondaryTap,
          child: widget.builder(context, enabled && _hover, enabled && _down),
        ),
      ),
    );
  }
}

/// The standard tint for a surface under the pointer, composited over whatever
/// the surface already is.
Color hoverOver(Color base, bool hover, bool down) {
  if (down) return Color.alphaBlend(T.pressTint, base);
  if (hover) return Color.alphaBlend(T.hoverTint, base);
  return base;
}

/// Fades and lifts a panel in as it appears.
///
/// Popovers used to blink into existence, which reads as a redraw rather than
/// as something opening. The travel is small — 6px — because the point is to
/// show where it came from, not to watch it fly.
class PopIn extends StatefulWidget {
  const PopIn({super.key, required this.child, this.from = const Offset(0, 6)});

  final Widget child;
  final Offset from;

  @override
  State<PopIn> createState() => _PopInState();
}

class _PopInState extends State<PopIn> with SingleTickerProviderStateMixin {
  late final _c = AnimationController(vsync: this, duration: Motion.base)
    ..forward();
  late final _a = CurvedAnimation(parent: _c, curve: Motion.curve);

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => AnimatedBuilder(
    animation: _a,
    builder: (context, child) => Opacity(
      opacity: _a.value,
      child: Transform.translate(
        offset: widget.from * (1 - _a.value),
        child: child,
      ),
    ),
    child: widget.child,
  );
}

/// A frosted panel. The blur is the design's main depth cue: the desk glow
/// bleeds through every floating surface, which is what stops a dark UI from
/// reading as flat black rectangles.
class Glass extends StatelessWidget {
  const Glass({
    super.key,
    required this.child,
    this.radius = 13,
    this.padding = EdgeInsets.zero,
    this.live = true,
  });

  final Widget child;
  final double radius;
  final EdgeInsets padding;

  /// Whether the backdrop is genuinely worth blurring. A BackdropFilter can
  /// never be raster-cached — its input is the live scene — so every panel
  /// carrying one re-blurs on every presented frame. A panel that sits over
  /// the STATIC desk (tools, bar, inspector: they never overlap the canvas)
  /// pays that price for a blur of a smooth gradient, which looks identical
  /// to the gradient. Those pass false; panels that really float over moving
  /// artwork (the bank) keep the live blur.
  final bool live;

  @override
  Widget build(BuildContext context) {
    final body = Container(
      padding: padding,
      decoration: BoxDecoration(
        color: T.panel,
        borderRadius: BorderRadius.circular(radius),
        border: Border.all(color: T.border),
        boxShadow: const [
          BoxShadow(
            color: Color(0x80000000),
            blurRadius: 44,
            offset: Offset(0, 14),
          ),
        ],
      ),
      child: child,
    );
    if (!live) return body;
    return ClipRRect(
      borderRadius: BorderRadius.circular(radius),
      child: BackdropFilter(
        filter: ImageFilter.blur(sigmaX: 22, sigmaY: 22),
        child: body,
      ),
    );
  }
}

/// The halftone mark: a photograph resolving into shapes. Dots shrink along the
/// diagonal, largest at the source corner. Drawn rather than shipped as an asset
/// so it stays crisp at every size, which is the whole point of the mark — it
/// has to survive being 16px in a taskbar.
class HalftoneMark extends StatelessWidget {
  const HalftoneMark({super.key, this.size = 26, this.color, this.tile = true});

  final double size;
  final Color? color;

  /// Draws the teal tile behind the dots — the app icon exactly as it appears
  /// in the taskbar. On by default so the mark in the window is the same object
  /// the user clicked to get here; off gives the bare dots for a dark surface
  /// that already provides its own background.
  final bool tile;

  @override
  Widget build(BuildContext context) => CustomPaint(
    size: Size.square(size),
    painter: _HalftonePainter(color ?? const Color(0xFFFFFFFF), tile),
  );
}

class _HalftonePainter extends CustomPainter {
  _HalftonePainter(this.color, this.tile);
  final Color color;
  final bool tile;

  @override
  void paint(Canvas canvas, Size size) {
    if (tile) {
      // The icon's own gradient and corner radius, so the two are one mark.
      final r = RRect.fromRectAndRadius(
        Offset.zero & size,
        Radius.circular(size.width * 0.235),
      );
      canvas.drawRRect(
        r,
        Paint()
          ..shader = const LinearGradient(
            begin: Alignment(-0.42, -0.91),
            end: Alignment(0.42, 0.91),
            colors: [Color(0xFF5FDCC8), Color(0xFF128577), Color(0xFF0C5F55)],
            stops: [0, 0.62, 1],
          ).createShader(Offset.zero & size),
      );
    }
    final pad = size.width * 0.15;
    final span = size.width - pad * 2;
    final n = size.width < 24 ? 2 : 3;
    final cell = span / n;
    // The scale ramp runs along the ANTI-diagonal, so the mark reads as a
    // gradient of resolution rather than a grid of circles.
    const ramp3 = [0.98, 0.8, 0.6, 0.4, 0.22];
    const ramp2 = [0.96, 0.62, 0.3];

    for (var r = 0; r < n; r++) {
      for (var c = 0; c < n; c++) {
        final dist = (n - 1 - r) + c;
        final scale = n == 2 ? ramp2[dist] : ramp3[dist];
        final d = cell * scale;
        final cx = pad + cell * c + cell / 2;
        final cy = pad + cell * r + cell / 2;
        canvas.drawCircle(
          Offset(cx, cy),
          d / 2,
          Paint()..color = color.withValues(alpha: 0.5 + 0.5 * scale),
        );
      }
    }
  }

  @override
  bool shouldRepaint(_HalftonePainter old) =>
      old.color != color || old.tile != tile;
}

/// A button in one of the design's three weights.
enum BtnKind { primary, ghost, danger }

class Btn extends StatefulWidget {
  const Btn(
    this.label, {
    super.key,
    this.onTap,
    this.kind = BtnKind.ghost,
    this.trailing,
  });

  final String label;
  final VoidCallback? onTap;
  final BtnKind kind;
  final Widget? trailing;

  @override
  State<Btn> createState() => _BtnState();
}

class _BtnState extends State<Btn> {
  @override
  Widget build(BuildContext context) {
    final enabled = widget.onTap != null;
    final (bg, fg) = switch (widget.kind) {
      BtnKind.primary => (T.teal, T.ink),
      BtnKind.ghost => (T.fill, T.body),
      BtnKind.danger => (const Color(0x26F0685F), T.danger),
    };
    return Pressable(
      onTap: widget.onTap,
      builder: (context, hover, down) {
        // The primary button is already bright, so it brightens FURTHER on
        // hover and darkens on press; a white wash over teal would just look
        // washed out.
        final Color fill;
        if (widget.kind == BtnKind.primary) {
          fill = down ? const Color(0xFF3FB3A0) : (hover ? T.tealBright : bg);
        } else {
          fill = hoverOver(bg, hover, down);
        }
        return AnimatedOpacity(
          duration: Motion.fast,
          opacity: enabled ? 1 : 0.38,
          child: AnimatedContainer(
            duration: Motion.fast,
            height: 29,
            padding: const EdgeInsets.symmetric(horizontal: 14),
            decoration: BoxDecoration(
              color: fill,
              borderRadius: BorderRadius.circular(9),
              border: widget.kind == BtnKind.ghost
                  ? Border.all(color: hover ? T.border : T.hairline)
                  : null,
              boxShadow: widget.kind == BtnKind.primary
                  ? [
                      BoxShadow(
                        color: const Color(0x4254CBB8),
                        blurRadius: hover ? 16 : 10,
                        offset: const Offset(0, 2),
                      ),
                    ]
                  : null,
            ),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              // Centred, so a button given more room than its label — one of a
              // stretched pair — does not sit with its text against the left
              // edge while its neighbour looks centred.
              mainAxisAlignment: MainAxisAlignment.center,
              children: [
                // Flexible with an ellipsis, always. The layout was measured
                // with English labels and every other language is wider; without
                // this a button in Ukrainian simply paints over its neighbour
                // instead of admitting it does not fit.
                Flexible(
                  child: Text(
                    widget.label,
                    maxLines: 1,
                    softWrap: false,
                    overflow: TextOverflow.ellipsis,
                    style: T.text(12.5, color: fg, weight: FontWeight.w600),
                  ),
                ),
                if (widget.trailing != null) ...[
                  const SizedBox(width: 7),
                  widget.trailing!,
                ],
              ],
            ),
          ),
        );
      },
    );
  }
}
