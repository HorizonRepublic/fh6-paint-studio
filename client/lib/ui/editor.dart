/// The shape editor.
///
/// The canvas is the engine's own render, so what is on screen is what will be
/// injected. Selection handles are painted over it as gesture aids. A shape
/// being created is also painted over it, translucent on purpose: the engine's
/// picture arrives a round-trip later, and a draft that admits being a draft
/// beats a canvas where new shapes pop in whenever the render lands.
library;

import 'dart:async';
import 'dart:math' as math;
import 'dart:ui' as ui;

import 'package:flutter/foundation.dart' show ValueListenable;
import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../state/editor.dart';
import '../state/studio.dart';
import 'source.dart';
import 'strings.dart';
import 'tokens.dart';

class EditorView extends StatefulWidget {
  const EditorView({
    super.key,
    required this.editor,
    required this.studio,
    required this.onClose,
    required this.onSaved,
  });

  final Editor editor;
  final Studio studio;
  final VoidCallback onClose;

  /// Leaving because the work was just STORED. Separate from [onClose], which
  /// now asks 'discard your edits?' — after a successful save that question is
  /// both wrong and frightening, since the undo stack is still full and the
  /// confirm button reads 'delete'.
  final VoidCallback onSaved;

  @override
  State<EditorView> createState() => _EditorViewState();
}

// Select IS the mover, Photoshop-style: click picks, the same drag moves.
// A separate move tool meant switching modes just to nudge what you picked.
enum _Tool { select, ellipse, rect, triangle, place }

/// The tools that DRAW rather than transform.
bool _isCreate(_Tool t) =>
    t == _Tool.ellipse ||
    t == _Tool.rect ||
    t == _Tool.triangle ||
    t == _Tool.place;

/// What the pointer is over. The editor asks this before it asks which TOOL is
/// active, because a corner handle means resize no matter what is selected in
/// the tool column — that is what a handle is for.
enum Grip {
  none,
  body,
  rotate,
  topLeft,
  topRight,
  bottomLeft,
  bottomRight,
  // Mid-edge handles: resize ONE axis (non-uniform), Photoshop-style.
  left,
  right,
  top,
  bottom,
  // The skew handle below the frame (words / triangles only).
  skew,
}

bool _isEdge(Grip g) =>
    g == Grip.left || g == Grip.right || g == Grip.top || g == Grip.bottom;

/// Half the side of a corner handle, in document pixels at the current zoom.
double gripReach(double scale) => 7 / scale;

/// Which grip is at [p], for the shape whose bounds are [b]. [canSkew] adds the
/// skew handle below the frame for the kinds that support it.
Grip gripAt(Offset p, Rect? b, double scale, {bool canSkew = false}) {
  if (b == null) return Grip.none;
  final r = b.inflate(3 / scale);
  final reach = gripReach(scale);
  final anchor = Offset(r.center.dx, r.top - rotateHandleGap / scale);
  // Generous on purpose: the anchor is the smallest target on the canvas, and
  // a near miss used to fall through and SELECT whatever sat behind it.
  if ((p - anchor).distance < reach * 2.4) return Grip.rotate;
  if (canSkew) {
    final sk = Offset(r.center.dx, r.bottom + rotateHandleGap / scale);
    if ((p - sk).distance < reach * 2.4) return Grip.skew;
  }
  bool near(Offset c) => (p - c).distance < reach;
  // Corners first: at a corner both axes resize, which beats a single edge.
  if (near(r.topLeft)) return Grip.topLeft;
  if (near(r.topRight)) return Grip.topRight;
  if (near(r.bottomLeft)) return Grip.bottomLeft;
  if (near(r.bottomRight)) return Grip.bottomRight;
  if (near(r.centerLeft)) return Grip.left;
  if (near(r.centerRight)) return Grip.right;
  if (near(r.topCenter)) return Grip.top;
  if (near(r.bottomCenter)) return Grip.bottom;
  return r.contains(p) ? Grip.body : Grip.none;
}

MouseCursor _cursorFor(Grip g, _Tool tool) => switch (g) {
  Grip.topLeft || Grip.bottomRight => SystemMouseCursors.resizeUpLeftDownRight,
  Grip.topRight || Grip.bottomLeft => SystemMouseCursors.resizeUpRightDownLeft,
  Grip.left || Grip.right => SystemMouseCursors.resizeLeftRight,
  Grip.top || Grip.bottom => SystemMouseCursors.resizeUpDown,
  // No skew cursor on this platform; the column-resize arrows are the closest
  // "slide this sideways" hint.
  Grip.skew => SystemMouseCursors.resizeColumn,
  // There is no rotate cursor on this platform; grab is the closest thing that
  // still says "this is a thing you take hold of".
  Grip.rotate => SystemMouseCursors.grab,
  Grip.body => SystemMouseCursors.move,
  Grip.none => switch (tool) {
    _Tool.select => SystemMouseCursors.basic,
    _ => SystemMouseCursors.precise,
  },
};

class _EditorViewState extends State<EditorView> {
  /// Drives the marching ants around the selection. Which shape is selected
  /// was genuinely hard to tell on a busy canvas — a static teal box among
  /// three thousand shapes is just another shape. Movement is the one thing
  /// the eye finds without being told where to look.
  ///
  /// A 30 Hz timer, NOT a vsync AnimationController: a repeating controller
  /// pumps a full frame every vsync forever — with four backdrop blurs in the
  /// scene that was a hot idle CPU with nobody touching anything. The timer
  /// runs only while something is selected.
  final _pulse = ValueNotifier<double>(0);
  Timer? _antsTimer;

  /// The one-shot selection flash. Separate from [_pulse] because it answers a
  /// different question: the ants say "this is selected" for as long as it is,
  /// the flash says "this, here" once, at the moment you pick it.
  final _flash = ValueNotifier<double>(0);
  Timer? _flashTimer;
  int _flashFor = -1;

  void _syncFlash() {
    final sel = ed.selected;
    if (sel == _flashFor) return;
    _flashFor = sel;
    _flashTimer?.cancel();
    _flashTimer = null;
    if (sel < 0) {
      _flash.value = 0;
      return;
    }
    var t = 0.0;
    _flash.value = 1;
    _flashTimer = Timer.periodic(const Duration(milliseconds: 33), (tm) {
      t += 33 / 350;
      if (t >= 1) {
        tm.cancel();
        _flashTimer = null;
        _flash.value = 0;
        return;
      }
      final k = 1 - t; // easeOutCubic decay
      _flash.value = k * k * k;
    });
  }

  void _syncAnts() {
    // Not while the bank is open: it covers the selection, so the 30 Hz timer
    // would only be driving a repaint under a live-blurred panel — re-running
    // that σ22 backdrop 30×/s for nothing the whole time the bank is browsed.
    final want = (ed.current != null || ed.groupLayer != null) && !_bankOpen;
    if (want && _antsTimer == null) {
      _antsTimer = Timer.periodic(const Duration(milliseconds: 33), (_) {
        _pulse.value = (_pulse.value + 33 / 700) % 1.0;
      });
    } else if (!want && _antsTimer != null) {
      _antsTimer!.cancel();
      _antsTimer = null;
    }
  }

  _Tool _tool = _Tool.select;
  Offset? _dragFrom;
  bool _marked = false;

  /// The previous click, for hand-rolled double-click detection. A real
  /// onDoubleTap handler would make every single click wait out the
  /// double-tap window, and selection has to feel instant.
  DateTime? _lastClickAt;
  Offset? _lastClickPos;

  /// Ctrl+wheel zoom, ×1 (fit) to ×8, and where the zoomed canvas is panned.
  double _zoom = 1;
  Offset _pan = Offset.zero;

  /// Onion-skin: the source image ghosted over the work, 0 (off) to 1. Tracing
  /// by hand wants the original in view, not remembered. Only offered on a fresh
  /// generation — a run opened from the library carries no source to show.
  double _onion = 0;

  Offset _clampPan(Offset p, Size viewport, Size content) {
    double axis(double view, double c, double v) =>
        c <= view ? (view - c) / 2 : v.clamp(view - c, 0.0);
    return Offset(
      axis(viewport.width, content.width, p.dx),
      axis(viewport.height, content.height, p.dy),
    );
  }

