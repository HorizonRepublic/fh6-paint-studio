/// The verdict card and the activity log.
///
/// The card answers one question — is this good — and it answers it in the order
/// a person asks: what is happening, how far along, how close the result is, and
/// only then the detail. The log is collapsed to a single ticker line because
/// during a good run nobody reads it, and expanded when something went wrong,
/// which is the only time it matters.
library;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../state/studio.dart';
import 'strings.dart';
import 'tokens.dart';

/// The three phases a run passes through, as the user experiences them. The
/// engine's own stage names are more precise and less useful — "back-fit" is not
/// a thing anyone is waiting for.
enum RunPhase { build, polish, finish }

class ActivityCard extends StatelessWidget {
  const ActivityCard({
    super.key,
    required this.studio,
    required this.logOpen,
    required this.onToggleLog,
    required this.onEdit,
    required this.onExport,
    required this.onInject,
  });

  final Studio studio;
  final bool logOpen;
  final VoidCallback onToggleLog;
  final VoidCallback onEdit;
  final VoidCallback onExport;
  final VoidCallback onInject;

  @override
  Widget build(BuildContext context) {
    if (studio.phase == Phase.empty && studio.log.isEmpty) {
      return const SizedBox.shrink();
    }

    final (glyph, colour, title, sub) = switch (studio.phase) {
      Phase.running => (
        '●',
        T.teal,
        studio.stage.isEmpty ? context.s('building') : studio.stage,
        '${studio.shapes} / ${studio.total}',
      ),
      Phase.done => (
        '✓',
        T.green,
        context.s('finished'),
        context.s('readyToInject'),
      ),
      Phase.failed => (
        '!',
        T.danger,
        context.s('failed'),
        studio.failure ?? '',
      ),
      _ => ('', T.hint, context.s('ready'), ''),
    };

    return Glass(
      // live:false — MISSED in the earlier sweep, and this is the one panel
      // whose own content ticks 4x/s for the whole run while the canvas beneath
      // it changes ~20x/s. A BackdropFilter is never raster-cached, so it was
      // re-running a sigma-22 blur on every presented frame for minutes.
      live: false,
      child: SizedBox(
        width: 292,
        // The card grows rather than being replaced when a fit lands and its
        // three actions appear. After minutes of waiting this is the one moment
        // in the app that has earned a beat of motion — and the growth says
        // "the same card gained actions", where the jump read as a new card.
        child: AnimatedSize(
          duration: Motion.slow,
          curve: Motion.curve,
          alignment: Alignment.topCenter,
          child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            _Head(
              glyph: glyph,
              colour: colour,
              title: title,
              sub: sub,
              subMono: studio.phase == Phase.running,
              ssim: studio.ssim,
              deltaE: studio.deltaE,
            ),
            const _Rule(),
            _Phases(studio: studio),
            const _Rule(),
            if (studio.phase == Phase.done) ...[
              Padding(
                padding: const EdgeInsets.fromLTRB(10, 9, 10, 9),
                // Two equal halves, then the primary action across the full
                // width. Reflowing them freely put two buttons on one line and
                // one adrift on the next, which reads as a mistake rather than
                // as a layout; this is the same shape in every language.
                child: Column(
                  children: [
                    Row(
                      children: [
                        Expanded(
                          child: Btn(context.s('editShapes'), onTap: onEdit),
                        ),
                        const SizedBox(width: 7),
                        Expanded(
                          child: Btn(context.s('export'), onTap: onExport),
                        ),
                      ],
                    ),
                    const SizedBox(height: 7),
                    SizedBox(
                      width: double.infinity,
                      child: Btn(
                        context.s('inject'),
                        kind: BtnKind.primary,
                        onTap: studio.injectAvailable ? onInject : null,
                      ),
                    ),
                  ],
                ),
              ),
              const _Rule(),
            ],
            _Ticker(studio: studio, open: logOpen, onTap: onToggleLog),
          ],
          ),
        ),
      ),
    );
  }
}

