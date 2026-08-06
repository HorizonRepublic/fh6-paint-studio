/// What this is, and the two facts a user needs when something goes wrong.
///
/// Which backend is doing the work, and whether the engine can write to the
/// game at all. Both are asked of the ENGINE rather than assumed here, because
/// the engine is a separate process and its answers are the ones that decide
/// whether an injection will land.
library;

import 'package:flutter/material.dart';

import '../state/studio.dart';
import 'strings.dart';
import 'tokens.dart';

class AboutSheet extends StatelessWidget {
  const AboutSheet({
    super.key,
    required this.studio,
    required this.onClose,
    required this.onOpenLibraryFolder,
  });

  final Studio studio;
  final VoidCallback onClose;
  final VoidCallback onOpenLibraryFolder;

  @override
  Widget build(BuildContext context) => Center(
    child: Glass(
      radius: 16,
      child: SizedBox(
        width: 420,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 16, 16, 12),
              child: Row(
                children: [
                  const HalftoneMark(size: 34),
                  const SizedBox(width: 12),
                  Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        'FH6 Paint Studio',
                        style: T.text(
                          16,
                          color: T.title,
                          weight: FontWeight.w600,
                        ),
                      ),
                      Text(
                        context.s('tagline'),
                        style: T.text(11.5, color: T.hint),
                      ),
                    ],
                  ),
                  const Spacer(),
                  Btn(context.s('close'), onTap: onClose),
                ],
              ),
            ),
            Container(height: 1, color: T.hairline),
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 16, 8),
              child: Column(
                children: [
                  _Row(
                    context.s('aboutEngine'),
                    studio.backend.isEmpty
                        ? context.s('aboutIdle')
                        : studio.backend,
                  ),
                  _Row(
                    context.s('aboutInjection'),
                    studio.injectAvailable
                        ? context.s('injReady')
                        : context.s('injUnavailable'),
                  ),
                  _Row(context.s('savedRuns'), '${studio.entries.length}'),
                ],
              ),
            ),
            Container(height: 1, color: T.hairline),
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 11, 16, 14),
              child: Row(
                children: [
                  Expanded(
                    child: Text(
                      context.s('injectWarn'),
                      style: T.text(11, color: T.hint),
                    ),
                  ),
                  const SizedBox(width: 10),
                  Btn(
                    context.s('openLibraryFolder'),
                    onTap: onOpenLibraryFolder,
                  ),
                ],
              ),
            ),
          ],
        ),
      ),
    ),
  );
}

class _Row extends StatelessWidget {
  const _Row(this.label, this.value);
  final String label;
  final String value;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.symmetric(vertical: 5),
    child: Row(
      children: [
        Text(label, style: T.text(12.5, color: T.soft)),
        const Spacer(),
        Text(value, style: T.monoText(12, color: T.body)),
      ],
    ),
  );
}
