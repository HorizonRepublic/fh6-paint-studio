/// The shape editor.
///
/// The canvas is the engine's own render, so what is on screen is what will be
/// injected. Selection handles and the drag outline are painted over it; those
/// are gesture aids and deliberately do not try to look like the shape's real
/// coverage, because anything that half-imitates the renderer invites the eye to
/// judge the imitation.
library;

import 'dart:math' as math;
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../state/editor.dart';
import '../state/studio.dart';
import 'strings.dart';
import 'tokens.dart';

class EditorView extends StatefulWidget {
  const EditorView({
    super.key,
    required this.editor,
    required this.studio,
    required this.onClose,
  });

  final Editor editor;
  final Studio studio;
  final VoidCallback onClose;

  @override
  State<EditorView> createState() => _EditorViewState();
}

enum _Tool { select, edit, ellipse, rect, triangle, place }

/// The tools that DRAW rather than transform.
bool _isCreate(_Tool t) =>
    t == _Tool.ellipse ||
    t == _Tool.rect ||
    t == _Tool.triangle ||
    t == _Tool.place;

/// What the pointer is over. The editor asks this before it asks which TOOL is
/// active, because a corner handle means resize no matter what is selected in
/// the tool column — that is what a handle is for.
enum Grip { none, body, rotate, topLeft, topRight, bottomLeft, bottomRight }

/// Half the side of a corner handle, in document pixels at the current zoom.
double gripReach(double scale) => 7 / scale;

/// Which grip is at [p], for the shape whose bounds are [b].
Grip gripAt(Offset p, Rect? b, double scale) {
  if (b == null) return Grip.none;
  final r = b.inflate(3 / scale);
  final reach = gripReach(scale);
  final anchor = Offset(r.center.dx, r.top - rotateHandleGap / scale);
  if ((p - anchor).distance < reach * 1.6) return Grip.rotate;
  bool near(Offset c) => (p - c).distance < reach;
  if (near(r.topLeft)) return Grip.topLeft;
  if (near(r.topRight)) return Grip.topRight;
  if (near(r.bottomLeft)) return Grip.bottomLeft;
  if (near(r.bottomRight)) return Grip.bottomRight;
  return r.contains(p) ? Grip.body : Grip.none;
}

MouseCursor _cursorFor(Grip g, _Tool tool) => switch (g) {
  Grip.topLeft || Grip.bottomRight => SystemMouseCursors.resizeUpLeftDownRight,
  Grip.topRight || Grip.bottomLeft => SystemMouseCursors.resizeUpRightDownLeft,
  // There is no rotate cursor on this platform; grab is the closest thing that
  // still says "this is a thing you take hold of".
  Grip.rotate => SystemMouseCursors.grab,
  Grip.body => SystemMouseCursors.move,
  Grip.none => switch (tool) {
    _Tool.select => SystemMouseCursors.basic,
    _Tool.edit => SystemMouseCursors.basic,
    _ => SystemMouseCursors.precise,
  },
};

