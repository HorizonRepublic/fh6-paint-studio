/// The editor document's structural invariants.
///
/// Each of these is a defect that shipped: they are cheap to reintroduce and
/// invisible until a user loses work, so they get a test rather than a comment.
library;

import 'package:flutter_test/flutter_test.dart';

import 'package:fh6_paint_studio/state/editor.dart';

/// A document with a background plus [n] shapes, all in one layer. No engine:
/// every assertion here is about the document, and refresh() no-ops without one.
Editor _doc(int n) {
  final d = Editor(() => null);
  d.load({
    'shapes': [
      {
        'type': 1,
        'data': [0.0, 0.0, 64.0, 64.0],
        'color': [0, 0, 0, 255],
      },
      for (var i = 0; i < n; i++)
        {
          'type': 0,
          'data': [8.0 + i, 8.0 + i, 4.0, 4.0, 0.0, 0.0],
          'color': [200, 100, 50, 255],
        },
    ],
  }, 64, 64);
  return d;
}

void main() {
  test('toJson hands out copies, not the live lists', () {
    final d = _doc(1);
    final json = d.shapes[1].toJson();
    final data = json['data'] as List;
    final color = json['color'] as List;
    d.shapes[1].data[0] = 999;
    d.shapes[1].color[0] = 1;
    expect(data[0], isNot(999), reason: 'an edit after the export reached it');
    expect(color[0], isNot(1));
  });

  test('the background cannot be duplicated', () {
    final d = _doc(1);
    d.select(0);
    d.duplicateSelected();
    expect(d.shapes.length, 2, reason: 'a second full-canvas rect was inserted');
  });

  test('duplicating clears the move-group whose indices it shifts', () {
    final d = _doc(3);
    d.select(1);
    d.toggleExtra(2);
    expect(d.extra, isNotEmpty);
    d.duplicateSelected();
    expect(d.extra, isEmpty);
  });

  test('undo restores the layers, not just the shapes', () {
    final d = _doc(1);
    final before = d.layers.length; // load() proposes layers from the geometry
    d.addLayer('second');
    final second = d.layers.last.id;
    d.shapes[1].layer = second;
    expect(d.layers.length, before + 1);
    d.removeLayer(second);
    expect(d.layers.length, before);
    d.undo();
    expect(d.layers.length, before + 1, reason: 'the layer did not come back');
    expect(d.layers.any((l) => l.id == second), isTrue);
    expect(d.shapes[1].layer, second, reason: 'the shape stayed reassigned');
  });

  test('locking a layer drops its shapes from the move-group', () {
    final d = _doc(3);
    d.addLayer('second');
    final second = d.layers.last.id;
    d.shapes[2].layer = second;
    d.select(1);
    d.toggleExtra(2);
    expect(d.extra, contains(2));
    d.setLayerLocked(second, true);
    expect(d.extra, isNot(contains(2)));
  });

  test('a new shape never lands in a hidden layer', () {
    final d = _doc(1);
    d.setLayerHidden(d.layers.first.id, true);
    d.addShape(EditShape(0, [4.0, 4.0, 2.0, 2.0, 0.0, 0.0], [1, 2, 3, 255]));
    final home = d.layerOf(d.shapes.last.layer);
    expect(home, isNotNull);
    expect(home!.hidden, isFalse);
  });

  test('removing a layer hands its shapes to the neighbour, not the bottom', () {
    final d = _doc(1);
    d.addLayer('mid');
    final mid = d.layers.last.id;
    d.addLayer('top');
    final top = d.layers.last.id;
    d.shapes[1].layer = top;
    d.removeLayer(top);
    expect(d.shapes[1].layer, mid);
  });
}
