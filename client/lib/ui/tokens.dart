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
  // Was #6E747C, which measured 3.87:1 against the panel over the desk — under
  // the readable minimum. Raising it to clear 4.5:1 put it 3/255 from `hint`,
  // i.e. the same paint under two names: a ramp stop that exists in the variable
  // list and not on screen. So `faint` IS `hint` now, honestly. The ramp's real
  // steps are title/body → dim → soft → hint; anything dimmer than hint is not
  // a step, it is unreadable.
  static const faint = hint;

  static const mono = 'Consolas';

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

  /// One number for "this cannot be used right now". Btn had 0.38, the rail
  /// 0.4, the expert rows 0.4, and the command bar's chips had nothing at all —
  /// so during a fit the three chips that drive the run looked exactly as
  /// clickable as they do at rest.
  static const disabledOpacity = 0.4;
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

  /// The few changes that are genuinely large: a whole canvas being repointed,
  /// the verdict card growing once a multi-minute fit is finally done. `base` is
  /// a hover-tint budget applied to a panel; using it for a full-surface change
  /// makes the change read as a jump that happened to take 150ms.
  static const slow = Duration(milliseconds: 220);

  /// Leaving is quicker than arriving: the entrance has to explain where the
  /// thing came from, the exit only has to not be in the way.
  static const exit = Duration(milliseconds: 110);

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
    this.semanticLabel,
    this.focusable = true,
    this.dimWhenDisabled = true,
  });

  /// Receives hover and pressed so the child can tint itself. Both are false
  /// when [onTap] is null: a disabled control must not pretend to respond.
  final Widget Function(BuildContext context, bool hover, bool down) builder;

  final VoidCallback? onTap;
  final VoidCallback? onSecondaryTap;
  final MouseCursor cursor;
  final HitTestBehavior behavior;

  /// What a screen reader should call this. Most controls carry their own text
  /// and need none; the glyph-only ones (⚙, ?, ✕) announce nothing without it.
  final String? semanticLabel;

  /// False for controls that would only add noise to the Tab order — a tile in
  /// a long grid whose real affordance is the pointer.
  final bool focusable;

  /// Fades the control when it has no action. Off for the few that already draw
  /// their own disabled state (Btn) and would otherwise dim twice.
  final bool dimWhenDisabled;

  @override
  State<Pressable> createState() => _PressableState();
}

class _PressableState extends State<Pressable> {
  bool _hover = false;
  bool _down = false;
  bool _focused = false;