class _Head extends StatelessWidget {
  const _Head({
    required this.glyph,
    required this.colour,
    required this.title,
    required this.sub,
    this.subMono = false,
    required this.ssim,
    required this.deltaE,
  });

  final String glyph;
  final Color colour;
  final String title;
  final String sub;

  /// True when [sub] is a live count. A number that changes three thousand
  /// times over a run re-measures the line on every digit in a proportional
  /// face, so the subtitle visibly twitches for the whole fit — the one thing
  /// the user watches longest.
  final bool subMono;
  final double? ssim;
  final double? deltaE;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.fromLTRB(13, 11, 13, 10),
    child: Row(
      children: [
        Container(
          width: 22,
          height: 22,
          alignment: Alignment.center,
          decoration: BoxDecoration(
            color: colour.withValues(alpha: 0.16),
            shape: BoxShape.circle,
          ),
          child: Text(glyph, style: T.text(10, color: colour)),
        ),
        const SizedBox(width: 9),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                title,
                style: T.text(13, color: T.title, weight: FontWeight.w600),
              ),
              Text(
                sub,
                style: subMono
                    ? T.monoText(11, color: T.hint)
                    : T.text(11, color: T.hint),
                overflow: TextOverflow.ellipsis,
              ),
            ],
          ),
        ),
        if (ssim != null) _Metric('SSIM', ssim!.toStringAsFixed(2)),
        if (ssim != null && deltaE != null) const SizedBox(width: 12),
        if (deltaE != null) _Metric('ΔE', deltaE!.toStringAsFixed(1)),
      ],
    ),
  );
}

class _Metric extends StatelessWidget {
  const _Metric(this.label, this.value);
  final String label;
  final String value;

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.end,
    children: [
      Text(label, style: T.label),
      Text(value, style: T.monoText(14, color: T.title)),
    ],
  );
}

/// The three phase rows. A done row carries the time it took; the active one
/// ticks; the ones ahead are outlines. It is the same list in every state, so
/// the card does not reshuffle itself as a run progresses.
class _Phases extends StatelessWidget {
  const _Phases({required this.studio});
  final Studio studio;

  @override
  Widget build(BuildContext context) {
    // The engine names its post-greedy work in the status line; anything with a
    // stage set means the shape building is behind us.
    final building = studio.phase == Phase.running && studio.stage.isEmpty;
    final polishing = studio.phase == Phase.running && studio.stage.isNotEmpty;
    final done = studio.phase == Phase.done;

    int stateOf(RunPhase p) => switch (p) {
      RunPhase.build => building ? 1 : (polishing || done ? 2 : 0),
      RunPhase.polish => polishing ? 1 : (done ? 2 : 0),
      RunPhase.finish => done ? 2 : 0,
    };

    // Each row reports ITS OWN span. Printing the run's total elapsed on all
    // three, which is what this did, said that every phase took the whole run.
    // A run opened from the library was not timed here, so every row would
    // report a confident 0:00 for work this session never watched happen.
    final timed = studio.elapsed > Duration.zero;
    final build = studio.buildEnd;
    final polish = studio.polishEnd;
    String timeOf(RunPhase p) {
      final polishEnd = polish ?? (polishing ? studio.elapsed : null);
      final span = switch (p) {
        RunPhase.build => build ?? (building ? studio.elapsed : null),
        // Null when the run was stopped between the two phases, which is a real
        // state and must not be an exception.
        RunPhase.polish =>
          (build == null || polishEnd == null) ? null : polishEnd - build,
        RunPhase.finish => done ? studio.elapsed : null,
      };
      return span == null || !timed ? '—' : _mmss(span);
    }

    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 7),
      child: Column(
        children: [
          for (final (p, key) in const [
            (RunPhase.build, 'buildShapes'),
            (RunPhase.polish, 'polishEdges'),
            (RunPhase.finish, 'finish'),
          ])
            _PhaseRow(
              state: stateOf(p),
              label: context.s(key),
              time: timeOf(p),
            ),
        ],
      ),
    );
  }

  static String _mmss(Duration d) =>
      '${d.inMinutes}:${(d.inSeconds % 60).toString().padLeft(2, '0')}';
}

