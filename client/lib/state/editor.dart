/// Editing a finished design by hand.
///
/// The document is the same list of shapes the engine produced and the injector
/// will write, kept in its wire form. Editing it is therefore not a conversion:
/// what is on screen is what gets injected, and a shape this editor does not
/// understand still survives being moved past.
///
/// Rendering is the engine's job, always. This holds a picture the engine drew
/// and asks for a new one when an edit is committed; during a drag it paints an
/// approximate outline over that picture so the gesture stays smooth. The ghost
/// is a gesture aid, never the thing being judged — a second renderer here would
/// be a second definition of what a livery looks like.
library;

import 'dart:async';
import 'dart:math' as math;
import 'dart:ui' as ui;

import 'package:flutter/foundation.dart';

import '../engine/engine_client.dart';
import '../engine/protocol.dart';

/// The primitives, by the type id in the document.
const typeBaseRect = 1; // [x1,y1,x2,y2] — the background, corner to corner
const typeRect = 2; // [cx,cy,halfW,halfH,deg]
const typeEllipse = 16; // [cx,cy,rx,ry,deg]
const typeTriangle = 32; // [x1,y1,x2,y2,x3,y3]
const typeLine = 64; // [x1,y1,x2,y2,halfWidth]
const typeGlow = 0xE4; // ellipse geometry, soft radial falloff
const typeDisk = 0xE2; // ellipse geometry, opaque core + soft rim

String shapeName(int type) => switch (type) {
  typeBaseRect => 'Background',
  typeRect => 'Rectangle',
  typeEllipse => 'Ellipse',
  typeTriangle => 'Triangle',
  typeLine => 'Line',
  typeGlow => 'Glow',
  typeDisk => 'Disk',
  _ => 'Shape $type',
};

/// One shape of [kind], filling a box — the same construction the preview tiles
/// use and the same one a drag on the canvas produces, so a tile is an honest
/// picture of what clicking it will place.
EditShape shapeOfKind(int kind, ui.Rect r, List<int> colour) {
  final c = List<int>.of(colour);
  if (kind == typeTriangle) {
    return EditShape(kind, [
      r.center.dx,
      r.top,
      r.right,
      r.bottom,
      r.left,
      r.bottom,
    ], c);
  }
  // Everything else in the bank is placed by a box. The primitives take half
  // extents; a dictionary word takes the FULL extents plus rotation and skew,
  // which is why the two are not the same five numbers.
  if (!_primitiveTypes.contains(kind)) {
    return EditShape(kind, [
      r.center.dx,
      r.center.dy,
      r.width,
      r.height,
      0,
      0,
    ], c);
  }
  return EditShape(kind, [
    r.center.dx,
    r.center.dy,
    r.width / 2,
    r.height / 2,
    0,
  ], c);
}

EditShape _sampleShape(int kind, double size, double pad) => shapeOfKind(
  kind,
  ui.Rect.fromLTRB(pad, pad, size - pad, size - pad),
  const [235, 238, 242, 255],
);

/// The document's own primitives, by the in-game word each one is stored as.
/// Everything else in the catalogue is a captured silhouette, placed by a full
/// affine frame rather than by half extents.
const _primitiveTypes = {
  typeRect,
  typeEllipse,
  typeTriangle,
  typeGlow,
  typeDisk,
  typeBaseRect,
  typeLine,
};

/// A named group of shapes.
///
/// Layers exist for the same reason they do in any drawing tool: three thousand
/// shapes is not a thing anyone can hold in their head, and the useful unit is
/// "the face" or "the background", not shape #1847. Locking is the important
/// half — a locked layer cannot be selected at all, so the work already
/// finished stops being something you can ruin with a stray drag.
class EditLayer {
  EditLayer(this.id, this.name, {this.locked = false, this.hidden = false});

  factory EditLayer.from(Map<String, dynamic> m) => EditLayer(
    (m['id'] as num).toInt(),
    m['name'] as String? ?? '',
    locked: m['locked'] as bool? ?? false,
    hidden: m['hidden'] as bool? ?? false,
  );

  final int id;
  String name;
  bool locked;
  bool hidden;

  Map<String, dynamic> toJson() => {
    'id': id,
    'name': name,
    'locked': locked,
    'hidden': hidden,
  };
}

/// A shape as the document stores it. Mutable on purpose: an edit is a change to
/// this list, and the list is what is exported and injected.
class EditShape {
  EditShape(this.type, this.data, this.color, {this.layer = 0});

  factory EditShape.from(Map<String, dynamic> m) => EditShape(
    (m['type'] as num).toInt(),
    ((m['data'] as List?) ?? const [])
        .map((v) => (v as num).toDouble())
        .toList(),
    ((m['color'] as List?) ?? const [0, 0, 0, 255])
        .map((v) => (v as num).toInt())
        .toList(),
    layer: (m['layer'] as num?)?.toInt() ?? 0,
  );