  void _wheel(PointerScrollEvent e, Size viewport, double fit, Offset origin) {
    if (HardwareKeyboard.instance.isControlPressed) {
      final zoomed = (_zoom * math.exp(-e.scrollDelta.dy * 0.0015)).clamp(
        1.0,
        8.0,
      );
      if (zoomed == _zoom) return;
      // The document point under the cursor stays under the cursor — zooming
      // toward the corner you are looking at, not the canvas centre.
      final doc = (e.localPosition - origin) / (fit * _zoom);
      final size = Size(ed.width * fit * zoomed, ed.height * fit * zoomed);
      setState(() {
        _zoom = zoomed;
        _pan = _clampPan(
          e.localPosition - doc * (fit * zoomed),
          viewport,
          size,
        );
      });
    } else if (_zoom > 1) {
      final size = Size(ed.width * fit * _zoom, ed.height * fit * _zoom);
      final step = HardwareKeyboard.instance.isShiftPressed
          ? Offset(e.scrollDelta.dy, 0)
          : Offset(e.scrollDelta.dx, e.scrollDelta.dy);
      setState(() => _pan = _clampPan(_pan - step, viewport, size));
    }
  }

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
    _antsTimer?.cancel();
    _flashTimer?.cancel();
    // Would otherwise fire ed.commit() into a closed editor.
    _nudgeSettle?.cancel();
    _pulse.dispose();
    _flash.dispose();
    super.dispose();
  }

  /// True while a text field owns the keyboard.
  ///
  /// Flutter dispatches keys from the focused leaf UP to the root, and
  /// `DefaultTextEditingShortcuts` lives near the root inside WidgetsApp — so
  /// this CallbackShortcuts, being far closer to the focus, wins EVERY key it
  /// binds before the text field ever sees it. With Backspace bound to delete
  /// and the arrows bound to nudge, fixing a typo in the bank's search box or
  /// the inspector's hex field silently deleted or moved the selected shape
  /// instead of editing the text.
  static bool get _typing =>
      FocusManager.instance.primaryFocus?.context?.widget is EditableText;

  @override
  Widget build(BuildContext context) {
    // The editor is its own screen, so it needs its own bindings: the shell's
    // shortcuts never reach here.
    return CallbackShortcuts(
      bindings: {
        const SingleActivator(LogicalKeyboardKey.keyZ, control: true): () {
          if (_typing) return; // the field's own undo
          if (ed.canUndo) ed.undo();
        },
        const SingleActivator(
          LogicalKeyboardKey.keyZ,
          control: true,
          shift: true,
        ): () {
          if (_typing) return;
          if (ed.canRedo) ed.redo();
        },
        const SingleActivator(LogicalKeyboardKey.keyY, control: true): () {
          if (_typing) return;
          if (ed.canRedo) ed.redo();
        },
        const SingleActivator(LogicalKeyboardKey.delete): _deleteKey,
        const SingleActivator(LogicalKeyboardKey.backspace): _deleteKey,
        const SingleActivator(LogicalKeyboardKey.keyD, control: true): () {
          if (_typing) return;
          ed.duplicateSelected();
        },
        // Nudge. Precise placement is what a shape editor is for, and by hand
        // the pointer cannot reliably do one pixel. Shift is the coarse step,
        // the pairing every editor uses.
        const SingleActivator(LogicalKeyboardKey.arrowLeft): () => _nudge(-1, 0),
        const SingleActivator(LogicalKeyboardKey.arrowRight): () => _nudge(1, 0),
        const SingleActivator(LogicalKeyboardKey.arrowUp): () => _nudge(0, -1),
        const SingleActivator(LogicalKeyboardKey.arrowDown): () => _nudge(0, 1),
        const SingleActivator(LogicalKeyboardKey.arrowLeft, shift: true): () =>
            _nudge(-10, 0),
        const SingleActivator(LogicalKeyboardKey.arrowRight, shift: true): () =>
            _nudge(10, 0),
        const SingleActivator(LogicalKeyboardKey.arrowUp, shift: true): () =>
            _nudge(0, -10),
        const SingleActivator(LogicalKeyboardKey.arrowDown, shift: true): () =>
            _nudge(0, 10),
        // Escape backs out of the innermost thing: the bank if it is open,
        // otherwise the selection. It reached nothing here before — the shell's
        // handler does not run while the editor owns the screen.
        const SingleActivator(LogicalKeyboardKey.escape): () {
          if (_bankOpen) {
            setState(() {
              _bankOpen = false;
              _syncAnts();
            });
          } else if (ed.selected >= 0 || ed.groupLayer != null) {
            ed.groupLayer = null;
            ed.select(-1);
          }
        },
      },
      child: Focus(autofocus: true, child: _body(context)),
    );
  }

  /// Coalesces a burst of arrow keys into ONE undo step, the same discipline a
  /// drag follows: a held arrow key is one act of moving, not forty.
  Timer? _nudgeSettle;

  void _deleteKey() {
    if (_typing) return; // Backspace/Delete belong to the caret in a text field
    ed.deleteSelected();
  }

  void _nudge(double dx, double dy) {
    if (_typing) return; // the arrows are moving a caret, not a shape
    final layer = ed.groupLayer;
    // `selected > 0`, not `>= 0`: index 0 is the background rect, and undoing
    // the first placed shape can leave the selection resting on it — arrow keys
    // then slid the canvas backing and left a transparent band down one edge.
    if (layer == null && ed.selected <= 0) return;
    if (_nudgeSettle == null) ed.mark();
    _nudgeSettle?.cancel();
    _nudgeSettle = Timer(const Duration(milliseconds: 400), () {
      _nudgeSettle = null;
      ed.commit();
    });

    if (layer != null) {
      ed.translateLayer(layer, dx, dy);
    } else {
      ed.current?.translate(dx, dy);
      for (final i in ed.extra) {
        if (i > 0 && i < ed.shapes.length && i != ed.selected) {
          ed.shapes[i].translate(dx, dy);
        }
      }
    }
    ed.live(); // queues its own guarded draft render
  }

  Widget _body(BuildContext context) {
    return AnimatedBuilder(
      animation: ed,
      builder: (context, _) {
        _syncAnts();
        _syncFlash();
        return _stack(context);
      },
    );
  }

  Widget _stack(BuildContext context) {
    return Stack(
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
            flash: _flash,
            zoom: _zoom,
            pan: _pan,
            group: ed.groupLayer == null
                ? null
                : ed.layerBounds(ed.groupLayer!),
            // Mid-transform the shape itself rides the pointer as a local
            // overlay: the engine's picture is a round-trip behind, and a
            // shape that trails the mouse feels broken even when it is
            // merely honest.
            preview: _newFrom != null && _newTo != null
                ? _shapeIn(Rect.fromPoints(_newFrom!, _newTo!), _tool, ed)
                : (ed.interBelow == null && _marked && _held != Grip.none
                      ? ed.current
                      : ed.settling),
            source: ed.reference ?? widget.studio.sourceImage,
            onion: _onion,
            onHover: _onHover,
            onScale: (v) => _scale = v,
            onWheel: _wheel,
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
              if (_doubleTapped('kind$k')) _placeKindCentered(k);
            }),
            tool: _tool,
            onPick: (t) => setState(() {
              // The bank button opens the panel; it arms nothing. Shapes
              // are added by double-clicking a tile — centre, standard
              // size, selected — never by drawing on the canvas.
              if (t == _Tool.place) {
                _bankOpen = !_bankOpen;
                if (_bankOpen) ed.loadCatalog();
                _syncAnts(); // pause/resume the ants under the bank overlay
                return;
              }
              _tool = t;
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
              onClose: () => setState(() {
                _bankOpen = false;
                _syncAnts(); // resume the ants now the selection is visible again
              }),
              onPick: (k) => setState(() {
                ed.pickedKind = k;
                if (_doubleTapped('kind$k')) _placeKindCentered(k);
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
          // Same right edge as the canvas and the onion pill below it. It was
          // 312 against their 300, so three panels stacked down the screen ended
          // on two different vertical lines.
          right: 300,
          child: _Bar(
            editor: ed,
            studio: widget.studio,
            onClose: widget.onClose,
            onSaved: widget.onSaved,
          ),
        ),
        // The onion-skin control, centred at the foot of the canvas where no
        // other panel sits. Always present in the editor: with no reference it
        // offers to load one, which is how a livery is traced from scratch.
        Positioned(
          left: 92,
          right: 300,
          bottom: 34,
          child: Align(
            alignment: Alignment.bottomCenter,
            child: _Onion(
              value: _onion,
              hasReference: (ed.reference ?? widget.studio.sourceImage) != null,
              onChanged: (v) => setState(() => _onion = v),
              onPick: _pickReference,
              // Only a reference loaded HERE can be cleared; a generation's own
              // source belongs to the run, not to this edit.
              onClear: ed.reference != null ? _clearReference : null,
            ),
          ),
        ),
      ],
    );
  }

  Future<void> _pickReference() async {
    final path = await pickImage();
    if (path == null) return;
    await ed.setReference(path);
    // The picker and the decode are both awaited, and the editor can be gone by the time they
    // answer — setState on a disposed State throws.
    if (!mounted) return;
    // Loading a reference is a request to see it: turn the ghost on if it was
    // off, and leave a strength the user already set alone.
    setState(() => _onion = _onion == 0 ? 0.6 : _onion);
  }

  void _clearReference() {
    ed.clearReference();
    setState(() {});
  }

  Rect? get _frame {
    final g = ed.groupLayer;
    return g != null ? ed.layerBounds(g) : ed.current?.localBounds;
  }

  /// The selected shape can take a skew (word / triangle) and is a single shape,
  /// not a group — which is when the skew handle is offered.
  bool get _canSkew => ed.groupLayer == null && (ed.current?.canSkew ?? false);

  /// The pointer in the selection frame's own space. The handles are drawn on
  /// the UNROTATED box turned with the shape, so hits are tested by turning
  /// the point back rather than chasing the corners forward.
  Offset _inFrame(Offset docPoint) {
    final s = ed.current;
    if (s == null || ed.groupLayer != null) return docPoint;
    final c = s.center;
    var d = docPoint - c;
    // Invert the frame's draw transform (rotate then skew about the centre) in
    // reverse: un-rotate first, then un-shear, so a hit lands on the same base
    // rect the grips are measured against.
    if (s.angle != 0) {
      final t = -s.angle * math.pi / 180;
      d = Offset(
        d.dx * math.cos(t) - d.dy * math.sin(t),
        d.dx * math.sin(t) + d.dy * math.cos(t),
      );
    }
    final sk = s.frameSkew;
    if (sk != 0) d = Offset(d.dx - sk * d.dy, d.dy);
    return c + d;
  }

  /// The pointer in the shape's own frame, RELATIVE to its centre (not re-added
  /// to it). Used to read a local-axis extent while resizing or skewing.
  Offset _toLocal(Offset docPoint, EditShape s) {
    final c = s.center;
    final t = -s.angle * math.pi / 180;
    final d = docPoint - c;
    return Offset(
      d.dx * math.cos(t) - d.dy * math.sin(t),
      d.dx * math.sin(t) + d.dy * math.cos(t),
    );
  }

  void _onHover(Offset docPoint) {
    final g = gripAt(_inFrame(docPoint), _frame, _scale, canSkew: _canSkew);
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
    // The canvas TRANSFORMS, the panel SELECTS. A click here never changes
    // the selection — and since the canvas does nothing else, a drag that
    // misses the frame still MOVES the selection instead of dying: losing
    // the drag to a near-miss on a small shape felt broken.
    _held = gripAt(_inFrame(docPoint), _frame, _scale, canSkew: _canSkew);
    if (_held == Grip.none && ed.current != null) _held = Grip.body;
    // A move-group moves and does nothing else: resize/rotate of N shapes at
    // once is a different feature wearing the same handles.
    if (ed.extra.isNotEmpty && _held != Grip.none) _held = Grip.body;
    // Non-uniform resize and skew are AFFINE, which the fast sprite composite (a
    // similarity: translate-rotate-uniform-scale) cannot represent. Those drags
    // take the draft-render path — the engine draws the real shape each frame —
    // so the composite is only set up for move / rotate / uniform-scale.
    final affine = _isEdge(_held) || _held == Grip.skew;
    if (_held != Grip.none && ed.groupLayer == null && !affine) {
      // Split the stack around the shape now, so by the time the hand is
      // really moving the drag is composited locally at frame rate.
      unawaited(ed.beginInteraction());
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

    // What the pointer took hold of decides the transform: a grip resizes or
    // turns, the body moves. One tool does all of it, like everywhere else.
    if (_held == Grip.none) return;

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
          for (final i in ed.extra) {
            if (i > 0 && i < ed.shapes.length && i != ed.selected) {
              ed.shapes[i].translate(d.dx, d.dy);
            }
          }
        // Mid-edge: resize ONE local axis about the centre (async sides).
        case Grip.left || Grip.right:
          s!.resizeLocal(halfW: _toLocal(docPoint, s).dx.abs());
        case Grip.top || Grip.bottom:
          s!.resizeLocal(halfH: _toLocal(docPoint, s).dy.abs());
        // Skew: the horizontal slide of the pointer, over the shape's height, is
        // the shear increment (word skew field / triangle vertices).
        case Grip.skew:
          final hh = s!.localBounds.height / 2;
          if (hh > 1) {
            s.skewBy((_toLocal(docPoint, s).dx - _toLocal(from, s).dx) / hh);
          }
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
      if (r.width > 3 && r.height > 3) {
        _place(r);
      } else {
        // A click. One click arms, a second within the window places — like
        // the game's own editor. A real onDoubleTap would tax every click.
        final now = DateTime.now();
        final isDouble =
            _lastClickAt != null &&
            _lastClickPos != null &&
            now.difference(_lastClickAt!) < const Duration(milliseconds: 400) &&
            (from - _lastClickPos!).distance * _scale < 8;
        if (isDouble) {
          final side = ed.width / 10;
          _place(Rect.fromCenter(center: from, width: side, height: side));
          _lastClickAt = null;
        } else {
          _lastClickAt = now;
          _lastClickPos = from;
        }
      }
      setState(() {
        _newFrom = null;
        _newTo = null;
      });
      return;
    }
    _dragFrom = null;
    _held = Grip.none;
    if (_marked) {
      ed.commit();
    } else {
      ed.endInteraction(); // a click, not a drag — drop the unused composite
    }
    _marked = false;
  }

  /// One add per gesture, the way the game does it: the shape lands selected
  /// and flashing, and the tool falls back to select so the next click edits
  /// instead of stamping another copy.
  void _place(Rect r) {
    ed.addShape(_shapeIn(r, _tool, ed));
    // Straight back to the pointer: the first thing anyone does with a fresh
    // shape is drag it where it belongs, and select IS the mover now.
    _tool = _Tool.select;
  }

  DateTime? _toolTapAt;
  Object? _toolTapWhat;

  /// A second tap on the same tool or bank tile within the window.
  bool _doubleTapped(Object what) {
    final now = DateTime.now();
    final isDouble =
        _toolTapWhat == what &&
        _toolTapAt != null &&
        now.difference(_toolTapAt!) < const Duration(milliseconds: 400);
    _toolTapWhat = what;
    _toolTapAt = now;
    return isDouble;
  }

  /// Drops one bank shape at the canvas centre at the standard size, selected
  /// and flashing. THE way shapes are added: double-click a tile, done.
  void _placeKindCentered(int k) {
    final side = ed.width / 4;
    final c = ed.current?.color;
    ed.addShape(
      shapeOfKind(
        k,
        Rect.fromCenter(
          center: Offset(ed.width / 2, ed.height / 2),
          width: side,
          height: side,
        ),
        [c?[0] ?? 235, c?[1] ?? 238, c?[2] ?? 242, 255],
      ),
    );
    _tool = _Tool.select;
    // The bank's job is done, and open it COVERS a third of the canvas — a
    // drag starting over it never reaches the shape underneath.
    _bankOpen = false;
  }
}

