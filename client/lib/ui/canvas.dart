/// The canvas: the source, the result, and the two ways of interrogating them.
///
/// Compare is a wipe rather than a side-by-side because the question is always
/// "did this shape survive", and the answer needs the two images in the SAME
/// place on screen. The handle lives ON the picture, where the eye already is —
/// a slider parked below the canvas asks you to look away from the thing you
/// are judging.
library;

import 'dart:math' as math;
import 'dart:ui' as ui;

import 'package:flutter/material.dart';

import '../state/studio.dart';
import 'strings.dart';
import 'tokens.dart';

/// The result, with the source revealed to its left.
///
/// [fraction] is where the boundary sits across the width, 0 at the left edge.
/// It is snapped to a whole pixel and the seam widget snaps the same way, which
/// is what makes the line land exactly on the join.
class _ComparePainter extends CustomPainter {
  const _ComparePainter({
    required this.result,
    required this.source,
    required this.srcView,
    required this.fraction,
    required this.dpr,
  });

  final ui.Image? result;
  final ui.Image? source;
  final Rect? srcView;
  final double fraction;
  final double dpr;

  /// Where the boundary sits, snapped so it cannot land inside a pixel.
  ///
  /// The window is not necessarily at 1:1 — at 125% or 150% scaling a whole
  /// logical pixel is a fractional device one, so the edge has to be quantised
  /// in DEVICE space or the join drifts by a fraction of a pixel and smears.
  static double seamAt(double width, double fraction, double dpr) {
    final device = (width * fraction * dpr).roundToDouble();
    return device / dpr;
  }

  void _draw(Canvas canvas, ui.Image image, Rect? src, Size size) {
    final from =
        src ??
        Rect.fromLTWH(0, 0, image.width.toDouble(), image.height.toDouble());
    canvas.drawImageRect(
      image,
      from,
      Offset.zero & size,
      Paint()..filterQuality = FilterQuality.medium,
    );
  }

  @override
  void paint(Canvas canvas, Size size) {
    final r = result;
    if (r != null) _draw(canvas, r, null, size);
    final s = source;
    if (s == null || fraction <= 0) return;
    canvas.save();
    // Not antialiased: a soft clip edge blends the two pictures into each
    // other for one column, which is the sliver of the old image that kept
    // appearing beside the handle.
    canvas.clipRect(
      Rect.fromLTWH(0, 0, seamAt(size.width, fraction, dpr), size.height),
      doAntiAlias: false,
    );
    _draw(canvas, s, srcView, size);
    canvas.restore();
  }

  @override
  bool shouldRepaint(_ComparePainter old) =>
      old.result != result ||
      old.source != source ||
      old.srcView != srcView ||
      old.fraction != fraction ||
      old.dpr != dpr;
}

class CanvasView extends StatelessWidget {
  const CanvasView({
    super.key,
    required this.studio,
    required this.cropping,
    required this.onCropDone,
  });

  final Studio studio;
  final bool cropping;
  final VoidCallback onCropDone;

  /// The part of the source the run covers. Everything here is drawn through
  /// it, so applying a crop changes the picture immediately instead of only
  /// changing what the next run will fit.
  Rect _view(ui.Image src) {
    final r = studio.region;
    if (r == null) {
      return Rect.fromLTWH(0, 0, src.width.toDouble(), src.height.toDouble());
    }
    return Rect.fromLTWH(
      r[0].toDouble(),
      r[1].toDouble(),
      r[2].toDouble(),
      r[3].toDouble(),
    );
  }

  @override
  Widget build(BuildContext context) {
    final result = studio.preview;
    final source = studio.sourceImage;
    if (result == null && source == null) return const SizedBox.shrink();

    // While cropping the SOURCE is shown whole: the rectangle is drawn against
    // the original, and a crop taken over a reconstruction would be a rectangle
    // in the wrong picture.
    if (cropping && source != null) {
      return _Framed(
        aspect: source.width / source.height,
        child: (box) => Stack(
          fit: StackFit.expand,
          children: [
            CustomPaint(painter: _ImagePainter(source, null)),
            CropOverlay(
              studio: studio,
              view: Size(box.maxWidth, box.maxHeight),
              onDone: onCropDone,
            ),
          ],
        ),
      );
    }

    final srcView = source == null ? null : _view(source);
    final aspect = result != null
        ? result.width / result.height
        : srcView!.width / srcView.height;

    return _Framed(
      aspect: aspect,
      child: (box) => Stack(
        fit: StackFit.expand,
        children: [
          // Before on the left, after on the right, the way every comparison
          // slider reads — and BOTH images in one painter, split at one
          // integer x.
          //
          // They used to be two layers with a ClipRect between them, and the
          // clip and the seam widget each worked the split out from their own
          // box — so the reveal and the line it is supposed to follow could sit
          // a few percent apart, and the new picture bled past the handle.
          // One paint, one boundary, no way for them to disagree.
          // Isolated: only THIS painter changes on a new preview frame or a
          // compare drag. Without the boundary each of those (~20×/s during a
          // fit) re-rasters the shared layer — the 46px plate shadow and the
          // full-canvas checker below — both of which are entirely static.
          // The wipe listens to its OWN notifier: dragging it used to go through
          // the studio's notifyListeners and rebuild the whole shell at pointer
          // rate for a number only this painter and the seam read.
          RepaintBoundary(
            child: ValueListenableBuilder<double>(
              valueListenable: studio.compareN,
              builder: (context, cmp, _) => CustomPaint(
                painter: _ComparePainter(
                  result: result,
                  source: source,
                  srcView: srcView,
                  fraction: result == null ? 1 : 1 - cmp,
                  dpr: MediaQuery.devicePixelRatioOf(context),
                ),
              ),
            ),
          ),
          if (result != null && source != null)
            _Seam(studio: studio, width: box.maxWidth),
        ],
      ),
    );
  }
}