  int type;
  List<double> data;
  List<int> color;

  /// Which layer owns this shape, by [EditLayer.id]. Stored with the shape
  /// rather than as a list on the layer, because every operation that matters —
  /// undo, reorder, delete — already copies shapes and would have to keep a
  /// separate membership list in step by hand.
  int layer;

  Map<String, dynamic> toJson() => {
    'type': type,
    'data': data,
    'color': color,
    'layer': layer,
    'score': 0,
  };

  EditShape copy() =>
      EditShape(type, List.of(data), List.of(color), layer: layer);

  /// A copy with coordinates and extents multiplied by [k], for draft renders
  /// at reduced resolution. Angles and skew are unitless and stay.
  EditShape scaledFor(double k) {
    final d = List.of(data);
    final n = (isBoxLike || isWordLike) ? 4 : d.length;
    for (var i = 0; i < n; i++) {
      d[i] *= k;
    }
    return EditShape(type, d, List.of(color), layer: layer);
  }

  bool get isEllipseLike =>
      type == typeEllipse || type == typeGlow || type == typeDisk;
  bool get isBoxLike => isEllipseLike || type == typeRect;

  /// A dictionary word from the bank: [cx,cy,W,H,rot,skew] — FULL extents,
  /// unlike the primitives' half extents. Everything that is not a primitive.
  bool get isWordLike =>
      !isBoxLike &&
      type != typeTriangle &&
      type != typeBaseRect &&
      type != typeLine;

  /// The centre, whatever the primitive. Triangles have no stored centre, so it
  /// is the centroid — which is also the point a drag should pivot around.
  ui.Offset get center {
    if (isBoxLike || isWordLike) return ui.Offset(data[0], data[1]);
    if (type == typeTriangle) {
      return ui.Offset(
        (data[0] + data[2] + data[4]) / 3,
        (data[1] + data[3] + data[5]) / 3,
      );
    }
    if (type == typeBaseRect || type == typeLine) {
      return ui.Offset((data[0] + data[2]) / 2, (data[1] + data[3]) / 2);
    }
    return ui.Offset.zero;
  }

  double get angle =>
      (isBoxLike || isWordLike) && data.length > 4 ? data[4] : 0;
  double get size =>
      isBoxLike ? math.max(data[2], data[3]) : bounds.longestSide;

  /// The shape's own box, unrotated: what the selection frame shows. [bounds]
  /// is the rotated EXTENT — right for fitting a view, wrong for a frame that
  /// must turn rigidly with the shape instead of breathing as it spins.
  ui.Rect get localBounds {
    if (isBoxLike || isWordLike) {
      final k = isWordLike ? 1 : 2; // words store full extents
      return ui.Rect.fromCenter(
        center: ui.Offset(data[0], data[1]),
        width: data[2] * k,
        height: data[3] * k,
      );
    }
    return bounds;
  }

  ui.Rect get bounds {
    if (isBoxLike || isWordLike) {
      // The rotated extent, so a selection box never crops a turned shape.
      final hw = isWordLike ? data[2] / 2 : data[2];
      final hh = isWordLike ? data[3] / 2 : data[3];
      final t = data[4] * math.pi / 180;
      final ex = (hw * math.cos(t)).abs() + (hh * math.sin(t)).abs();
      final ey = (hw * math.sin(t)).abs() + (hh * math.cos(t)).abs();
      return ui.Rect.fromLTRB(
        data[0] - ex,
        data[1] - ey,
        data[0] + ex,
        data[1] + ey,
      );
    }
    if (type == typeTriangle) {
      final xs = [data[0], data[2], data[4]];
      final ys = [data[1], data[3], data[5]];
      return ui.Rect.fromLTRB(
        xs.reduce(math.min),
        ys.reduce(math.min),
        xs.reduce(math.max),
        ys.reduce(math.max),
      );
    }
    return ui.Rect.fromLTRB(
      math.min(data[0], data[2]),
      math.min(data[1], data[3]),
      math.max(data[0], data[2]),
      math.max(data[1], data[3]),
    );
  }