/// Turns the dragged rectangle into a shape.
///
/// The colour comes from whatever was selected last, falling back to a mid
/// grey. Sampling the picture under the drag would be nicer and is not possible
/// synchronously — the render lives on the GPU — so the honest default is one
/// the user can see and immediately change in the inspector.
EditShape _shapeIn(Rect r, _Tool tool, Editor ed) {
  final base = ed.current?.color;
  // Full alpha always: inheriting a translucent colour made a fresh shape
  // arrive invisible. The RGB still follows the selection; white otherwise —
  // it reads on the dark desk and on most artwork.
  final c = [base?[0] ?? 235, base?[1] ?? 238, base?[2] ?? 242, 255];
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
    required this.flash,
    required this.zoom,
    required this.pan,
    required this.group,
    required this.preview,
    required this.source,
    required this.onion,
    required this.onHover,
    required this.onWheel,
    required this.onScale,
    required this.onPointerDown,
    required this.onPointerMove,
    required this.onPointerUp,
  });

  final Editor editor;
  final _Tool tool;
  final Grip hover;
  final ValueListenable<double> pulse;
  final ValueListenable<double> flash;

  /// The box around a whole selected LAYER, when one is picked as a group.
  final Rect? group;

  /// The shape being dragged out or just added, drawn locally until the
  /// engine's picture includes it.
  final EditShape? preview;

  /// The original picture, ghosted over the work at [onion] when [onion] > 0.
  /// Null when there is nothing to trace against.
  final ui.Image? source;
  final double onion;

  final double zoom;
  final Offset pan;
  final void Function(
    PointerScrollEvent,
    Size viewport,
    double fit,
    Offset origin,
  )
  onWheel;
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
    return LayoutBuilder(
      builder: (context, box) {
        final viewport = box.biggest;
        // One scale for both axes; ×1 zoom is exactly the old fit-to-view.
        final fit = math.min(
          viewport.width / editor.width,
          viewport.height / editor.height,
        );
        final scale = fit * zoom;
        final content = Size(editor.width * scale, editor.height * scale);
        double axis(double view, double c, double v) =>
            c <= view ? (view - c) / 2 : v.clamp(view - c, 0.0);
        final origin = Offset(
          axis(viewport.width, content.width, pan.dx),
          axis(viewport.height, content.height, pan.dy),
        );
        // Local is VIEWPORT space, not the picture's: the gestures cover the
        // whole area around the canvas, so a shape dragged off the picture
        // can still be grabbed and brought back.
        Offset toDoc(Offset local) => (local - origin) / scale;
        onScale(scale);

        return Listener(
          onPointerSignal: (e) {
            if (e is PointerScrollEvent) onWheel(e, viewport, fit, origin);
          },
          child: GestureDetector(
            behavior: HitTestBehavior.opaque,
            onTapDown: (d) => onPointerDown(toDoc(d.localPosition)),
            // Taps never reach onPanEnd, and a create-tool click has to
            // finish its gesture somewhere.
            onTapUp: (_) => onPointerUp(),
            onPanStart: (d) => onPointerDown(toDoc(d.localPosition)),
            onPanUpdate: (d) => onPointerMove(toDoc(d.localPosition)),
            onPanEnd: (_) => onPointerUp(),
            child: MouseRegion(
              cursor: _cursorFor(hover, tool),
              onHover: (e) => onHover(toDoc(e.localPosition)),
              onExit: (_) => onHover(const Offset(-1e6, -1e6)),
              child: ClipRect(
                child: Stack(
                  children: [
                    Positioned(
                      left: origin.dx,
                      top: origin.dy,
                      width: content.width,
                      height: content.height,
                      child: Stack(
                        fit: StackFit.expand,
                        children: [
                          // Transparency read-out: a checkerboard behind the
                          // render, so a transparent vinyl looks transparent
                          // instead of borrowing the desk. Opaque results cover
                          // it completely.
                          const RepaintBoundary(child: CustomPaint(painter: _Checker())),
                          // Its own compositor layer: without the boundary a
                          // gesture-rate repaint climbed to the ROUTE layer
                          // and re-recorded the whole window — desk dither,
                          // four backdrop blurs — every frame of every drag.
                          RepaintBoundary(
                            child: editor.interBelow == null
                                ? RawImage(
                                    image: img,
                                    fit: BoxFit.fill,
                                    filterQuality: FilterQuality.medium,
                                  )
                                // The drag-time composite: three cached
                                // layers and one canvas transform, no engine
                                // in the loop.
                                : CustomPaint(
                                    painter: _LiveStack(editor, scale),
                                  ),
                          ),
                          // The reference ghost, over the work and under the
                          // handles: it is there to trace against, so it must
                          // sit on top of the render but never eat a gesture.
                          if (source != null && onion > 0)
                            RepaintBoundary(
                              child: IgnorePointer(
                                // Alpha via a modulate colour, not Opacity: an
                                // Opacity widget pushes a saveLayer the size of
                                // the whole zoomed canvas (thousands of px² at
                                // ×8) and rebuilds one per onion-slider tick;
                                // modulating the image's own alpha needs no layer.
                                child: RawImage(
                                  image: source,
                                  fit: BoxFit.fill,
                                  filterQuality: FilterQuality.medium,
                                  color: Color.fromRGBO(
                                    255,
                                    255,
                                    255,
                                    onion.clamp(0.0, 1.0),
                                  ),
                                  colorBlendMode: BlendMode.modulate,
                                ),
                              ),
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
                                preview: preview,
                                group: group,
                                extras: [
                                  for (final i in editor.extra)
                                    if (i < editor.shapes.length)
                                      editor.shapes[i],
                                ],
                                pulse: pulse,
                                flash: flash,
                                tick: editor.canvasTick,
                              ),
                            ),
                          ),
                        ],
                      ),
                    ),
                  ],
                ),
              ),
            ),
          ),
        );
      },
    );
  }
}