class _EditorViewState extends State<EditorView>
    with SingleTickerProviderStateMixin {
  /// A slow pulse for the selection outline. Which shape is selected was
  /// genuinely hard to tell on a busy canvas — a static teal box among three
  /// thousand shapes is just another shape. Movement is the one thing the eye
  /// finds without being told where to look.
  late final _pulse = AnimationController(
    vsync: this,
    duration: const Duration(milliseconds: 1100),
  )..repeat(reverse: true);

  _Tool _tool = _Tool.select;
  Offset? _dragFrom;
  bool _marked = false;

  /// What the pointer is over right now, and what it grabbed when it went down.
  /// Kept apart so a drag that starts on a corner keeps resizing even after the
  /// pointer has left that corner behind.
  Grip _hover = Grip.none;
  Grip _held = Grip.none;
  double _scale = 1;

  /// The rectangle being dragged out with a create tool, in document space.
  Offset? _newFrom;
  Offset? _newTo;

  /// Whether the bank is on screen. It opens with its tool and stays open, so
  /// placing ten stars is ten drags rather than ten trips to a menu.
  bool _bankOpen = false;

  Editor get ed => widget.editor;

  @override
  void dispose() {
    _pulse.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    // The editor is its own screen, so it needs its own bindings: the shell's
    // shortcuts never reach here.
    return CallbackShortcuts(
      bindings: {
        const SingleActivator(LogicalKeyboardKey.keyZ, control: true): () {
          if (ed.canUndo) ed.undo();
        },
        const SingleActivator(
          LogicalKeyboardKey.keyZ,
          control: true,
          shift: true,
        ): () {
          if (ed.canRedo) ed.redo();
        },
        const SingleActivator(LogicalKeyboardKey.keyY, control: true): () {
          if (ed.canRedo) ed.redo();
        },
        const SingleActivator(LogicalKeyboardKey.delete): ed.deleteSelected,
      },
      child: Focus(autofocus: true, child: _body(context)),
    );
  }

  Widget _body(BuildContext context) {
    return AnimatedBuilder(
      animation: ed,
      builder: (context, _) => Stack(
        fit: StackFit.expand,
        children: [
          // No background of its own: the shell paints the desk behind this,
          // so entering the editor no longer changes the room the app is in.
          Positioned(
            left: 92,
            top: 52,
            right: 300,
            bottom: 24,
            child: _Canvas(
              editor: ed,
              tool: _tool,
              hover: _hover,
              pulse: _pulse,
              group: ed.groupLayer == null
                  ? null
                  : ed.layerBounds(ed.groupLayer!),
              pending: _newFrom == null || _newTo == null
                  ? null
                  : Rect.fromPoints(_newFrom!, _newTo!),
              onHover: _onHover,
              onScale: (v) => _scale = v,
              onPointerDown: _down,
              onPointerMove: _move,
              onPointerUp: _up,
            ),
          ),
          Positioned(
            left: 14,
            top: 60,
            child: _Tools(
              editor: ed,
              recent: ed.recent,
              onPickKind: (k) => setState(() {
                ed.pickedKind = k;
                _tool = _Tool.place;
              }),
              tool: _tool,
              onPick: (t) => setState(() {
                _tool = t;
                if (t == _Tool.place) {
                  _bankOpen = true;
                  ed.loadCatalog();
                } else {
                  ed.pickedKind = null;
                }
              }),
            ),
          ),
          if (_bankOpen)
            Positioned(
              left: 62,
              top: 60,
              bottom: 24,
              child: _Bank(
                editor: ed,
                onClose: () => setState(() => _bankOpen = false),
                onPick: (k) => setState(() {
                  ed.pickedKind = k;
                  _tool = _Tool.place;
                }),
              ),
            ),
          Positioned(
            right: 12,
            top: 8,
            bottom: 16,
            child: _Inspector(editor: ed),
          ),
          Positioned(
            left: 92,
            top: 8,
            right: 312,
            child: _Bar(
              editor: ed,
              studio: widget.studio,
              onClose: widget.onClose,
            ),
          ),
        ],
      ),
    );
  }

  Rect? get _frame {
    final g = ed.groupLayer;
    return g != null ? ed.layerBounds(g) : ed.current?.bounds;
  }

  void _onHover(Offset docPoint) {
    final g = gripAt(docPoint, _frame, _scale);
    if (g != _hover) setState(() => _hover = g);
  }

  void _down(Offset docPoint) {
    if (_isCreate(_tool)) {
      setState(() {
        _newFrom = docPoint;
        _newTo = docPoint;
      });
      return;
    }
    final s = ed.current;
    _held = gripAt(docPoint, _frame, _scale);
    // A handle is a handle: grabbing one transforms the current selection
    // whatever the tool column says. Only a click on empty space, or inside a
    // shape while the select tool is active, picks something new.
    final onHandle = _held != Grip.none && _held != Grip.body;
    if (!onHandle &&
        !(s != null && _tool != _Tool.select && _held == Grip.body)) {
      ed.select(ed.hitTest(docPoint));
      _held = gripAt(docPoint, _frame, _scale);
    }
    _dragFrom = docPoint;
    _marked = false;
  }

  void _move(Offset docPoint) {
    if (_isCreate(_tool)) {
      if (_newFrom != null) setState(() => _newTo = docPoint);
      return;
    }
    final from = _dragFrom;
    final s = ed.current;
    final groupHeld = ed.groupLayer;
    if (from == null || (s == null && groupHeld == null)) return;

    // What the pointer took hold of decides the transform. Select only ever
    // acts through a grip; Edit acts on the body as well, which is the whole
    // difference between the two modes.
    final onGrip = _held != Grip.none && _held != Grip.body;
    if (!onGrip && !(_tool == _Tool.edit && _held == Grip.body)) return;

    // The undo snapshot is taken once per GESTURE, not per frame: a drag that
    // recorded every motion event would make undo mean "go back one pixel".
    if (!_marked) {
      ed.mark();
      _marked = true;
    }
    final d = docPoint - from;
    final centre = groupHeld != null
        ? ed.layerBounds(groupHeld)?.center
        : s?.center;
    if (centre == null) return;

    double turn() {
      final a0 = math.atan2(from.dy - centre.dy, from.dx - centre.dx);
      final a1 = math.atan2(docPoint.dy - centre.dy, docPoint.dx - centre.dx);
      return (a1 - a0) * 180 / math.pi;
    }

    double growth() {
      final was = (from - centre).distance;
      final now = (docPoint - centre).distance;
      return was > 1 ? (now / was) : 1;
    }

    if (groupHeld != null) {
      switch (_held) {
        case Grip.rotate:
          ed.rotateLayer(groupHeld, turn());
        case Grip.body:
          ed.translateLayer(groupHeld, d.dx, d.dy);
        default:
          ed.scaleLayer(groupHeld, growth().clamp(0.5, 2.0));
      }
    } else {
      switch (_held) {
        case Grip.rotate:
          s!.rotateBy(turn());
        case Grip.body:
          s!.translate(d.dx, d.dy);
        default:
          s!.scaleBy(growth().clamp(0.2, 5.0));
      }
    }
    _dragFrom = docPoint;
    ed.live();
  }

  void _up() {
    final from = _newFrom;
    final to = _newTo;
    if (from != null && to != null) {
      final r = Rect.fromPoints(from, to);
      // A click, not a drag. Nothing is created rather than leaving a
      // zero-sized shape somewhere the user cannot see or select it.
      if (r.width > 3 && r.height > 3) ed.addShape(_shapeIn(r, _tool, ed));
      setState(() {
        _newFrom = null;
        _newTo = null;
      });
      return;
    }
    _dragFrom = null;
    _held = Grip.none;
    if (_marked) ed.commit();
    _marked = false;
  }
}