  /// Whether a point is inside. Exact for the primitives a user can select; the
  /// gradient kinds use their footprint, which is what their handles show.
  bool contains(ui.Offset p) {
    if (isWordLike) {
      // The word's box, turned back: its true footprint lives in the engine's
      // masks, and the box is what the frame shows and the hand expects.
      final t = -data[4] * math.pi / 180;
      final dx = p.dx - data[0], dy = p.dy - data[1];
      final rx = dx * math.cos(t) - dy * math.sin(t);
      final ry = dx * math.sin(t) + dy * math.cos(t);
      return rx.abs() <= math.max(0.5, data[2] / 2) &&
          ry.abs() <= math.max(0.5, data[3] / 2);
    }
    switch (type) {
      case typeEllipse || typeGlow || typeDisk:
        final t = -data[4] * math.pi / 180;
        final dx = p.dx - data[0], dy = p.dy - data[1];
        final rx = dx * math.cos(t) - dy * math.sin(t);
        final ry = dx * math.sin(t) + dy * math.cos(t);
        final a = math.max(0.5, data[2]), b = math.max(0.5, data[3]);
        return (rx * rx) / (a * a) + (ry * ry) / (b * b) <= 1;
      case typeRect:
        final t = -data[4] * math.pi / 180;
        final dx = p.dx - data[0], dy = p.dy - data[1];
        final rx = dx * math.cos(t) - dy * math.sin(t);
        final ry = dx * math.sin(t) + dy * math.cos(t);
        return rx.abs() <= math.max(0.5, data[2]) &&
            ry.abs() <= math.max(0.5, data[3]);
      case typeTriangle:
        return _inTriangle(p);
      default:
        return bounds.contains(p);
    }
  }

  bool _inTriangle(ui.Offset p) {
    double cross(double ax, double ay, double bx, double by) =>
        ax * by - ay * bx;
    final d1 = cross(
      data[2] - data[0],
      data[3] - data[1],
      p.dx - data[0],
      p.dy - data[1],
    );
    final d2 = cross(
      data[4] - data[2],
      data[5] - data[3],
      p.dx - data[2],
      p.dy - data[3],
    );
    final d3 = cross(
      data[0] - data[4],
      data[1] - data[5],
      p.dx - data[4],
      p.dy - data[5],
    );
    final neg = d1 < 0 || d2 < 0 || d3 < 0;
    final pos = d1 > 0 || d2 > 0 || d3 > 0;
    return !(neg && pos);
  }

  void translate(double dx, double dy) {
    if (isBoxLike || isWordLike) {
      data[0] += dx;
      data[1] += dy;
      return;
    }
    for (var i = 0; i + 1 < data.length; i += 2) {
      data[i] += dx;
      data[i + 1] += dy;
    }
  }

  void scaleBy(double k) {
    if (isBoxLike || isWordLike) {
      final floor = isWordLike ? 1.0 : 0.5;
      data[2] = math.max(floor, data[2] * k);
      data[3] = math.max(floor, data[3] * k);
      return;
    }
    final c = center;
    for (var i = 0; i + 1 < data.length; i += 2) {
      data[i] = c.dx + (data[i] - c.dx) * k;
      data[i + 1] = c.dy + (data[i + 1] - c.dy) * k;
    }
  }

  void rotateBy(double deg) {
    if (isBoxLike || isWordLike) {
      data[4] = (data[4] + deg) % 360;
      return;
    }
    final c = center;
    final t = deg * math.pi / 180;
    for (var i = 0; i + 1 < data.length; i += 2) {
      final dx = data[i] - c.dx, dy = data[i + 1] - c.dy;
      data[i] = c.dx + dx * math.cos(t) - dy * math.sin(t);
      data[i + 1] = c.dy + dx * math.sin(t) + dy * math.cos(t);
    }
  }

  /// Mirrors across the document's own axis, not the shape's: mirroring a decal
  /// is about where it sits on the panel.
  void mirror(double axis, {required bool horizontal}) {
    if (isBoxLike || isWordLike) {
      if (horizontal) {
        data[0] = 2 * axis - data[0];
        data[4] = (360 - data[4]) % 360;
      } else {
        data[1] = 2 * axis - data[1];
        data[4] = (180 - data[4]) % 360;
      }
      return;
    }
    for (var i = 0; i + 1 < data.length; i += 2) {
      if (horizontal) {
        data[i] = 2 * axis - data[i];
      } else {
        data[i + 1] = 2 * axis - data[i + 1];
      }
    }
  }
}

/// Repaint driver for the canvas during live gestures. The PAINTERS listen to
/// this; the widget tree does not — rebuilding a 3000-row layer panel at
/// pointer-event rate is what made dragging crawl no matter how cheap the
/// canvas itself was.
class CanvasTick extends ChangeNotifier {
  void tick() => notifyListeners();
}

class Editor extends ChangeNotifier {
  Editor(this._engine);

  final canvasTick = CanvasTick();

  final EngineClient _engine;

  final shapes = <EditShape>[];
  final layers = <EditLayer>[];
  int width = 0;
  int height = 0;

  /// Where new shapes go, and what the layer actions act on.
  int activeLayer = 0;

  /// Set while a whole LAYER is the thing being transformed rather than one
  /// shape. Dragging then moves everything in it together.
  int? groupLayer;

  int _nextLayerId = 1;

  /// The bank, as the engine reports it. Empty until [loadCatalog] answers;
  /// the editor is usable meanwhile with the primitives it already knows.
  final catalog = <Map<String, dynamic>>[];

  /// The last kinds placed, most recent first. A palette of what you are
  /// actually using beats a palette of everything: a livery is usually the same
  /// six silhouettes over and over.
  final recent = <int>[];

