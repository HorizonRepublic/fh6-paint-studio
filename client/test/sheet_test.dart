/// The advanced sheet now carries the whole expert block, which made it taller
/// than any window — it must scroll instead of overflowing, and every control
/// has to build without an engine behind it (knob defaults not yet fetched).
library;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:fh6_paint_studio/state/studio.dart';
import 'package:fh6_paint_studio/ui/sheet.dart';
import 'package:fh6_paint_studio/ui/strings.dart';

void main() {
  testWidgets('the expert sheet builds, scrolls and edits without an engine', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(1280, 800);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);

    final studio = Studio();
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: Strings(
            lang: languages.first,
            child: AdvancedSheet(studio: studio, onClose: () {}),
          ),
        ),
      ),
    );
    await tester.pump();
    expect(tester.takeException(), isNull);

    // The expert header is on screen; a knob further down is reachable by
    // scrolling, which is the whole point of the scroll container.
    expect(find.text('EXPERT TUNING'), findsOneWidget);
    final scrollable = find.byType(SingleChildScrollView);
    expect(scrollable, findsOneWidget);
    await tester.drag(scrollable, const Offset(0, -600));
    await tester.pump();
    expect(tester.takeException(), isNull);

    // Toggling a knob records an override; the reset-all clears it again.
    studio.setChoice('PolishIters', 777);
    await tester.pump();
    expect(studio.isOverridden('PolishIters'), isTrue);
    studio.clearChoices(expertKeys);
    await tester.pump();
    expect(studio.isOverridden('PolishIters'), isFalse);
    expect(tester.takeException(), isNull);
  });
}