/// Turns the dragged rectangle into a shape.
///
/// The colour comes from whatever was selected last, falling back to a mid
/// grey. Sampling the picture under the drag would be nicer and is not possible
/// synchronously — the render lives on the GPU — so the honest default is one
/// the user can see and immediately change in the inspector.
EditShape _shapeIn(Rect r, _Tool tool, Editor ed) {
  final colour = ed.current?.color ?? const [150, 150, 150, 255];
  final c = List<int>.of(colour);
  final picked = ed.pickedKind;
  if (tool == _Tool.place && picked != null) {
    return shapeOfKind(picked, r, c);
  }
  return switch (tool) {
    _Tool.rect => EditShape(typeRect, [
      r.center.dx,
      r.center.dy,
      r.width / 2,
      r.height / 2,
      0,
    ], c),
    _Tool.triangle => EditShape(typeTriangle, [
      r.center.dx,
      r.top,
      r.right,
      r.bottom,
      r.left,
      r.bottom,
    ], c),
    _ => EditShape(typeEllipse, [
      r.center.dx,
      r.center.dy,
      r.width / 2,
      r.height / 2,
      0,
    ], c),
  };
}

class _Canvas extends StatelessWidget {
  const _Canvas({
    required this.editor,
    required this.tool,
    required this.hover,
    required this.pulse,
    required this.group,
    required this.pending,
    required this.onHover,
    required this.onScale,
    required this.onPointerDown,
    required this.onPointerMove,
    required this.onPointerUp,
  });

  final Editor editor;
  final _Tool tool;
  final Grip hover;
  final Animation<double> pulse;

  /// The box around a whole selected LAYER, when one is picked as a group.
  final Rect? group;

  /// The rectangle being dragged out right now, in document space.
  final Rect? pending;
  final void Function(Offset) onHover;
  final void Function(double) onScale;
  final void Function(Offset) onPointerDown;
  final void Function(Offset) onPointerMove;
  final VoidCallback onPointerUp;

  @override
  Widget build(BuildContext context) {
    final img = editor.render;
    if (img == null || editor.width == 0) {
      return Center(
        child: Text(
          editor.error ?? context.s('rendering'),
          style: T.text(12.5, color: T.soft),
        ),
      );
    }
    return Center(
      child: AspectRatio(
        aspectRatio: editor.width / editor.height,
        child: LayoutBuilder(
          builder: (context, box) {
            // One scale for both axes: the aspect is already fixed above, so
            // document pixels map to view pixels by a single factor.
            final scale = box.maxWidth / editor.width;
            Offset toDoc(Offset local) => local / scale;
            onScale(scale);

            return GestureDetector(
              onTapDown: (d) => onPointerDown(toDoc(d.localPosition)),
              onPanStart: (d) => onPointerDown(toDoc(d.localPosition)),
              onPanUpdate: (d) => onPointerMove(toDoc(d.localPosition)),
              onPanEnd: (_) => onPointerUp(),
              child: MouseRegion(
                cursor: _cursorFor(hover, tool),
                onHover: (e) => onHover(toDoc(e.localPosition)),
                onExit: (_) => onHover(const Offset(-1e6, -1e6)),
                child: Stack(
                  fit: StackFit.expand,
                  children: [
                    RawImage(
                      image: img,
                      fit: BoxFit.fill,
                      filterQuality: FilterQuality.medium,
                    ),
                    // Repainting on the pulse only: the handles are cheap and
                    // the image underneath is not, so the heartbeat must not
                    // drag the whole canvas into every frame.
                    RepaintBoundary(
                      child: CustomPaint(
                        painter: _Handles(
                          shape: editor.current,
                          scale: scale,
                          stale: editor.rendering,
                          hover: hover,
                          pending: pending,
                          group: group,
                          pulse: pulse,
                        ),
                      ),
                    ),
                  ],
                ),
              ),
            );
          },
        ),
      ),
    );
  }
}

/// How far above the selection the rotate anchor sits, in view pixels.
const rotateHandleGap = 22.0;

/// The selection outline, its corner handles, and the rotate anchor.
class _Handles extends CustomPainter {
  _Handles({
    required this.shape,
    required this.scale,
    required this.stale,
    required this.hover,
    required this.pending,
    required this.group,
    required this.pulse,
  }) : super(repaint: pulse);

  final EditShape? shape;
  final double scale;
  final bool stale;
  final Grip hover;
  final Rect? pending;
  final Rect? group;
  final Animation<double> pulse;