  /// The kind a drag will create. Null means one of the three primitive tools
  /// is driving instead.
  int? pickedKind;

  Future<void> loadCatalog() async {
    if (catalog.isNotEmpty) return;
    try {
      catalog.addAll(await _engine.shapeCatalog());
      notifyListeners();
    } catch (e) {
      error = '$e';
    }
  }

  /// A small picture of one kind, rendered by the engine so a tile shows what
  /// the game will actually draw rather than an approximation of it.
  final _previews = <int, Future<ui.Image>>{};

  Future<ui.Image> preview(int kind, {int size = 64}) =>
      _previews[kind] ??= _renderPreview(kind, size);

  Future<ui.Image> _renderPreview(int kind, int size) async {
    final pad = size * 0.14;
    final frame = await _engine.render(
      shapes: [
        // A dark plate under a light shape: a silhouette on transparency reads
        // as nothing at all against a dark panel.
        {
          'type': typeBaseRect,
          'data': [0.0, 0.0, size.toDouble(), size.toDouble()],
          'color': [26, 28, 31, 255],
          'score': 0,
        },
        _sampleShape(kind, size.toDouble(), pad).toJson(),
      ],
      width: size,
      height: size,
    );
    final done = Completer<ui.Image>();
    ui.decodeImageFromPixels(
      frame.pixels,
      frame.width,
      frame.height,
      ui.PixelFormat.rgba8888,
      done.complete,
    );
    return done.future;
  }

  /// The engine's picture of the current document.
  ui.Image? render;
  bool rendering = false;
  bool _dirty = false;
  bool _dirtyDraft = true;

  /// Long side of a draft render, in pixels. Small enough that the engine
  /// answers within a pointer frame or two on a full document.
  static const _draftLongSide = 700.0;

  /// Long side of a committed render — display-resolution, not native.
  static const _fullLongSide = 1600.0;

  /// The shape just added, until a render that includes it lands. The picture
  /// trails the document by a round-trip, so without this a new shape exists
  /// but cannot be seen.
  EditShape? settling;
  int _settleWaits = 0;

  int selected = -1;
  String? error;

  final _undo = <List<EditShape>>[];
  final _redo = <List<EditShape>>[];

  bool get canUndo => _undo.isNotEmpty;
  bool get canRedo => _redo.isNotEmpty;
  EditShape? get current =>
      selected >= 0 && selected < shapes.length ? shapes[selected] : null;

  void load(Map<String, dynamic> geometry, int w, int h) {
    shapes
      ..clear()
      ..addAll(
        ((geometry['shapes'] as List?) ?? const []).map(
          (m) => EditShape.from(m as Map<String, dynamic>),
        ),
      );
    layers
      ..clear()
      ..addAll(
        ((geometry['layers'] as List?) ?? const []).map(
          (m) => EditLayer.from(m as Map<String, dynamic>),
        ),
      );
    // A document from the engine has no layers yet, and one from an older save
    // may name a layer that no longer exists. Either way it ends up with at
    // least one layer that every shape belongs to.
    if (layers.isEmpty) _proposeLayers();
    final known = {for (final l in layers) l.id};
    for (final sh in shapes) {
      if (!known.contains(sh.layer)) sh.layer = layers.first.id;
    }
    _nextLayerId = layers.map((l) => l.id).reduce(math.max) + 1;
    activeLayer = layers.first.id;
    groupLayer = null;

    width = w;
    height = h;
    selected = -1;
    extra.clear();
    _undo.clear();
    _redo.clear();
    notifyListeners();
    refresh();
  }

  /// Starting layers for a document that has none.
  ///
  /// A fit hands back one flat stack of up to three thousand shapes, which is
  /// not something anyone can work with. The split is by SIZE, because that is
  /// how the greedy actually spends its budget: the first shapes cover whole
  /// regions and the last ones are detail a few pixels across. It also happens
  /// to be the split that makes locking useful — freeze the big forms, then
  /// tune the fine ones without ever picking the wrong thing.
  ///
  /// Deliberately a PROPOSAL. The layers are ordinary layers from the moment
  /// they exist: rename them, merge them, ignore them.
  void _proposeLayers() {
    final base = EditLayer(0, 'Background');
    final big = EditLayer(1, 'Large forms');
    final mid = EditLayer(2, 'Detail');
    final fine = EditLayer(3, 'Fine detail');
    layers.addAll([base, big, mid, fine]);
    _nextLayerId = 4;

    final areas = <double>[];
    for (var i = 1; i < shapes.length; i++) {
      final b = shapes[i].bounds;
      areas.add(b.width * b.height);
    }
    if (areas.isEmpty) return;
    // Thirds by RANK, not by absolute size: a logo and a portrait have nothing
    // in common in pixels, but both have a biggest third and a smallest third.
    final sorted = List<double>.of(areas)..sort();
    final low = sorted[sorted.length ~/ 3];
    final high = sorted[(sorted.length * 2) ~/ 3];

    for (var i = 0; i < shapes.length; i++) {
      if (i == 0) {
        shapes[i].layer = base.id;
        continue;
      }
      final b = shapes[i].bounds;
      final a = b.width * b.height;
      shapes[i].layer = a >= high ? big.id : (a >= low ? mid.id : fine.id);
    }
  }