  @override
  Widget build(BuildContext context) {
    final enabled = widget.onTap != null || widget.onSecondaryTap != null;
    final core = MouseRegion(
      cursor: enabled ? widget.cursor : SystemMouseCursors.basic,
      // Mounted guards: a pointer's up/cancel/exit is delivered to whatever its
      // hit test found at DOWN time, so a control removed mid-gesture (the
      // empty-screen card while the picked image swaps in) still receives the
      // tail of the gesture after dispose.
      onEnter: (_) => setState(() => _hover = true),
      onExit: (_) {
        if (!mounted) return;
        setState(() {
          _hover = false;
          _down = false;
        });
      },
      child: Listener(
        onPointerDown: enabled ? (_) => setState(() => _down = true) : null,
        onPointerUp: enabled
            ? (_) {
                if (mounted) setState(() => _down = false);
              }
            : null,
        onPointerCancel: enabled
            ? (_) {
                if (mounted) setState(() => _down = false);
              }
            : null,
        child: GestureDetector(
          behavior: widget.behavior,
          onTap: widget.onTap,
          onSecondaryTap: widget.onSecondaryTap,
          // Keyboard focus reuses the HOVER visual rather than inventing a
          // second lit state: the control already has a "the pointer is here"
          // look, and focus means the same thing for the keyboard.
          child: widget.builder(
            context,
            enabled && (_hover || _focused),
            enabled && _down,
          ),
        ),
      ),
    );

    // Every control in the app goes through here, so this is the one place that
    // can give the whole UI a Tab order and Enter/Space activation. Without it
    // nothing but a TextField or a Slider could take focus at all — the app was
    // pointer-only end to end.
    final Widget body = !enabled || !widget.focusable
        ? core
        : FocusableActionDetector(
            mouseCursor: MouseCursor.defer,
            onShowFocusHighlight: (v) => setState(() => _focused = v),
            actions: <Type, Action<Intent>>{
              ActivateIntent: CallbackAction<ActivateIntent>(
                onInvoke: (_) {
                  widget.onTap?.call();
                  return null;
                },
              ),
            },
            child: core,
          );

    // One place decides what "unavailable" looks like, so a control class that
    // never got its own disabled state still gets one.
    final shown = !enabled && widget.dimWhenDisabled
        ? Opacity(opacity: T.disabledOpacity, child: body)
        : body;

    if (widget.semanticLabel == null) return shown;
    return Semantics(
      label: widget.semanticLabel,
      button: true,
      enabled: enabled,
      child: shown,
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
      ),
      child: child,
    );
    // The clip is NOT part of the blur: a panel still has to cut its children to
    // its own corners whether or not the backdrop is being filtered. It used to
    // sit on the live path only, so every `live: false` panel — the inspector,
    // the editor bar, the tools, the command bar — let a selected row's teal fill
    // square off the panel's 13px corner. Only the FILTER is conditional.
    final clipped = ClipRRect(
      borderRadius: BorderRadius.circular(radius),
      child: live
          ? BackdropFilter(
              filter: ImageFilter.blur(sigmaX: 22, sigmaY: 22),
              child: body,
            )
          : body,
    );
    // The shadow is drawn OUTSIDE the clip. It used to live in the body's own
    // decoration, which — once the clip became unconditional — meant every panel
    // clipped away its own shadow: a shadow is by definition painted outside the
    // rounded rect it belongs to. The panels went flat, losing the depth cue the
    // whole glass recipe exists for.
    return DecoratedBox(
      decoration: BoxDecoration(
        borderRadius: BorderRadius.circular(radius),
        boxShadow: const [
          BoxShadow(
            color: Color(0x80000000),
            blurRadius: 44,
            offset: Offset(0, 14),
          ),
        ],
      ),
      child: clipped,
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

/// The icons that were text glyphs.
///
/// The window buttons already make this argument and act on it: a font
/// character depends on which Segoe is installed and lands a pixel or two off
/// centre. Emoji are worse still — `👁 🔒 🚫 🔓` resolve through Segoe UI Emoji,
/// paint in full colour inside a monochrome dark panel, and IGNORE the `color`
/// they are given, so the tint that every other icon uses to show on/off did
/// nothing to them. Drawn strokes obey the colour, keep one weight, and are the
/// same on every machine and in every locale.
enum Ico { eye, eyeOff, lock, unlock, group, moveTo, close, plus }

class Icon2 extends StatelessWidget {
  const Icon2(this.icon, {super.key, required this.color, this.size = 13});

  final Ico icon;
  final Color color;
  final double size;

  @override
  Widget build(BuildContext context) => SizedBox(
    width: size,
    height: size,
    child: CustomPaint(painter: _IcoPainter(icon, color)),
  );
}

class _IcoPainter extends CustomPainter {
  _IcoPainter(this.icon, this.color);
  final Ico icon;
  final Color color;

  @override
  void paint(Canvas canvas, Size size) {
    final p = Paint()
      ..color = color
      ..style = PaintingStyle.stroke
      ..strokeWidth = 1.2
      ..strokeCap = StrokeCap.round
      ..strokeJoin = StrokeJoin.round;
    final w = size.width;
    final h = size.height;
    final cx = w / 2;
    final cy = h / 2;

    switch (icon) {
      // An almond on its side: two arcs meeting at the corners, pupil in the
      // middle. Drawn rather than a font eye so it keeps the 1.2px weight.
      case Ico.eye || Ico.eyeOff:
        // Lens opened to 0.04/0.96 and the pupil cut to 0.09r: at 13px the old
        // 0.16/0.84 lens spanned ~4.4px around a ~3.9px pupil, so the two 1.2px
        // strokes merged into a blob and the icon read as a filled dot.
        final path = Path()
          ..moveTo(w * 0.06, cy)
          ..quadraticBezierTo(cx, h * 0.04, w * 0.94, cy)
          ..quadraticBezierTo(cx, h * 0.96, w * 0.06, cy);
        canvas.drawPath(path, p);
        canvas.drawCircle(Offset(cx, cy), w * 0.09, p);
        if (icon == Ico.eyeOff) {
          canvas.drawLine(
            Offset(w * 0.14, h * 0.86),
            Offset(w * 0.86, h * 0.14),
            p,
          );
        }
      // Body plus shackle; the open one lifts its shackle off the right side.
      case Ico.lock || Ico.unlock:
        final body = Rect.fromLTWH(w * 0.18, h * 0.46, w * 0.64, h * 0.40);
        canvas.drawRRect(
          RRect.fromRectAndRadius(body, Radius.circular(w * 0.1)),
          p,
        );
        final shackle = Path();
        if (icon == Ico.lock) {
          shackle
            ..moveTo(w * 0.32, h * 0.46)
            ..lineTo(w * 0.32, h * 0.30)
            ..arcToPoint(
              Offset(w * 0.68, h * 0.30),
              radius: Radius.circular(w * 0.18),
            )
            ..lineTo(w * 0.68, h * 0.46);
        } else {
          shackle
            ..moveTo(w * 0.32, h * 0.46)
            ..lineTo(w * 0.32, h * 0.30)
            ..arcToPoint(
              Offset(w * 0.68, h * 0.30),
              radius: Radius.circular(w * 0.18),
            );
        }
        canvas.drawPath(shackle, p);
      // Two overlapping frames: "these move as one body".
      case Ico.group:
        canvas.drawRect(
          Rect.fromLTWH(w * 0.10, h * 0.28, w * 0.52, h * 0.52),
          p,
        );
        canvas.drawRect(
          Rect.fromLTWH(w * 0.38, h * 0.14, w * 0.52, h * 0.52),
          p,
        );
      // An arrow turning down into the row: "put the selection here".
      case Ico.moveTo:
        canvas.drawLine(
          Offset(w * 0.20, h * 0.20),
          Offset(w * 0.72, h * 0.20),
          p,
        );
        canvas.drawLine(
          Offset(w * 0.72, h * 0.20),
          Offset(w * 0.72, h * 0.72),
          p,
        );
        canvas.drawLine(
          Offset(w * 0.72, h * 0.72),
          Offset(w * 0.52, h * 0.52),
          p,
        );
        canvas.drawLine(
          Offset(w * 0.72, h * 0.72),
          Offset(w * 0.92, h * 0.52),
          p,
        );
      case Ico.close:
        canvas.drawLine(
          Offset(w * 0.22, h * 0.22),
          Offset(w * 0.78, h * 0.78),
          p,
        );
        canvas.drawLine(
          Offset(w * 0.78, h * 0.22),
          Offset(w * 0.22, h * 0.78),
          p,
        );
      case Ico.plus:
        canvas.drawLine(Offset(w * 0.5, h * 0.18), Offset(w * 0.5, h * 0.82), p);
        canvas.drawLine(Offset(w * 0.18, h * 0.5), Offset(w * 0.82, h * 0.5), p);
    }
  }

  @override
  bool shouldRepaint(_IcoPainter old) =>
      old.icon != icon || old.color != color;
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
      // Btn fades itself below; letting Pressable do it too would square the
      // opacity and make a disabled button nearly invisible.
      dimWhenDisabled: false,
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
          opacity: enabled ? 1 : T.disabledOpacity,
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
              // A neutral shadow, not a teal glow. The old one was coloured
              // light emitted BY the control, which nothing in the scene could
              // cast — the one effect in the app that decorated rather than
              // described. The affordance was never carried by it anyway: the
              // fill is already the brightest thing on screen, and hover
              // brightens it while press darkens it. Revert = put the
              // 0x4254CBB8 shadow back here.
              boxShadow: widget.kind == BtnKind.primary
                  ? [
                      BoxShadow(
                        color: const Color(0x59000000),
                        blurRadius: hover ? 10 : 6,
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