  @override
  void paint(Canvas canvas, Size size) {
    final p = pending;
    if (p != null) {
      canvas.drawRect(
        Rect.fromLTRB(
          p.left * scale,
          p.top * scale,
          p.right * scale,
          p.bottom * scale,
        ),
        Paint()
          ..style = PaintingStyle.stroke
          ..strokeWidth = 1.5
          ..color = T.tealBright,
      );
    }
    // The pulse rides the outline's brightness and width, so it reads at a
    // glance without turning into a strobe.
    final beat = 0.55 + 0.45 * pulse.value;
    final s = shape;
    final g = group;
    if (s == null && g == null) return;

    final b = g ?? s!.bounds;
    final r = Rect.fromLTRB(
      b.left * scale,
      b.top * scale,
      b.right * scale,
      b.bottom * scale,
    ).inflate(3);

    // While a render is in flight the outline dims: the picture underneath is
    // one edit behind, and saying so is better than looking authoritative.
    final line = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = 1.2 + 0.8 * beat
      ..color = (stale ? T.teal.withValues(alpha: 0.45) : T.teal).withValues(
        alpha: (stale ? 0.45 : 1.0) * beat,
      );

    // The frame TURNS with the shape. A box that stays square while the thing
    // inside it rotates says the wrong thing about what is being edited — the
    // shape is not becoming a different shape, it is turning.
    final angle = g == null ? (s?.angle ?? 0) : 0.0;
    canvas.save();
    if (angle != 0) {
      canvas.translate(r.center.dx, r.center.dy);
      canvas.rotate(angle * math.pi / 180);
      canvas.translate(-r.center.dx, -r.center.dy);
    }
    canvas.drawRect(r, line);
    canvas.restore();
    final handle = Paint()..color = T.teal;
    final lit = Paint()..color = const Color(0xFFFFFFFF);
    const corners = [
      (Grip.topLeft, 0),
      (Grip.topRight, 1),
      (Grip.bottomLeft, 2),
      (Grip.bottomRight, 3),
    ];
    final points = [r.topLeft, r.topRight, r.bottomLeft, r.bottomRight];
    for (final (grip, i) in corners) {
      final on = hover == grip;
      // The one under the pointer grows and goes white. Without it there is no
      // way to know a corner has been acquired until the drag has already
      // started doing something.
      final side = on ? 11.0 : 8.0;
      canvas.drawRRect(
        RRect.fromRectAndRadius(
          Rect.fromCenter(center: points[i], width: side, height: side),
          const Radius.circular(2),
        ),
        on ? lit : handle,
      );
    }

    // Something to actually hold while turning the shape. Above the selection
    // rather than on a corner, so it cannot be confused with a resize grip.
    final anchor = Offset(r.center.dx, r.top - rotateHandleGap);
    canvas.drawLine(
      Offset(r.center.dx, r.top),
      anchor,
      Paint()
        ..color = T.teal
        ..strokeWidth = 1.5,
    );
    final onAnchor = hover == Grip.rotate;
    canvas.drawCircle(anchor, onAnchor ? 8 : 6, onAnchor ? lit : handle);
    canvas.drawCircle(anchor, 3, Paint()..color = const Color(0xFF06231F));
  }

  @override
  bool shouldRepaint(_Handles old) =>
      old.shape != shape ||
      old.scale != scale ||
      old.stale != stale ||
      old.hover != hover ||
      old.pending != pending ||
      old.group != group;
}

class _Tools extends StatelessWidget {
  const _Tools({
    required this.tool,
    required this.onPick,
    required this.editor,
    required this.recent,
    required this.onPickKind,
  });
  final _Tool tool;
  final void Function(_Tool) onPick;
  final Editor editor;
  final List<int> recent;
  final void Function(int kind) onPickKind;

  // Two modes, not four. Move, scale and rotate were separate tools for a
  // selection that already carries handles for all three — switching tools to
  // reach a handle you are pointing at is ceremony, so Edit simply does what
  // the grip under the pointer says.
  static const _defs = <(_Tool, String, String)>[
    (_Tool.select, '⌖', 'toolSelect'),
    (_Tool.edit, '✥', 'toolEdit'),
    (_Tool.ellipse, '◯', 'toolEllipse'),
    (_Tool.rect, '▢', 'toolRect'),
    (_Tool.triangle, '△', 'toolTriangle'),
    (_Tool.place, '▦', 'bank'),
  ];

  @override
  Widget build(BuildContext context) => Glass(
    radius: 11,
    child: Padding(
      padding: const EdgeInsets.all(5),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          for (final (t, glyph, key) in _defs) ...[
            if (t == _Tool.ellipse)
              Container(
                height: 1,
                margin: const EdgeInsets.symmetric(vertical: 5),
                color: T.hairline,
              ),
            Tooltip(
              message: context.s(key),
              child: Pressable(
                onTap: () => onPick(t),
                builder: (context, hover, down) => AnimatedContainer(
                  duration: Motion.fast,
                  width: 34,
                  height: 34,
                  margin: const EdgeInsets.symmetric(vertical: 1),
                  alignment: Alignment.center,
                  decoration: BoxDecoration(
                    color: t == tool
                        ? T.tealWash
                        : hoverOver(const Color(0x00000000), hover, down),
                    borderRadius: BorderRadius.circular(9),
                  ),
                  child: Text(
                    glyph,
                    style: T.text(
                      15,
                      color: t == tool
                          ? T.tealBright
                          : (hover ? T.title : T.dim),
                    ),
                  ),
                ),
              ),
            ),
          ],
          // The last silhouettes used, under a divider. A livery is usually the
          // same handful of shapes over and over, and reopening the bank for
          // each one is the part that makes a manual editor tiring.
          if (recent.isNotEmpty) ...[
            Container(
              height: 1,
              margin: const EdgeInsets.symmetric(vertical: 5),
              color: T.hairline,
            ),
            for (final kind in recent.take(10))
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 1),
                child: SizedBox(
                  width: 34,
                  height: 34,
                  child: _BankTile(
                    editor: editor,
                    kind: kind,
                    label: '',
                    picked: editor.pickedKind == kind,
                    onTap: () => onPickKind(kind),
                  ),
                ),
              ),
          ],
        ],
      ),
    ),
  );
}

