/// The caption buttons have to stay clickable on the EMPTY screen.
///
/// They did not: the runner learns where its drag band ends from one
/// fire-and-forget channel report, and when that report was lost the band kept
/// the runner's startup guess — every caption button left of that line dragged
/// the window instead of clicking, for the whole session. Three guards here:
/// the report retries until the runner answers, the startup guess covers the
/// widest locale's cluster, and the buttons actually open what they say.
library;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

import 'package:fh6_paint_studio/state/studio.dart';
import 'package:fh6_paint_studio/ui/activity.dart';
import 'package:fh6_paint_studio/ui/shell.dart';
import 'package:fh6_paint_studio/ui/sheet.dart';
import 'package:fh6_paint_studio/ui/strings.dart';
import 'package:fh6_paint_studio/ui/window.dart';

/// Mirrors g_controls_dip's initial value in windows/runner/flutter_window.cpp.
/// If this test fails after a locale change, grow BOTH numbers.
const kStartupControlsDip = 1050;

Future<void> _pumpEmptyShell(WidgetTester tester, Size size, Lang lang) async {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);

  final studio = Studio();

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
  // The test font (Ahem) is wider than any real one, so a cluster that fits
  // under the startup band here fits at runtime too.
  for (final lang in languages) {
    testWidgets('the startup drag band spares the whole cluster (${lang.code})', (
      tester,
    ) async {
      await _pumpEmptyShell(tester, const Size(1600, 1000), lang);

      final cluster = tester.getRect(find.byType(CaptionControls));
      expect(
        cluster.width,
        lessThan(kStartupControlsDip.toDouble()),
        reason:
            'the caption cluster outgrew the runner startup band: buttons '
            'left of the band drag the window instead of clicking until the '
            'first width report lands',
      );
    });
  }

  // The runner's drag band is measured from the window's RIGHT edge, so the
  // cluster must actually sit there. It did not: Flexible + Spacer split the
  // header's free space and parked the cluster up to hundreds of px short of
  // the edge, where the band swallowed its left half — dead caption buttons
  // that no widget test of the cluster alone could see.
  for (final width in const [1100.0, 1600.0, 2200.0]) {
    for (final lang in languages) {
      testWidgets('the cluster hugs the right edge at $width (${lang.code})', (
        tester,
      ) async {
        await _pumpEmptyShell(tester, Size(width, 1000), lang);
        final shell = tester.getRect(find.byType(Shell));
        final cluster = tester.getRect(find.byType(CaptionControls));
        expect(
          shell.right - cluster.right,
          lessThan(1.0),
          reason: 'the caption cluster drifted from the right edge: the '
              'native drag band would cover its left buttons',
        );
      });
    }
  }

  testWidgets('empty screen: settings opens from the header', (tester) async {
    await _pumpEmptyShell(tester, const Size(1600, 1000), langFor('uk'));
    await tester.tap(find.text('⚙'));
    await tester.pumpAndSettle();
    expect(find.byType(SettingsSheet), findsOneWidget);
  });

  testWidgets('empty screen: the language menu opens from the header', (
    tester,
  ) async {
    final uk = langFor('uk');
    await _pumpEmptyShell(tester, const Size(1600, 1000), uk);
    await tester.tap(find.text(uk.endonym));
    await tester.pumpAndSettle();
    // Deutsch appears nowhere but in the open menu.
    expect(find.text('Deutsch'), findsOneWidget);
  });

  testWidgets('empty screen: the log opens from the header', (tester) async {
    final uk = langFor('uk');
    await _pumpEmptyShell(tester, const Size(1600, 1000), uk);
    await tester.tap(find.text(uk.get('logButton')));
    await tester.pumpAndSettle();
    expect(find.byType(LogDrawer), findsOneWidget);
  });

  // The settings sheet holds translated labels at a fixed width; the wide
  // locales overflowed its diagnostics row (23px in Ukrainian under Ahem).
  for (final lang in languages) {
    testWidgets('the settings sheet lays out (${lang.code})', (tester) async {
      await _pumpEmptyShell(tester, const Size(1600, 1000), lang);
      await tester.tap(find.text('⚙'));
      await tester.pumpAndSettle();
      expect(find.byType(SettingsSheet), findsOneWidget);
      expect(tester.takeException(), isNull);
    });
  }

  testWidgets('the width report retries until the runner answers', (
    tester,
  ) async {
    var attempts = 0;
    var delivered = -1;
    tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
      const MethodChannel('fh6/window'),
      (call) async {
        if (call.method == 'setControlsWidth') {
          attempts++;
          if (attempts < 3) {
            throw PlatformException(code: 'lost');
          }
          delivered = call.arguments as int;
        }
        return null;
      },
    );
    addTearDown(
      () => tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        const MethodChannel('fh6/window'),
        null,
      ),
    );

    await tester.pumpWidget(
      const MaterialApp(
        home: Row(
          children: [
            Spacer(),
            CaptionControls(children: [SizedBox(width: 600, height: 52)]),
          ],
        ),
      ),
    );
    await tester.pump();

    // Two lost reports, half a second apart, then the one that lands.
    await tester.pump(const Duration(milliseconds: 600));
    await tester.pump(const Duration(milliseconds: 600));
    await tester.pump(const Duration(milliseconds: 600));

    expect(attempts, greaterThanOrEqualTo(3));
    // What travels is the distance from the window's right edge to the
    // cluster's left edge — here the cluster is flush right, so its width.
    expect(delivered, 600);
  });
}