class _PhaseRow extends StatelessWidget {
  const _PhaseRow({
    required this.state,
    required this.label,
    required this.time,
  });

  final int state; // 0 ahead, 1 active, 2 done
  final String label;
  final String time;

  @override
  // Animated because "the build finished, the polish has begun" is the only
  // substantive news a multi-minute run ever reports, and it used to arrive as
  // a one-frame swap of four colours — blink and it never happened.
  // NB the border is transparent rather than null in states 1 and 2: a null
  // border cannot lerp, so it would snap while the fill eased.
  Widget build(BuildContext context) => AnimatedContainer(
    duration: Motion.base,
    curve: Motion.curve,
    height: 26,
    padding: const EdgeInsets.symmetric(horizontal: 5),
    decoration: BoxDecoration(
      color: state == 1 ? const Color(0x1A54CBB8) : const Color(0x0054CBB8),
      borderRadius: BorderRadius.circular(7),
    ),
    child: Row(
      children: [
        AnimatedContainer(
          duration: Motion.base,
          curve: Motion.curve,
          width: 16,
          height: 16,
          alignment: Alignment.center,
          decoration: BoxDecoration(
            shape: BoxShape.circle,
            color: switch (state) {
              2 => const Color(0x295FD08D),
              1 => const Color(0x3354CBB8),
              _ => const Color(0x0054CBB8),
            },
            border: Border.all(
              color: state == 0 ? T.border : const Color(0x00FFFFFF),
              width: 1.5,
            ),
          ),
          child: AnimatedSwitcher(
            duration: Motion.base,
            child: Text(
              state == 2 ? '✓' : (state == 1 ? '●' : ''),
              key: ValueKey(state),
              style: T.text(9, color: state == 2 ? T.green : T.teal),
            ),
          ),
        ),
        const SizedBox(width: 9),
        Text(label, style: T.text(12, color: state == 0 ? T.faint : T.body)),
        const Spacer(),
        Text(time, style: T.monoText(11, color: state == 1 ? T.teal : T.hint)),
      ],
    ),
  );
}

/// One line of the log, and the handle that opens all of it.
class _Ticker extends StatelessWidget {
  const _Ticker({
    required this.studio,
    required this.open,
    required this.onTap,
  });

  final Studio studio;
  final bool open;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final last = studio.log.isEmpty ? '' : studio.log.last.text;
    return Pressable(
      onTap: onTap,
      builder: (context, hover, down) => AnimatedContainer(
        duration: Motion.fast,
        height: 32,
        padding: const EdgeInsets.symmetric(horizontal: 11),
        color: hoverOver(const Color(0x00000000), hover, down),
        child: Row(
          children: [
            Container(
              width: 5,
              height: 5,
              decoration: BoxDecoration(
                shape: BoxShape.circle,
                color: studio.isRunning ? T.teal : T.faint,
              ),
            ),
            const SizedBox(width: 8),
            Expanded(
              child: Text(
                last,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: T.monoText(10.5, color: T.hint),
              ),
            ),
            const SizedBox(width: 6),
            Text(
              '${studio.log.length}',
              style: T.monoText(10.5, color: T.faint),
            ),
            const SizedBox(width: 5),
            Text(open ? '▾' : '▴', style: T.text(9, color: T.hint)),
          ],
        ),
      ),
    );
  }
}

/// The full log, as a drawer along the bottom. Filters exist because a failed
/// run's one useful line is otherwise buried under sixty routine ones.
class LogDrawer extends StatefulWidget {
  const LogDrawer({super.key, required this.studio, required this.onClose});

  final Studio studio;
  final VoidCallback onClose;

