/// How to use this, in five steps.
///
/// It sits behind a button rather than a first-run tour because the two things
/// a newcomer cannot guess are not about this app at all: that the game has to
/// be running with a template group open, and that the vinyl must be saved and
/// reloaded before the result is visible. Everything else is discoverable.
///
/// Written in the app's own language, so the steps are translated with the rest
/// of the interface.
library;

import 'package:flutter/material.dart';

import 'strings.dart';
import 'tokens.dart';

class HelpSheet extends StatelessWidget {
  const HelpSheet({super.key, required this.onClose});

  final VoidCallback onClose;

  static const _stepKeys = [
    ('helpStep1', 'helpStep1Body'),
    ('helpStep2', 'helpStep2Body'),
    ('helpStep3', 'helpStep3Body'),
    ('helpStep4', 'helpStep4Body'),
    ('helpStep5', 'helpStep5Body'),
  ];

  @override
  Widget build(BuildContext context) => Center(
    child: Glass(
      radius: 16,
      // See AboutSheet: opaque scrim above it, so the live blur only costs.
      live: false,
      child: SizedBox(
        width: 560,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 15, 16, 12),
              child: Row(
                children: [
                  Text(
                    context.s('help'),
                    style: T.text(16, color: T.title, weight: FontWeight.w600),
                  ),
                  const Spacer(),
                  Btn(context.s('close'), onTap: onClose),
                ],
              ),
            ),
            Container(height: 1, color: T.hairline),
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 16, 6),
              child: Column(
                children: [
                  for (var i = 0; i < _stepKeys.length; i++)
                    _Step(
                      number: i + 1,
                      title: context.s(_stepKeys[i].$1),
                      body: context.s(_stepKeys[i].$2),
                    ),
                ],
              ),
            ),
            Container(height: 1, color: T.hairline),
            // The bindings were invisible: only Ctrl+Enter is advertised, on the
            // Generate button. The key names stay literal — a keyboard is
            // labelled the same in every language this app speaks.
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 16, 6),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(context.s('shortcuts').toUpperCase(), style: T.label),
                  const SizedBox(height: 8),
                  for (final (keys, labelKey) in [
                    ('Ctrl ⏎', 'generate'),
                    ('Ctrl O', 'chooseFile'),
                    ('F1', 'help'),
                    ('Esc', 'close'),
                    ('Ctrl Z / Ctrl Y', 'undo'),
                    ('Ctrl D', 'duplicate'),
                    ('← ↑ → ↓ / Shift', 'moveHere'),
                    ('Del', 'delete'),
                  ])
                    _Key(keys: keys, label: context.s(labelKey)),
                ],
              ),
            ),
            Container(height: 1, color: T.hairline),
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 11, 16, 14),
              child: Text(
                context.s('helpWarning'),
                style: T.text(11.5, color: T.amber),
              ),
            ),
          ],
        ),
      ),
    ),
  );
}

class _Key extends StatelessWidget {
  const _Key({required this.keys, required this.label});

  final String keys;
  final String label;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.only(bottom: 6),
    child: Row(
      children: [
        SizedBox(
          width: 132,
          child: Text(keys, style: T.monoText(11, color: T.tealBright)),
        ),
        Expanded(child: Text(label, style: T.text(11.5, color: T.hint))),
      ],
    ),
  );
}

class _Step extends StatelessWidget {
  const _Step({required this.number, required this.title, required this.body});

  final int number;
  final String title;
  final String body;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.only(bottom: 12),
    child: Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Container(
          width: 20,
          height: 20,
          alignment: Alignment.center,
          decoration: const BoxDecoration(
            color: T.tealFaint,
            shape: BoxShape.circle,
          ),
          child: Text('$number', style: T.monoText(11, color: T.tealBright)),
        ),
        const SizedBox(width: 11),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                title,
                style: T.text(13, color: T.body, weight: FontWeight.w500),
              ),
              const SizedBox(height: 2),
              Text(body, style: T.text(11.5, color: T.hint)),
            ],
          ),
        ),
      ],
    ),
  );
}
