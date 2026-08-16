import 'package:fh6_paint_studio/state/studio.dart';
import 'package:fh6_paint_studio/ui/sheet.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  test('an expert override is marked, applied and clearable', () {
    final s = Studio();
    expect(s.isOverridden('PolishIters'), isFalse);

    s.setChoice('PolishIters', 500);
    expect(s.isOverridden('PolishIters'), isTrue);
    expect(s.knobValue('PolishIters'), 500);
    expect(s.choices['PolishIters'], 500);

    // Clearing puts the engine's sentinel back — before a connection that is
    // null, and either way the knob follows the preset again.
    s.clearChoice('PolishIters');
    expect(s.isOverridden('PolishIters'), isFalse);
    expect(s.choices['PolishIters'], isNull);
  });

  test('reset-all clears every expert knob at once', () {
    final s = Studio();
    s.setChoice('Random', 100000);
    s.setChoice('Grid', 96);
    s.setChoice('Quality', 'ultra');
    s.clearChoices(expertKeys);
    for (final k in expertKeys) {
      expect(s.isOverridden(k), isFalse, reason: k);
    }
  });

  test('an untouched knob displays the engine-reported default', () {
    final s = Studio();
    expect(s.knobValue('Random'), isNull); // nothing fetched yet
    s.setChoice('Random', 12345);
    expect(s.knobValue('Random'), 12345); // the override wins over any default
  });
}