/// What transparency looks like: the checkerboard every image editor uses.
///
/// It is here because the desk behind the canvas is a teal-tinted glow, and a
/// picture with an alpha channel composited straight onto it came out green in
/// exactly the places that were meant to be empty. A neutral plate under the
/// image both fixes the tint and says which parts are see-through, which a flat
/// dark rectangle would hide.
///
/// The squares are barely separated on purpose: this has to read as absence
/// behind a dark artwork, not as a pattern competing with it.
class _Transparency extends StatelessWidget {
  const _Transparency();

  @override
  Widget build(BuildContext context) =>
      const CustomPaint(painter: _CheckerPainter());
}

class _CheckerPainter extends CustomPainter {
  const _CheckerPainter();

  static const _cell = 11.0;
  static const _dark = Color(0xFF111213);
  static const _light = Color(0xFF17191A);

  static ui.Image? _tile;

  /// One 2×2-cell tile, tiled by a repeating shader — the same treatment the
  /// editor's checker already had, while this one still emitted a drawRect per
  /// cell (~10k on a large canvas). It matters most during a crossfade, when the
  /// whole subtree is being rasterised into a layer.
  static ui.Image _buildTile() {
    final rec = ui.PictureRecorder();
    final c = Canvas(rec);
    c.drawRect(
      const Rect.fromLTWH(0, 0, _cell * 2, _cell * 2),
      Paint()..color = _dark,
    );
    final light = Paint()..color = _light;
    c.drawRect(const Rect.fromLTWH(0, 0, _cell, _cell), light);
    c.drawRect(const Rect.fromLTWH(_cell, _cell, _cell, _cell), light);
    return rec.endRecording().toImageSync(
      (_cell * 2).toInt(),
      (_cell * 2).toInt(),
    );
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
  bool shouldRepaint(_CheckerPainter old) => false;
}

/// The rounded, shadowed plate every canvas state sits in.
class _Framed extends StatelessWidget {
  const _Framed({required this.aspect, required this.child});

  final double aspect;
  final Widget Function(BoxConstraints) child;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.fromLTRB(24, 12, 24, 84),
    child: Center(
      child: AspectRatio(
        aspectRatio: aspect,
        child: LayoutBuilder(
          builder: (context, box) => Container(
            clipBehavior: Clip.antiAlias,
            decoration: BoxDecoration(
              borderRadius: BorderRadius.circular(10),
              boxShadow: const [
                BoxShadow(
                  color: Color(0x99000000),
                  blurRadius: 46,
                  offset: Offset(0, 18),
                ),
              ],
            ),
            child: Stack(
              fit: StackFit.expand,
              children: [const _Transparency(), child(box)],
            ),
          ),
        ),
      ),
    ),
  );
}

/// Draws an image, optionally only a sub-rectangle of it, stretched to fill.
class _ImagePainter extends CustomPainter {
  const _ImagePainter(this.image, this.src);
  final ui.Image image;
  final Rect? src;

  @override
  void paint(Canvas canvas, Size size) {
    final source =
        src ??
        Rect.fromLTWH(0, 0, image.width.toDouble(), image.height.toDouble());
    canvas.drawImageRect(
      image,
      source,
      Offset.zero & size,
      Paint()..filterQuality = FilterQuality.medium,
    );
  }

  @override
  bool shouldRepaint(_ImagePainter old) => old.image != image || old.src != src;
}

/// The draggable divider. Dragging the PICTURE is the gesture people try first,
/// so the whole canvas accepts it and the seam only shows where it currently is.
class _Seam extends StatelessWidget {
  const _Seam({required this.studio, required this.width});

  final Studio studio;
  final double width;

