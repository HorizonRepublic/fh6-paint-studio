// Every language has to carry every key. A missing one falls back to English
// silently, which is the failure that ships: the app looks translated until the
// one screen nobody checked.

import 'package:fh6_paint_studio/ui/strings.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  test('every language defines every key', () {
    final english = languages.first;
    expect(english.code, 'en');

    for (final lang in languages) {
      for (final key in english.keys) {
        // Presence, not string comparison: a word that is identical in two
        // languages ("Export") is not a missing translation, and comparing
        // values cannot tell the two apart.
        expect(lang.has(key), isTrue, reason: '${lang.code} is missing "$key"');
        expect(lang.get(key), isNotEmpty, reason: '${lang.code}/$key is empty');
      }
      expect(
        lang.keys.length,
        english.keys.length,
        reason: '${lang.code} has a different number of keys',
      );
    }
  });

  test('the twelve locales match the ones the project ships', () {
    expect(languages.map((l) => l.code).toList(), [
      'en',
      'uk',
      'de',
      'es',
      'pt-BR',
      'fr',
      'pl',
      'it',
      'tr',
      'zh-CN',
      'ja',
      'ko',
    ]);
  });

  test('a system locale picks the closest language, not English', () {
    // The regional variants are the case that matters: pt-PT and de-AT have no
    // catalogue of their own and must not fall to English.
    expect(langForSystem('uk_UA').code, 'uk');
    expect(langForSystem('pt-PT').code, 'pt-BR');
    expect(langForSystem('de-AT').code, 'de');
    expect(langForSystem('zh-CN').code, 'zh-CN');
    expect(langForSystem('nl-NL').code, 'en'); // genuinely unsupported
  });
}
