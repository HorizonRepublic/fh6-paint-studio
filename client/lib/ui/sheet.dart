/// The settings behind the two chips.
///
/// Every switch here is a measured trade, not a preference, so each one says
/// what it costs. Two of them — the contour and false-edge terms — are the only
/// knobs in the whole engine whose sign is not consistent across pictures: each
/// helps about half of them and hurts the rest by a few percent. No automatic
/// chooser survived replication at that effect size, which is exactly why they
/// are a toggle and not a default.
library;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../state/logfile.dart';
import '../state/studio.dart';
import 'strings.dart';
import 'tokens.dart';

void _copyPath() {
  final p = AppLog.path;
  if (p != null) Clipboard.setData(ClipboardData(text: p));
}

class AdvancedSheet extends StatelessWidget {
  const AdvancedSheet({super.key, required this.studio, required this.onClose});

  final Studio studio;
  final VoidCallback onClose;

  String get _edge => studio.choices['EdgeTerms'] as String? ?? '';

  /// The two edge terms live in one string field, so a toggle has to compose
  /// with the other rather than overwrite it.
  void _setEdge({bool? eagle, bool? fe}) {
    final noEagle = eagle ?? (_edge == 'no-eagle' || _edge == 'no-both');
    final noFe = fe ?? (_edge == 'no-fe' || _edge == 'no-both');
    final value = switch ((noEagle, noFe)) {
      (true, true) => 'no-both',
      (true, false) => 'no-eagle',
      (false, true) => 'no-fe',
      _ => '',
    };
    studio.setChoice('EdgeTerms', value);
  }

  @override
  Widget build(BuildContext context) {
    final noEagle = _edge == 'no-eagle' || _edge == 'no-both';
    final noFe = _edge == 'no-fe' || _edge == 'no-both';
    final mono = (studio.choices['MonoColor'] as String? ?? '').isNotEmpty;

    return Center(
      child: Glass(
        radius: 16,
        child: SizedBox(
          width: 560,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(15, 14, 15, 12),
                child: Row(
                  children: [
                    Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          context.s('advancedTitle'),
                          style: T.text(
                            15,
                            color: T.title,
                            weight: FontWeight.w600,
                          ),
                        ),
                        Text(
                          'overriding ${studio.mode}',
                          style: T.text(11.5, color: T.hint),
                        ),
                      ],
                    ),
                    const Spacer(),
                    Btn(
                      context.s('done'),
                      kind: BtnKind.primary,
                      onTap: onClose,
                    ),
                  ],
                ),
              ),
              Container(height: 1, color: T.hairline),
              Padding(
                padding: const EdgeInsets.fromLTRB(15, 6, 15, 8),
                child: Column(
                  children: [
                    _Row(
                      label: context.s('srcRes'),
                      help: context.s('srcResHelp'),
                      value: studio.sourceRes,
                      onChanged: studio.setSourceRes,
                    ),
                    _Row(
                      label: context.s('keepIn'),
                      help: context.s('keepInHelp'),
                      value: studio.keepInside,
                      onChanged: studio.setKeepInside,
                    ),
                    _Row(
                      label: context.s('fastMode'),
                      help: context.s('fastModeHelp'),
                      value: studio.choices['AIFast'] as bool? ?? false,
                      onChanged: (v) => studio.setChoice('AIFast', v),
                    ),
                    _Row(
                      label: context.s('dropContour'),
                      help: context.s('dropContourHelp'),
                      value: noEagle,
                      onChanged: (v) => _setEdge(eagle: v),
                    ),
                    _Row(
                      label: context.s('dropFalseEdge'),
                      help: context.s('dropFalseEdgeHelp'),
                      value: noFe,
                      onChanged: (v) => _setEdge(fe: v),
                    ),
                    _Row(
                      label: context.s('singleColour'),
                      help: context.s('singleColourHelp'),
                      value: mono,
                      onChanged: (v) =>
                          studio.setChoice('MonoColor', v ? 'auto' : ''),
                    ),
                    _Row(
                      label: context.s('economy'),
                      help: context.s('economyHelp'),
                      value: studio.choices['Economy'] as bool? ?? false,
                      onChanged: (v) => studio.setChoice('Economy', v),
                    ),
                    _Stepper(
                      label: context.s('bestOf'),
                      help: context.s('bestOfHelp'),
                      value: (studio.choices['BestOf'] as num?)?.toInt() ?? 1,
                      min: 1,
                      // The engine clamps at nine; offering more would be a
                      // control that silently does nothing past the fourth tap.
                      max: 9,
                      onChanged: (v) => studio.setChoice('BestOf', v),
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
}

class _Row extends StatelessWidget {
  const _Row({
    required this.label,
    required this.help,
    required this.value,
    required this.onChanged,
  });

  final String label;
  final String help;
  final bool value;
  final ValueChanged<bool> onChanged;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: () => onChanged(!value),
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      padding: const EdgeInsets.symmetric(vertical: 7, horizontal: 8),
      margin: const EdgeInsets.symmetric(vertical: 1),
      decoration: BoxDecoration(
        color: hoverOver(const Color(0x00000000), hover, down),
        borderRadius: BorderRadius.circular(8),
      ),
      child: Row(
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  label,
                  style: T.text(12.5, color: hover ? T.title : T.body),
                ),
                Text(help, style: T.text(10.5, color: T.hint)),
              ],
            ),
          ),
          const SizedBox(width: 14),
          _Toggle(value: value),
        ],
      ),
    ),
  );
}