  /// Snapshots the document before a change. Every mutation goes through this,
  /// which is what makes undo total rather than a list of special cases.
  void mark() {
    _undo.add(shapes.map((s) => s.copy()).toList());
    if (_undo.length > 100) _undo.removeAt(0);
    _redo.clear();
  }

  void undo() => _swap(_undo, _redo);
  void redo() => _swap(_redo, _undo);

  void _swap(List<List<EditShape>> from, List<List<EditShape>> to) {
    if (from.isEmpty) return;
    endInteraction();
    extra.clear();
    to.add(shapes.map((s) => s.copy()).toList());
    final restored = from.removeLast();
    shapes
      ..clear()
      ..addAll(restored);
    if (selected >= shapes.length) selected = shapes.length - 1;
    notifyListeners();
    refresh();
  }

  /// A frame of a live gesture: repaint the handles AND ask the engine for a
  /// fresh picture. The render is coalesced — one in flight, at most one queued
  /// — so dragging cannot build a backlog, and the shape follows the pointer
  /// instead of jumping into place when the mouse is released.
  void live() {
    // Painters only — no notifyListeners, no tree rebuild. The shape objects
    // are mutated in place, so a repaint alone shows the new geometry.
    canvasTick.tick();
    // With the interaction composite up OR still rendering, the engine hears
    // nothing: a draft queued behind the composite's three renders only
    // delays the very thing that ends the drafting. Group drags and a failed
    // composite (_interBusy cleared in its finally) keep the draft path.
    if (interBelow == null && !_interBusy) unawaited(refresh(draft: true));
  }

  /// Extra selected shapes beyond [selected] — move-only companions: the
  /// group translates together, but resize/rotate/inspector stay with the
  /// primary. Cleared by any plain select or structural change, because the
  /// members are INDICES and a reorder would silently retarget them.
  final extra = <int>{};

  void select(int i) {
    if (i != selected) endInteraction();
    selected = i;
    extra.clear();
    notifyListeners();
  }

  /// Ctrl+click in the panel: joins or leaves the move-group.
  void toggleExtra(int i) {
    if (i <= 0 || i >= shapes.length || i == selected) return;
    if (selected < 0) {
      select(i);
      return;
    }
    if (!extra.remove(i)) extra.add(i);
    endInteraction();
    notifyListeners();
  }

  /// Shift+click in the panel: the whole run between the primary and [i]
  /// becomes the move-group. Locked and hidden layers stay out of it — the
  /// range must not quietly drag what a lock was meant to protect.
  void extendTo(int i) {
    if (i <= 0 || i >= shapes.length) return;
    if (selected <= 0) {
      select(i);
      return;
    }
    extra.clear();
    final lo = math.min(selected, i);
    final hi = math.max(selected, i);
    for (var n = lo; n <= hi; n++) {
      if (n == selected) continue;
      final l = layerOf(shapes[n].layer);
      if (l != null && (l.locked || l.hidden)) continue;
      extra.add(n);
    }
    endInteraction();
    notifyListeners();
  }

  // ---- live interaction -----------------------------------------------

  /// The stack split around the selected shape, rendered ONCE when a gesture
  /// starts: everything below it, the shape alone on transparency, everything
  /// above. During the gesture the canvas is composited locally from these
  /// three at frame rate and the engine is asked for NOTHING — the per-tick
  /// full-stack renders are what made dragging stutter and freeze. The commit
  /// at the gesture's end renders the truth; this composite is sRGB and
  /// approximate on purpose.
  ui.Image? interBelow, interSprite, interAbove;
  List<double>? interStart; // the shape's data as the sprite was rendered
  int _interFor = -1;
  bool _interBusy = false;
  bool _interRetire =
      false; // drop the composite when the next FULL frame lands