class _Bar extends StatelessWidget {
  const _Bar({
    required this.editor,
    required this.studio,
    required this.onClose,
  });

  final Editor editor;
  final Studio studio;
  final VoidCallback onClose;

  @override
  Widget build(BuildContext context) => Glass(
    radius: 11,
    child: SizedBox(
      height: 44,
      child: Row(
        children: [
          const SizedBox(width: 12),
          Text(
            context.s('editing'),
            style: T.text(13, color: T.title, weight: FontWeight.w600),
          ),
          const SizedBox(width: 8),
          Text(
            '${editor.shapes.length - 1} shapes · ${editor.width}×${editor.height}',
            style: T.monoText(11, color: T.hint),
          ),
          const Spacer(),
          _Icon(
            '↶',
            on: false,
            tip: '${editor.canUndo ? '' : ''}${context.s('undo')}  Ctrl+Z',
            onTap: editor.canUndo ? editor.undo : null,
          ),
          const SizedBox(width: 6),
          _Icon(
            '↷',
            on: false,
            tip: '${context.s('redo')}  Ctrl+Shift+Z',
            onTap: editor.canRedo ? editor.redo : null,
          ),
          const SizedBox(width: 12),
          Btn(
            context.s('saveToRuns'),
            kind: BtnKind.primary,
            onTap: () {
              studio.adoptEdited(editor.toGeometry(), editor.render);
              onClose();
            },
          ),
          const SizedBox(width: 6),
          Btn(context.s('close'), onTap: onClose),
          const SizedBox(width: 12),
        ],
      ),
    ),
  );
}

class _Inspector extends StatefulWidget {
  const _Inspector({required this.editor});
  final Editor editor;

  @override
  State<_Inspector> createState() => _InspectorState();
}

/// The colour as the user would type it back in.
String _hex(List<int> c) =>
    c[0].toRadixString(16).padLeft(2, '0').toUpperCase() +
    c[1].toRadixString(16).padLeft(2, '0').toUpperCase() +
    c[2].toRadixString(16).padLeft(2, '0').toUpperCase();

class _InspectorState extends State<_Inspector> {
  /// Layers the user has folded shut. Collapsed rather than expanded, so a
  /// layer that appears later is open by default — a new layer you cannot see
  /// the contents of would be a layer you have to click twice to use.
  final _collapsed = <int>{};

  Editor get editor => widget.editor;

  void _toggleOpen(int id) => setState(() {
    if (!_collapsed.remove(id)) _collapsed.add(id);
  });

  static const _swatches = [
    Color(0xFFC06A82),
    Color(0xFF4E74B0),
    Color(0xFFE0A84B),
    Color(0xFF54CBB8),
    Color(0xFFEDEFF2),
    Color(0xFF1A1C1F),
    Color(0xFF7C5BC0),
    Color(0xFFD9694F),
    Color(0xFF6E747C),
  ];