/// A small integer, set by two buttons. A slider for a range of nine would be
/// harder to land on than the number is worth.
class _Stepper extends StatelessWidget {
  const _Stepper({
    required this.label,
    required this.help,
    required this.value,
    required this.min,
    required this.max,
    required this.onChanged,
  });

  final String label;
  final String help;
  final int value;
  final int min;
  final int max;
  final ValueChanged<int> onChanged;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.symmetric(vertical: 8, horizontal: 8),
    child: Row(
      children: [
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(label, style: T.text(12.5, color: T.body)),
              Text(help, style: T.text(10.5, color: T.hint)),
            ],
          ),
        ),
        const SizedBox(width: 14),
        _Step('−', onTap: value > min ? () => onChanged(value - 1) : null),
        SizedBox(
          width: 30,
          child: Text(
            '$value',
            textAlign: TextAlign.center,
            style: T.monoText(13, color: T.title),
          ),
        ),
        _Step('+', onTap: value < max ? () => onChanged(value + 1) : null),
      ],
    ),
  );
}

class _Step extends StatelessWidget {
  const _Step(this.glyph, {required this.onTap});
  final String glyph;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) => Pressable(
    onTap: onTap,
    builder: (context, hover, down) => AnimatedContainer(
      duration: Motion.fast,
      width: 26,
      height: 26,
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: hoverOver(T.fillSoft, hover, down),
        borderRadius: BorderRadius.circular(7),
      ),
      child: Text(
        glyph,
        style: T.text(
          13,
          color: onTap == null ? T.faint : (hover ? T.title : T.dim),
        ),
      ),
    ),
  );
}

class _Toggle extends StatelessWidget {
  const _Toggle({required this.value});
  final bool value;

  @override
  Widget build(BuildContext context) => AnimatedContainer(
    duration: const Duration(milliseconds: 130),
    width: 34,
    height: 20,
    padding: const EdgeInsets.all(2),
    alignment: value ? Alignment.centerRight : Alignment.centerLeft,
    decoration: BoxDecoration(
      color: value ? T.teal : T.fill,
      borderRadius: BorderRadius.circular(10),
    ),
    child: Container(
      width: 16,
      height: 16,
      decoration: const BoxDecoration(
        color: Color(0xFFFFFFFF),
        shape: BoxShape.circle,
        boxShadow: [
          BoxShadow(
            color: Color(0x66000000),
            blurRadius: 3,
            offset: Offset(0, 1),
          ),
        ],
      ),
    ),
  );
}

/// The app's own settings, as opposed to the engine's.
///
/// The distinction matters: everything in [AdvancedSheet] changes what the fit
/// produces, and everything here changes how the app behaves around it. They
/// were one button for a while and it was the wrong button — someone looking
/// for a sound toggle should not be reading about contour terms.
class SettingsSheet extends StatelessWidget {
  const SettingsSheet({super.key, required this.studio, required this.onClose});

  final Studio studio;
  final VoidCallback onClose;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Glass(
        radius: 16,
        child: SizedBox(
          width: 520,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Padding(
                padding: const EdgeInsets.fromLTRB(15, 14, 15, 12),
                child: Row(
                  children: [
                    Text(
                      context.s('settings'),
                      style: T.text(
                        15,
                        color: T.title,
                        weight: FontWeight.w600,
                      ),
                    ),
                    const Spacer(),
                    Btn(
                      context.s('done'),
                      kind: BtnKind.primary,
                      onTap: onClose,
                    ),
                  ],
                ),
              ),
              Container(height: 1, color: T.hairline),
              Padding(
                padding: const EdgeInsets.fromLTRB(15, 6, 15, 8),
                child: Column(
                  children: [
                    _Row(
                      label: context.s('soundOnFinish'),
                      help: context.s('soundOnFinishHelp'),
                      value: studio.soundOnFinish,
                      onChanged: studio.setSoundOnFinish,
                    ),
                    _Row(
                      label: context.s('flashOnFinish'),
                      help: context.s('flashOnFinishHelp'),
                      value: studio.flashOnFinish,
                      onChanged: studio.setFlashOnFinish,
                    ),
                  ],
                ),
              ),
              Container(height: 1, color: T.hairline),
              // A fit can take minutes, so the answer to "did it work" is
              // usually somewhere else on the machine by the time it lands.
              Padding(
                padding: const EdgeInsets.fromLTRB(15, 11, 15, 14),
                child: Row(
                  children: [
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(context.s('diagnostics'), style: T.label),
                          const SizedBox(height: 3),
                          Text(
                            AppLog.path ?? context.s('logHint'),
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: T.monoText(10, color: T.hint),
                          ),
                        ],
                      ),
                    ),
                    const SizedBox(width: 10),
                    Btn(context.s('copyPath'), onTap: _copyPath),
                    const SizedBox(width: 7),
                    Btn(context.s('openLogFolder'), onTap: AppLog.reveal),
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