  // compare 1 is the whole result; the seam therefore sits at the LEFT edge
  // when it is 1 and travels right as the original is revealed.
  void _set(double x) => studio.setCompare((1 - x / width).clamp(0.0, 1.0));

  @override
  Widget build(BuildContext context) => Stack(
    children: [
      Positioned.fill(
        child: MouseRegion(
          cursor: SystemMouseCursors.resizeLeftRight,
          child: GestureDetector(
            behavior: HitTestBehavior.translucent,
            onHorizontalDragStart: (d) => _set(d.localPosition.dx),
            onHorizontalDragUpdate: (d) => _set(d.localPosition.dx),
          ),
        ),
      ),
      // Only the label moves with the drag; the gesture layer above is fixed.
      // Positioned.fill, not a bare child: _label returns a Stack whose only
      // child is Positioned, and a Stack with no non-positioned child takes the
      // SMALLEST size its constraints allow — as a loose child of this Stack
      // that is zero, and the label would be laid out against nothing. The fill
      // gives it the tight box the Positioned inside is measured against.
      // IgnorePointer so the drag layer beneath still gets every pointer.
      Positioned.fill(
        child: IgnorePointer(
          child: ValueListenableBuilder<double>(
            valueListenable: studio.compareN,
            builder: (context, cmp, _) => _label(context, cmp),
          ),
        ),
      ),
    ],
  );

  Widget _label(BuildContext context, double cmp) {
    final x = _ComparePainter.seamAt(
      width,
      1 - cmp,
      MediaQuery.devicePixelRatioOf(context),
    );
    return Stack(
      children: [
        if (cmp < 1)
          Positioned(
            left: x - 1,
            top: 0,
            bottom: 0,
            child: IgnorePointer(
              child: Column(
                mainAxisAlignment: MainAxisAlignment.center,
                children: [
                  // Only the label. The line itself belongs to the painter,
                  // which is the one thing that knows where the cut is.
                  Container(
                    padding: const EdgeInsets.symmetric(
                      horizontal: 7,
                      vertical: 3,
                    ),
                    decoration: BoxDecoration(
                      color: T.teal,
                      borderRadius: BorderRadius.circular(6),
                    ),
                    child: Text(
                      '${context.s('before')} · ${context.s('after')}',
                      style: T.text(10, color: T.ink, weight: FontWeight.w600),
                    ),
                  ),
                ],
              ),
            ),
          ),
      ],
    );
  }
}

/// The aspect a crop is locked to. Template is the shape of an FH6 panel — the
/// thing the crop usually has to fit — so it is offered by name rather than as a
/// number nobody would remember.
enum CropRatio { free, square, portrait, template }

double? _ratioOf(CropRatio r) => switch (r) {
  CropRatio.free => null,
  CropRatio.square => 1.0,
  CropRatio.portrait => 2 / 3,
  CropRatio.template => 3 / 4,
};

/// The crop rectangle, dragged directly on the source.
///
/// It is kept in the RAW file's pixels and only projected to the view for
/// drawing, so the number that reaches the engine never depends on the window
/// size — which is what a fractional crop would have quietly done.
class CropOverlay extends StatefulWidget {
  const CropOverlay({
    super.key,
    required this.studio,
    required this.view,
    required this.onDone,
  });

  final Studio studio;
  final Size view;
  final VoidCallback onDone;

  @override
  State<CropOverlay> createState() => _CropOverlayState();
}

class _CropOverlayState extends State<CropOverlay> {
  Rect? _rect; // in source pixels
  Offset? _dragStart;
  CropRatio _ratio = CropRatio.free;

  @override
  void initState() {
    super.initState();
    // Re-entering crop starts from the crop already applied, so a small
    // correction does not mean drawing the whole rectangle again.
    final r = widget.studio.region;
    if (r != null) {
      _rect = Rect.fromLTWH(
        r[0].toDouble(),
        r[1].toDouble(),
        r[2].toDouble(),
        r[3].toDouble(),
      );
    }
  }

  double get _scale => widget.view.width / widget.studio.sourceW;

  Rect get _current =>
      _rect ??
      Rect.fromLTWH(
        0,
        0,
        widget.studio.sourceW.toDouble(),
        widget.studio.sourceH.toDouble(),
      );

  Offset _toSource(Offset local) => Offset(
    (local.dx / _scale).clamp(0, widget.studio.sourceW.toDouble()),
    (local.dy / _scale).clamp(0, widget.studio.sourceH.toDouble()),
  );