/// The onion-skin control: a ghost of the reference over the work, a slider to
/// fade it, and the way to load or swap the picture. A pill at the foot of the
/// canvas rather than a checkbox in a panel, because it is a thing you reach for
/// mid-edit and turn back down. With no reference yet, the whole pill is the
/// invitation to load one — the entry to tracing a livery from scratch.
class _Onion extends StatelessWidget {
  const _Onion({
    required this.value,
    required this.hasReference,
    required this.onChanged,
    required this.onPick,
    required this.onClear,
  });

  final double value;
  final bool hasReference;
  final void Function(double) onChanged;
  final VoidCallback onPick;

  /// Drops the reference. Null when the picture is a generation's own source,
  /// which belongs to the run and is not this edit's to remove.
  final VoidCallback? onClear;

  @override
  Widget build(BuildContext context) {
    if (!hasReference) {
      return Glass(
        radius: 11,
        live: false,
        child: Pressable(
          onTap: onPick,
          builder: (context, hover, down) => Padding(
            padding: const EdgeInsets.symmetric(horizontal: 11, vertical: 7),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                // Was U+FF0B, the FULLWIDTH plus — a whole em of advance for a
                // glyph the size of the text beside it, so the label never sat
                // where the padding said it would.
                Icon2(
                  Ico.plus,
                  color: hover ? T.tealBright : T.dim,
                  size: 11,
                ),
                const SizedBox(width: 7),
                Text(
                  context.s('reference'),
                  style: T.text(11.5, color: hover ? T.title : T.dim),
                ),
              ],
            ),
          ),
        ),
      );
    }
    final on = value > 0;
    return Glass(
      radius: 11,
      live: false,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 4),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Tooltip(
              message: context.s('reference'),
              // A tap turns it on at a readable strength and off again — the
              // slider is for tuning, not for finding the switch.
              child: Pressable(
                onTap: () => onChanged(on ? 0 : 0.55),
                builder: (context, hover, down) => AnimatedContainer(
                  duration: Motion.fast,
                  curve: Motion.curve,
                  width: 30,
                  height: 26,
                  alignment: Alignment.center,
                  decoration: BoxDecoration(
                    color: on
                        ? T.tealWash
                        : hoverOver(const Color(0x00000000), hover, down),
                    borderRadius: BorderRadius.circular(8),
                  ),
                  child: Text(
                    '◉',
                    style: T.text(
                      14,
                      color: on ? T.tealBright : (hover ? T.title : T.dim),
                    ),
                  ),
                ),
              ),
            ),
            if (on) ...[
              const SizedBox(width: 2),
              SizedBox(
                width: 116,
                child: SliderTheme(
                  data: SliderThemeData(
                    trackHeight: 4,
                    activeTrackColor: T.teal,
                    inactiveTrackColor: T.fill,
                    thumbColor: const Color(0xFFFFFFFF),
                    overlayShape: SliderComponentShape.noOverlay,
                    thumbShape: const RoundSliderThumbShape(
                      enabledThumbRadius: 6,
                    ),
                  ),
                  child: Slider(
                    value: value.clamp(0.0, 1.0),
                    onChanged: onChanged,
                  ),
                ),
              ),
              const SizedBox(width: 5),
              SizedBox(
                width: 30,
                child: Text(
                  '${(value * 100).round()}%',
                  style: T.monoText(11, color: T.hint),
                ),
              ),
            ] else
              const SizedBox(width: 4),
            _Icon('⟳', on: false, tip: context.s('reference'), onTap: onPick),
            if (onClear != null) ...[
              const SizedBox(width: 2),
              _Icon('✕', on: false, tip: context.s('reference'), onTap: onClear),
            ],
          ],
        ),
      ),
    );
  }
}