  @override
  Widget build(BuildContext context) {
    final s = editor.current;
    return Glass(
      child: SizedBox(
        width: 276,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(12, 11, 12, 9),
              child: Row(
                children: [
                  Text(
                    s == null
                        ? context.s('nothingSelected')
                        : shapeName(s.type),
                    style: T.text(13, color: T.title, weight: FontWeight.w600),
                  ),
                  const Spacer(),
                  if (s != null)
                    Text(
                      '#${editor.selected}',
                      style: T.monoText(11, color: T.hint),
                    ),
                ],
              ),
            ),
            Container(height: 1, color: T.hairline),
            if (s != null) ...[
              Padding(
                padding: const EdgeInsets.fromLTRB(12, 11, 12, 11),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(
                      children: [
                        _Field('X', s.center.dx.toStringAsFixed(1)),
                        const SizedBox(width: 7),
                        _Field('Y', s.center.dy.toStringAsFixed(1)),
                      ],
                    ),
                    const SizedBox(height: 7),
                    Row(
                      children: [
                        _Field(
                          context.s('fieldSize'),
                          s.size.toStringAsFixed(1),
                        ),
                        const SizedBox(width: 7),
                        _Field(
                          context.s('fieldAngle'),
                          s.angle.toStringAsFixed(1),
                        ),
                      ],
                    ),
                    const SizedBox(height: 13),
                    Text(context.s('colour').toUpperCase(), style: T.label),
                    const SizedBox(height: 5),
                    Row(
                      children: [
                        Container(
                          width: 32,
                          height: 27,
                          decoration: BoxDecoration(
                            color: Color.fromARGB(
                              255,
                              s.color[0],
                              s.color[1],
                              s.color[2],
                            ),
                            borderRadius: BorderRadius.circular(7),
                            border: Border.all(color: T.border),
                          ),
                        ),
                        const SizedBox(width: 7),
                        Text(
                          '#${_hex(s.color)}',
                          style: T.monoText(12, color: T.body),
                        ),
                      ],
                    ),
                    const SizedBox(height: 7),
                    Row(
                      children: [
                        for (final c in _swatches)
                          Expanded(
                            child: GestureDetector(
                              onTap: () => editor.setColor(
                                (c.r * 255).round(),
                                (c.g * 255).round(),
                                (c.b * 255).round(),
                              ),
                              child: Container(
                                height: 17,
                                margin: const EdgeInsets.only(right: 3),
                                decoration: BoxDecoration(
                                  color: c,
                                  borderRadius: BorderRadius.circular(5),
                                  border: Border.all(color: T.border),
                                ),
                              ),
                            ),
                          ),
                      ],
                    ),
                    const SizedBox(height: 11),
                    Text(context.s('opacity').toUpperCase(), style: T.label),
                    SliderTheme(
                      data: SliderThemeData(
                        trackHeight: 4,
                        activeTrackColor: T.teal,
                        inactiveTrackColor: T.fill,
                        thumbColor: const Color(0xFFFFFFFF),
                        overlayShape: SliderComponentShape.noOverlay,
                        thumbShape: const RoundSliderThumbShape(
                          enabledThumbRadius: 7,
                        ),
                      ),
                      child: Slider(
                        value: s.color[3].toDouble().clamp(0, 255),
                        max: 255,
                        onChanged: (v) => editor.setAlpha(v.round()),
                      ),
                    ),
                    const SizedBox(height: 6),
                    GridView.count(
                      crossAxisCount: 2,
                      shrinkWrap: true,
                      physics: const NeverScrollableScrollPhysics(),
                      childAspectRatio: 4.4,
                      crossAxisSpacing: 6,
                      mainAxisSpacing: 6,
                      children: [
                        _Action(context.s('back'), editor.lowerSelected),
                        _Action(context.s('forward'), editor.raiseSelected),
                        _Action(
                          context.s('mirrorH'),
                          () => editor.mirrorSelected(horizontal: true),
                        ),
                        _Action(
                          context.s('mirrorV'),
                          () => editor.mirrorSelected(horizontal: false),
                        ),
                        _Action(
                          context.s('duplicate'),
                          editor.duplicateSelected,
                        ),
                        _Action(
                          context.s('delete'),
                          editor.deleteSelected,
                          danger: true,
                        ),
                      ],
                    ),
                  ],
                ),
              ),
              Container(height: 1, color: T.hairline),
            ],
            Padding(
              padding: const EdgeInsets.fromLTRB(12, 9, 12, 6),
              child: Row(
                children: [
                  Text(context.s('layers').toUpperCase(), style: T.label),
                  const Spacer(),
                  _Action(context.s('newLayer'), () {
                    editor.addLayer(
                      '${context.s('layers')} ${editor.layers.length + 1}',
                    );
                  }),
                  const SizedBox(width: 6),
                  Text(
                    '${editor.shapes.length}',
                    style: T.monoText(11, color: T.hint),
                  ),
                ],
              ),
            ),
            // One tree, not two lists. A layer that only COUNTS its shapes
            // is a label; a layer you can open and see what is inside is the
            // thing the word means everywhere else.
            Expanded(
              child: ListView(
                padding: const EdgeInsets.fromLTRB(8, 4, 8, 9),
                children: [
                  for (final l in editor.layers) ...[
                    _LayerRow(
                      editor: editor,
                      layer: l,
                      open: !_collapsed.contains(l.id),
                      onToggleOpen: () => _toggleOpen(l.id),
                    ),
                    if (!_collapsed.contains(l.id))
                      // Top of the stack first, so the list reads in the order
                      // the shapes are painted over each other.
                      for (final idx in editor.indicesIn(l.id).reversed)
                        _ShapeRow(editor: editor, index: idx),
                  ],
                ],
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// One shape, under the layer that owns it.
class _ShapeRow extends StatelessWidget {
  const _ShapeRow({required this.editor, required this.index});

  final Editor editor;
  final int index;

  @override
  Widget build(BuildContext context) {
    final sh = editor.shapes[index];
    final sel = index == editor.selected;
    final layer = editor.layerOf(sh.layer);
    final frozen = layer != null && (layer.locked || layer.hidden);
    return Pressable(
      onTap: frozen ? null : () => editor.select(index),
      builder: (context, hover, down) => Opacity(
        opacity: frozen ? 0.4 : 1,
        child: AnimatedContainer(
          duration: Motion.fast,
          height: 26,
          // Indented under its layer: the indent IS the statement that this
          // shape belongs to that layer and moves with it.
          margin: const EdgeInsets.only(left: 16, bottom: 1),
          padding: const EdgeInsets.symmetric(horizontal: 8),
          decoration: BoxDecoration(
            color: sel
                ? T.tealWash
                : hoverOver(const Color(0x00000000), hover, down),
            borderRadius: BorderRadius.circular(7),
          ),
          child: Row(
            children: [
              Container(
                width: 12,
                height: 12,
                decoration: BoxDecoration(
                  color: Color.fromARGB(
                    255,
                    sh.color[0],
                    sh.color[1],
                    sh.color[2],
                  ),
                  borderRadius: BorderRadius.circular(4),
                  border: Border.all(color: T.border),
                ),
              ),
              const SizedBox(width: 8),
              Expanded(
                child: Text(
                  shapeName(sh.type),
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: T.text(11.5, color: sel ? T.tealBright : T.dim),
                ),
              ),
              Text('$index', style: T.monoText(10, color: T.faint)),
            ],
          ),
        ),
      ),
    );
  }
}

class _Icon extends StatelessWidget {
  const _Icon(this.glyph, {required this.on, required this.tip, this.onTap});

  final String glyph;
  final bool on;
  final String tip;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) => Tooltip(
    message: tip,
    child: Pressable(
      onTap: onTap,
      builder: (context, hover, down) => AnimatedContainer(
        duration: Motion.fast,
        width: 22,
        height: 22,
        alignment: Alignment.center,
        decoration: BoxDecoration(
          color: hoverOver(const Color(0x00000000), hover, down),
          borderRadius: BorderRadius.circular(6),
        ),
        child: Text(
          glyph,
          style: T.text(10, color: on ? T.tealBright : T.soft),
        ),
      ),
    ),
  );
}

class _Field extends StatelessWidget {
  const _Field(this.label, this.value);
  final String label;
  final String value;

  @override
  Widget build(BuildContext context) => Expanded(
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(label, style: T.label),
        const SizedBox(height: 3),
        Container(
          height: 27,
          padding: const EdgeInsets.symmetric(horizontal: 8),
          alignment: Alignment.centerLeft,
          decoration: BoxDecoration(
            color: T.fillSoft,
            borderRadius: BorderRadius.circular(7),
            border: Border.all(color: T.border),
          ),
          child: Text(value, style: T.monoText(12, color: T.body)),
        ),
      ],
    ),
  );
}