  Future<void> beginInteraction() async {
    final i = selected;
    if (i < 0 || _interBusy) return;
    if (_interFor == i && interBelow != null) {
      _interRetire = false; // a fresh grab keeps the composite alive
      return;
    }
    _interBusy = true;
    _interFor = i;
    try {
      final k = math.min(1.0, _draftLongSide / math.max(width, height));
      final w = math.max(1, (width * k).round());
      final h = math.max(1, (height * k).round());
      List<Map<String, dynamic>> part(Iterable<EditShape> ss) => [
        for (final s in ss)
          if (layerOf(s.layer)?.hidden != true)
            (k == 1.0 ? s : s.scaledFor(k)).toJson(),
      ];
      // The pose is snapshotted with the request: the hand is already moving
      // the live shape while these render.
      final snap = shapes[i].copy();
      // The renderer treats shapes[0] as the BACKGROUND slot — with a
      // transparent render it is skipped outright. The sprite and the
      // above-stack must ride on a stub, or their first shape vanishes:
      // that was the drag where the shape disappeared until release.
      const stub = {
        'type': 1,
        'data': [0.0, 0.0, 0.0, 0.0],
        'color': [0, 0, 0, 0],
        'score': 0,
      };
      // The list is snapshotted synchronously and all requests leave at
      // once: the server is FIFO anyway, and awaiting each reply before
      // SENDING the next added two idle round-trips to every grab.
      final all = List<EditShape>.of(shapes);
      ui.Image below, sprite;
      ui.Image? above;
      if (extra.isEmpty) {
        final fb = _engine.render(
          shapes: part(all.take(i)),
          width: w,
          height: h,
        );
        final fs = _engine.render(
          shapes: [
            stub,
            ...part([snap]),
          ],
          width: w,
          height: h,
          transparent: true,
        );
        final fa = _engine.render(
          shapes: [stub, ...part(all.skip(i + 1))],
          width: w,
          height: h,
          transparent: true,
        );
        below = await _decodeFrame(await fb);
        sprite = await _decodeFrame(await fs);
        above = await _decodeFrame(await fa);
      } else {
        // A move-group: the sprite is EVERY selected shape in stack order,
        // riding on top of everything else. The z-order is approximate for
        // the gesture; the commit render restores the truth.
        final sel = {i, ...extra};
        final fb = _engine.render(
          shapes: part([
            for (var n = 0; n < all.length; n++)
              if (!sel.contains(n)) all[n],
          ]),
          width: w,
          height: h,
        );
        final fs = _engine.render(
          shapes: [
            stub,
            ...part([
              for (var n = 0; n < all.length; n++)
                if (sel.contains(n)) all[n],
            ]),
          ],
          width: w,
          height: h,
          transparent: true,
        );
        below = await _decodeFrame(await fb);
        sprite = await _decodeFrame(await fs);
      }
      if (_interFor != i) {
        below.dispose();
        sprite.dispose();
        above?.dispose();
        return;
      }
      interBelow = below;
      interSprite = sprite;
      interAbove = above;
      interStart = List.of(snap.data);
      notifyListeners();
    } catch (_) {
      endInteraction(); // the draft-render path keeps working without this
    } finally {
      _interBusy = false;
    }
  }

  void endInteraction() {
    final old = [interBelow, interSprite, interAbove];
    interBelow = interSprite = interAbove = null;
    interStart = null;
    _interFor = -1;
    // Disposed a beat later: a repaint scheduled before listeners heard the
    // news may still hold these.
    Future<void>.delayed(const Duration(milliseconds: 300), () {
      for (final img in old) {
        img?.dispose();
      }
    });
  }

  Future<ui.Image> _decodeFrame(PreviewFrame f) {
    final done = Completer<ui.Image>();
    ui.decodeImageFromPixels(
      f.pixels,
      f.width,
      f.height,
      ui.PixelFormat.rgba8888,
      done.complete,
    );
    return done.future;
  }

  /// The topmost shape under a point. Topmost because that is the one the user
  /// can see there — searching bottom-up would select whatever is buried.
  EditLayer? layerOf(int id) {
    for (final l in layers) {
      if (l.id == id) return l;
    }
    return null;
  }

  /// Whether a shape can be picked at all. A locked or hidden layer is skipped
  /// entirely — not dimmed, not selected-then-ignored — which is the whole
  /// point of locking one.
  bool _pickable(EditShape s) {
    final l = layerOf(s.layer);
    return l == null || (!l.locked && !l.hidden);
  }

  int hitTest(ui.Offset p) {
    for (var i = shapes.length - 1; i >= 1; i--) {
      if (_pickable(shapes[i]) && shapes[i].contains(p)) return i;
    }
    return -1;
  }

  /// The shapes a layer owns, in document order.
  Iterable<EditShape> shapesIn(int layerId) =>
      shapes.where((s) => s.layer == layerId);

  int countIn(int layerId) => shapesIn(layerId).length;

  /// The positions a layer occupies in the stack, bottom first. Positions
  /// rather than shapes, because selection is by index everywhere else.
  List<int> indicesIn(int layerId) => [
    for (var i = 0; i < shapes.length; i++)
      if (shapes[i].layer == layerId) i,
  ];

  /// The box around everything in a layer, or null if it is empty.
  ui.Rect? layerBounds(int layerId) {
    ui.Rect? box;
    for (final s in shapesIn(layerId)) {
      box = box == null ? s.bounds : box.expandToInclude(s.bounds);
    }
    return box;
  }

  // ---- layer actions --------------------------------------------------------

  void addLayer(String name) {
    mark();
    final l = EditLayer(_nextLayerId++, name);
    layers.add(l);
    activeLayer = l.id;
    notifyListeners();
  }