/// The transparency checkerboard drawn behind the canvas, so a transparent
/// vinyl reads as transparent rather than as the desk showing through.
class _Checker extends CustomPainter {
  const _Checker();

  static const _cell = 9.0;
  static ui.Image? _tile;

  /// One 2×2-cell tile, built once and tiled with a repeating shader. The
  /// painter is sized to the WHOLE zoomed document (the Positioned parent), so
  /// the old per-cell loop was ~478k drawRects re-run on every zoom tick at ×8;
  /// a single tiled drawRect is O(1) in the document size.
  static ui.Image _buildTile() {
    final rec = ui.PictureRecorder();
    final c = ui.Canvas(rec);
    c.drawRect(
      const Rect.fromLTWH(0, 0, _cell * 2, _cell * 2),
      Paint()..color = const Color(0xFF2B2E33),
    );
    final light = Paint()..color = const Color(0xFF3A3D42);
    c.drawRect(const Rect.fromLTWH(0, 0, _cell, _cell), light);
    c.drawRect(const Rect.fromLTWH(_cell, _cell, _cell, _cell), light);
    return rec.endRecording().toImageSync((_cell * 2).toInt(), (_cell * 2).toInt());
  }

  @override
  void paint(Canvas canvas, Size size) {
    final tile = _tile ??= _buildTile();
    canvas.drawRect(
      Offset.zero & size,
      Paint()
        // filterQuality none: the tile is an exact pixel grid, and at 125%/150%
        // scaling the default bilinear resample turned a crisp checker into a
        // soft moire. Nearest keeps the cells as hard as the old per-cell rects.
        ..filterQuality = FilterQuality.none
        ..shader = ui.ImageShader(
          tile,
          TileMode.repeated,
          TileMode.repeated,
          Matrix4.identity().storage,
          filterQuality: FilterQuality.none,
        ),
    );
  }

  @override
  bool shouldRepaint(_Checker old) => false;
}

/// The gesture-time canvas: everything below the held shape, its own sprite
/// under the live transform, everything above. sRGB and approximate on
/// purpose — the commit render is the truth; this one only has to keep up
/// with the hand.
class _LiveStack extends CustomPainter {
  _LiveStack(this.editor, this.scale)
    : super(repaint: Listenable.merge([editor, editor.canvasTick]));
  final Editor editor;
  final double scale;

  @override
  void paint(Canvas canvas, Size size) {
    final below = editor.interBelow;
    final sprite = editor.interSprite;
    // Null for a move-group: its sprite rides on top of everything, z-order
    // approximated until the commit render.
    final above = editor.interAbove;
    final start = editor.interStart;
    final s = editor.current;
    if (below == null || sprite == null || start == null || s == null) {
      return;
    }
    final dst = Offset.zero & size;
    final paint = Paint()..filterQuality = FilterQuality.medium;
    Rect src(ui.Image i) =>
        Rect.fromLTWH(0, 0, i.width.toDouble(), i.height.toDouble());

    canvas.drawImageRect(below, src(below), dst, paint);

    // Where the shape was when its sprite rendered, versus where the hand has
    // it now: the difference is one translate-rotate-scale about the centre.
    final was = EditShape(s.type, List.of(start), s.color);
    final c0 = was.center;
    final c1 = s.center;
    double rotDeg, r;
    if (s.isBoxLike || s.isWordLike) {
      // Parameterised by a stored angle and extents.
      rotDeg = s.angle - was.angle;
      r = was.size > 0.01 ? s.size / was.size : 1.0;
    } else {
      // Triangle/line: no stored angle. Every editor transform (move/scale/turn) moves the vertices
      // as a similarity about the centre, so a single reference vertex recovers both the turn and the
      // scale — which is what lets a triangle preview-rotate instead of snapping only on release.
      final p0 = Offset(was.data[0], was.data[1]) - c0;
      final p1 = Offset(s.data[0], s.data[1]) - c1;
      r = p0.distance > 0.01 ? p1.distance / p0.distance : 1.0;
      rotDeg = (math.atan2(p1.dy, p1.dx) - math.atan2(p0.dy, p0.dx)) * 180 / math.pi;
    }
    canvas.save();
    canvas.translate(c1.dx * scale, c1.dy * scale);
    canvas.rotate(rotDeg * math.pi / 180);
    canvas.scale(r);
    canvas.translate(-c0.dx * scale, -c0.dy * scale);
    canvas.drawImageRect(sprite, src(sprite), dst, paint);
    canvas.restore();

    if (above != null) canvas.drawImageRect(above, src(above), dst, paint);
  }