class _Action extends StatelessWidget {
  const _Action(this.label, this.onTap, {this.danger = false});
  final String label;
  final VoidCallback onTap;
  final bool danger;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: danger
            ? hoverOver(const Color(0x26F0685F), hover, down)
            : hoverOver(T.fillSoft, hover, down),
        borderRadius: BorderRadius.circular(7),
      ),
      child: Text(
        label,
        style: T.text(
          11.5,
          color: danger ? T.danger : (hover ? T.title : T.dim),
        ),
      ),
    ),
  );
}

/// The in-game bank: every silhouette the engine can draw, as tiles.
///
/// The tiles are RENDERED by the engine, one small canvas per kind, rather than
/// drawn here from an approximation. A picker whose tiles do not match what
/// lands on the canvas is worse than no picker: you would learn to distrust it
/// and go back to placing ellipses.
class _Bank extends StatefulWidget {
  const _Bank({
    required this.editor,
    required this.onPick,
    required this.onClose,
  });

  final Editor editor;
  final void Function(int kind) onPick;
  final VoidCallback onClose;

  @override
  State<_Bank> createState() => _BankState();
}

class _BankState extends State<_Bank> {
  final _search = TextEditingController();
  String _category = '';

  Editor get ed => widget.editor;

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final q = _search.text.trim().toLowerCase();
    final categories = <String>{
      for (final k in ed.catalog) (k['category'] as String? ?? ''),
    }..remove('');
    final items = ed.catalog.where((k) {
      if (_category.isNotEmpty && k['category'] != _category) return false;
      if (q.isEmpty) return true;
      return (k['label'] as String? ?? '').toLowerCase().contains(q);
    }).toList();

    return Glass(
      radius: 13,
      child: SizedBox(
        width: 320,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(12, 10, 8, 6),
              child: Row(
                children: [
                  Text(context.s('bank').toUpperCase(), style: T.label),
                  const SizedBox(width: 8),
                  Text(
                    '${items.length}',
                    style: T.monoText(10, color: T.faint),
                  ),
                  const Spacer(),
                  _Icon(
                    '✕',
                    on: false,
                    tip: context.s('close'),
                    onTap: widget.onClose,
                  ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.fromLTRB(10, 0, 10, 8),
              child: SizedBox(
                height: 28,
                child: TextField(
                  controller: _search,
                  onChanged: (_) => setState(() {}),
                  style: T.text(12, color: T.body),
                  cursorColor: T.teal,
                  decoration: InputDecoration(
                    isDense: true,
                    filled: true,
                    fillColor: T.fillSoft,
                    hintText: context.s('search'),
                    hintStyle: T.text(12, color: T.faint),
                    contentPadding: const EdgeInsets.symmetric(
                      horizontal: 9,
                      vertical: 6,
                    ),
                    border: OutlineInputBorder(
                      borderRadius: BorderRadius.circular(8),
                      borderSide: BorderSide.none,
                    ),
                  ),
                ),
              ),
            ),
            SizedBox(
              height: 26,
              child: ListView(
                scrollDirection: Axis.horizontal,
                padding: const EdgeInsets.symmetric(horizontal: 10),
                children: [
                  _Chip(
                    label: context.s('all'),
                    on: _category.isEmpty,
                    onTap: () => setState(() => _category = ''),
                  ),
                  for (final c in categories)
                    Padding(
                      padding: const EdgeInsets.only(left: 4),
                      child: _Chip(
                        label: c,
                        on: _category == c,
                        onTap: () => setState(() => _category = c),
                      ),
                    ),
                ],
              ),
            ),
            const SizedBox(height: 6),
            Expanded(
              child: ed.catalog.isEmpty
                  ? Center(
                      child: Text(
                        context.s('rendering'),
                        style: T.text(12, color: T.soft),
                      ),
                    )
                  : GridView.builder(
                      padding: const EdgeInsets.fromLTRB(10, 0, 10, 10),
                      gridDelegate:
                          const SliverGridDelegateWithMaxCrossAxisExtent(
                            maxCrossAxisExtent: 64,
                            crossAxisSpacing: 6,
                            mainAxisSpacing: 6,
                          ),
                      itemCount: items.length,
                      itemBuilder: (context, i) {
                        final kind = (items[i]['type'] as num).toInt();
                        return _BankTile(
                          editor: ed,
                          kind: kind,
                          label: items[i]['label'] as String? ?? '',
                          picked: ed.pickedKind == kind,
                          onTap: () => widget.onPick(kind),
                          onPlace: () => ed.addShape(
                            shapeOfKind(
                              kind,
                              Rect.fromCenter(
                                center: Offset(ed.width / 2, ed.height / 2),
                                width: ed.width / 4,
                                height: ed.width / 4,
                              ),
                              ed.current?.color ?? const [235, 238, 242, 255],
                            ),
                          ),
                        );
                      },
                    ),
            ),
          ],
        ),
      ),
    );
  }
}