  /// Removes a layer and gives its shapes back to the first one. Deleting the
  /// shapes with it would make a lock-and-forget layer a trap.
  void removeLayer(int id) {
    if (layers.length <= 1) return;
    mark();
    final home = layers.firstWhere((l) => l.id != id).id;
    for (final s in shapes) {
      if (s.layer == id) s.layer = home;
    }
    layers.removeWhere((l) => l.id == id);
    if (activeLayer == id) activeLayer = home;
    if (groupLayer == id) groupLayer = null;
    commit();
  }

  void renameLayer(int id, String name) {
    layerOf(id)?.name = name;
    notifyListeners();
  }

  void setLayerLocked(int id, bool v) {
    final l = layerOf(id);
    if (l == null) return;
    l.locked = v;
    // Nothing in a locked layer may stay selected, or the inspector would go on
    // editing what the lock was meant to protect.
    if (v && current != null && current!.layer == id) selected = -1;
    if (v && groupLayer == id) groupLayer = null;
    notifyListeners();
  }

  void setLayerHidden(int id, bool v) {
    final l = layerOf(id);
    if (l == null) return;
    l.hidden = v;
    if (v && current != null && current!.layer == id) selected = -1;
    commit();
  }

  void setActiveLayer(int id) {
    activeLayer = id;
    notifyListeners();
  }

  /// Picks the whole layer as the thing being transformed, or lets it go.
  void toggleGroup(int id) {
    final l = layerOf(id);
    if (l == null || l.locked || l.hidden) return;
    groupLayer = groupLayer == id ? null : id;
    if (groupLayer != null) {
      activeLayer = id;
      selected = -1;
    }
    notifyListeners();
  }

  void assignSelectedTo(int id) {
    final s = current;
    if (s == null || selected == 0) return;
    mark();
    s.layer = id;
    commit();
  }

  void translateLayer(int id, double dx, double dy) {
    for (final s in shapesIn(id)) {
      s.translate(dx, dy);
    }
  }

  void scaleLayer(int id, double k) {
    final box = layerBounds(id);
    if (box == null) return;
    for (final s in shapesIn(id)) {
      // Scaling about the LAYER's centre, not each shape's own: otherwise the
      // shapes grow in place and the group falls apart.
      final c = s.center;
      s.scaleBy(k);
      final want = box.center + (c - box.center) * k;
      final now = s.center;
      s.translate(want.dx - now.dx, want.dy - now.dy);
    }
  }

  /// Turns the whole layer about its own centre.
  ///
  /// Each shape is turned on the spot AND carried around the group's centre —
  /// both, or the layer would either pivot as a rigid board of unturned shapes
  /// or spin every shape in place without moving. [EditShape.rotateBy] leaves a
  /// shape's own centre where it was, which is what makes the second step a
  /// plain correction rather than a special case per primitive.
  void rotateLayer(int id, double deg) {
    final box = layerBounds(id);
    if (box == null) return;
    final t = deg * math.pi / 180;
    final cos = math.cos(t), sin = math.sin(t);
    for (final s in shapesIn(id)) {
      final was = s.center - box.center;
      s.rotateBy(deg);
      final want =
          box.center +
          ui.Offset(was.dx * cos - was.dy * sin, was.dx * sin + was.dy * cos);
      final now = s.center;
      s.translate(want.dx - now.dx, want.dy - now.dy);
    }
  }

  /// Adds a shape the user drew, on top of the stack and in the active layer.
  void addShape(EditShape s) {
    recent
      ..remove(s.type)
      ..insert(0, s.type);
    if (recent.length > 10) recent.removeLast();
    mark();
    s.layer = activeLayer;
    shapes.add(s);
    selected = shapes.length - 1;
    extra.clear();
    groupLayer = null;
    settling = s;
    // A render already in flight was asked before this shape existed; only the
    // one after it can show the shape.
    _settleWaits = rendering ? 2 : 1;
    commit();
  }

  void commit() {
    // The composite outlives the gesture ON PURPOSE: dropped at release, the
    // canvas fell back to the STALE picture and the shape flashed back to its
    // old place for the render's round-trip. It retires when the fresh
    // full-resolution frame actually lands.
    if (interBelow != null && _interFor == selected) {
      _interRetire = true;
    } else {
      endInteraction();
    }
    notifyListeners();
    refresh();
  }

  void deleteSelected() {
    final s = current;
    if (s == null || selected == 0) return; // the background is not deletable
    mark();
    extra.clear();
    final layer = s.layer;
    final was = selected;
    shapes.removeAt(selected);
    // Focus falls to the NEXT ROW of the panel — the nearest same-layer shape
    // below the deleted one in the stack — not to whatever slid into the old
    // index, which could be any layer and looked random.
    final ids = indicesIn(layer);
    if (ids.isNotEmpty) {
      selected = ids.lastWhere(
        (i) => i < was,
        orElse: () => ids.firstWhere((i) => i >= was, orElse: () => ids.last),
      );
    } else {
      selected = math.min(was, shapes.length - 1);
    }
    commit();
  }

