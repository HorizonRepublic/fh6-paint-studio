/// The command bar has to stay laid out at the narrow end, and Stop has to stay
/// hittable.
///
/// It did not: the running/idle clusters were made to cross-fade by wrapping
/// them in a `Flexible` next to the existing `Spacer`. Two flex children split
/// the free space between them, so the cluster floated in the middle of the bar
/// on a wide window — and on a narrow one with a wide locale the pair
/// manufactured an overflow that pushed Stop outside its own box, where a tap
/// could not reach it. Stopping a multi-minute fit is the one thing that must
/// never become unreachable.
library;

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:fh6_paint_studio/state/studio.dart';
import 'package:fh6_paint_studio/ui/shell.dart';
import 'package:fh6_paint_studio/ui/strings.dart';

Future<void> _pumpShell(WidgetTester tester, Size size, Lang lang) async {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);

  final studio = Studio()
    ..sourcePath = 'C:\\pictures\\a-fairly-long-source-name.png'
    ..sourceName = 'a-fairly-long-source-name.png';

  await tester.pumpWidget(
    MaterialApp(
      home: Scaffold(
        body: Strings(
          lang: lang,
          child: Shell(studio: studio, lang: lang, onLanguage: (_) {}),
        ),
      ),
    ),
  );
  await tester.pump();
}

void main() {
  // The floor the runner enforces (WM_GETMINMAXINFO in win32_window.cpp). Every
  // locale is checked, not a sample: the labels differ by hundreds of pixels
  // across the twelve, and picking the minimum by eye is what let the header
  // overflow in German while looking fine in English.
  for (final lang in languages) {
    testWidgets('the shell lays out at the minimum window size (${lang.code})', (
      tester,
    ) async {
      await _pumpShell(tester, const Size(1100, 680), lang);

      // A RenderFlex overflow arrives as an exception here — which is exactly
      // how the regression presented: controls pushed outside their own box,
      // where a tap cannot reach them.
      expect(tester.takeException(), isNull);
    });
  }

  testWidgets('the idle cluster sits at the right edge of the bar', (
    tester,
  ) async {
    await _pumpShell(tester, const Size(1600, 1000), languages.first);

    final bar = tester.getRect(find.byType(Shell));
    // Generate is the last thing in the bar; it must hug the right-hand side
    // rather than float somewhere in the middle of the free space.
    final generate = find.text(langFor('en').get('generate'));
    expect(generate, findsOneWidget);
    final box = tester.getRect(generate);
    expect(
      bar.right - box.right,
      lessThan(200),
      reason: 'the cluster drifted away from the right edge of the bar',
    );
  });
}