  @override
  bool shouldRepaint(_LiveStack old) => true;
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
    required this.preview,
    required this.group,
    required this.extras,
    required this.pulse,
    required this.tick,
    required this.flash,
  }) : super(repaint: Listenable.merge([pulse, tick, flash]));

  /// A one-shot envelope, 1 → 0 over ~350ms, restarted whenever the selection
  /// changes. The shape's own flash used to ride the marching-ants pulse: an
  /// endless 1s sine flooding the selected shape with up to 45% white, forever,
  /// in the one tool whose entire job is judging colour against the artwork —
  /// and phase-free, so clicking a shape could produce no visible flash at all.
  /// Now it fires AT the moment of selection, says "these pixels", and stops;
  /// the two-tone ants remain the steady-state indicator.
  final ValueListenable<double> flash;

  /// The gesture-rate repaint driver; the frame reads the shape's LIVE data
  /// at paint time, so a repaint alone tracks the hand.
  final Listenable tick;

  final EditShape? shape;
  final double scale;
  final bool stale;
  final Grip hover;
  final EditShape? preview;
  final Rect? group;

  /// The move-group companions: framed like the primary, but without grips —
  /// the group only translates.
  final List<EditShape> extras;

  final ValueListenable<double> pulse;

  @override
  void paint(Canvas canvas, Size size) {
    final pv = preview;
    if (pv != null) _drawPreview(canvas, pv);
    final s = shape;
    final g = group;
    if (s == null && g == null) return;

    // The shape's OWN box, not the rotated extent: with the canvas turned
    // below, this frame spins rigidly with the shape instead of breathing.
    final b = g ?? s!.localBounds;
    final r = Rect.fromLTRB(
      b.left * scale,
      b.top * scale,
      b.right * scale,
      b.bottom * scale,
    ).inflate(3);

    // While a render is in flight the outline dims: the picture underneath is
    // one edit behind, and saying so is better than looking authoritative.
    final dim = stale ? 0.45 : 1.0;
    final under = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = 1.6
      ..color = const Color(0xFF06231F).withValues(alpha: 0.9 * dim);
    final ants = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = 1.6
      ..color = T.tealBright.withValues(alpha: dim);

    // The selected shape itself flashes, like the game's own editor. The whole
    // point of the fit is that shapes dissolve into the picture, so the frame
    // alone says "somewhere in this box" while the flash says "these pixels".
    // Skipped entirely once the envelope has decayed — the steady state is now
    // no flood at all, only the ants.
    if (s != null && g == null && flash.value > 0.001) {
      final blink = flash.value * dim;
      final path = _pathOf(s);
      if (path != null) {
        canvas.drawPath(
          path,
          Paint()
            ..color = const Color(0xFFFFFFFF).withValues(alpha: 0.45 * blink),
        );
        canvas.drawPath(
          path,
          Paint()
            ..style = PaintingStyle.stroke
            ..strokeWidth = 1.5
            ..color = T.tealBright.withValues(alpha: blink),
        );
      } else {
        // A dictionary word — its true footprint lives in the engine's masks,
        // so the box is what there is to flash.
        final bb = s.bounds;
        canvas.drawRect(
          Rect.fromLTRB(
            bb.left * scale,
            bb.top * scale,
            bb.right * scale,
            bb.bottom * scale,
          ),
          Paint()
            ..color = const Color(0xFFFFFFFF).withValues(alpha: 0.25 * blink),
        );
      }
    }

    // The frame TURNS with the shape. A box that stays square while the thing
    // inside it rotates says the wrong thing about what is being edited — the
    // shape is not becoming a different shape, it is turning.
    final angle = g == null ? (s?.angle ?? 0) : 0.0;
    final skew = g == null ? (s?.frameSkew ?? 0) : 0.0;
    // The frame becomes a parallelogram, but the handle GLYPHS must not: only
    // their POSITIONS shear. shear() slides a point sideways by skew·(distance
    // below the centre); the rotate-only canvas then turns everything rigidly,
    // so a square handle stays a square sitting on the parallelogram's corner
    // instead of collapsing into a rhombus.
    Offset shear(Offset p) =>
        skew == 0 ? p : Offset(p.dx + skew * (p.dy - r.center.dy), p.dy);
    canvas.save();
    if (angle != 0) {
      canvas.translate(r.center.dx, r.center.dy);
      canvas.rotate(angle * math.pi / 180);
      canvas.translate(-r.center.dx, -r.center.dy);
    }
    // Two-tone marching ants: bright dashes crawling over a dark underlay.
    // A single-colour outline — pulsing or not — vanishes into artwork that
    // happens to match it; opposite tones cannot both match what is beneath.
    final tl = shear(r.topLeft), tr = shear(r.topRight);
    final br = shear(r.bottomRight), bl = shear(r.bottomLeft);
    final outline = Path()
      ..moveTo(tl.dx, tl.dy)
      ..lineTo(tr.dx, tr.dy)
      ..lineTo(br.dx, br.dy)
      ..lineTo(bl.dx, bl.dy)
      ..close();
    canvas.drawPath(outline, under);
    canvas.drawPath(_dashed(outline, pulse.value), ants);
    // A move-group gets frames but NO grips: it only translates, and handles
    // that resize one shape of five would be a lie.
    if (extras.isNotEmpty) {
      canvas.restore();
      for (final e in extras) {
        final b2 = e.localBounds;
        final r2 = Rect.fromLTRB(
          b2.left * scale,
          b2.top * scale,
          b2.right * scale,
          b2.bottom * scale,
        ).inflate(3);
        canvas.save();
        if (e.angle != 0) {
          canvas.translate(r2.center.dx, r2.center.dy);
          canvas.rotate(e.angle * math.pi / 180);
          canvas.translate(-r2.center.dx, -r2.center.dy);
        }
        canvas.drawRect(r2, under);
        canvas.drawPath(_dashed(Path()..addRect(r2), pulse.value), ants);
        canvas.restore();
      }
      return;
    }
    // The grips and the rotate anchor stay INSIDE the turned frame: they are
    // hit-tested in the frame's own space, so drawing them outside it showed
    // handles in one place and grabbed them in another.
    final handle = Paint()..color = T.teal;
    final lit = Paint()..color = const Color(0xFFFFFFFF);
    const corners = [
      (Grip.topLeft, 0),
      (Grip.topRight, 1),
      (Grip.bottomLeft, 2),
      (Grip.bottomRight, 3),
    ];
    final points = [
      shear(r.topLeft),
      shear(r.topRight),
      shear(r.bottomLeft),
      shear(r.bottomRight),
    ];
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

    // Mid-edge handles: async single-axis resize. Single shape only — a group
    // scales uniformly, so it keeps corners but shows no edges.
    if (g == null) {
      const edges = [
        (Grip.left, 0),
        (Grip.right, 1),
        (Grip.top, 2),
        (Grip.bottom, 3),
      ];
      final epts = [
        shear(r.centerLeft),
        shear(r.centerRight),
        shear(r.topCenter),
        shear(r.bottomCenter),
      ];
      for (final (grip, i) in edges) {
        final on = hover == grip;
        final side = on ? 10.0 : 7.0;
        canvas.drawRRect(
          RRect.fromRectAndRadius(
            Rect.fromCenter(center: epts[i], width: side, height: side),
            const Radius.circular(2),
          ),
          on ? lit : handle,
        );
      }
      // Skew handle below the frame, for the kinds the game can shear (word /
      // triangle). A diamond, so it reads apart from the round rotate knob.
      if (s != null && s.canSkew) {
        final sk = shear(Offset(r.center.dx, r.bottom + rotateHandleGap));
        canvas.drawLine(
          shear(Offset(r.center.dx, r.bottom)),
          sk,
          Paint()
            ..color = T.teal
            ..strokeWidth = 1.5,
        );
        final onSkew = hover == Grip.skew;
        final half = onSkew ? 7.0 : 5.5;
        canvas.save();
        canvas.translate(sk.dx, sk.dy);
        canvas.rotate(math.pi / 4);
        canvas.drawRect(
          Rect.fromCenter(center: Offset.zero, width: half * 2, height: half * 2),
          onSkew ? lit : handle,
        );
        canvas.restore();
      }
    }

    // Something to actually hold while turning the shape. Above the selection
    // rather than on a corner, so it cannot be confused with a resize grip.
    final anchor = shear(Offset(r.center.dx, r.top - rotateHandleGap));
    canvas.drawLine(
      shear(Offset(r.center.dx, r.top)),
      anchor,
      Paint()
        ..color = T.teal
        ..strokeWidth = 1.5,
    );
    final onAnchor = hover == Grip.rotate;
    canvas.drawCircle(anchor, onAnchor ? 8 : 6, onAnchor ? lit : handle);
    canvas.drawCircle(anchor, 3, Paint()..color = const Color(0xFF06231F));
    canvas.restore();
  }

  /// [phase] runs 0..1 and slides the dashes one period per cycle.
  static Path _dashed(Path src, double phase) {
    const on = 7.0, off = 6.0, period = on + off;
    final out = Path();
    for (final m in src.computeMetrics()) {
      var start = phase * period - period;
      while (start < m.length) {
        final a = math.max(0.0, start);
        final b = math.min(m.length, start + on);
        if (b > a) out.addPath(m.extractPath(a, b), Offset.zero);
        start += period;
      }
    }
    return out;
  }

  /// The shape being dragged out or just added, as a translucent stand-in: the
  /// engine's picture arrives a round-trip later, and a draft that admits
  /// being a draft beats a shape that pops in whenever the render lands. Words
  /// fall back to their box — their true footprint lives in the engine's masks
  /// and imitating it here would only invite the eye to judge the imitation.
  void _drawPreview(Canvas canvas, EditShape s) {
    final line = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = 1.5
      ..color = T.tealBright;
    final p = _pathOf(s);
    if (p == null) {
      final b = s.bounds;
      canvas.drawRect(
        Rect.fromLTRB(
          b.left * scale,
          b.top * scale,
          b.right * scale,
          b.bottom * scale,
        ),
        line,
      );
      return;
    }
    final col = s.color;
    canvas.drawPath(
      p,
      Paint()
        ..color = Color.fromARGB(
          (col[3] * 0.7).round().clamp(0, 255),
          col[0],
          col[1],
          col[2],
        ),
    );
    canvas.drawPath(p, line);
  }

  /// A shape's exact geometry in view space, or null for the dictionary kinds
  /// whose footprint only the engine can draw.
  Path? _pathOf(EditShape s) {
    final d = s.data;
    if (s.isBoxLike) {
      final rect = Rect.fromCenter(
        center: Offset(d[0], d[1]) * scale,
        width: d[2] * 2 * scale,
        height: d[3] * 2 * scale,
      );
      final p = Path();
      if (s.isEllipseLike) {
        p.addOval(rect);
      } else {
        p.addRect(rect);
      }
      final sk = s.frameSkew;
      if (s.angle == 0 && sk == 0) return p;
      final m = Matrix4.identity()
        ..translateByDouble(rect.center.dx, rect.center.dy, 0, 1);
      if (s.angle != 0) m.rotateZ(s.angle * math.pi / 180);
      if (sk != 0) m.multiply(Matrix4.identity()..setEntry(0, 1, sk));
      m.translateByDouble(-rect.center.dx, -rect.center.dy, 0, 1);
      return p.transform(m.storage);
    }
    if (s.type == typeTriangle) {
      return Path()
        ..moveTo(d[0] * scale, d[1] * scale)
        ..lineTo(d[2] * scale, d[3] * scale)
        ..lineTo(d[4] * scale, d[5] * scale)
        ..close();
    }
    return null;
  }

  @override
  bool shouldRepaint(_Handles old) =>
      old.shape != shape ||
      old.scale != scale ||
      old.stale != stale ||
      old.hover != hover ||
      old.preview != preview ||
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
  // The pointer and the bank. The primitives are ordinary bank shapes and
  // earn no seats of their own (owner's call, twice).
  static const _defs = <(_Tool, String, String)>[
    (_Tool.select, '⌖', 'toolSelect'),
    (_Tool.place, '▦', 'bank'),
  ];

  @override
  Widget build(BuildContext context) => Glass(
    radius: 11,
    live: false,
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
                  curve: Motion.curve,
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
    required this.onSaved,
  });

  final Editor editor;
  final Studio studio;
  final VoidCallback onClose;
  final VoidCallback onSaved;

  @override
  Widget build(BuildContext context) => Glass(
    radius: 11,
    live: false,
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
          // A warning, not a wall: the editor takes any count, the game does
          // not, and finding that out at inject time is the wrong moment.
          if (editor.shapes.length > 3000) ...[
            const SizedBox(width: 8),
            Text(
              '⚠ ${context.s('overCap')}',
              style: T.text(11, color: T.amber, weight: FontWeight.w600),
            ),
          ],
          const Spacer(),
          _Icon(
            '↶',
            on: false,
            // (An abandoned `canUndo ? '' : ''` used to sit here — a half-done
            // attempt at a disabled state. Pressable now dims any control whose
            // onTap is null, so undo and redo grey out on their own.)
            tip: '${context.s('undo')}  Ctrl+Z',
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
            onTap: () async {
              studio.adoptEdited(
                editor.toGeometry(),
                editor.render,
                width: editor.width,
                height: editor.height,
                name: editor.referenceName,
              );
              final ok = await studio.saveToLibrary(
                studio.sourceName ?? editor.referenceName ?? 'Untitled',
              );
              // Only leave if it actually landed. It used to close either way,
              // so a save that failed (no disk, engine restarting) looked
              // exactly like one that worked — the editor shut and Runs was
              // empty. The work itself survives on the canvas via adoptEdited,
              // but the user had no way to know that.
              if (ok) onSaved();
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

  /// Whether the free colour picker is unfolded under the swatches.
  bool _pickerOpen = false;

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
    // The layer tree, flattened to a row list: a layer header, then its shapes
    // (top of the stack first) when it is open. A plain list rather than nested
    // widgets so the panel below can build LAZILY — only the visible rows, not a
    // _ShapeRow per shape up to three thousand of them on every notify.
    final rows = <Object>[];
    for (final l in editor.layers) {
      // Shape 0 is the canvas backing (the renderer's background slot), not a
      // shape the user made — it never appears in the tree, and a layer that
      // held only it is dropped so a from-scratch vinyl shows just real shapes.
      final idx = editor.indicesIn(l.id).where((i) => i != 0).toList();
      if (idx.isEmpty) continue;
      rows.add(l);
      if (!_collapsed.contains(l.id)) rows.addAll(idx.reversed);
    }
    return Glass(
      live: false,
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
                    if (s.canSkew) ...[
                      const SizedBox(height: 7),
                      Row(
                        children: [
                          _Field(
                            'SKEW',
                            (s.data.length > 5 ? s.data[5] : 0.0)
                                .toStringAsFixed(2),
                          ),
                          const SizedBox(width: 7),
                          const Spacer(),
                        ],
                      ),
                    ],
                    const SizedBox(height: 13),
                    Text(context.s('colour').toUpperCase(), style: T.label),
                    const SizedBox(height: 5),
                    // The chip is the door to the free picker: the swatches
                    // cover taste, the picker covers the exact pixel.
                    Pressable(
                      onTap: () => setState(() => _pickerOpen = !_pickerOpen),
                      builder: (context, hover, down) => Row(
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
                              border: Border.all(
                                color: hover || _pickerOpen ? T.teal : T.border,
                              ),
                            ),
                          ),
                          const SizedBox(width: 7),
                          Text(
                            '#${_hex(s.color)}',
                            style: T.monoText(12, color: T.body),
                          ),
                          const Spacer(),
                          Text(
                            _pickerOpen ? '▴' : '▾',
                            style: T.text(11, color: T.soft),
                          ),
                        ],
                      ),
                    ),
                    if (_pickerOpen) ...[
                      const SizedBox(height: 7),
                      _ColorPicker(editor: editor),
                    ],
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
                        // One undo step per sweep, not one per tick.
                        onChangeStart: (_) => editor.mark(),
                        onChanged: (v) => editor.previewAlpha(v.round()),
                        onChangeEnd: (_) => editor.commit(),
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
                    // Minus the background backing slot — count only real shapes.
                    '${editor.shapes.length - 1}',
                    style: T.monoText(11, color: T.hint),
                  ),
                ],
              ),
            ),
            // One tree, not two lists. A layer that only COUNTS its shapes
            // is a label; a layer you can open and see what is inside is the
            // thing the word means everywhere else.
            Expanded(
              child: ListView.builder(
                padding: const EdgeInsets.fromLTRB(8, 4, 8, 9),
                itemCount: rows.length,
                itemBuilder: (context, i) {
                  final r = rows[i];
                  if (r is EditLayer) {
                    return _LayerRow(
                      editor: editor,
                      layer: r,
                      open: !_collapsed.contains(r.id),
                      onToggleOpen: () => _toggleOpen(r.id),
                    );
                  }
                  // An index into the shape stack; the flattened list already
                  // put the top of the stack first.
                  return _ShapeRow(editor: editor, index: r as int);
                },
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
    final sel = index == editor.selected || editor.extra.contains(index);
    final layer = editor.layerOf(sh.layer);
    final frozen = layer != null && (layer.locked || layer.hidden);
    return Pressable(
      // Ctrl+click joins the move-group, Shift+click takes the whole run
      // from the primary; a plain click selects alone.
      // Draws its own frozen dim below, so Pressable must not add a second one:
      // 0.4 x 0.4 = 0.16 made locked/hidden rows all but invisible. Also not a
      // Tab stop — a virtualized list of up to 3000 rows would otherwise create
      // and destroy a focus node per scroll frame.
      dimWhenDisabled: false,
      focusable: false,
      onTap: frozen
          ? null
          : () {
              final keys = HardwareKeyboard.instance;
              if (keys.isShiftPressed) {
                editor.extendTo(index);
              } else if (keys.isControlPressed) {
                editor.toggleExtra(index);
              } else {
                editor.select(index);
              }
            },
      builder: (context, hover, down) => Opacity(
        opacity: frozen ? 0.4 : 1,
        child: AnimatedContainer(
          duration: Motion.fast,
          curve: Motion.curve,
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
  const _Icon(
    this.glyph, {
    required this.on,
    required this.tip,
    this.onTap,
    this.ico,
  });

  final String glyph;
  final bool on;
  final String tip;
  final VoidCallback? onTap;

  /// When set, the icon is DRAWN and [glyph] is ignored. Used wherever the
  /// glyph was an emoji: those painted in colour and ignored the tint, so a
  /// hidden layer and a visible one looked identical apart from the picture.
  final Ico? ico;

  @override
  Widget build(BuildContext context) => Tooltip(
    message: tip,
    child: Pressable(
      onTap: onTap,
      builder: (context, hover, down) => AnimatedContainer(
        duration: Motion.fast,
        curve: Motion.curve,
        width: 22,
        height: 22,
        alignment: Alignment.center,
        decoration: BoxDecoration(
          color: hoverOver(const Color(0x00000000), hover, down),
          borderRadius: BorderRadius.circular(6),
        ),
        child: ico != null
            ? Icon2(ico!, color: on ? T.tealBright : T.soft)
            : Text(
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
      curve: Motion.curve,
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: danger
            ? hoverOver(const Color(0x26F0685F), hover, down)
            : hoverOver(T.fillSoft, hover, down),
        borderRadius: BorderRadius.circular(7),
      ),
      child: Text(
        label,
        // The one place Btn's lesson never reached: these sit in a fixed 123px
        // grid cell, and "Duplizieren"/"Дублювати" are wider than the English
        // the grid was measured with.
        maxLines: 1,
        softWrap: false,
        overflow: TextOverflow.ellipsis,
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
      // The bank covers the canvas almost entirely and scrolls a grid, so the
      // live blur re-ran over a strip of edge on every scroll frame.
      live: false,
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
                              [
                                ed.current?.color[0] ?? 235,
                                ed.current?.color[1] ?? 238,
                                ed.current?.color[2] ?? 242,
                                255,
                              ],
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
        curve: Motion.curve,
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
      curve: Motion.curve,
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
/// The free colour picker: a saturation/value field for the current hue, a
/// hue strip, and a hex field. A drag is ONE undo step — mark on touch,
/// preview while moving, commit on release — because a sweep across the field
/// that left a hundred undo entries would make undo mean "one pixel of hue".
class _ColorPicker extends StatefulWidget {
  const _ColorPicker({required this.editor});
  final Editor editor;

  @override
  State<_ColorPicker> createState() => _ColorPickerState();
}

class _ColorPickerState extends State<_ColorPicker> {
  double _hue = 0, _sat = 0, _val = 0;
  bool _dragging = false;
  final _hexField = TextEditingController();
  final _hexFocus = FocusNode();
  List<int> _synced = const [-1, -1, -1];

  Editor get ed => widget.editor;

  @override
  void initState() {
    super.initState();
    _hexFocus.addListener(() {
      if (!_hexFocus.hasFocus) _commitHex();
    });
  }

  @override
  void dispose() {
    _hexField.dispose();
    _hexFocus.dispose();
    super.dispose();
  }

  /// Adopts the shape's colour when someone else changed it — a swatch, a new
  /// selection. Skipped mid-drag, and hue survives greys, which RGB forgets.
  void _syncFromShape() {
    final c = ed.current?.color;
    if (c == null || _dragging) return;
    if (c[0] == _synced[0] && c[1] == _synced[1] && c[2] == _synced[2]) return;
    final hsv = HSVColor.fromColor(Color.fromARGB(255, c[0], c[1], c[2]));
    if (hsv.saturation > 0.001 && hsv.value > 0.001) _hue = hsv.hue;
    _sat = hsv.saturation;
    _val = hsv.value;
    _synced = [c[0], c[1], c[2]];
    if (!_hexFocus.hasFocus) _hexField.text = _hex(c);
  }

  void _push({required bool preview}) {
    final c = HSVColor.fromAHSV(1, _hue, _sat, _val).toColor();
    final r = (c.r * 255).round();
    final g = (c.g * 255).round();
    final b = (c.b * 255).round();
    _synced = [r, g, b];
    if (!_hexFocus.hasFocus) _hexField.text = _hex(_synced);
    if (preview) {
      ed.previewColor(r, g, b);
    } else {
      ed.commit();
    }
  }

  void _commitHex() {
    final t = _hexField.text.trim().replaceFirst('#', '');
    final v = t.length == 6 ? int.tryParse(t, radix: 16) : null;
    if (v == null) {
      final c = ed.current?.color;
      if (c != null) _hexField.text = _hex(c);
      return;
    }
    _synced = const [-1, -1, -1]; // force the fields to re-derive
    ed.setColor((v >> 16) & 0xFF, (v >> 8) & 0xFF, v & 0xFF);
  }

  void _svAt(Offset p, Size size) {
    setState(() {
      _sat = (p.dx / size.width).clamp(0.0, 1.0);
      _val = 1 - (p.dy / size.height).clamp(0.0, 1.0);
    });
    _push(preview: true);
  }

  void _hueAt(Offset p, double width) {
    setState(() => _hue = (p.dx / width).clamp(0.0, 1.0) * 360);
    _push(preview: true);
  }

  Widget _thumb(Color fill) => Container(
    width: 14,
    height: 14,
    decoration: BoxDecoration(
      color: fill,
      shape: BoxShape.circle,
      border: Border.all(color: const Color(0xFFFFFFFF), width: 2),
      boxShadow: const [BoxShadow(color: Color(0x66000000), blurRadius: 3)],
    ),
  );

  @override
  Widget build(BuildContext context) {
    _syncFromShape();
    final hueOnly = HSVColor.fromAHSV(1, _hue, 1, 1).toColor();
    final current = HSVColor.fromAHSV(1, _hue, _sat, _val).toColor();
    return Column(
      children: [
        SizedBox(
          height: 118,
          width: double.infinity,
          child: LayoutBuilder(
            builder: (context, box) {
              final size = Size(box.maxWidth, 118);
              return GestureDetector(
                onPanStart: (d) {
                  ed.mark();
                  _dragging = true;
                  _svAt(d.localPosition, size);
                },
                onPanUpdate: (d) => _svAt(d.localPosition, size),
                onPanEnd: (_) {
                  _dragging = false;
                  _push(preview: false);
                },
                child: Stack(
                  clipBehavior: Clip.none,
                  children: [
                    Positioned.fill(
                      child: Container(
                        decoration: BoxDecoration(
                          borderRadius: BorderRadius.circular(8),
                          border: Border.all(color: T.border),
                          gradient: LinearGradient(
                            colors: [const Color(0xFFFFFFFF), hueOnly],
                          ),
                        ),
                      ),
                    ),
                    Positioned.fill(
                      child: Container(
                        decoration: BoxDecoration(
                          borderRadius: BorderRadius.circular(8),
                          gradient: const LinearGradient(
                            begin: Alignment.topCenter,
                            end: Alignment.bottomCenter,
                            colors: [Color(0x00000000), Color(0xFF000000)],
                          ),
                        ),
                      ),
                    ),
                    Positioned(
                      left: _sat * size.width - 7,
                      top: (1 - _val) * size.height - 7,
                      child: _thumb(current),
                    ),
                  ],
                ),
              );
            },
          ),
        ),
        const SizedBox(height: 8),
        SizedBox(
          height: 14,
          width: double.infinity,
          child: LayoutBuilder(
            builder: (context, box) {
              final w = box.maxWidth;
              return GestureDetector(
                onPanStart: (d) {
                  ed.mark();
                  _dragging = true;
                  _hueAt(d.localPosition, w);
                },
                onPanUpdate: (d) => _hueAt(d.localPosition, w),
                onPanEnd: (_) {
                  _dragging = false;
                  _push(preview: false);
                },
                child: Stack(
                  clipBehavior: Clip.none,
                  children: [
                    Positioned.fill(
                      child: Container(
                        decoration: BoxDecoration(
                          borderRadius: BorderRadius.circular(7),
                          border: Border.all(color: T.border),
                          gradient: const LinearGradient(
                            colors: [
                              Color(0xFFFF0000),
                              Color(0xFFFFFF00),
                              Color(0xFF00FF00),
                              Color(0xFF00FFFF),
                              Color(0xFF0000FF),
                              Color(0xFFFF00FF),
                              Color(0xFFFF0000),
                            ],
                          ),
                        ),
                      ),
                    ),
                    Positioned(
                      left: (_hue / 360) * w - 7,
                      top: 0,
                      child: _thumb(hueOnly),
                    ),
                  ],
                ),
              );
            },
          ),
        ),
        const SizedBox(height: 8),
        Container(
          height: 26,
          padding: const EdgeInsets.symmetric(horizontal: 8),
          alignment: Alignment.centerLeft,
          decoration: BoxDecoration(
            color: T.fillSoft,
            borderRadius: BorderRadius.circular(7),
            border: Border.all(color: _hexFocus.hasFocus ? T.teal : T.border),
          ),
          child: TextField(
            controller: _hexField,
            focusNode: _hexFocus,
            onSubmitted: (_) => _commitHex(),
            style: T.monoText(12, color: T.body),
            cursorColor: T.teal,
            decoration: InputDecoration(
              isDense: true,
              border: InputBorder.none,
              hintText: 'RRGGBB',
              hintStyle: T.monoText(12, color: T.faint),
            ),
          ),
        ),
      ],
    );
  }
}

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
        curve: Motion.curve,
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
                curve: Motion.curve,
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
              '',
              ico: Ico.group,
              on: grouped,
              tip: context.s('groupSelect'),
              onTap: () => editor.toggleGroup(layer.id),
            ),
            _Icon(
              '',
              ico: layer.hidden ? Ico.eyeOff : Ico.eye,
              on: layer.hidden,
              tip: context.s('hideLayer'),
              onTap: () => editor.setLayerHidden(layer.id, !layer.hidden),
            ),
            _Icon(
              '',
              ico: layer.locked ? Ico.lock : Ico.unlock,
              on: layer.locked,
              tip: context.s('lockLayer'),
              onTap: () => editor.setLayerLocked(layer.id, !layer.locked),
            ),
            if (editor.current != null && !layer.locked)
              _Icon(
                '',
                ico: Ico.moveTo,
                on: false,
                tip: context.s('moveHere'),
                onTap: () => editor.assignSelectedTo(layer.id),
              ),
            // Removing a layer keeps its shapes — they go back to the first
            // layer — so this is not a way to lose work by accident. The last
            // layer cannot go, because every shape has to be in one.
            _Icon(
              '',
              ico: Ico.close,
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