  @override
  State<LogDrawer> createState() => _LogDrawerState();
}

class _LogDrawerState extends State<LogDrawer> {
  String _filter = 'all';
  final _scroll = ScrollController();

  @override
  void dispose() {
    _scroll.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final lines = widget.studio.log.where((l) {
      if (_filter == 'all') return true;
      if (_filter == 'problems') return l.level == 'error' || l.level == 'warn';
      return l.level == 'done';
    }).toList();

    // live:false — the drawer sits over its own content, not the animating canvas; a static
    // blur snapshot reads the same and stops re-blurring 20×/s during a fit.
    return Glass(
      live: false,
      child: SizedBox(
        width: 720,
        height: 260,
        child: Column(
          children: [
            SizedBox(
              height: 36,
              child: Row(
                children: [
                  const SizedBox(width: 11),
                  Text(context.s('activity').toUpperCase(), style: T.label),
                  const Spacer(),
                  for (final (key, label) in [
                    ('all', context.s('all')),
                    ('problems', context.s('problems')),
                    ('results', context.s('results')),
                  ])
                    Padding(
                      padding: const EdgeInsets.only(right: 3),
                      child: _Chip(
                        label: label,
                        on: _filter == key,
                        onTap: () => setState(() => _filter = key),
                      ),
                    ),
                  const SizedBox(width: 8),
                  Btn(
                    context.s('copy'),
                    onTap: () => Clipboard.setData(
                      ClipboardData(
                        text: lines
                            .map((l) => '${l.time}  ${l.text}')
                            .join('\n'),
                      ),
                    ),
                  ),
                  const SizedBox(width: 5),
                  Btn(
                    context.s('clear'),
                    onTap: () => setState(widget.studio.clearLog),
                  ),
                  const SizedBox(width: 5),
                  Btn(context.s('close'), onTap: widget.onClose),
                  const SizedBox(width: 11),
                ],
              ),
            ),
            const _Rule(),
            Expanded(
              // The log holds the last 500 lines: a scrollbar is how you see
              // you are not at the end, and how you get back with the mouse.
              child: Scrollbar(
                controller: _scroll,
                child: ListView.builder(
                controller: _scroll,
                reverse: true,
                padding: const EdgeInsets.fromLTRB(11, 8, 11, 10),
                itemCount: lines.length,
                itemBuilder: (context, i) {
                  final l = lines[lines.length - 1 - i];
                  return Padding(
                    padding: const EdgeInsets.only(bottom: 4),
                    child: Row(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          l.time,
                          // Was an inline #5C6169 — a seventh grey below the
                          // whole ramp, and under the readable minimum.
                          style: T.monoText(11.5, color: T.hint),
                        ),
                        const SizedBox(width: 10),
                        SizedBox(
                          width: 52,
                          child: Text(
                            l.level,
                            style: T.monoText(
                              11.5,
                              color: switch (l.level) {
                                'error' => T.danger,
                                'warn' => T.amber,
                                'done' => T.green,
                                _ => T.faint,
                              },
                            ),
                          ),
                        ),
                        Expanded(
                          child: SelectableText(
                            l.text,
                            style: T.monoText(11.5, color: T.dim),
                          ),
                        ),
                      ],
                    ),
                  );
                },
              ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _Chip extends StatelessWidget {
  const _Chip({required this.label, required this.on, required this.onTap});
  final String label;
  final bool on;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      height: 22,
      padding: const EdgeInsets.symmetric(horizontal: 9),
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: on ? T.tealWash : hoverOver(T.fillSoft, hover, down),
        borderRadius: BorderRadius.circular(6),
      ),
      child: Text(
        label,
        style: T.text(11, color: on ? T.tealBright : (hover ? T.body : T.soft)),
      ),
    ),
  );
}

class _Rule extends StatelessWidget {
  const _Rule();
  @override
  Widget build(BuildContext context) => Container(height: 1, color: T.hairline);
}