  void duplicateSelected() {
    final s = current;
    if (s == null) return;
    mark();
    final copy = s.copy()..translate(8, 8);
    shapes.insert(selected + 1, copy);
    selected += 1;
    commit();
  }

  void raiseSelected() => _move(1);
  void lowerSelected() => _move(-1);

  void _move(int by) {
    final i = selected;
    // Index 0 is the background and stays there: everything is composited over
    // it, and moving it would put the canvas colour on top of the art.
    if (i < 1 || i + by < 1 || i + by >= shapes.length) return;
    mark();
    extra.clear();
    final s = shapes.removeAt(i);
    shapes.insert(i + by, s);
    selected = i + by;
    commit();
  }

  void mirrorSelected({required bool horizontal}) {
    final s = current;
    if (s == null) return;
    mark();
    s.mirror(horizontal ? width / 2 : height / 2, horizontal: horizontal);
    commit();
  }

  void setColor(int r, int g, int b) {
    final s = current;
    if (s == null) return;
    mark();
    s.color[0] = r;
    s.color[1] = g;
    s.color[2] = b;
    commit();
  }

  /// Colour mid-drag: paints without touching undo. The picker marks once
  /// when the drag starts and commits once when it ends, so a sweep across
  /// the field is one undo step, not a hundred.
  void previewColor(int r, int g, int b) {
    final s = current;
    if (s == null) return;
    s.color[0] = r;
    s.color[1] = g;
    s.color[2] = b;
    live();
  }

  void setAlpha(int a) {
    final s = current;
    if (s == null) return;
    mark();
    s.color[3] = a.clamp(0, 255);
    commit();
  }

  /// Alpha mid-drag: paints without touching undo — the slider marks once on
  /// grab and commits once on release, like the colour picker.
  void previewAlpha(int a) {
    final s = current;
    if (s == null) return;
    s.color[3] = a.clamp(0, 255);
    live();
  }

  /// Asks the engine for a fresh picture. Coalesced: an edit during a render
  /// queues exactly one more, so dragging cannot build a backlog of stale
  /// pictures that then flash past in order.
  ///
  /// [draft] renders at a reduced resolution. The full-size picture of a
  /// 3000-shape document takes long enough that the canvas trailed the
  /// pointer by half a second — a live gesture wants cadence, not fidelity.
  /// The commit at the gesture's end asks for the full frame.
  Future<void> refresh({bool draft = false}) async {
    if (rendering) {
      _dirty = true;
      _dirtyDraft = _dirtyDraft && draft; // one full request upgrades the queue
      return;
    }
    rendering = true;
    notifyListeners();
    try {
      // Even the FULL render is capped near display resolution: the viewport
      // shows ~1100 logical px, and rendering/shipping/decoding a 16 MB
      // native frame per mouse-up bought pixels nobody could see. Export and
      // injection use the GEOMETRY, which never loses precision.
      final k = math.min(
        1.0,
        (draft ? _draftLongSide : _fullLongSide) / math.max(width, height),
      );
      final frame = await _engine.render(
        // A hidden layer leaves the picture but not the document: it is still
        // exported and still undoable, it simply is not drawn.
        shapes: shapes
            .where((s) => layerOf(s.layer)?.hidden != true)
            .map((s) => (k == 1.0 ? s : s.scaledFor(k)).toJson())
            .toList(),
        width: math.max(1, (width * k).round()),
        height: math.max(1, (height * k).round()),
      );
      final completer = Completer<ui.Image>();
      ui.decodeImageFromPixels(
        frame.pixels,
        frame.width,
        frame.height,
        ui.PixelFormat.rgba8888,
        completer.complete,
      );
      // Swap BEFORE disposing: the await parks this function while the tree
      // still paints the old image — disposing it first was a use-after-free
      // for however long the decode took.
      final fresh = await completer.future;
      final old = render;
      render = fresh;
      old?.dispose();
      error = null;
    } catch (e) {
      error = '$e';
    }
    if (_settleWaits > 0 && --_settleWaits == 0) settling = null;
    // Only a FULL frame retires the composite — a stale queued draft landing
    // first would bring the old picture back for a blink.
    if (_interRetire && !draft) {
      _interRetire = false;
      endInteraction();
    }
    rendering = false;
    notifyListeners();
    if (_dirty) {
      _dirty = false;
      final d = _dirtyDraft;
      _dirtyDraft = true;
      unawaited(refresh(draft: d));
    }
  }

  Map<String, dynamic> toGeometry() => {
    'shapes': shapes.map((s) => s.toJson()).toList(),
    'layers': layers.map((l) => l.toJson()).toList(),
  };

  @override
  void dispose() {
    render?.dispose();
    super.dispose();
  }
}