  @override
  Widget build(BuildContext context) {
    final r = _current;
    final view = Rect.fromLTWH(
      r.left * _scale,
      r.top * _scale,
      r.width * _scale,
      r.height * _scale,
    );

    return Stack(
      fit: StackFit.expand,
      children: [
        GestureDetector(
          onPanStart: (d) =>
              setState(() => _dragStart = _toSource(d.localPosition)),
          onPanUpdate: (d) {
            final start = _dragStart;
            if (start == null) return;
            var now = _toSource(d.localPosition);
            // A locked aspect follows the WIDTH being drawn and derives the
            // height, so the rectangle tracks the pointer on one axis instead
            // of fighting it on both.
            final k = _ratioOf(_ratio);
            if (k != null) {
              final w = now.dx - start.dx;
              final h = w.abs() / k * (now.dy < start.dy ? -1 : 1);
              now = Offset(now.dx, start.dy + h);
            }
            setState(() => _rect = Rect.fromPoints(start, now));
          },
          onPanEnd: (_) {
            final drawn = _rect;
            // A stray click is not a crop: anything under a few pixels is
            // discarded rather than committed as a degenerate rectangle.
            if (drawn != null && (drawn.width < 8 || drawn.height < 8)) {
              setState(() => _rect = null);
            }
          },
          child: CustomPaint(painter: _CropPainter(view)),
        ),
        Positioned(
          left: 0,
          right: 0,
          bottom: 0,
          child: Container(
            padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 9),
            color: const Color(0xCC08090A),
            child: Row(
              children: [
                Text(
                  '${r.width.round()} × ${r.height.round()}',
                  style: T.monoText(12, color: T.body),
                ),
                const SizedBox(width: 12),
                for (final (ratio, label) in [
                  (CropRatio.free, context.s('free')),
                  (CropRatio.square, '1:1'),
                  (CropRatio.portrait, '2:3'),
                  (CropRatio.template, context.s('template')),
                ])
                  Padding(
                    padding: const EdgeInsets.only(right: 3),
                    child: _RatioChip(
                      label: label,
                      on: _ratio == ratio,
                      onTap: () => setState(() => _ratio = ratio),
                    ),
                  ),
                const Spacer(),
                Btn(
                  context.s('reset'),
                  onTap: () {
                    setState(() => _rect = null);
                    widget.studio.setRegion(null);
                    widget.onDone();
                  },
                ),
                const SizedBox(width: 7),
                Btn(
                  context.s('applyCrop'),
                  kind: BtnKind.primary,
                  onTap: () {
                    final drawn = _rect;
                    widget.studio.setRegion(
                      drawn == null
                          ? null
                          : [
                              drawn.left.round(),
                              drawn.top.round(),
                              math.max(1, drawn.width.round()),
                              math.max(1, drawn.height.round()),
                            ],
                    );
                    widget.onDone();
                  },
                ),
              ],
            ),
          ),
        ),
      ],
    );
  }
}

class _RatioChip extends StatelessWidget {
  const _RatioChip({
    required this.label,
    required this.on,
    required this.onTap,
  });

  final String label;
  final bool on;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      height: 22,
      padding: const EdgeInsets.symmetric(horizontal: 10),
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: on ? T.tealWash : hoverOver(T.fillSoft, hover, down),
        borderRadius: BorderRadius.circular(6),
      ),
      child: Text(
        label,
        style: T.text(
          11.5,
          color: on ? T.tealBright : (hover ? T.body : T.soft),
        ),
      ),
    ),
  );
}

class _CropPainter extends CustomPainter {
  const _CropPainter(this.rect);
  final Rect rect;

  @override
  void paint(Canvas canvas, Size size) {
    // Everything outside the selection is dimmed with one path rather than four
    // rectangles, so the corners cannot end up double-darkened.
    final outside = Path.combine(
      PathOperation.difference,
      Path()..addRect(Offset.zero & size),
      Path()..addRect(rect),
    );
    canvas.drawPath(outside, Paint()..color = const Color(0x99000000));
    canvas.drawRect(
      rect,
      Paint()
        ..style = PaintingStyle.stroke
        ..strokeWidth = 1.5
        ..color = T.teal,
    );

    final guide = Paint()
      ..color = const Color(0x33FFFFFF)
      ..strokeWidth = 1;
    for (var i = 1; i < 3; i++) {
      final x = rect.left + rect.width * i / 3;
      final y = rect.top + rect.height * i / 3;
      canvas.drawLine(Offset(x, rect.top), Offset(x, rect.bottom), guide);
      canvas.drawLine(Offset(rect.left, y), Offset(rect.right, y), guide);
    }

    final handle = Paint()..color = T.teal;
    for (final c in [
      rect.topLeft,
      rect.topRight,
      rect.bottomLeft,
      rect.bottomRight,
    ]) {
      canvas.drawRRect(
        RRect.fromRectAndRadius(
          Rect.fromCenter(center: c, width: 9, height: 9),
          const Radius.circular(2),
        ),
        handle,
      );
    }
  }

  @override
  bool shouldRepaint(_CropPainter old) => old.rect != rect;
}
