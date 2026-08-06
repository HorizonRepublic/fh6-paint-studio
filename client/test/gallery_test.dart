/// The gallery has to survive a dialog opening over it.
///
/// It did not: the shell's overlays were unkeyed children of one Stack, so the
/// confirmation appearing re-matched every later child against a different
/// widget and threw away the gallery's State — its filter and its search text
/// went with it, and the filter chips stopped responding.
library;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:fh6_paint_studio/state/studio.dart';
import 'package:fh6_paint_studio/ui/confirm.dart';
import 'package:fh6_paint_studio/ui/gallery.dart';
import 'package:fh6_paint_studio/ui/strings.dart';

void main() {
  testWidgets('the gallery keeps its state while a dialog opens over it', (
    tester,
  ) async {
    // A real window, not the 800x600 default: the gallery header lays its
    // filters out beside the sort and search controls, and at 800px they are
    // legitimately scrolled out of reach.
    tester.view.physicalSize = const Size(1600, 1000);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    final studio = Studio()
      ..entries.addAll([
        {'id': 'a', 'name': 'one', 'preset': 'anime', 'shapes': 10},
        {'id': 'b', 'name': 'two', 'preset': 'photo', 'shapes': 20},
      ]);

    Confirm? pending;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: Strings(
            lang: languages.first,
            child: StatefulBuilder(
              builder: (context, setState) => Stack(
                children: [
                  Positioned.fill(
                    key: const ValueKey('gallery'),
                    child: Gallery(
                      studio: studio,
                      onClose: () {},
                      onConfirm: (c) => setState(() => pending = c),
                    ),
                  ),
                  if (pending != null)
                    Positioned.fill(
                      key: const ValueKey('confirm'),
                      child: ConfirmDialog(
                        confirm: pending!,
                        onDismiss: () => setState(() => pending = null),
                      ),
                    ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
    await tester.pump();

    final galleryState = tester.state(find.byType(Gallery));

    // Raise a question, then dismiss it.
    (galleryState as dynamic).widget.onConfirm(
      Confirm(
        title: 't',
        body: 'b',
        action: 'a',
        destructive: true,
        onConfirm: () {},
      ),
    );
    await tester.pump();
    expect(find.byType(ConfirmDialog), findsOneWidget);

    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();
    expect(find.byType(ConfirmDialog), findsNothing);

    // The SAME State object has to still be there: a new one means the filter
    // and the search box were silently reset.
    expect(identical(tester.state(find.byType(Gallery)), galleryState), isTrue);

    // And the filters still answer.
    await tester.tap(find.text('Photo'));
    await tester.pump();
    expect(find.text('one'), findsNothing);
    expect(find.text('two'), findsOneWidget);
  });
}