class _BankTile extends StatelessWidget {
  const _BankTile({
    required this.editor,
    required this.kind,
    required this.label,
    required this.picked,
    required this.onTap,
    this.onPlace,
  });

  final Editor editor;
  final int kind;
  final String label;
  final bool picked;
  final VoidCallback onTap;

  /// Drops the shape straight onto the middle of the canvas, for when the point
  /// is "give me one of those" rather than "put one exactly here".
  final VoidCallback? onPlace;

  @override
  Widget build(BuildContext context) => Tooltip(
    message: label,
    child: Pressable(
      onTap: onTap,
      onSecondaryTap: onPlace,
      builder: (context, hover, down) => AnimatedContainer(
        duration: Motion.fast,
        clipBehavior: Clip.antiAlias,
        decoration: BoxDecoration(
          color: const Color(0xFF15171A),
          borderRadius: BorderRadius.circular(8),
          border: Border.all(
            color: picked ? T.teal : (hover ? T.tealFaint : T.hairline),
            width: picked ? 1.5 : 1,
          ),
        ),
        child: FutureBuilder<ui.Image>(
          future: editor.preview(kind),
          builder: (context, snap) => snap.hasData
              ? RawImage(image: snap.data, fit: BoxFit.contain)
              : const SizedBox.shrink(),
        ),
      ),
    ),
  );
}

class _Chip extends StatelessWidget {
  const _Chip({required this.label, required this.on, required this.onTap});
  final String label;
  final bool on;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      padding: const EdgeInsets.symmetric(horizontal: 9),
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: on ? T.tealWash : hoverOver(T.fillSoft, hover, down),
        borderRadius: BorderRadius.circular(6),
      ),
      child: Text(label, style: T.text(11, color: on ? T.tealBright : T.soft)),
    ),
  );
}

/// One layer: what it is called, how many shapes it holds, and the states that
/// matter — is it the target of new work, and is it protected.
class _LayerRow extends StatelessWidget {
  const _LayerRow({
    required this.editor,
    required this.layer,
    required this.open,
    required this.onToggleOpen,
  });

  final Editor editor;
  final EditLayer layer;
  final bool open;
  final VoidCallback onToggleOpen;

  @override
  Widget build(BuildContext context) {
    final active = editor.activeLayer == layer.id;
    final grouped = editor.groupLayer == layer.id;
    return Pressable(
      onTap: () => editor.setActiveLayer(layer.id),
      builder: (context, hover, down) => AnimatedContainer(
        duration: Motion.fast,
        height: 30,
        margin: const EdgeInsets.only(bottom: 2),
        padding: const EdgeInsets.only(left: 6, right: 2),
        decoration: BoxDecoration(
          color: grouped
              ? T.tealWash
              : (active
                    ? T.tealFaint
                    : hoverOver(const Color(0x00000000), hover, down)),
          borderRadius: BorderRadius.circular(7),
        ),
        child: Row(
          children: [
            Pressable(
              onTap: onToggleOpen,
              builder: (context, h, d) => AnimatedRotation(
                duration: Motion.fast,
                turns: open ? 0.25 : 0,
                child: Text('▸', style: T.text(10, color: T.soft)),
              ),
            ),
            const SizedBox(width: 6),
            Expanded(
              child: Text(
                layer.name,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: T.text(
                  12,
                  color: layer.hidden
                      ? T.faint
                      : (active || grouped ? T.tealBright : T.dim),
                ),
              ),
            ),
            Text(
              '${editor.countIn(layer.id)}',
              style: T.monoText(10, color: T.faint),
            ),
            const SizedBox(width: 2),
            // Selecting the layer as one body — this is what "drag it all
            // together" means, and it is off unless asked for.
            _Icon(
              '⛶',
              on: grouped,
              tip: context.s('groupSelect'),
              onTap: () => editor.toggleGroup(layer.id),
            ),
            _Icon(
              layer.hidden ? '🚫' : '👁',
              on: layer.hidden,
              tip: context.s('hideLayer'),
              onTap: () => editor.setLayerHidden(layer.id, !layer.hidden),
            ),
            _Icon(
              layer.locked ? '🔒' : '🔓',
              on: layer.locked,
              tip: context.s('lockLayer'),
              onTap: () => editor.setLayerLocked(layer.id, !layer.locked),
            ),
            if (editor.current != null && !layer.locked)
              _Icon(
                '↴',
                on: false,
                tip: context.s('moveHere'),
                onTap: () => editor.assignSelectedTo(layer.id),
              ),
            // Removing a layer keeps its shapes — they go back to the first
            // layer — so this is not a way to lose work by accident. The last
            // layer cannot go, because every shape has to be in one.
            _Icon(
              '✕',
              on: false,
              tip: context.s('delete'),
              onTap: editor.layers.length > 1
                  ? () => editor.removeLayer(layer.id)
                  : null,
            ),
          ],
        ),
      ),
    );
  }
}
